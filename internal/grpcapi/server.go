package grpcapi

import (
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	ariadnepb "ariadne/internal/gen/ariadne"
)

// NewServer собирает gRPC-сервер с перехватчиками.
//
// Порядок в цепочке важен и обратный привычному: первым идёт Recover, чтобы
// накрыть собой остальные, следом RequestID — иначе Logger и запись о панике
// вышли бы без идентификатора вызова.
//
// enableReflection включает отражение, по которому grpcurl и Postman видят
// список методов. В бою держится выключенным: наружу не стоит объявлять то,
// что у тебя есть.
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
