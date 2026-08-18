// Package grpcapi — gRPC-транспорт поверх того же service.Service, что и HTTP.
//
// Второй транспорт заведён по требованию backend: HTTP остаётся основным, gRPC
// нужен для вызовов между сервисами. Логика чистки в обоих одна и та же —
// расходятся они только в перехватчиках и в форме ошибок.
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

// RequestIDInterceptor выдаёт вызову идентификатор, кладёт его в логгер и в
// метаданные ответа заголовком x-request-id.
//
// В тело ответа идентификатор не попадает — так же, как в HTTP, где он живёт
// только в заголовке. Клиент, которому нужно сослаться на вызов, берёт его
// оттуда.
func RequestIDInterceptor(base *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := uuid.New().String()
		reqLogger := base.With("request_id", id)
		ctx = logger.ToContext(ctx, reqLogger)

		_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", id))

		return handler(ctx, req)
	}
}

// LoggerInterceptor пишет строку на завершённый вызов: метод, код, время.
// Логгер берётся из context, поэтому строка уже помечена идентификатором —
// ставить перехватчик надо ПОСЛЕ RequestIDInterceptor.
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

// RecoverInterceptor ловит панику в обработчике и превращает её в codes.Internal.
//
// Без него паника уносит весь процесс вместе со всеми параллельными вызовами.
// Наружу уходит только «internal error»; стек остаётся в логе.
//
// Именованное возвращаемое значение err здесь обязательно: подменить ответ из
// defer можно только через него.
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
