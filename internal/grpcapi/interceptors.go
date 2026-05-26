package grpcapi

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"ariadne/internal/logger"

	"github.com/google/uuid"
)

func RequestIDInterceptor(base *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := uuid.New().String()
		reqLogger := base.With("request_id", id)
		ctx = logger.ToContext(ctx, reqLogger)

		_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", id))

		return handler(ctx, req)
	}
}

func LoggerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()

		resp, err := handler(ctx, req)

		log := logger.FromContext(ctx)
		code := status.Code(err)
		log.Info("grpc request",
			"method", info.FullMethod,
			"code", code.String(),
			"elapsed", time.Since(start),
		)

		return resp, err
	}
}

func RecoverInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				log := logger.FromContext(ctx)
				log.Error("panic recovered", "method", info.FullMethod, "panic", rec, "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}
