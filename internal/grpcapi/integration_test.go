package grpcapi

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"ariadne/internal/codec"
	ariadnepb "ariadne/internal/gen/ariadne"
	"ariadne/internal/geo"
	"ariadne/internal/service"
)

func startTestServer(t *testing.T) ariadnepb.RouteServiceClient {
	t.Helper()

	cfg := testConfig()
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
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

func TestIntegration_HappyPath(t *testing.T) {
	client := startTestServer(t)

	var header metadata.MD
	resp, err := client.ResolveCollisions(
		context.Background(),
		&ariadnepb.ResolveCollisionsRequest{RouteCompressed: testRoute(t)},
		grpc.Header(&header),
	)
	require.NoError(t, err)

	assert.NotEmpty(t, resp.RouteCompressed)
	assert.Greater(t, resp.LengthMeters, float64(0))
	assert.Greater(t, resp.PointsCount, int32(0))

	ids := header.Get("x-request-id")
	require.NotEmpty(t, ids)
	assert.NotEmpty(t, ids[0])
}

func TestIntegration_RealRoute(t *testing.T) {
	routeData, err := os.ReadFile("../api/testdata/route_3016.txt")
	if err != nil {
		t.Skipf("testdata not available: %v", err)
	}

	client := startTestServer(t)
	ctx := context.Background()

	resp, err := client.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: strings.TrimSpace(string(routeData)),
	})
	require.NoError(t, err)

	assert.Less(t, resp.PointsCount, int32(3016))
	assert.NotZero(t, resp.RemovedPointsCount)
	assert.Greater(t, resp.LengthMeters, float64(0))
}

func TestIntegration_ReturnDebug(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	resp, err := client.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: testRoute(t),
		ReturnDebug:     true,
	})
	require.NoError(t, err)

	assert.NotEmpty(t, resp.Debug)
	for _, s := range resp.Debug {
		assert.NotEmpty(t, s.Name)
		assert.Greater(t, s.PointsBefore, int32(0))
	}
}

func TestIntegration_Errors(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	tests := []struct {
		name     string
		req      *ariadnepb.ResolveCollisionsRequest
		wantCode codes.Code
	}{
		{
			name:     "empty route",
			req:      &ariadnepb.ResolveCollisionsRequest{RouteCompressed: ""},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "invalid base64",
			req:      &ariadnepb.ResolveCollisionsRequest{RouteCompressed: "!!!invalid!!!"},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.ResolveCollisions(ctx, tt.req)
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tt.wantCode, st.Code())
		})
	}
}

func TestIntegration_RequestIDOnError(t *testing.T) {
	client := startTestServer(t)

	var header metadata.MD
	_, err := client.ResolveCollisions(
		context.Background(),
		&ariadnepb.ResolveCollisionsRequest{RouteCompressed: ""},
		grpc.Header(&header),
	)
	require.Error(t, err)

	ids := header.Get("x-request-id")
	require.NotEmpty(t, ids)
	assert.NotEmpty(t, ids[0])
}

func TestIntegration_MaxRecvMsgSize(t *testing.T) {
	cfg := testConfig()
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(h, logger, 100, false) // 100 bytes limit

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

	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	points := make([]geo.Point, 100)
	for i := range points {
		points[i] = geo.Point{
			Time: t0.Add(time.Duration(i) * time.Second),
			Lon:  37.617 + float64(i)*0.0001,
			Lat:  55.755 + float64(i)*0.0001,
		}
	}
	encoded, err := codec.Encode(points)
	require.NoError(t, err)

	_, err = client.ResolveCollisions(context.Background(), &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: encoded,
	})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.ResourceExhausted, st.Code())
}

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

	_, err = client.ResolveCollisions(context.Background(), &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: "test",
	})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
}

func TestIntegration_HealthCheck(t *testing.T) {
	cfg := testConfig()
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
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

func (p *panicHandler) ResolveCollisions(context.Context, *ariadnepb.ResolveCollisionsRequest) (*ariadnepb.ResolveCollisionsResponse, error) {
	panic("test panic")
}
