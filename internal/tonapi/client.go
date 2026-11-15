// Package tonapi — клиент TonAPI (tonapi.io), индексатора блокчейна TON.
// Закрывает то, чего нет ни в CoinGecko, ни в GeckoTerminal:
// поиск TON-токенов по НАЗВАНИЮ, включая шиткоины без страницы в CoinGecko.
package tonapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "https://tonapi.io/v2"

// Client — точка входа для запросов к TonAPI.
type Client struct {
	http    *http.Client
	baseURL string
	log     *slog.Logger
}

func New(log *slog.Logger) *Client {
	return &Client{
		// Без keep-alive — та же причина, что и в пакете coingecko.
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{DisableKeepAlives: true},
		},
		baseURL: defaultBaseURL,
		log:     log,
	}
}

// Token — токен TON, найденный поиском. Address — в raw-виде («0:hex…»),
// как его отдаёт TonAPI; конвертация в EQ — на вызывающей стороне.
type Token struct {
	Address  string
	Name     string // без суффикса «· jetton»
	ImageURL string
	Trust    string // "whitelist" | "none" | "blacklist"
}

type searchResponse struct {
	Accounts []struct {
		Address string `json:"address"`
		Name    string `json:"name"`
		Preview string `json:"preview"`
		Trust   string `json:"trust"`
	} `json:"addresses"`
}

// Search ищет аккаунты TON по подстроке имени и оставляет только джеттоны
// (токены): TonAPI помечает их суффиксом «· jetton» в имени.
func (c *Client) Search(ctx context.Context, name string) ([]Token, error) {
	var out searchResponse
	path := "/accounts/search?name=" + url.QueryEscape(name)
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	tokens := make([]Token, 0, len(out.Accounts))
	for _, a := range out.Accounts {
		if !strings.Contains(a.Name, "jetton") {
			continue // обычный кошелёк/контракт, не токен
		}
		tokens = append(tokens, Token{
			Address:  a.Address,
			Name:     strings.TrimSpace(strings.ReplaceAll(a.Name, "· jetton", "")),
			ImageURL: a.Preview,
			Trust:    a.Trust,
		})
	}
	return tokens, nil
}

// get выполняет GET-запрос и декодирует JSON-ответ в out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("tonapi: статус %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
