// «Живые» данные для чипов: цена, лого, изменение за 24ч.
package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"graphicks-analyzator/internal/coingecko"
	"graphicks-analyzator/internal/geckoterminal"
)

// --- GET /api/coins?ids=bitcoin,ethereum ---

// Markets возвращает «живые» данные монет: лого, текущая цена, изменение
// за 24ч. Обычные монеты — из CoinGecko, DEX-токены (gt:*) — из GeckoTerminal.
func (s *Server) Markets(w http.ResponseWriter, r *http.Request) {
	ids := strings.Split(r.URL.Query().Get("ids"), ",")
	if len(ids) == 0 || ids[0] == "" {
		writeJSON(w, http.StatusOK, []coingecko.Market{})
		return
	}
	if len(ids) > 10 {
		ids = ids[:10]
	}

	key := "markets:" + strings.Join(ids, ",")
	if v, ok := s.marketCache.Get(key); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	var cgIDs []string
	result := make([]coingecko.Market, 0, len(ids))
	for _, id := range ids {
		if !strings.HasPrefix(id, "gt:") {
			cgIDs = append(cgIDs, id)
			continue
		}
		tok, err := s.gtTokenByID(r.Context(), id)
		if err != nil {
			s.log.Warn("нет данных GT для чипа", "id", id, "err", err)
			continue
		}
		result = append(result, coingecko.Market{
			ID:           id,
			Symbol:       tok.Symbol,
			Name:         tok.Name,
			Image:        tok.ImageURL,
			CurrentPrice: tok.PriceUSD,
			// Изменения за 24ч у DEX-токена нет — чип покажет только цену.
		})
	}
	if len(cgIDs) > 0 {
		markets, err := s.cg.Markets(r.Context(), cgIDs, "usd")
		if err != nil {
			s.log.Warn("живые данные CoinGecko не пришли", "err", err)
		} else {
			result = append(result, markets...)
		}
	}

	s.marketCache.Set(key, result, time.Minute)
	writeJSON(w, http.StatusOK, result)
}

// gtTokenByID разбирает id "gt:сеть/адрес" и достаёт токен с его
// главным пулом (с кэшем). Если relationships.top_pools пуст — а у
// мелких токенов бывает — дополнительно спрашиваем список пулов,
// а для TON в самом крайнем случае — DeDust.
func (s *Server) gtTokenByID(ctx context.Context, id string) (*geckoterminal.Token, error) {
	if tok, ok := s.gtTokenCache.Get(id); ok {
		return tok, nil
	}
	net, addr, ok := strings.Cut(strings.TrimPrefix(id, "gt:"), "/")
	if !ok || net == "" || addr == "" {
		return nil, fmt.Errorf("кривой id DEX-токена: %s", id)
	}
	tok, err := s.gt.Token(ctx, net, addr)
	if err != nil {
		return nil, err
	}
	if tok.TopPoolID == "" {
		poolIDs, err := s.gt.TokenPools(ctx, net, addr)
		if err != nil {
			s.log.Warn("пулы GT не ответили", "net", net, "err", err)
		}
		if len(poolIDs) > 0 {
			tok.TopPoolID = poolIDs[0]
		} else if net == "ton" {
			// Запасной путь для TON: адрес пула достаём из DeDust.
			poolAddr, err := s.tonPoolFromDedust(ctx, addr)
			if err != nil {
				return nil, fmt.Errorf("у токена %s не нашёлся пул: %w", tok.Name, err)
			}
			tok.TopPoolID = poolAddr
		} else {
			return nil, fmt.Errorf("у токена %s нет пулов на DEX", tok.Name)
		}
	}
	s.gtTokenCache.Set(id, tok, time.Hour)
	return tok, nil
}
