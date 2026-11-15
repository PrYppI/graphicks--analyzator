// Package api — HTTP-слой сервера: хендлеры /api/search, /api/coins,
// /api/chart и утилиты ответов. Здесь валидируются параметры, кэшируются
// результаты и собираются данные из четырёх источников (CoinGecko,
// Binance, GeckoTerminal, DeDust/TonAPI) в единый формат для фронтенда.
//
// Раскладка по файлам:
//   chart.go   — главный эндпоинт /api/chart и выравнивание серий
//   series.go  — получение истории цен одной монеты из нужного источника
//   search.go  — поиск монет (CoinGecko + адреса + TonAPI)
//   markets.go — «живые» данные для чипов
//   ton.go     — TON-специфика: пулы DeDust, поиск TonAPI, прогрев
//   server.go  — этот файл: сборка зависимостей и утилиты HTTP
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"graphicks-analyzator/internal/binance"
	"graphicks-analyzator/internal/cache"
	"graphicks-analyzator/internal/coingecko"
	"graphicks-analyzator/internal/dedust"
	"graphicks-analyzator/internal/geckoterminal"
	"graphicks-analyzator/internal/tonapi"
)

// Server держит зависимости, общие для всех хендлеров, и их кэши.
type Server struct {
	cg *coingecko.Client
	bn *binance.Client // глубокая история (глубже 365 дней)
	gt *geckoterminal.Client
	dd *dedust.Client // TON-пулы, когда GeckoTerminal лежит
	ta *tonapi.Client // поиск TON-токенов по имени

	seriesCache  *cache.Cache[[][]float64]          // история цен одной монеты
	binanceCache *cache.Cache[[][]float64]          // сшитая история монеты на Binance
	klinesCache  *cache.Cache[[][]float64]          // сырые свечи одной пары Binance
	gtTokenCache *cache.Cache[*geckoterminal.Token] // DEX-токен: инфо + главный пул
	ddPoolCache  *cache.Cache[[]dedust.Pool]        // весь список пулов DeDust (тяжёлый ответ)
	searchCache  *cache.Cache[[]searchItem]         // результаты поиска
	marketCache  *cache.Cache[[]coingecko.Market]   // живые данные по монетам

	log *slog.Logger
}

// New собирает сервер и запускает фоновую чистку кэшей.
func New(cg *coingecko.Client, bn *binance.Client, gt *geckoterminal.Client, dd *dedust.Client, ta *tonapi.Client, log *slog.Logger) *Server {
	return &Server{
		cg:           cg,
		bn:           bn,
		gt:           gt,
		dd:           dd,
		ta:           ta,
		log:          log,
		seriesCache:  cache.New[[][]float64](time.Minute),
		binanceCache: cache.New[[][]float64](time.Hour),
		klinesCache:  cache.New[[][]float64](24 * time.Hour),
		gtTokenCache: cache.New[*geckoterminal.Token](time.Hour),
		ddPoolCache:  cache.New[[]dedust.Pool](24 * time.Hour),
		searchCache:  cache.New[[]searchItem](time.Minute),
		marketCache:  cache.New[[]coingecko.Market](time.Minute),
	}
}

// --- Утилиты HTTP ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// httpError превращает ошибку в HTTP-ответ. Тексты наших ошибок уходят
// как есть; ошибки источников логируются целиком, а наружу — только
// безопасные обобщённые формулировки.
func httpError(w http.ResponseWriter, log *slog.Logger, err error) {
	var cgErr *coingecko.Error
	if errors.As(err, &cgErr) {
		switch {
		case cgErr.Status == http.StatusTooManyRequests:
			writeErr(w, http.StatusTooManyRequests, "лимит CoinGecko исчерпан, подождите минуту")
		case cgErr.Status == http.StatusNotFound:
			writeErr(w, http.StatusNotFound, "монета не найдена")
		default:
			log.Error("ошибка CoinGecko", "status", cgErr.Status, "body", cgErr.Body)
			writeErr(w, http.StatusBadGateway, "CoinGecko недоступен, попробуйте позже")
		}
		return
	}

	var bnErr *binance.Error
	if errors.As(err, &bnErr) {
		if bnErr.Status == http.StatusBadRequest {
			// Чаще всего это «нет такой пары» — монета не торгуется на Binance.
			writeErr(w, http.StatusNotFound, "у этой монеты нет пары на Binance — период глубже года недоступен")
		} else {
			log.Error("ошибка Binance", "status", bnErr.Status, "body", bnErr.Body)
			writeErr(w, http.StatusBadGateway, "Binance недоступен, попробуйте позже")
		}
		return
	}

	var gtErr *geckoterminal.Error
	if errors.As(err, &gtErr) {
		log.Error("ошибка GeckoTerminal", "status", gtErr.Status, "body", gtErr.Body)
		writeErr(w, http.StatusBadGateway, "GeckoTerminal недоступен, попробуйте позже")
		return
	}

	var ddErr *dedust.Error
	if errors.As(err, &ddErr) {
		log.Error("ошибка DeDust", "status", ddErr.Status, "body", ddErr.Body)
		writeErr(w, http.StatusBadGateway, "DeDust недоступен, попробуйте позже")
		return
	}

	// Таймауты источников (частое следствие антифрод-блокировок по IP).
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		writeErr(w, http.StatusGatewayTimeout, "источник данных не ответил вовремя — подождите минуту и повторите")
		return
	}

	log.Error("внутренняя ошибка", "err", err)
	writeErr(w, http.StatusInternalServerError, "внутренняя ошибка")
}
