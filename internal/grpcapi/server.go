package grpcapi

import (
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	ariadnepb "ariadne/internal/gen/ariadne"
)

func NewServer(h *Handler, logger *slog.Logger, maxRecvMsgSize int, enableReflection bool) *grpc.Server {
	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxRecvMsgSize),
		grpc.ChainUnaryInterceptor(
			RecoverInterceptor(),
			RequestIDInterceptor(logger),
			LoggerInterceptor(),
		),
	)
	ariadnepb.RegisterRouteServiceServer(srv, h)

	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(srv, healthSrv)
	healthSrv.SetServingStatus("ariadne.v1.RouteService", healthpb.HealthCheckResponse_SERVING)

	if enableReflection {
		reflection.Register(srv)
	}

	return srv
}
