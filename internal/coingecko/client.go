// Package coingecko — минимальный клиент публичного API CoinGecko.
// Здесь только то, что нужно сайту: поиск монет, история цен и
// «живые» данные по списку монет.
package coingecko

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.coingecko.com/api/v3"

// Client — точка входа для запросов к CoinGecko.
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  string // бесплатный демо-ключ; может быть пустым
	log     *slog.Logger
}

// New создаёт клиент. apiKey — необязательный демо-ключ
// ( env CG_DEMO_KEY ); без него лимиты заметно ниже.
func New(apiKey string, log *slog.Logger) *Client {
	return &Client{
		// Таймаут на весь запрос: зависший CoinGecko не должен
		// держать наши горутины вечно.
		//
		// Транспорт без keep-alive: долгоживущий процесс за NAT/прокси
		// может копить «протухшие» соединения в пуле — они висят до
		// таймаута, и все запросы встают. Новое соединение на каждый
		// запрос стоит десятки миллисекунд, зато залипнуть нечему.
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{DisableKeepAlives: true},
		},
		baseURL: defaultBaseURL,
		apiKey:  apiKey,
		log:     log,
	}
}

// Error — неудачный HTTP-ответ от CoinGecko.
type Error struct {
	Status     int
	Body       string
	RetryAfter time.Duration // из заголовка Retry-After, если CoinGecko его прислал (обычно при 429)
}

func (e *Error) Error() string {
	return fmt.Sprintf("coingecko: статус %d: %s", e.Status, e.Body)
}

// Retryable сообщает, стоит ли повторить запрос.
func (e *Error) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// get выполняет GET-запрос с повторами при 429/5xx и декодирует
// JSON-ответ в out. out должен быть указателем.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	const attempts = 3
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			// Пауза перед повтором: 1с, 2с, 4с; при 429 слушаем Retry-After.
			pause := time.Duration(1<<uint(i-1)) * time.Second
			var cgErr *Error
			if errors.As(lastErr, &cgErr) && cgErr.RetryAfter > pause {
				pause = cgErr.RetryAfter
			}
			c.log.Warn("повтор запроса к CoinGecko", "attempt", i+1, "pause", pause, "url", u, "err", lastErr)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pause):
			}
		}
		if err := c.do(ctx, u, out); err == nil {
			return nil
		} else {
			lastErr = err
			var cgErr *Error
			// Повторяем «временные» ответы (429/5xx) и сетевые сбои
			// (таймауты, обрывы соединения). 404 и т.п. возвращаем сразу.
			if errors.As(err, &cgErr) && !cgErr.Retryable() {
				return err
			}
		}
	}
	return lastErr
}

// do выполняет один HTTP-запрос и разбирает ответ.
func (c *Client) do(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("x-cg-demo-api-key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10)) // читаем максимум 4КБ ошибки
		e := &Error{Status: resp.StatusCode, Body: string(body)}
		if sec, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && sec > 0 && sec <= 60 {
			e.RetryAfter = time.Duration(sec) * time.Second
		}
		return e
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- Типы ответов CoinGecko ---

// SearchCoin — элемент ответа /search.
type SearchCoin struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
	Thumb  string `json:"thumb"`
	Rank   int    `json:"market_cap_rank"`
}

type searchResponse struct {
	Coins []SearchCoin `json:"coins"`
}

// Market — «живые» данные монеты из /coins/markets.
type Market struct {
	ID                       string  `json:"id"`
	Symbol                   string  `json:"symbol"`
	Name                     string  `json:"name"`
	Image                    string  `json:"image"`
	CurrentPrice             float64 `json:"current_price"`
	PriceChangePercentage24h float64 `json:"price_change_percentage_24h"`
	MarketCapRank            int     `json:"market_cap_rank"`
}

// MarketChart — ответ /coins/{id}/market_chart.
// Prices — пары [unix_ms, цена].
type MarketChart struct {
	Prices [][]float64 `json:"prices"`
}

// --- Методы ---

// Search ищет монеты по подстроке (по названию или тикеру).
func (c *Client) Search(ctx context.Context, query string) ([]SearchCoin, error) {
	q := url.Values{"query": {query}}
	var out searchResponse
	if err := c.get(ctx, "/search", q, &out); err != nil {
		return nil, err
	}
	return out.Coins, nil
}

// Markets возвращает живые данные (лого, цена, изменение за 24ч) по списку id.
func (c *Client) Markets(ctx context.Context, ids []string, vsCurrency string) ([]Market, error) {
	q := url.Values{
		"vs_currency": {vsCurrency},
		"ids":         {strings.Join(ids, ",")},
	}
	var out []Market
	if err := c.get(ctx, "/coins/markets", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MarketChart возвращает историю цен. days — строка, потому что CoinGecko
// принимает и число ("90"), и "max". Гранулярность API выбирает сам:
// 5-минутная для days=1, часовая до 90 дней, дневная дальше.
func (c *Client) MarketChart(ctx context.Context, id, vsCurrency, days string) (*MarketChart, error) {
	q := url.Values{
		"vs_currency": {vsCurrency},
		"days":        {days},
	}
	var out MarketChart
	path := "/coins/" + url.PathEscape(id) + "/market_chart"
	if err := c.get(ctx, path, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Ticker — биржевая пара монеты из ответа /coins/{id}/tickers.
// Base/Target — активы в том виде, как их называет биржа (у Binance
// Toncoin исторически TON, хотя CoinGecko уже зовёт его GRAM).
// Внимание: биржа лежит в market.identifier, параметр exchange_id
// API игнорирует — фильтруем на нашей стороне.
type Ticker struct {
	Base   string `json:"base"`
	Target string `json:"target"`
	Market struct {
		Identifier string `json:"identifier"`
	} `json:"market"`
}

type tickersResponse struct {
	Tickers []Ticker `json:"tickers"`
}

// Tickers возвращает биржевые пары монеты со всех бирж (по страницам
// по 100). Параметр exchange_id API на деле игнорирует — фильтруйте
// сами по Market.Identifier. Нужен, чтобы найти пары монеты на Binance.
func (c *Client) Tickers(ctx context.Context, id string, pages int) ([]Ticker, error) {
	var all []Ticker
	for page := 1; page <= pages; page++ {
		q := url.Values{"depth": {"false"}, "page": {strconv.Itoa(page)}}
		var out tickersResponse
		path := "/coins/" + url.PathEscape(id) + "/tickers"
		if err := c.get(ctx, path, q, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Tickers...)
		if len(out.Tickers) < 100 {
			break
		}
	}
	return all, nil
}
