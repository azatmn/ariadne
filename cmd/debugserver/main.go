// Command debugserver — лёгкий сервер ТОЛЬКО с синхронной ручкой чистки
// (/v1/routes/resolve-collisions) для фронт-отладчика (ariadne-debug-proxy).
// НЕ для прода: ни Redis, ни воркеров, ни gRPC. Прод — cmd/server (async).
//
// Запуск:  go run ./cmd/debugserver   (слушает cfg.Port, по умолчанию :8080)
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"ariadne/internal/api"
	"ariadne/internal/config"
	"ariadne/internal/debugapi"
	"ariadne/internal/service"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		slog.Warn("invalid LOG_LEVEL, using default INFO", "value", cfg.LogLevel, "error", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	svc := service.New(cfg, service.NewRouter(cfg, logger))
	h := debugapi.NewHandler(svc, cfg.MaxDecompressedBytes, cfg.ResolveTimeout)

	// Middleware переиспользуем из internal/api (Recover/RequestID/Logger/LimitBody).
	r := chi.NewRouter()
	r.Use(api.Recover(logger))
	r.Use(api.RequestID(logger))
	r.Use(api.Logger(logger))
	r.Use(api.LimitBody(cfg.MaxBodyBytes))
	errMw := api.ErrorMiddleware(logger)

	r.Get("/healthz", api.Healthz)
	r.Post("/v1/routes/resolve-collisions", errMw(h.HandleResolve))

	addr := ":" + cfg.Port
	logger.Warn("DEBUG server starting — synchronous resolve only, NOT for production", "addr", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		logger.Error("debug server failed", "error", err)
		os.Exit(1)
	}
}
