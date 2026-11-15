// Package binance — минимальный клиент публичного API Binance.
// Зачем он тут: бесплатный CoinGecko отдаёт историю только за последние
// 365 дней, а у Binance дневные свечи доступны с 2017 года без ключа.
// Используется, когда период заходит глубже года (days=max).
package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

const defaultBaseURL = "https://api.binance.com"

// Client — точка входа для запросов к Binance.
type Client struct {
	http    *http.Client
	baseURL string
	log     *slog.Logger
}

func New(log *slog.Logger) *Client {
	return &Client{
		// Без keep-alive — та же причина, что и в пакете coingecko:
		// у долгоживущего процесса за NAT пул соединений протухает.
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{DisableKeepAlives: true},
		},
		baseURL: defaultBaseURL,
		log:     log,
	}
}

// Error — неудачный HTTP-ответ Binance.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("binance: статус %d: %s", e.Status, e.Body)
}

// DailySeries возвращает дневную историю пары symbol в виде пар
// [unix_ms, цена на открытие дня]. Binance отдаёт максимум 1000 свечей
// за запрос — ходим пагинацией, пока не вычерпаем всю историю.
func (c *Client) DailySeries(ctx context.Context, symbol string) ([][]float64, error) {
	var out [][]float64
	const limit = 1000
	start := int64(0) // 0 — с самого начала торгов парой

	for page := 0; ; page++ {
		u := fmt.Sprintf("%s/api/v3/klines?symbol=%s&interval=1d&limit=%d", c.baseURL, symbol, limit)
		if start > 0 {
			u += fmt.Sprintf("&startTime=%d", start)
		}

		// Kline — массив вида [openTime, open, high, low, close, ...]:
		// время числом, цены строками.
		var raw [][]any
		if err := c.get(ctx, u, &raw); err != nil {
			return nil, err
		}
		for _, k := range raw {
			if len(k) < 2 {
				continue
			}
			openTime, okTime := k[0].(float64)
			openStr, okOpen := k[1].(string)
			if !okTime || !okOpen {
				continue
			}
			open, err := strconv.ParseFloat(openStr, 64)
			if err != nil {
				continue
			}
			out = append(out, []float64{openTime, open})
		}

		// Меньше лимита — дошли до конца истории.
		if len(raw) < limit {
			return out, nil
		}
		last, _ := raw[len(raw)-1][0].(float64)
		start = int64(last) + 1
		if page > 50 { // страховка от бесконечного цикла
			c.log.Warn("пагинация Binance прервана по лимиту страниц", "symbol", symbol)
			return out, nil
		}
	}
}

// get выполняет GET-запрос и декодирует JSON-ответ в out.
func (c *Client) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return &Error{Status: resp.StatusCode, Body: string(body)}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
