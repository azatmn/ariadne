package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func NewRouter(h *Handler, logger *slog.Logger, maxBodyBytes int64) http.Handler {
	r := chi.NewRouter()

	r.Use(Recover(logger))
	r.Use(RequestID)
	r.Use(Logger(logger))
	r.Use(LimitBody(maxBodyBytes))

	errMw := ErrorMiddleware(logger)

	r.Get("/healthz", Healthz)
	r.Get("/readyz", Readyz)
	r.Post("/v1/routes/resolve-collisions", errMw(h.HandleResolve))

	return r
}
