package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type AppHandler func(http.ResponseWriter, *http.Request) error

func ErrorMiddleware(logger *slog.Logger) func(AppHandler) http.HandlerFunc {
	return func(handler AppHandler) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			err := handler(w, r)
			if err == nil {
				return
			}

			reqID := w.Header().Get("X-Request-ID")

			if appErr, ok := errors.AsType[*AppError](err); ok {
				status := codeToStatus[appErr.Code]
				if status >= 500 {
					logger.Error("handler error",
						"request_id", reqID,
						"code", appErr.Code,
						"message", appErr.Message,
						"error", appErr.Err,
					)
				} else {
					logger.Warn("handler error",
						"request_id", reqID,
						"code", appErr.Code,
						"message", appErr.Message,
						"error", appErr.Err,
					)
				}
				WriteError(w, r, appErr.Code, appErr.Message)
			} else {
				logger.Error("unexpected error", "request_id", reqID, "error", err)
				WriteError(w, r, CodeInternal, "internal error")
			}
		}
	}
}

func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered", "request_id", w.Header().Get("X-Request-ID"), "panic", rec)
					WriteError(w, r, CodeInternal, "internal error")
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

func LimitBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.code = code
	sw.ResponseWriter.WriteHeader(code)
}

func Logger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}

			next.ServeHTTP(sw, r)

			logger.Info("request",
				"request_id", sw.Header().Get("X-Request-ID"),
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.code,
				"elapsed", time.Since(start),
			)
		})
	}
}

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", uuid.New().String())
		next.ServeHTTP(w, r)
	})
}
