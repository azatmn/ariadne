package grpcapi

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"ariadne/internal/codec"
	"ariadne/internal/config"
	ariadnepb "ariadne/internal/gen/ariadne"
	"ariadne/internal/geo"
	"ariadne/internal/service"
)

func testConfig() *config.Config {
	return &config.Config{
		DedupDistanceMeters:  2.0,
		DedupTimeGap:         60 * time.Second,
		MaxPoints:            50000,
		IntersectMaxIter:     100,
		MaxSpeedKmh:          150,
		MaxLoopMeters:        100,
		MaxLoopSeconds:       10,
		MaxDecompressedBytes: 100 << 20,
		ResolveTimeout:       25 * time.Second,
	}
}

func testRoute(t *testing.T) string {
	t.Helper()
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	points := []geo.Point{
		{Time: t0, Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(10 * time.Second), Lon: 37.617400, Lat: 55.755900},
		{Time: t0.Add(20 * time.Second), Lon: 37.617500, Lat: 55.756000},
		{Time: t0.Add(30 * time.Second), Lon: 37.617600, Lat: 55.756100},
	}
	encoded, err := codec.Encode(points)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func testHandler(t *testing.T) *Handler {
	t.Helper()
	cfg := testConfig()
	return NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
}

func TestResolveCollisions_HappyPath(t *testing.T) {
	h := testHandler(t)
	ctx := context.Background()

	resp, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: testRoute(t),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("response is nil")
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
}

func TestResolveCollisions_EmptyRoute(t *testing.T) {
	h := testHandler(t)
	ctx := context.Background()

	_, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: "",
	})
	if err == nil {
		t.Fatal("expected error for empty route")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("want code InvalidArgument, got %s", st.Code())
	}
}

func TestResolveCollisions_InvalidBase64(t *testing.T) {
	h := testHandler(t)
	ctx := context.Background()

	_, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: "not-valid-base64!!!",
	})
	if err == nil {
		t.Fatal("expected error for invalid data")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("want code InvalidArgument, got %s", st.Code())
	}
}

func TestResolveCollisions_TooManyPoints(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPoints = 2
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	ctx := context.Background()

	_, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: testRoute(t),
	})
	if err == nil {
		t.Fatal("expected error for too many points")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.ResourceExhausted {
		t.Errorf("want code ResourceExhausted, got %s", st.Code())
	}
}

func TestResolveCollisions_DecompressedTooLarge(t *testing.T) {
	cfg := testConfig()
	cfg.MaxDecompressedBytes = 1
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	ctx := context.Background()

	_, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: testRoute(t),
	})
	if err == nil {
		t.Fatal("expected error for decompressed too large")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.ResourceExhausted {
		t.Errorf("want code ResourceExhausted, got %s", st.Code())
	}
}

func TestResolveCollisions_ReturnDebug(t *testing.T) {
	h := testHandler(t)
	ctx := context.Background()

	resp, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: testRoute(t),
		ReturnDebug:     true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("response is nil")
	}

	if len(resp.Debug) == 0 {
		t.Error("expected debug stats when returnDebug=true")
	}
	for _, s := range resp.Debug {
		if s.Name == "" {
			t.Error("stage name should not be empty")
		}
	}
}

func TestResolveCollisions_NoDebugByDefault(t *testing.T) {
	h := testHandler(t)
	ctx := context.Background()

	resp, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: testRoute(t),
		ReturnDebug:     false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("response is nil")
	}

	if len(resp.Debug) != 0 {
		t.Errorf("expected no debug stats by default, got %d", len(resp.Debug))
	}
}

func TestResolveCollisions_Timeout(t *testing.T) {
	cfg := testConfig()
	cfg.ResolveTimeout = 1 * time.Nanosecond
	cfg.IntersectMaxIter = 1_000_000
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)

	ctx := context.Background()

	_, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: testRoute(t),
	})
	if err == nil {
		return
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.DeadlineExceeded {
		t.Errorf("want code DeadlineExceeded, got %s", st.Code())
	}
}
