package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	require.NoError(t, err, "Load")

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Port", cfg.Port, "8080"},
		{"ReadTimeout", cfg.ReadTimeout, 10 * time.Second},
		{"WriteTimeout", cfg.WriteTimeout, 30 * time.Second},
		{"IdleTimeout", cfg.IdleTimeout, 2 * time.Minute},
		{"ShutdownTimeout", cfg.ShutdownTimeout, 15 * time.Second},
		{"MaxBodyBytes", cfg.MaxBodyBytes, int64(10 << 20)},
		{"MaxDecompressedBytes", cfg.MaxDecompressedBytes, int64(20 << 20)},
		{"DedupDistanceMeters", cfg.DedupDistanceMeters, 2.0},
		{"DedupTimeGap", cfg.DedupTimeGap, 60 * time.Second},
		{"MaxPoints", cfg.MaxPoints, 50_000},
		{"IntersectMaxIter", cfg.IntersectMaxIter, 10_000},
		{"MaxSpeedKmh", cfg.MaxSpeedKmh, 150.0},
		{"MaxAccelKmhPerSec", cfg.MaxAccelKmhPerSec, 20.0},
		{"MaxLoopMeters", cfg.MaxLoopMeters, 100.0},
		{"MaxLoopSeconds", cfg.MaxLoopSeconds, 10.0},
		{"SimplifyMinMeters", cfg.SimplifyMinMeters, 5.0},
		{"ResolveTimeout", cfg.ResolveTimeout, 25 * time.Second},
		{"LogLevel", cfg.LogLevel, "info"},
	}

	for _, c := range checks {
		assert.Equal(t, c.want, c.got, c.name)
	}
}

func TestLoadCustomEnv(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("READ_TIMEOUT", "5s")
	t.Setenv("MAX_BODY_BYTES", "5242880")
	t.Setenv("MAX_DECOMPRESSED_BYTES", "52428800")
	t.Setenv("MAX_POINTS", "10000")
	t.Setenv("MAX_SPEED_KMH", "200")
	t.Setenv("DEDUP_DISTANCE_METERS", "5.0")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	require.NoError(t, err, "Load")

	assert.Equal(t, "9090", cfg.Port, "Port")
	assert.Equal(t, 5*time.Second, cfg.ReadTimeout, "ReadTimeout")
	assert.Equal(t, int64(5<<20), cfg.MaxBodyBytes, "MaxBodyBytes")
	assert.Equal(t, int64(50<<20), cfg.MaxDecompressedBytes, "MaxDecompressedBytes")
	assert.Equal(t, 10_000, cfg.MaxPoints, "MaxPoints")
	assert.Equal(t, 200.0, cfg.MaxSpeedKmh, "MaxSpeedKmh")
	assert.Equal(t, 5.0, cfg.DedupDistanceMeters, "DedupDistanceMeters")
	assert.Equal(t, "debug", cfg.LogLevel, "LogLevel")
}

func TestLoadInvalidEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("MAX_POINTS", "abc")
	t.Setenv("MAX_SPEED_KMH", "not-a-number")
	t.Setenv("READ_TIMEOUT", "10")
	t.Setenv("MAX_BODY_BYTES", "xyz")

	cfg, err := Load()
	require.NoError(t, err, "Load")

	assert.Equal(t, 50_000, cfg.MaxPoints, "MaxPoints")
	assert.Equal(t, 150.0, cfg.MaxSpeedKmh, "MaxSpeedKmh")
	assert.Equal(t, 10*time.Second, cfg.ReadTimeout, "ReadTimeout")
	assert.Equal(t, int64(10<<20), cfg.MaxBodyBytes, "MaxBodyBytes")
}

func TestLoadInvalidBoolFallsBackToDefault(t *testing.T) {
	t.Setenv("SWAGGER_ENABLED", "not-a-bool")
	t.Setenv("GRPC_REFLECTION", "maybe")

	cfg, err := Load()
	require.NoError(t, err, "Load")

	assert.Equal(t, false, cfg.SwaggerEnabled, "SwaggerEnabled")
	assert.Equal(t, false, cfg.GRPCReflection, "GRPCReflection")
}
