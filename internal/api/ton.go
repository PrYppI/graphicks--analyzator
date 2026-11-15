// TON-специфика: поиск токенов по имени (TonAPI), запасной источник
// адресов пулов (DeDust) и прогрев тяжёлых кэшей при старте.
package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"graphicks-analyzator/internal/dedust"
)

// allDedustPools возвращает кэшированный список всех пулов DeDust.
// Ответ тяжёлый (десятки МБ) — тянем редко, держим сутки.
func (s *Server) allDedustPools(ctx context.Context) ([]dedust.Pool, error) {
	if pools, ok := s.ddPoolCache.Get("all"); ok {
		return pools, nil
	}
	pools, err := s.dd.Pools(ctx)
	if err != nil {
		return nil, err
	}
	if len(pools) == 0 {
		return nil, fmt.Errorf("DeDust вернул пустой список пулов")
	}
	s.ddPoolCache.Set("all", pools, 24*time.Hour)
	return pools, nil
}

// Warmup подгружает в фон тяжёлые кэши при старте сервера, чтобы первый
// поиск TON-токенов по имени не ждал минуту на скачивании списка пулов.
func (s *Server) Warmup() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := s.allDedustPools(ctx); err != nil {
		s.log.Warn("прогрев DeDust не удался — список докачается при первом поиске", "err", err)
	}
}

// tonPoolFromDedust ищет самый ликвидный пул TON-токена через API DeDust.
// Адрес токена и найденный адрес пула — в user-friendly виде (EQ…),
// как их ждёт GeckoTerminal.
func (s *Server) tonPoolFromDedust(ctx context.Context, addr string) (string, error) {
	pools, err := s.allDedustPools(ctx)
	if err != nil {
		return "", err
	}

	var best *dedust.Pool
	var bestRes float64
	for i := range pools {
		p := &pools[i]
		for _, a := range p.Assets {
			if strings.EqualFold(a.Address, addr) && a.Reserve > bestRes {
				best, bestRes = p, a.Reserve
			}
		}
	}
	if best == nil {
		return "", fmt.Errorf("в DeDust нет пула для токена")
	}
	s.log.Info("пул найден через DeDust", "pool", best.Address, "reserve", bestRes)
	return best.Address, nil
}
