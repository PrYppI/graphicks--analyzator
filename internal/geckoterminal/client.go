// Package geckoterminal — клиент публичного API GeckoTerminal.
// GeckoTerminal — это DEX-раздел CoinGecko: видит токены, которые
// торгуются только на децентрализованных биржах и не имеют страницы
// в основном CoinGecko. График такого токена берём из свечей его
// главного пула — этот эндпоинт работает без ключа (в отличие от
// /tokens/{addr}/ohlcv, который требует демо-ключ).
package geckoterminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultBaseURL = "https://api.geckoterminal.com/api/v2"

// Client — точка входа для запросов к GeckoTerminal.
type Client struct {
	http    *http.Client
	baseURL string
	log     *slog.Logger

	// Троттлинг: лимит GT — 30 запросов/мин, и при превышении он не
	// возвращает 429, а молча подвешивает соединения. Держим паузу
	// 2.5 с между любыми двумя запросами — тогда лимит не трогаем.
	limMu  sync.Mutex
	nextOK time.Time
}

func New(log *slog.Logger) *Client {
	return &Client{
		// Без keep-alive — та же причина, что и в пакете coingecko.
		http: &http.Client{
			Timeout:   90 * time.Second,
			Transport: &http.Transport{DisableKeepAlives: true},
		},
		baseURL: defaultBaseURL,
		log:     log,
	}
}

// throttle выдерживает паузу между запросами (вызовы выстраиваются в очередь).
func (c *Client) throttle() {
	c.limMu.Lock()
	defer c.limMu.Unlock()
	now := time.Now()
	if c.nextOK.After(now) {
		time.Sleep(c.nextOK.Sub(now))
		c.nextOK = time.Now().Add(2500 * time.Millisecond)
	} else {
		c.nextOK = now.Add(2500 * time.Millisecond)
	}
}

// Error — неудачный HTTP-ответ GeckoTerminal.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("geckoterminal: статус %d: %s", e.Status, e.Body)
}

// Token — информация о токене на DEX: имя, картинка, цена, главный пул.
type Token struct {
	Network   string
	Address   string
	Name      string
	Symbol    string
	ImageURL  string
	PriceUSD  float64
	CGCoinID  string // id страницы в основном CoinGecko, если она есть
	TopPoolID string // адрес главного пула — источник свечей
}

type tokenResponse struct {
	Data struct {
		Attributes struct {
			Address    string `json:"address"`
			Name       string `json:"name"`
			Symbol     string `json:"symbol"`
			ImageURL   string `json:"image_url"`
			PriceUSD   string `json:"price_usd"`
			CGCoinID   string `json:"coingecko_coin_id"`
		} `json:"attributes"`
		Relationships struct {
			TopPools struct {
				Data []struct {
					ID string `json:"id"` // вида "ton_EQ..."
				} `json:"data"`
			} `json:"top_pools"`
		} `json:"relationships"`
	} `json:"data"`
}

// Token возвращает информацию о токене на указанной сети.
func (c *Client) Token(ctx context.Context, network, address string) (*Token, error) {
	var out tokenResponse
	path := "/networks/" + url.PathEscape(network) + "/tokens/" + url.PathEscape(address)
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	a := out.Data.Attributes
	t := &Token{
		Network:  network,
		Address:  a.Address,
		Name:     a.Name,
		Symbol:   a.Symbol,
		ImageURL: a.ImageURL,
		CGCoinID: a.CGCoinID,
	}
	if p, err := strconv.ParseFloat(a.PriceUSD, 64); err == nil {
		t.PriceUSD = p
	}
	if n := len(out.Data.Relationships.TopPools.Data); n > 0 {
		id := out.Data.Relationships.TopPools.Data[0].ID
		t.TopPoolID = strings.TrimPrefix(id, network+"_")
	}
	return t, nil
}

