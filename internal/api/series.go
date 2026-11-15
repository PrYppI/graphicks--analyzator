// Получение истории цен одной монеты из нужного источника.
// Маршрутизация — в series(): какой источник выбрать и почему.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"graphicks-analyzator/internal/binance"
	"graphicks-analyzator/internal/coingecko"
)

// series возвращает историю цен одной монеты — из кэша или из источника,
// выбранного по виду монеты и периоду:
//
//	gt:…    — DEX-токен: свечи главного пула на GeckoTerminal;
//	max     — история глубже 365 дней: дневные свечи с Binance
//	          (бесплатный CoinGecko глубже не отдаёт);
//	иначе   — CoinGecko market_chart.
//
// Чтобы добавить новый источник, допиши case здесь и метод-загрузчик рядом.
func (s *Server) series(ctx context.Context, id, vs string, p period) ([][]float64, error) {
	key := id + ":" + vs + ":" + p.days
	if v, ok := s.seriesCache.Get(key); ok {
		return v, nil
	}
	var (
		prices [][]float64
		err    error
	)
	switch {
	case strings.HasPrefix(id, "gt:"):
		// DEX-токен: график из свечей его главного пула на GeckoTerminal.
		prices, err = s.gtSeries(ctx, id, p)
	case p.days == "max":
		// Бесплатный CoinGecko не отдаёт историю глубже 365 дней —
		// такой период целиком берём с Binance (только USDT-пары).
		prices, err = s.binanceSeriesFor(ctx, id)
	default:
		var mc *coingecko.MarketChart
		mc, err = s.cg.MarketChart(ctx, id, vs, p.days)
		if err == nil {
			prices = mc.Prices
		}
	}
	if err != nil {
		return nil, err
	}
	s.seriesCache.Set(key, prices, ttlForDays(p.days))
	return prices, nil
}

// seriesAll загружает истории всех монет параллельно: горутина на монету,
// WaitGroup ждёт все. Закэшированные монеты к внешним API не сходят.
func (s *Server) seriesAll(ctx context.Context, ids []string, vs string, periods []period) ([][][]float64, error) {
	var (
		wg   sync.WaitGroup
		out  = make([][][]float64, len(ids))
		errs = make([]error, len(ids))
	)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i], errs[i] = s.series(ctx, ids[i], vs, periods[i])
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ids[i], err)
		}
	}
	return out, nil
}

// gtSeries — история цен DEX-токена: токен -> его главный пул ->
// свечи пула на GeckoTerminal. Ключ не нужен, эндпоинт пулов публичный.
func (s *Server) gtSeries(ctx context.Context, id string, p period) ([][]float64, error) {
	tok, err := s.gtTokenByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if tok.TopPoolID == "" {
		return nil, fmt.Errorf("у токена %s нет пулов на DEX", tok.Name)
	}
	timeframe, targetStart := gtTimeframe(p)
	return s.gt.PoolSeries(ctx, tok.Network, tok.TopPoolID, timeframe, targetStart)
}

// gtTimeframe подбирает свечи под длину периода: короткие — часовые,
// длинные — дневные; и считает, докуда качать историю.
func gtTimeframe(p period) (timeframe string, targetStartMs int64) {
	switch p.days {
	case "1", "7", "14", "30":
		timeframe = "hour" // 1000 часовых свечей покрывают 41 день
	default:
		timeframe = "day"
	}
	if p.fromMs != 0 {
		targetStartMs = p.fromMs
	} else if d, err := strconv.Atoi(p.days); err == nil {
		targetStartMs = time.Now().AddDate(0, 0, -d).UnixMilli()
	}
	return timeframe, targetStartMs
}

// binanceSeriesFor возвращает дневную историю монеты на Binance,
// собранную из ВСЕХ её USDT-пар. Одна пара — не всегда достаточно:
// биржа переименовывает листинги (TON -> GRAM), старая пара замораживается,
// а история продолжается в новой — их нужно сшить.
func (s *Server) binanceSeriesFor(ctx context.Context, id string) ([][]float64, error) {
	if v, ok := s.binanceCache.Get(id); ok {
		return v, nil
	}

	// 1. Кандидаты: все USDT-базы монеты из её тикеров на ЛЮБЫХ биржах.
	// Замёрзшие пары (переименования!) пропадают из тикеров конкретной
	// биржи, но имя старого актива остаётся в парах других бирж — оно-то
	// и подсказывает, какую пару искать на Binance.
	var candidates []string
	if tickers, err := s.cg.Tickers(ctx, id, 3); err == nil {
		seen := map[string]bool{}
		for _, t := range tickers {
			base := strings.ToUpper(strings.TrimSpace(t.Base))
			// Настоящий тикер короткий; адреса контрактов DEX отсеиваются длиной.
			if !strings.EqualFold(t.Target, "USDT") || len(base) < 2 || len(base) > 10 || seen[base] {
				continue
			}
			seen[base] = true
			candidates = append(candidates, base+"USDT")
		}
	}
	if len(candidates) == 0 {
		markets, err := s.cg.Markets(ctx, []string{id}, "usd")
		if err != nil {
			return nil, err
		}
		if len(markets) == 0 || markets[0].Symbol == "" {
			return nil, fmt.Errorf("не удалось определить тикер монеты %s", id)
		}
		candidates = []string{strings.ToUpper(markets[0].Symbol) + "USDT"}
	}
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}

	// 2. История каждой пары (каждая — в кэше по символу). Пары без
	// истории или несуществующие пропускаем.
	var pairs [][][]float64
	for _, sym := range candidates {
		prices, ok := s.klinesCache.Get(sym)
		if !ok {
			var err error
			prices, err = s.bn.DailySeries(ctx, sym)
			if err != nil {
				var bnErr *binance.Error
				if errors.As(err, &bnErr) && bnErr.Status == http.StatusBadRequest {
					continue // нет такой пары — пробуем следующего кандидата
				}
				return nil, err
			}
			s.klinesCache.Set(sym, prices, 24*time.Hour)
		}
		if len(prices) > 0 {
			pairs = append(pairs, prices)
		}
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("монета %s не торгуется на Binance", id)
	}

	// 3. Самая старая пара — основа; пары-продолжения стыкуем к ней
	// по возрастанию времени без нахлёста.
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0][0] < pairs[j][0][0] })
	merged := append([][]float64(nil), pairs[0]...) // копия: не трогаем кэш
	for _, p := range pairs[1:] {
		last := int64(merged[len(merged)-1][0])
		for _, pt := range p {
			if int64(pt[0]) > last {
				merged = append(merged, pt)
			}
		}
	}

	s.binanceCache.Set(id, merged, 24*time.Hour)
	return merged, nil
}

// ttlForDays подбирает TTL кэша под гранулярность данных:
// свежие данные устаревают быстро, старые можно не обновлять часами.
func ttlForDays(days string) time.Duration {
	n, err := strconv.Atoi(days)
	if err != nil { // "max"
		return 2 * time.Hour
	}
	switch {
	case n <= 7:
		return 3 * time.Minute
	case n <= 30:
		return 10 * time.Minute
	case n <= 180:
		return 30 * time.Minute
	default:
		return time.Hour
	}
}
