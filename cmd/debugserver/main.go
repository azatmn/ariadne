// Command debugserver — лёгкий сервер ТОЛЬКО с синхронной ручкой чистки
// (/v1/routes/resolve-collisions) для фронт-отладчика (ariadne-debug-proxy).
// НЕ для прода: ни Redis, ни воркеров, ни gRPC. Прод — cmd/server (async).
//
// Запуск:  go run ./cmd/debugserver   (слушает cfg.Port, по умолчанию :8080)
package main

import (
	"ariadne/internal/osrm"
	"ariadne/internal/pipeline"
	"context"
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

	svc := service.New(cfg, newRouter(cfg, logger))
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

// newRouter поднимает клиент маршрутизатора.
//
// Возвращает nil, если адрес не задан: сервис обязан работать и без OSRM —
// чистка и дорисовка тогда пропускают трек насквозь и предупреждают об этом.
// Связь проверяем сразу: сервис, который не может достучаться до
// маршрутизатора, должен сказать это в логе на старте, а не выяснять на
// первой же задаче, когда разбираться будет некому.
func newRouter(cfg *config.Config, logger *slog.Logger) pipeline.Router {
	if cfg.OSRMURL == "" {
		logger.Warn("OSRM_URL is empty: cleaning and gap filling disabled")
		return nil
	}

	client, err := osrm.New(osrm.Config{
		BaseURL:        cfg.OSRMURL,
		MaxParallel:    cfg.OSRMMaxParallel,
		RequestTimeout: cfg.OSRMTimeout,
	})
	if err != nil {
		logger.Error("osrm client init failed", "url", cfg.OSRMURL, "error", err)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.OSRMTimeout)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		logger.Error("osrm unreachable", "url", cfg.OSRMURL, "error", err)
	} else {
		logger.Info("osrm connected", "url", cfg.OSRMURL)
	}
	return client
}
