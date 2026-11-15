// Package dedust — минимальный клиент публичного API DeDust (DEX на TON).
// Нужен для двух вещей: найти адрес пула TON-токена (когда GeckoTerminal
// не отдаёт ни relationships.top_pools, ни /tokens/{addr}/pools) и
// поискать шиткоин по тикеру — в основном CoinGecko таких токенов нет.
//
// Формат пулов DeDust: assets — массив из двух сторон (нативный TON или
// джеттон с metadata: имя/тикер/лого), lastPrice — последняя цена,
// reserves — сырые резервы (в минимальных единицах токена, без декималов).
// Адреса джеттонов и пулов приходят сразу в user-friendly виде (EQ…).
package dedust

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

const defaultBaseURL = "https://api.dedust.io/v2"

// Client — точка входа для запросов к DeDust.
type Client struct {
	http    *http.Client
	baseURL string
	log     *slog.Logger
}

func New(log *slog.Logger) *Client {
	return &Client{
		// Без keep-alive — та же причина, что и в пакете coingecko.
		// Таймаут большой: /v2/pools отдаёт десятки мегабайт и думает
		// подольше, результат кэшируется на сутки.
		http: &http.Client{
			Timeout:   180 * time.Second,
			Transport: &http.Transport{DisableKeepAlives: true},
		},
		baseURL: defaultBaseURL,
		log:     log,
	}
}

// Error — неудачный HTTP-ответ DeDust.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("dedust: статус %d: %s", e.Status, e.Body)
}

// Asset — одна сторона пула: нативный TON или джеттон.
type Asset struct {
	Type     string // "native" | "jetton"
	Address  string // пусто для нативного TON
	Symbol   string
	Name     string
	ImageURL string
	Reserve  float64 // сырой резерв (минимальные единицы токена)
}

// Pool — пул DeDust. Адреса — в user-friendly виде (EQ…).
type Pool struct {
	Address   string
	Assets    [2]Asset
	LastPrice *float64 // nil, если цены нет
}

type poolResponse struct {
	Address string `json:"address"`
	Assets  []struct {
		Type     string `json:"type"`
		Address  string `json:"address"`
		Metadata *struct {
			Name   string `json:"name"`
			Symbol string `json:"symbol"`
			Image  string `json:"image"`
		} `json:"metadata"`
	} `json:"assets"`
	LastPrice *float64 `json:"lastPrice"`
	Reserves  []string `json:"reserves"`
}

// Pools возвращает все пулы DeDust. Ответ большой (десятки мегабайт) —
// вызывайте редко и кэшируйте надолго.
func (c *Client) Pools(ctx context.Context) ([]Pool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/pools", nil)
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

	var raw []poolResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	pools := make([]Pool, 0, len(raw))
	for _, p := range raw {
		if len(p.Assets) != 2 {
			continue
		}
		pool := Pool{Address: p.Address, LastPrice: p.LastPrice}
		for i, a := range p.Assets {
			asset := Asset{Type: a.Type, Address: a.Address}
			if a.Metadata != nil {
				asset.Symbol = a.Metadata.Symbol
				asset.Name = a.Metadata.Name
				asset.ImageURL = a.Metadata.Image
			}
			if i < len(p.Reserves) {
				asset.Reserve, _ = strconv.ParseFloat(p.Reserves[i], 64)
			}
			pool.Assets[i] = asset
		}
		pools = append(pools, pool)
	}
	c.log.Info("DeDust: получено пулов", "count", len(pools))
	return pools, nil
}
