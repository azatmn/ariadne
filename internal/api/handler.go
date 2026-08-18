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
// Error — при failed. У lengthMeters НЕТ omitempty: у done валидная длина может
// быть ровно 0 (все точки в одной координате), и поле должно присутствовать,
// чтобы клиент отличал «длина 0» от «поля нет». В gRPC это и так работает
// (proto3 всегда шлёт скалярный 0).
//
// Оговорка о неполном результате приехала отдельным полем degraded, а НЕ новым
// значением status. Новое значение сломало бы живого клиента: он проверяет
// status == "done", не совпало бы — и готовый результат ушёл бы в мусор, а
// задача считалась бы вечно незавершённой. Лишнее поле старый клиент не заметит.
//
// Комментарии к полям здесь уходят в swagger как есть, целиком: swag берёт весь
// блок, а doc-комментарий типа игнорирует. Потому подробности живут тут, а над
// полем остаётся строка, которую не стыдно показать в Swagger UI.
type StatusResponse struct {
	TaskKey         string  `json:"taskKey"`
	Status          string  `json:"status"`
	RouteCompressed string  `json:"routeCompressed,omitempty"`
	LengthMeters    float64 `json:"lengthMeters"`
	Error           string  `json:"error,omitempty"`

	// Километражу верить с оговоркой: часть дыр осталась прямыми через поля.
	Degraded bool `json:"degraded,omitempty"`

	// Та же оговорка словами, для человека при разборе.
	Warnings []string `json:"warnings,omitempty"`
}

// DebugResponse — ответ GET /v1/tasks/{taskKey}/debug.
type DebugResponse struct {
	TaskKey string                `json:"taskKey"`
	Status  string                `json:"status"`
	Debug   []pipeline.StageStats `json:"debug,omitempty"`
}

// HandleSubmit принимает маршрут, кладёт карточку в Redis и ставит её в очередь.
// НЕ декодит вход (это работа воркера) — submit обязан быть мгновенным.
// @Summary      Сдать маршрут в очередь на очистку
// @Description  Принимает сжатый маршрут, ставит задачу в очередь, сразу отдаёт taskKey. Обработка — в фоне; результат забирать по taskKey.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Param        body body SubmitRequest true "Маршрут для обработки"
// @Success      202 {object} SubmitResponse
// @Failure      400 {object} ErrorPayload
// @Failure      500 {object} ErrorPayload
// @Router       /v1/tasks [post]
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
// @Summary      Статус и результат задачи
// @Description  По taskKey возвращает статус (pending/done/failed); при done — очищенный маршрут и длину, при failed — текст ошибки.
// @Tags         tasks
// @Produce      json
// @Param        taskKey path string true "Ключ задачи (из submit)"
// @Success      200 {object} StatusResponse
// @Failure      404 {object} ErrorPayload
// @Failure      500 {object} ErrorPayload
// @Router       /v1/tasks/{taskKey} [get]
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
		resp.Degraded = task.Degraded
		resp.Warnings = task.Warnings
	case taskstore.StatusFailed:
		resp.Error = task.Error
	}
	return writeJSON(w, r, http.StatusOK, resp)
}

// HandleDebug отдаёт разбор по стадиям pipeline (пусто, пока задача не done).
// @Summary      Разбор задачи по стадиям pipeline
// @Description  По taskKey возвращает статистику по каждой стадии очистки (заполнена при done).
// @Tags         tasks
// @Produce      json
// @Param        taskKey path string true "Ключ задачи (из submit)"
// @Success      200 {object} DebugResponse
// @Failure      404 {object} ErrorPayload
// @Failure      500 {object} ErrorPayload
// @Router       /v1/tasks/{taskKey}/debug [get]
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
