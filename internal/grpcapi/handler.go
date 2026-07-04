package grpcapi

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ariadnepb "ariadne/internal/gen/ariadne"
	"ariadne/internal/taskstore"
)

// storeTimeout — таймаут на операции с Redis внутри RPC. Родитель — ctx вызова,
// чтобы отмена клиента их прерывала.
const storeTimeout = 5 * time.Second

type Handler struct {
	ariadnepb.UnimplementedRouteServiceServer
	store *taskstore.Store
}

// NewHandler собирает gRPC-хендлер. Транспорт async-only — нужен только store.
func NewHandler(store *taskstore.Store) *Handler {
	return &Handler{store: store}
}

// SubmitTask кладёт карточку в Redis и ставит её в очередь. Вход НЕ декодит —
// это работа воркера, submit обязан быть мгновенным.
func (h *Handler) SubmitTask(ctx context.Context, req *ariadnepb.SubmitTaskRequest) (*ariadnepb.SubmitTaskResponse, error) {
	if req.GetRouteCompressed() == "" {
		return nil, status.Error(codes.InvalidArgument, "route_compressed is required")
	}

	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	// taskKey уникален: SaveNew через SET NX, при коллизии uuid — до 3 попыток.
	var taskKey string
	for attempt := 0; attempt < 3; attempt++ {
		candidate := uuid.NewString()
		task := &taskstore.Task{
			Key:       candidate,
			Status:    taskstore.StatusPending,
			Input:     req.GetRouteCompressed(),
			CreatedAt: time.Now().UTC(),
		}
		ok, err := h.store.SaveNew(ctx, task)
		if err != nil {
			return nil, status.Error(codes.Internal, "cannot save task")
		}
		if ok {
			taskKey = candidate
			break
		}
	}
	if taskKey == "" {
		return nil, status.Error(codes.Internal, "cannot allocate unique task key")
	}

	// Сначала карточка, потом очередь — иначе воркер выхватит ключ до карточки.
	if err := h.store.Enqueue(ctx, taskKey); err != nil {
		return nil, status.Error(codes.Internal, "cannot enqueue task")
	}

	return &ariadnepb.SubmitTaskResponse{TaskKey: taskKey}, nil
}

// GetTask отдаёт статус и — при done — очищенный маршрут с длиной.
func (h *Handler) GetTask(ctx context.Context, req *ariadnepb.GetTaskRequest) (*ariadnepb.GetTaskResponse, error) {
	task, err := h.getTask(ctx, req.GetTaskKey())
	if err != nil {
		return nil, err
	}
	if task == nil { // недостижимо при текущем getTask; страховка от будущих правок
		return nil, status.Error(codes.Internal, "task not found")
	}

	resp := &ariadnepb.GetTaskResponse{TaskKey: task.Key, Status: string(task.Status)}
	switch task.Status {
	case taskstore.StatusDone:
		resp.RouteCompressed = task.Result
		resp.LengthMeters = task.LengthMeters
	case taskstore.StatusFailed:
		resp.Error = task.Error
	}
	return resp, nil
}

// GetTaskDebug отдаёт разбор по стадиям pipeline (пусто, пока задача не done).
func (h *Handler) GetTaskDebug(ctx context.Context, req *ariadnepb.GetTaskDebugRequest) (*ariadnepb.GetTaskDebugResponse, error) {
	task, err := h.getTask(ctx, req.GetTaskKey())
	if err != nil {
		return nil, err
	}
	if task == nil { // недостижимо при текущем getTask; страховка от будущих правок
		return nil, status.Error(codes.Internal, "task not found")
	}

	debug := make([]*ariadnepb.StageStats, len(task.Debug))
	for i, s := range task.Debug {
		debug[i] = &ariadnepb.StageStats{
			Name:         s.Name,
			PointsBefore: int32(s.PointsBefore),
			PointsAfter:  int32(s.PointsAfter),
			Elapsed:      s.Elapsed,
		}
	}
	return &ariadnepb.GetTaskDebugResponse{
		TaskKey: task.Key,
		Status:  string(task.Status),
		Debug:   debug,
	}, nil
}

// getTask читает карточку с таймаутом; пустой ключ → InvalidArgument,
// отсутствие → NotFound.
func (h *Handler) getTask(ctx context.Context, taskKey string) (*taskstore.Task, error) {
	if taskKey == "" {
		return nil, status.Error(codes.InvalidArgument, "task_key is required")
	}

	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	task, err := h.store.Get(ctx, taskKey)
	if errors.Is(err, taskstore.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "task not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot read task")
	}
	return task, nil
}
