// Package debugapi — синхронная ручка чистки маршрута ТОЛЬКО для фронт-отладчика
// (ariadne-debug-proxy). В прод-сервисе её нет: там всё async. Живёт отдельно,
// чтобы прод `internal/api` был чисто асинхронным. Запускается через
// cmd/debugserver. Переиспользует ошибки/middleware из internal/api.
package debugapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"ariadne/internal/api"
	"ariadne/internal/codec"
	"ariadne/internal/logger"
	"ariadne/internal/pipeline"
	"ariadne/internal/service"
)

type ResolveRequest struct {
	RouteCompressed string `json:"routeCompressed"`
	ReturnDebug     bool   `json:"returnDebug,omitempty"`
}

type ResolveResponse struct {
	RouteCompressed    string                `json:"routeCompressed"`
	LengthMeters       float64               `json:"lengthMeters"`
	LengthBeforeMeters float64               `json:"lengthBeforeMeters"`
	PointsCount        int                   `json:"pointsCount"`
	RemovedPointsCount int                   `json:"removedPointsCount"`
	Warnings           []string              `json:"warnings,omitempty"`
	Debug              []pipeline.StageStats `json:"debug,omitempty"`
}

type Handler struct {
	svc                  *service.Service
	maxDecompressedBytes int64
	resolveTimeout       time.Duration
}

func NewHandler(svc *service.Service, maxDecompressedBytes int64, resolveTimeout time.Duration) *Handler {
	return &Handler{svc: svc, maxDecompressedBytes: maxDecompressedBytes, resolveTimeout: resolveTimeout}
}

// HandleResolve синхронно прогоняет маршрут через pipeline и отдаёт результат.
func (h *Handler) HandleResolve(w http.ResponseWriter, r *http.Request) error {
	var req ResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return &api.AppError{Code: api.CodeInvalidRequest, Message: "request body too large", Err: err}
		}
		return &api.AppError{Code: api.CodeInvalidRequest, Message: "invalid JSON body", Err: err}
	}

	if req.RouteCompressed == "" {
		return &api.AppError{Code: api.CodeInvalidRequest, Message: "routeCompressed is required"}
	}

	points, err := codec.Decode(req.RouteCompressed, h.maxDecompressedBytes)
	if err != nil {
		if errors.Is(err, codec.ErrDecompressedTooLarge) {
			return &api.AppError{Code: api.CodeRouteTooLarge, Message: "decompressed data too large", Err: err}
		}
		return &api.AppError{Code: api.CodeInvalidRouteFormat, Message: "cannot decode routeCompressed", Err: err}
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.resolveTimeout)
	defer cancel()

	result, err := h.svc.Resolve(ctx, points)
	if err != nil {
		return mapError(err)
	}

	encoded, err := codec.Encode(result.Points)
	if err != nil {
		return &api.AppError{Code: api.CodeInternal, Message: "cannot encode result", Err: err}
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

func mapError(err error) *api.AppError {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &api.AppError{Code: api.CodeInternal, Message: "processing timeout", Err: err}
	case errors.Is(err, service.ErrTooManyPoints):
		return &api.AppError{Code: api.CodeRouteTooLarge, Message: "too many points"}
	case errors.Is(err, service.ErrTooFewPoints):
		return &api.AppError{Code: api.CodeUnprocessableRoute, Message: "route too short after processing"}
	default:
		return &api.AppError{Code: api.CodeInternal, Message: "pipeline error", Err: err}
	}
}
