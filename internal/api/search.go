// Поиск монет для селекторов: CoinGecko по названию/тикету, контракты —
// через GeckoTerminal, шиткоины TON по имени — через TonAPI.
package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"graphicks-analyzator/internal/coingecko"
	"graphicks-analyzator/internal/tonaddr"
)

type searchItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
	Thumb  string `json:"thumb"`
	Rank   int    `json:"rank"`
}

// maxQueryLen — верхняя граница поискового запроса: реальные названия и
// адреса короче, а длинную строку не зачем передавать внешним API.
const maxQueryLen = 120

// looksLikeAddress — похоже ли на адрес контракта: длинная строка
// без пробелов из букв, цифр и дефисов (TON/EVM/Solana).
func looksLikeAddress(q string) bool {
	if len(q) < 20 || len(q) > maxQueryLen {
		return false
	}
	for _, r := range q {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// --- GET /api/search?q=... ---

// Search ищет монеты для селекторов и собирает результаты из трёх мест:
//  1. CoinGecko — по названию/тикету (все «обычные» монеты);
//  2. GeckoTerminal — если запрос похож на адрес контракта (DEX-токен);
//  3. TonAPI — шиткоины TON по имени, которых в CoinGecko нет.
//
// Результаты TonAPI всегда идут после CoinGecko — иначе один
// «однофамилец» из CoinGecko затирает настоящие DEX-токены.
func (s *Server) Search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > maxQueryLen {
		q = q[:maxQueryLen]
	}
	if len(q) < 2 {
		writeJSON(w, http.StatusOK, []searchItem{}) // пустой запрос — пустой список
		return
	}

	key := "search:" + strings.ToLower(q)
	if v, ok := s.searchCache.Get(key); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	items := make([]searchItem, 0, 20)
	cgFailed := false

	if coins, err := s.cg.Search(r.Context(), q); err == nil {
		// Релевантность: CoinGecko сортирует по своему усмотрению, и нужная
		// монета может затеряться среди «однофамильцев». Точное совпадение
		// тикера поднимаем наверх, затем совпадение по префиксу.
		ql := strings.ToLower(q)
		relevance := func(c coingecko.SearchCoin) int {
			sym, name := strings.ToLower(c.Symbol), strings.ToLower(c.Name)
			switch {
			case sym == ql:
				return 0
			case strings.HasPrefix(sym, ql), strings.HasPrefix(name, ql):
				return 1
			default:
				return 2
			}
		}
		sort.SliceStable(coins, func(i, j int) bool { return relevance(coins[i]) < relevance(coins[j]) })
		if len(coins) > 20 {
			coins = coins[:20]
		}
		for _, c := range coins {
			items = append(items, searchItem{ID: c.ID, Name: c.Name, Symbol: c.Symbol, Thumb: c.Thumb, Rank: c.Rank})
		}
	} else {
		// CoinGecko недоступен — адресный поиск всё равно попробуем.
		cgFailed = true
		s.log.Warn("поиск CoinGecko не удался", "err", err)
	}

	// Адрес контракта: резолв через GeckoTerminal, ставим первым.
	if looksLikeAddress(q) {
		if item, ok := s.gtResolve(r.Context(), q); ok {
			items = append([]searchItem{item}, items...)
		}
	}

	// Шиткоины TON по имени — из TonAPI, в конец выдачи.
	items = append(items, s.tonNameSearch(r.Context(), q)...)

	// Пустой результат при сбое источников (или для адреса, чей резолв
	// не удался) НЕ кэшируем — иначе единичный сбой источника отравляет
	// выдачу на все 10 минут, и токен «не находится» без причины.
	if len(items) == 0 && (cgFailed || looksLikeAddress(q)) {
		writeJSON(w, http.StatusOK, items)
		return
	}

	s.searchCache.Set(key, items, 10*time.Minute)
	writeJSON(w, http.StatusOK, items)
}

// gtNetworksForAddress — в каких сетях искать адрес. Префиксы выдают
// цепочку: EQ*/UQ* — TON, 0x — EVM, длинная base58 — Solana.
func gtNetworksForAddress(q string) []string {
	u := strings.ToUpper(q)
	switch {
	case strings.HasPrefix(u, "EQ") || strings.HasPrefix(u, "UQ"):
		return []string{"ton"}
	case strings.HasPrefix(q, "0x"):
		return []string{"eth", "bsc", "base", "polygon_pos", "arbitrum", "avax"}
	default:
		return []string{"solana", "ton"}
	}
}

// gtResolve пытается понять строку как адрес DEX-токена. Если у токена
// есть страница в основном CoinGecko — ведём её (данные богаче),
// иначе создаём id вида "gt:сеть/адрес".
func (s *Server) gtResolve(ctx context.Context, q string) (searchItem, bool) {
	for _, net := range gtNetworksForAddress(q) {
		tok, err := s.gt.Token(ctx, net, q)
		if err != nil {
			continue // в этой сети токена нет
		}
		item := searchItem{
			ID:     "gt:" + net + "/" + q,
			Name:   tok.Name,
			Symbol: tok.Symbol,
			Thumb:  tok.ImageURL,
		}
		if tok.CGCoinID != "" {
			item.ID = tok.CGCoinID
		}
		return item, true
	}
	return searchItem{}, false
}

// tonNameSearch ищет TON-токен по НАЗВАНИЮ через TonAPI — для шиткоинов,
// которых нет в основном CoinGecko (кейс: «durov» находит Durov-токены).
// TonAPI отдаёт raw-адреса («0:hex…») — конвертируем в EQ для id "gt:ton/…".
func (s *Server) tonNameSearch(ctx context.Context, q string) []searchItem {
	tokens, err := s.ta.Search(ctx, q)
	if err != nil {
		s.log.Warn("поиск TonAPI не удался", "err", err)
		return nil
	}

	items := make([]searchItem, 0, len(tokens))
	seen := map[string]bool{}
	// Whitelist-токены (проверенные) — наверх.
	sort.Slice(tokens, func(i, j int) bool { return tokens[i].Trust == "whitelist" && tokens[j].Trust != "whitelist" })
	for _, t := range tokens {
		eq, ok := tonaddr.FriendlyFromRaw(t.Address)
		if !ok || seen[eq] {
			continue
		}
		seen[eq] = true
		items = append(items, searchItem{ID: "gt:ton/" + eq, Name: t.Name, Symbol: t.Name, Thumb: t.ImageURL})
		if len(items) >= 8 {
			break
		}
	}
	return items
}