type poolsResponse struct {
	Data []struct {
		ID         string `json:"id"` // вида "ton_EQ..."
		Attributes struct {
			Name         string `json:"name"`
			ReserveInUSD string `json:"reserve_in_usd"`
		} `json:"attributes"`
	} `json:"data"`
}

// TokenPools возвращает адреса пулов токена, упорядоченные API
// (сначала самые ликвидные). Нужен, когда relationships.top_pools
// у токена пуст — такое встречается у мелких токенов.
func (c *Client) TokenPools(ctx context.Context, network, address string) ([]string, error) {
	var out poolsResponse
	path := "/networks/" + url.PathEscape(network) + "/tokens/" + url.PathEscape(address) + "/pools"
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	var ids []string
	for _, p := range out.Data {
		ids = append(ids, strings.TrimPrefix(p.ID, network+"_"))
	}
	return ids, nil
}

// PoolSeries возвращает свечи пула как пары [unix_ms, цена закрытия]
// по возрастанию времени. timeframe — "day" или "hour".
// targetStartMs — до какой даты докачивать историю (0 — одна страница).
// API отдаёт до 1000 свечей за запрос, новые первыми — идём пагинацией назад.
func (c *Client) PoolSeries(ctx context.Context, network, poolID, timeframe string, targetStartMs int64) ([][]float64, error) {
	var out [][]float64
	before := int64(0)
	limit := 1000
	for page := 0; page < 4; page++ {
		// ВАЖНО: get() сам клеит базовый URL — сюда передаём только путь.
		path := fmt.Sprintf("/networks/%s/pools/%s/ohlcv/%s?aggregate=1&currency=usd&limit=%d",
			url.PathEscape(network), url.PathEscape(poolID), timeframe, limit)
		if before > 0 {
			path += "&before_timestamp=" + strconv.FormatInt(before, 10)
		}
		var resp poolOHLCVResponse
		if err := c.get(ctx, path, &resp); err != nil {
			var gtErr *Error
			// Некоторые развёртывания API капризничают с большим лимитом
			// страницы — пробуем 100 и повторяем эту же страницу.
			if limit != 100 && errors.As(err, &gtErr) && gtErr.Status == http.StatusNotFound {
				limit = 100
				page--
				continue
			}
			return nil, err
		}
		list := resp.Data.Attributes.OHLCVList
		if len(list) == 0 {
			break
		}
		for _, k := range list {
			if len(k) < 5 {
				continue
			}
			// Время в секундах, берем цену закрытия свечи.
			out = append(out, []float64{k[0] * 1000, k[4]})
		}
		oldest := int64(list[len(list)-1][0]) * 1000
		if targetStartMs <= 0 || oldest <= targetStartMs {
			break // нужная глубина достигнута
		}
		before = oldest / 1000 // следующая страница — старше
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out, nil
}

type poolOHLCVResponse struct {
	Data struct {
		Attributes struct {
			OHLCVList [][]float64 `json:"ohlcv_list"`
		} `json:"attributes"`
	} `json:"data"`
}

// get выполняет GET-запрос с ретраями и декодирует JSON-ответ в out.
// Сетевые сбои и 5xx повторяем до 3 попыток (пауза 2.5 с — она же
// работает как троттлинг); честные ответы (404 и т.п.) не повторяем.
func (c *Client) get(ctx context.Context, path string, out any) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2500 * time.Millisecond):
			}
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		raw, err := c.do(attemptCtx, path)
		cancel()
		if err == nil {
			return json.Unmarshal(raw, out)
		}
		lastErr = err
		var gtErr *Error
		if errors.As(err, &gtErr) && gtErr.Status != http.StatusTooManyRequests && gtErr.Status < 500 {
			return err
		}
	}
	return lastErr
}

// do выполняет один HTTP-запрос (с троттлингом) и возвращает тело ответа.
func (c *Client) do(ctx context.Context, path string) ([]byte, error) {
	c.throttle()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, &Error{Status: resp.StatusCode, Body: string(body)}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}
