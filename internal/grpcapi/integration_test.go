package grpcapi

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

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
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Stop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.RouteCompressed == "" {
		t.Error("route_compressed is empty")
	}
	if resp.LengthMeters <= 0 {
		t.Error("length_meters should be > 0")
	}
	if resp.PointsCount <= 0 {
		t.Error("points_count should be > 0")
	}

	ids := header.Get("x-request-id")
	if len(ids) == 0 || ids[0] == "" {
		t.Error("expected x-request-id in response metadata")
	}
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.PointsCount >= 3016 {
		t.Errorf("expected fewer points after cleanup, got %d", resp.PointsCount)
	}
	if resp.RemovedPointsCount == 0 {
		t.Error("expected some points removed")
	}
	if resp.LengthMeters <= 0 {
		t.Error("length_meters should be > 0")
	}
}

func TestIntegration_ReturnDebug(t *testing.T) {
	client := startTestServer(t)
	ctx := context.Background()

	resp, err := client.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: testRoute(t),
		ReturnDebug:     true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Debug) == 0 {
		t.Error("expected debug stats")
	}
	for _, s := range resp.Debug {
		if s.Name == "" {
			t.Error("stage name is empty")
		}
		if s.PointsBefore <= 0 {
			t.Errorf("stage %s: points_before should be > 0", s.Name)
		}
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
			if err == nil {
				t.Fatal("expected error")
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("expected gRPC status, got %v", err)
			}
			if st.Code() != tt.wantCode {
				t.Errorf("want code %s, got %s: %s", tt.wantCode, st.Code(), st.Message())
			}
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
	if err == nil {
		t.Fatal("expected error")
	}

	ids := header.Get("x-request-id")
	if len(ids) == 0 || ids[0] == "" {
		t.Error("expected x-request-id in response metadata even on error")
	}
}

func TestIntegration_MaxRecvMsgSize(t *testing.T) {
	cfg := testConfig()
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(h, logger, 100, false) // 100 bytes limit

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Stop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
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
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.ResolveCollisions(context.Background(), &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: encoded,
	})
	if err == nil {
		t.Fatal("expected error for message exceeding size limit")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status, got %v", err)
	}
	if st.Code() != codes.ResourceExhausted {
		t.Errorf("want code ResourceExhausted, got %s: %s", st.Code(), st.Message())
	}
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
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Stop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := ariadnepb.NewRouteServiceClient(conn)

	_, err = client.ResolveCollisions(context.Background(), &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: "test",
	})
	if err == nil {
		t.Fatal("expected error after panic")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status, got %v", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("want code Internal, got %s", st.Code())
	}
}

func TestIntegration_HealthCheck(t *testing.T) {
	cfg := testConfig()
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewServer(h, logger, 10<<20, false)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Stop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	healthClient := healthpb.NewHealthClient(conn)

	resp, err := healthClient.Check(context.Background(), &healthpb.HealthCheckRequest{
		Service: "ariadne.v1.RouteService",
	})
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("want SERVING, got %s", resp.Status)
	}

	// Пустой service = общий статус сервера
	resp, err = healthClient.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check (empty service) failed: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("want SERVING for empty service, got %s", resp.Status)
	}
}

type panicHandler struct {
	ariadnepb.UnimplementedRouteServiceServer
}

func (p *panicHandler) ResolveCollisions(context.Context, *ariadnepb.ResolveCollisionsRequest) (*ariadnepb.ResolveCollisionsResponse, error) {
	panic("test panic")
}
