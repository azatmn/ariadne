package grpcapi

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"ariadne/internal/codec"
	ariadnepb "ariadne/internal/gen/ariadne"
	"ariadne/internal/service"
)

type Handler struct {
	ariadnepb.UnimplementedRouteServiceServer
	svc                  *service.Service
	maxDecompressedBytes int64
	resolveTimeout       time.Duration
}

func NewHandler(svc *service.Service, maxDecompressedBytes int64, resolveTimeout time.Duration) *Handler {
	return &Handler{
		svc:                  svc,
		maxDecompressedBytes: maxDecompressedBytes,
		resolveTimeout:       resolveTimeout,
	}
}

func (h *Handler) ResolveCollisions(ctx context.Context, req *ariadnepb.ResolveCollisionsRequest) (*ariadnepb.ResolveCollisionsResponse, error) {
	if req.GetRouteCompressed() == "" {
		return nil, status.Error(codes.InvalidArgument, "route_compressed is required")
	}

	points, err := codec.Decode(req.GetRouteCompressed(), h.maxDecompressedBytes)
	if err != nil {
		if errors.Is(err, codec.ErrDecompressedTooLarge) {
			return nil, status.Error(codes.ResourceExhausted, "decompressed data too large")
		}
		return nil, status.Error(codes.InvalidArgument, "cannot decode route_compressed")
	}

	ctx, cancel := context.WithTimeout(ctx, h.resolveTimeout)
	defer cancel()

	result, err := h.svc.Resolve(ctx, points)
	if err != nil {
		return nil, h.mapError(err)
	}

	encoded, err := codec.Encode(result.Points)
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot encode result")
	}

	resp := &ariadnepb.ResolveCollisionsResponse{
		RouteCompressed:    encoded,
		LengthMeters:       result.LengthMeters,
		LengthBeforeMeters: result.BeforeLenMeters,
		PointsCount:        int32(len(result.Points)),
		RemovedPointsCount: int32(result.BeforeCount - len(result.Points)),
		Warnings:           result.Warnings,
	}

	if req.GetReturnDebug() {
		resp.Debug = make([]*ariadnepb.StageStats, len(result.Stats))
		for i, s := range result.Stats {
			resp.Debug[i] = &ariadnepb.StageStats{
				Name:         s.Name,
				PointsBefore: int32(s.PointsBefore),
				PointsAfter:  int32(s.PointsAfter),
				Elapsed:      s.Elapsed,
			}
		}
	}

	return resp, nil
}

func (h *Handler) mapError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "processing timeout")
	case errors.Is(err, service.ErrTooManyPoints):
		return status.Error(codes.ResourceExhausted, "too many points")
	case errors.Is(err, service.ErrTooFewPoints):
		return status.Error(codes.InvalidArgument, "route too short after processing")
	default:
		return status.Error(codes.Internal, "pipeline error")
	}
}
