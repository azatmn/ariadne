package grpcapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
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
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.NotEmpty(t, resp.RouteCompressed)
	assert.Greater(t, resp.LengthMeters, float64(0))
	assert.Greater(t, resp.PointsCount, int32(0))
}

func TestResolveCollisions_EmptyRoute(t *testing.T) {
	h := testHandler(t)
	ctx := context.Background()

	_, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: "",
	})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestResolveCollisions_InvalidBase64(t *testing.T) {
	h := testHandler(t)
	ctx := context.Background()

	_, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: "not-valid-base64!!!",
	})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestResolveCollisions_TooManyPoints(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPoints = 2
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	ctx := context.Background()

	_, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: testRoute(t),
	})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.ResourceExhausted, st.Code())
}

func TestResolveCollisions_DecompressedTooLarge(t *testing.T) {
	cfg := testConfig()
	cfg.MaxDecompressedBytes = 1
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	ctx := context.Background()

	_, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: testRoute(t),
	})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.ResourceExhausted, st.Code())
}

func TestResolveCollisions_ReturnDebug(t *testing.T) {
	h := testHandler(t)
	ctx := context.Background()

	resp, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: testRoute(t),
		ReturnDebug:     true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.NotEmpty(t, resp.Debug)
	for _, s := range resp.Debug {
		assert.NotEmpty(t, s.Name)
	}
}

func TestResolveCollisions_NoDebugByDefault(t *testing.T) {
	h := testHandler(t)
	ctx := context.Background()

	resp, err := h.ResolveCollisions(ctx, &ariadnepb.ResolveCollisionsRequest{
		RouteCompressed: testRoute(t),
		ReturnDebug:     false,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Empty(t, resp.Debug)
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
	require.True(t, ok)
	assert.Equal(t, codes.DeadlineExceeded, st.Code())
}
