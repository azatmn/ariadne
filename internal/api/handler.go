package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"ariadne/internal/codec"
	"ariadne/internal/service"
)

type ResolveRequest struct {
	RouteCompressed string `json:"routeCompressed"`
	ReturnDebug     bool   `json:"returnDebug,omitempty"`
}

// ResolveResponse — успешный ответ.
type ResolveResponse struct {
	RouteCompressed    string   `json:"routeCompressed"`
	LengthMeters       float64  `json:"lengthMeters"`
	LengthBeforeMeters float64  `json:"lengthBeforeMeters"`
	PointsCount        int      `json:"pointsCount"`
	RemovedPointsCount int      `json:"removedPointsCount"`
	Warnings           []string `json:"warnings,omitempty"`
	Debug              any      `json:"debug,omitempty"`
}

type Handler struct {
	svc *service.Service
}

func NewHandler(svc *service.Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) HandleResolve(w http.ResponseWriter, r *http.Request) error {
	var req ResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return &AppError{Code: CodeInvalidRequest, Message: "invalid JSON body", Err: err}
	}

	if req.RouteCompressed == "" {
		return &AppError{Code: CodeInvalidRequest, Message: "routeCompressed is required"}
	}

	points, err := codec.Decode(req.RouteCompressed)
	if err != nil {
		return &AppError{Code: CodeInvalidRouteFormat, Message: "cannot decode routeCompressed", Err: err}
	}

	result, err := h.svc.Resolve(r.Context(), points)
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
	_ = json.NewEncoder(w).Encode(resp)
	return nil
}

func (h *Handler) mapError(err error) *AppError {
	switch {
	case errors.Is(err, service.ErrTooManyPoints):
		return &AppError{Code: CodeRouteTooLarge, Message: "too many points"}
	case errors.Is(err, service.ErrTooFewPoints):
		return &AppError{Code: CodeUnprocessableRoute, Message: "route too short after processing"}
	default:
		return &AppError{Code: CodeInternal, Message: "pipeline error", Err: err}
	}
}
