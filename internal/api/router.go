package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	httpSwagger "github.com/swaggo/http-swagger/v2"
)

// NewRouter собирает HTTP-роутер. queueBroken — проверка «разбор очереди
// мёртв» для /readyz; nil, если спрашивать не у кого (debug-сервер работает
// без очереди вовсе).
func NewRouter(h *Handler, logger *slog.Logger, maxBodyBytes int64, swaggerEnabled bool, queueBroken func() bool) http.Handler {
	r := chi.NewRouter()

	r.Use(Recover(logger))
	r.Use(RequestID(logger))
	r.Use(Logger(logger))
	r.Use(LimitBody(maxBodyBytes))

	errMw := ErrorMiddleware(logger)

	r.Get("/healthz", Healthz)
	r.Get("/readyz", ReadyzHandler(queueBroken))

	// Асинхронные задачи (синхронная чистка вынесена в cmd/debugserver).
	r.Post("/v1/tasks", errMw(h.HandleSubmit))
	r.Get("/v1/tasks/{taskKey}", errMw(h.HandleStatus))
	r.Get("/v1/tasks/{taskKey}/debug", errMw(h.HandleDebug))

	if swaggerEnabled {
		r.Get("/swagger/*", httpSwagger.WrapHandler)
	}

	return r
}
