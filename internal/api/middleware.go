package api

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"ariadne/internal/logger"

	"github.com/google/uuid"
)

// AppHandler — хендлер, который возвращает ошибку вместо того, чтобы писать её
// в ответ самому.
//
// Обычный http.HandlerFunc ошибку вернуть не может, и в каждом хендлере
// приходилось бы дублировать «залогировать, подобрать код, записать тело».
// Здесь хендлер только возвращает *AppError, а всё это делает один раз
// ErrorMiddleware.
type AppHandler func(http.ResponseWriter, *http.Request) error

// ErrorMiddleware превращает AppHandler в обычный http.HandlerFunc: сам пишет
// тело ошибки и сам её логирует.
//
// Уровень лога выбирается по коду ответа: 5xx — Error (сломались мы, надо
// смотреть), 4xx — Warn (клиент прислал негодное, это не поломка сервиса).
// Иначе разбор чужих кривых запросов тонул бы в тех же строках, что и
// настоящие аварии.
//
// Ошибка не типа *AppError наружу не раскрывается: клиент получит «internal
// error», подробности уйдут только в лог. Внутренний текст может содержать имя
// хоста Redis или кусок чужих данных.
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

// Recover ловит панику в любом хендлере и отвечает 500 вместо обрыва соединения.
//
// Без него паника в одном запросе роняет весь процесс, а с ним — только этот
// запрос. Стек пишется в лог целиком; клиенту не уходит ничего, кроме «internal
// error».
//
// Ставится ПЕРВЫМ в цепочке, иначе паника в соседнем middleware пролетит мимо.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered", "request_id", w.Header().Get("X-Request-ID"), "panic", rec, "stack", string(debug.Stack()))
					WriteError(w, r, CodeInternal, "internal error")
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// LimitBody обрезает тело запроса на maxBytes. Первая линия против бомбы
// сжатия: до распаковки дело просто не доходит.
//
// http.MaxBytesReader, а не своя обёртка: он ещё и закрывает соединение, не
// давая клиенту дослать остаток мимо счётчика.
func LimitBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// statusWriter запоминает код ответа: сам http.ResponseWriter его не отдаёт,
// а логу он нужен. Начальное значение 200 — хендлер, ничего не написавший
// явно, отвечает именно так.
type statusWriter struct {
	http.ResponseWriter
	code int
}

// WriteHeader запоминает код и передаёт его дальше.
func (sw *statusWriter) WriteHeader(code int) {
	sw.code = code
	sw.ResponseWriter.WriteHeader(code)
}

// Unwrap отдаёт исходный ResponseWriter. Без него обёртка съедала бы
// http.Flusher и http.Hijacker: стандартная библиотека добирается до них
// именно через Unwrap.
func (sw *statusWriter) Unwrap() http.ResponseWriter {
	return sw.ResponseWriter
}

// Logger пишет одну строку на завершённый запрос: метод, путь, код, время.
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

// RequestID выдаёт запросу идентификатор, кладёт его в заголовок X-Request-ID
// и в логгер, который дальше едет по context.
//
// Благодаря этому строки от одного запроса — приём, стадии чистки, походы к
// маршрутизатору, ответ — собираются в логе по одному ключу. Без него разобрать
// лог при четырёх воркерах разом невозможно.
//
// Идёт ПОСЛЕ Recover, но ДО Logger: паника должна попасть в лог уже с
// идентификатором.
func RequestID(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := uuid.New().String()
			w.Header().Set("X-Request-ID", id)

			reqLogger := base.With("request_id", id)
			ctx := logger.ToContext(r.Context(), reqLogger)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
