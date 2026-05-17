package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"ariadne/internal/codec"
	"ariadne/internal/config"
	"ariadne/internal/geo"
	"ariadne/internal/pipeline"
)

type ResolveRequest struct {
	RouteCompressed string `json:"routeCompressed"`
	ReturnDebug     bool   `json:"returnDebug,omitempty"`
}

// ResolveResponse — успешный ответ.
type ResolveResponse struct {
	RouteCompressed        string   `json:"routeCompressed"`
	LengthMeters           float64  `json:"lengthMeters"`
	PointsCount            int      `json:"pointsCount,omitempty"`
	RemovedPointsCount     int      `json:"removedPointsCount,omitempty"`
	RemovedCollisionsCount int      `json:"removedCollisionsCount,omitempty"`
	LengthBeforeMeters     float64  `json:"lengthBeforeMeters,omitempty"`
	Warnings               []string `json:"warnings,omitempty"`
	Debug                  any      `json:"debug,omitempty"`
}

type Handler struct {
	cfg    *config.Config
	logger *slog.Logger
}

func NewHandler(cfg *config.Config, logger *slog.Logger) *Handler {
	return &Handler{cfg: cfg, logger: logger}
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

	if len(points) > h.cfg.MaxPoints {
		return &AppError{Code: CodeRouteTooLarge, Message: "too many points"}
	}

	beforeCount := len(points)
	beforeMeters := geo.TotalLength(points)

	params := h.buildParams()
	pl := pipeline.New(params, h.logger)

	cleaned, warnings, err := pl.Run(points)
	if err != nil {
		return &AppError{Code: CodeInternal, Message: "pipeline error", Err: err}
	}

	for _, msg := range warnings {
		h.logger.Warn("pipeline warning", "warning", msg)
	}

	if len(cleaned) < 2 {
		return &AppError{Code: CodeUnprocessableRoute, Message: "route too short after processing"}
	}

	encoded, err := codec.Encode(cleaned)
	if err != nil {
		return &AppError{Code: CodeInternal, Message: "cannot encode result", Err: err}
	}

	resp := ResolveResponse{
		RouteCompressed:    encoded,
		LengthMeters:       geo.TotalLength(cleaned),
		PointsCount:        len(cleaned),
		RemovedPointsCount: beforeCount - len(cleaned),
		LengthBeforeMeters: beforeMeters,
		Warnings:           warnings,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	return nil
}

func (h *Handler) buildParams() pipeline.Params {
	return pipeline.Params{
		DedupDistanceMeters: h.cfg.DedupDistanceMeters,
		DedupTimeGap:        h.cfg.DedupTimeGap,
		IntersectMaxIter:    h.cfg.IntersectMaxIter,
		MaxSpeedKmh:         h.cfg.MaxSpeedKmh,
		MaxLoopMeters:       h.cfg.MaxLoopMeters,
		MaxLoopSeconds:      h.cfg.MaxLoopSeconds,
	}
}
