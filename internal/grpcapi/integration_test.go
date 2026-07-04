package grpcapi

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	ariadnepb "ariadne/internal/gen/ariadne"
)

func startTestServer(t *testing.T) ariadnepb.RouteServiceClient {
	t.Helper()

	store, _ := testStore(t)
	h := NewHandler(store)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(h, logger, 10<<20, false)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "failed to listen")

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Stop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err, "failed to dial")
	t.Cleanup(func() { _ = conn.Close() })

	return ariadnepb.NewRouteServiceClient(conn)
}

// submit проходит весь стек сервера: возвращает taskKey и x-request-id в хедере.
func TestIntegration_SubmitReturnsKey(t *testing.T) {
	client := startTestServer(t)

	var header metadata.MD
	resp, err := client.SubmitTask(
		context.Background(),
		&ariadnepb.SubmitTaskRequest{RouteCompressed: testRoute(t)},
		grpc.Header(&header),
	)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetTaskKey())

	ids := header.Get("x-request-id")
	require.NotEmpty(t, ids)
	assert.NotEmpty(t, ids[0])
}

// полный round-trip по сети: submit → get. Воркера в тест-сервере нет, поэтому pending.
func TestIntegration_SubmitThenGet(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	sub, err := client.SubmitTask(ctx, &ariadnepb.SubmitTaskRequest{RouteCompressed: testRoute(t)})
	require.NoError(t, err)

	got, err := client.GetTask(ctx, &ariadnepb.GetTaskRequest{TaskKey: sub.GetTaskKey()})
	require.NoError(t, err)
	assert.Equal(t, sub.GetTaskKey(), got.GetTaskKey())
	assert.Equal(t, "pending", got.GetStatus())
}

func TestIntegration_Errors(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	tests := []struct {
		name     string
		run      func() error
		wantCode codes.Code
	}{
		{
			name: "empty route on submit",
			run: func() error {
				_, e := client.SubmitTask(ctx, &ariadnepb.SubmitTaskRequest{RouteCompressed: ""})
				return e
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "unknown task on get",
			run:      func() error { _, e := client.GetTask(ctx, &ariadnepb.GetTaskRequest{TaskKey: "missing"}); return e },
			wantCode: codes.NotFound,
		},
		{
			name:     "empty key on get",
			run:      func() error { _, e := client.GetTask(ctx, &ariadnepb.GetTaskRequest{TaskKey: ""}); return e },
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tt.wantCode, st.Code())
		})
	}
}

// RequestID-перехватчик кладёт x-request-id в хедер даже на ошибке.
func TestIntegration_RequestIDOnError(t *testing.T) {
	client := startTestServer(t)

	var header metadata.MD
	_, err := client.SubmitTask(
		context.Background(),
		&ariadnepb.SubmitTaskRequest{RouteCompressed: ""},
		grpc.Header(&header),
	)
	require.Error(t, err)

	ids := header.Get("x-request-id")
	require.NotEmpty(t, ids)
	assert.NotEmpty(t, ids[0])
}

// Сервер режет запрос больше лимита размера.
func TestIntegration_MaxRecvMsgSize(t *testing.T) {
	store, _ := testStore(t)
	h := NewHandler(store)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(h, logger, 100, false) // лимит 100 байт

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "failed to listen")
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Stop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err, "failed to dial")
	t.Cleanup(func() { _ = conn.Close() })

	client := ariadnepb.NewRouteServiceClient(conn)

	big := strings.Repeat("x", 500) // запрос заведомо > 100 байт
	_, err = client.SubmitTask(context.Background(), &ariadnepb.SubmitTaskRequest{RouteCompressed: big})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.ResourceExhausted, st.Code())
}

// Recover-перехватчик превращает панику хендлера в codes.Internal, не роняя сервер.
func TestIntegration_RecoverInterceptor(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(10<<20),
		grpc.ChainUnaryInterceptor(
			RecoverInterceptor(),
			RequestIDInterceptor(logger),
			LoggerInterceptor(),
		),
	)

	ariadnepb.RegisterRouteServiceServer(srv, &panicHandler{})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "failed to listen")
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Stop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err, "failed to dial")
	t.Cleanup(func() { _ = conn.Close() })

	client := ariadnepb.NewRouteServiceClient(conn)

	_, err = client.SubmitTask(context.Background(), &ariadnepb.SubmitTaskRequest{RouteCompressed: "test"})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
}

func TestIntegration_HealthCheck(t *testing.T) {
	store, _ := testStore(t)
	h := NewHandler(store)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(h, logger, 10<<20, false)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "failed to listen")
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Stop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err, "failed to dial")
	t.Cleanup(func() { _ = conn.Close() })

	healthClient := healthpb.NewHealthClient(conn)

	resp, err := healthClient.Check(context.Background(), &healthpb.HealthCheckRequest{
		Service: "ariadne.v1.RouteService",
	})
	require.NoError(t, err, "health check failed")
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.Status)

	// Пустой service = общий статус сервера
	resp, err = healthClient.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err, "health check (empty service) failed")
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.Status)
}

type panicHandler struct {
	ariadnepb.UnimplementedRouteServiceServer
}

func (p *panicHandler) SubmitTask(context.Context, *ariadnepb.SubmitTaskRequest) (*ariadnepb.SubmitTaskResponse, error) {
	panic("test panic")
}
