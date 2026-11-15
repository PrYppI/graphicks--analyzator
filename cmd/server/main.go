// Команда сервера: один бинарь раздаёт статику фронтенда и API.
//
//	PORT=8080 CG_DEMO_KEY=xxx go run ./cmd/server
package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"graphicks-analyzator/internal/api"
	"graphicks-analyzator/internal/binance"
	"graphicks-analyzator/internal/coingecko"
	"graphicks-analyzator/internal/dedust"
	"graphicks-analyzator/internal/geckoterminal"
	"graphicks-analyzator/internal/tonapi"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	port := envOr("PORT", "8080")
	staticDir := envOr("STATIC_DIR", "web/dist")
	apiKey := os.Getenv("CG_DEMO_KEY") // бесплатный демо-ключ с coingecko.com — необязателен

	cg := coingecko.New(apiKey, log)
	bn := binance.New(log) // глубокая история: CoinGecko бесплатный отдаёт лишь последние 365 дней
	gt := geckoterminal.New(log)  // DEX-токены (шиткоины) через их главный пул
	dd := dedust.New(log)         // запасной источник адресов TON-пулов
	ta := tonapi.New(log)         // поиск TON-токенов по имени
	srv := api.New(cg, bn, gt, dd, ta, log)
	go srv.Warmup() // качаем список пулов DeDust в фоне — первый поиск TON-токенов не ждёт

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/search", srv.Search)
	mux.HandleFunc("GET /api/coins", srv.Markets)
	mux.HandleFunc("GET /api/chart", srv.Chart)
	// Всё прочее под /api/* — явный 404, а не index.html.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"неизвестный API-метод"}` + "\n"))
	})
	mux.Handle("/", spaHandler(staticDir))

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           logMiddleware(mux, log),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Контекст отменяется по Ctrl+C / SIGTERM — по нему плавно глушим сервер.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		log.Info("получен сигнал завершения, останавливаемся…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Error("не удалось остановиться чисто", "err", err)
		}
	}()

	log.Info("сервер запущен",
		"addr", "http://localhost:"+port,
		"demo_key", apiKey != "",
		"static", staticDir,
	)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("сервер упал", "err", err)
		os.Exit(1)
	}
	log.Info("сервер остановлен")
}

// spaHandler отдаёт файлы из staticDir. Если файла нет — значит это
// маршрут фронтенда (например /compare/btc/eth), отдаём index.html.
func spaHandler(dir string) http.Handler {
	fsys := os.DirFS(dir)
	files := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(fsys, name); err != nil {
			data, err := fs.ReadFile(fsys, "index.html")
			if err != nil {
				// Чаще всего фронт просто ещё не собран.
				http.Error(w, "фронтенд не собран: выполните npm install && npm run build в web/", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// statusRecorder запоминает код ответа, чтобы middleware мог его залогировать.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func logMiddleware(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("запрос",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start).Round(time.Millisecond),
		)
	})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
