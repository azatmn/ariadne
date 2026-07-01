package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"ariadne/internal/logger"
	"ariadne/internal/pipeline"
	"ariadne/internal/taskstore"
)

// Handler — async-хендлер: приём задач в очередь и опрос по taskKey.
// Синхронная чистка вынесена в отдельный debug-бинарь (internal/debugapi).
type Handler struct {
	store *taskstore.Store
}

func NewHandler(store *taskstore.Store) *Handler {
	return &Handler{store: store}
}

// storeTimeout — таймаут на операции с Redis внутри хендлеров задач.
const storeTimeout = 5 * time.Second

// SubmitRequest — тело POST /v1/tasks. routeCompressed — сжатый маршрут (zlib).
type SubmitRequest struct {
	RouteCompressed string `json:"routeCompressed"`
}

// SubmitResponse — ответ на приём задачи: номерок для опроса статуса.
type SubmitResponse struct {
	TaskKey string `json:"taskKey"`
}

// StatusResponse — ответ GET /v1/tasks/{taskKey}. Result/Length есть при done,
// Error — при failed.
type StatusResponse struct {
	TaskKey         string  `json:"taskKey"`
	Status          string  `json:"status"`
	RouteCompressed string  `json:"routeCompressed,omitempty"`
	LengthMeters    float64 `json:"lengthMeters,omitempty"`
	Error           string  `json:"error,omitempty"`
}

// DebugResponse — ответ GET /v1/tasks/{taskKey}/debug.
type DebugResponse struct {
	TaskKey string                `json:"taskKey"`
	Status  string                `json:"status"`
	Debug   []pipeline.StageStats `json:"debug,omitempty"`
}

// HandleSubmit принимает маршрут, кладёт карточку в Redis и ставит её в очередь.
// НЕ декодит вход (это работа воркера) — submit обязан быть мгновенным.
func (h *Handler) HandleSubmit(w http.ResponseWriter, r *http.Request) error {
	var req SubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return &AppError{Code: CodeInvalidRequest, Message: "request body too large", Err: err}
		}
		return &AppError{Code: CodeInvalidRequest, Message: "invalid JSON body", Err: err}
	}
	if req.RouteCompressed == "" {
		return &AppError{Code: CodeInvalidRequest, Message: "routeCompressed is required"}
	}

	ctx, cancel := context.WithTimeout(r.Context(), storeTimeout)
	defer cancel()

	// taskKey уникален: SaveNew пишет через SET NX. При (астрономически
	// невероятной) коллизии uuid — генерим заново, до 3 попыток.
	var taskKey string
	for attempt := 0; attempt < 3; attempt++ {
		candidate := uuid.NewString()
		task := &taskstore.Task{
			Key:       candidate,
			Status:    taskstore.StatusPending,
			Input:     req.RouteCompressed,
			CreatedAt: time.Now().UTC(),
		}
		ok, err := h.store.SaveNew(ctx, task)
		if err != nil {
			return &AppError{Code: CodeInternal, Message: "cannot save task", Err: err}
		}
		if ok {
			taskKey = candidate
			break
		}
	}
	if taskKey == "" {
		return &AppError{Code: CodeInternal, Message: "cannot allocate unique task key"}
	}

	// Карточка уже в Redis → ставим номерок в очередь. Порядок важен: иначе
	// воркер мог бы выхватить ключ из очереди раньше, чем появилась карточка.
	if err := h.store.Enqueue(ctx, taskKey); err != nil {
		return &AppError{Code: CodeInternal, Message: "cannot enqueue task", Err: err}
	}

	return writeJSON(w, r, http.StatusAccepted, SubmitResponse{TaskKey: taskKey})
}

// HandleStatus отдаёт статус задачи и — при done — очищенный маршрут с длиной.
func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) error {
	task, err := h.getTask(r, chi.URLParam(r, "taskKey"))
	if err != nil {
		return err
	}

	resp := StatusResponse{TaskKey: task.Key, Status: string(task.Status)}
	switch task.Status {
	case taskstore.StatusDone:
		resp.RouteCompressed = task.Result
		resp.LengthMeters = task.LengthMeters
	case taskstore.StatusFailed:
		resp.Error = task.Error
	}
	return writeJSON(w, r, http.StatusOK, resp)
}

// HandleDebug отдаёт разбор по стадиям pipeline (пусто, пока задача не done).
func (h *Handler) HandleDebug(w http.ResponseWriter, r *http.Request) error {
	task, err := h.getTask(r, chi.URLParam(r, "taskKey"))
	if err != nil {
		return err
	}
	return writeJSON(w, r, http.StatusOK, DebugResponse{
		TaskKey: task.Key,
		Status:  string(task.Status),
		Debug:   task.Debug,
	})
}

// getTask читает карточку с таймаутом, маппит отсутствие в 404.
func (h *Handler) getTask(r *http.Request, taskKey string) (*taskstore.Task, error) {
	ctx, cancel := context.WithTimeout(r.Context(), storeTimeout)
	defer cancel()

	task, err := h.store.Get(ctx, taskKey)
	if errors.Is(err, taskstore.ErrNotFound) {
		return nil, &AppError{Code: CodeNotFound, Message: "task not found"}
	}
	if err != nil {
		return nil, &AppError{Code: CodeInternal, Message: "cannot read task", Err: err}
	}
	return task, nil
}

// writeJSON пишет JSON-ответ; ошибку записи только логирует (клиент уже ушёл).
func writeJSON(w http.ResponseWriter, r *http.Request, status int, payload any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logger.FromContext(r.Context()).Error("failed to write response", "error", err)
	}
	return nil
}
