package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"ariadne/internal/codec"
	"ariadne/internal/logger"
	"ariadne/internal/pipeline"
	"ariadne/internal/service"
	"ariadne/internal/taskstore"
)

type ResolveRequest struct {
	RouteCompressed string `json:"routeCompressed" example:"eJy..." `
	ReturnDebug     bool   `json:"returnDebug,omitempty" example:"false"`
}

type ResolveResponse struct {
	RouteCompressed    string                `json:"routeCompressed" example:"eJy..."`
	LengthMeters       float64               `json:"lengthMeters" example:"1168113.85"`
	LengthBeforeMeters float64               `json:"lengthBeforeMeters" example:"1170260.79"`
	PointsCount        int                   `json:"pointsCount" example:"2981"`
	RemovedPointsCount int                   `json:"removedPointsCount" example:"35"`
	Warnings           []string              `json:"warnings,omitempty" example:"intersect: max iterations reached"`
	Debug              []pipeline.StageStats `json:"debug,omitempty"`
}

type Handler struct {
	svc                  *service.Service
	store                *taskstore.Store
	maxDecompressedBytes int64
	resolveTimeout       time.Duration
}

// NewHandler собирает HTTP-хендлер. svc нужен синхронному /resolve-collisions
// (под снос), store — асинхронным /v1/tasks. Синхронные тесты передают store=nil.
func NewHandler(svc *service.Service, store *taskstore.Store, maxDecompressedBytes int64, resolveTimeout time.Duration) *Handler {
	return &Handler{svc: svc, store: store, maxDecompressedBytes: maxDecompressedBytes, resolveTimeout: resolveTimeout}
}

// HandleResolve обрабатывает маршрут через pipeline очистки.
// @Summary      Устранение коллизий GPS-маршрута
// @Description  Принимает сжатый маршрут, прогоняет через pipeline (sort, speed filter, dedup, intersections), возвращает очищенный результат
// @Tags         routes
// @Accept       json
// @Produce      json
// @Param        body body ResolveRequest true "Маршрут для обработки"
// @Success      200 {object} ResolveResponse
// @Failure      400 {object} ErrorPayload
// @Failure      413 {object} ErrorPayload
// @Failure      422 {object} ErrorPayload
// @Failure      500 {object} ErrorPayload
// @Router       /v1/routes/resolve-collisions [post]
func (h *Handler) HandleResolve(w http.ResponseWriter, r *http.Request) error {
	var req ResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return &AppError{Code: CodeInvalidRequest, Message: "request body too large", Err: err}
		}
		return &AppError{Code: CodeInvalidRequest, Message: "invalid JSON body", Err: err}
	}

	if req.RouteCompressed == "" {
		return &AppError{Code: CodeInvalidRequest, Message: "routeCompressed is required"}
	}

	points, err := codec.Decode(req.RouteCompressed, h.maxDecompressedBytes)
	if err != nil {
		if errors.Is(err, codec.ErrDecompressedTooLarge) {
			return &AppError{Code: CodeRouteTooLarge, Message: "decompressed data too large", Err: err}
		}
		return &AppError{Code: CodeInvalidRouteFormat, Message: "cannot decode routeCompressed", Err: err}
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.resolveTimeout)
	defer cancel()

	result, err := h.svc.Resolve(ctx, points)
	if err != nil {
		return h.mapError(err)
	}

	encoded, err := codec.Encode(result.Points)
	if err != nil {
		return &AppError{Code: CodeInternal, Message: "cannot encode result", Err: err}
	}

	resp := ResolveResponse{
		RouteCompressed:    encoded,
		LengthMeters:       result.LengthMeters,
		LengthBeforeMeters: result.BeforeLenMeters,
		PointsCount:        len(result.Points),
		RemovedPointsCount: result.BeforeCount - len(result.Points),
		Warnings:           result.Warnings,
	}

	if req.ReturnDebug {
		resp.Debug = result.Stats
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.FromContext(r.Context()).Error("failed to write response", "error", err)
	}
	return nil
}

func (h *Handler) mapError(err error) *AppError {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &AppError{Code: CodeInternal, Message: "processing timeout", Err: err}
	case errors.Is(err, service.ErrTooManyPoints):
		return &AppError{Code: CodeRouteTooLarge, Message: "too many points"}
	case errors.Is(err, service.ErrTooFewPoints):
		return &AppError{Code: CodeUnprocessableRoute, Message: "route too short after processing"}
	default:
		return &AppError{Code: CodeInternal, Message: "pipeline error", Err: err}
	}
}
