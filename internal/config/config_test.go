package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Port", cfg.Port, "8080"},
		{"ReadTimeout", cfg.ReadTimeout, 10 * time.Second},
		{"WriteTimeout", cfg.WriteTimeout, 30 * time.Second},
		{"ShutdownTimeout", cfg.ShutdownTimeout, 15 * time.Second},
		{"MaxBodyBytes", cfg.MaxBodyBytes, int64(10 << 20)},
		{"MaxDecompressedBytes", cfg.MaxDecompressedBytes, int64(100 << 20)},
		{"DedupDistanceMeters", cfg.DedupDistanceMeters, 2.0},
		{"DedupTimeGap", cfg.DedupTimeGap, 60 * time.Second},
		{"MaxPoints", cfg.MaxPoints, 50_000},
		{"IntersectMaxIter", cfg.IntersectMaxIter, 10_000},
		{"MaxSpeedKmh", cfg.MaxSpeedKmh, 150.0},
		{"MaxLoopMeters", cfg.MaxLoopMeters, 100.0},
		{"MaxLoopSeconds", cfg.MaxLoopSeconds, 10.0},
		{"ResolveTimeout", cfg.ResolveTimeout, 25 * time.Second},
		{"LogLevel", cfg.LogLevel, "info"},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
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
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Port != "9090" {
		t.Errorf("Port: got %s, want 9090", cfg.Port)
	}
	if cfg.ReadTimeout != 5*time.Second {
		t.Errorf("ReadTimeout: got %v, want 5s", cfg.ReadTimeout)
	}
	if cfg.MaxBodyBytes != 5<<20 {
		t.Errorf("MaxBodyBytes: got %d, want %d", cfg.MaxBodyBytes, 5<<20)
	}
	if cfg.MaxDecompressedBytes != 50<<20 {
		t.Errorf("MaxDecompressedBytes: got %d, want %d", cfg.MaxDecompressedBytes, 50<<20)
	}
	if cfg.MaxPoints != 10_000 {
		t.Errorf("MaxPoints: got %d, want 10000", cfg.MaxPoints)
	}
	if cfg.MaxSpeedKmh != 200.0 {
		t.Errorf("MaxSpeedKmh: got %f, want 200", cfg.MaxSpeedKmh)
	}
	if cfg.DedupDistanceMeters != 5.0 {
		t.Errorf("DedupDistanceMeters: got %f, want 5.0", cfg.DedupDistanceMeters)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel: got %s, want debug", cfg.LogLevel)
	}
}

func TestLoadInvalidEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("MAX_POINTS", "abc")
	t.Setenv("MAX_SPEED_KMH", "not-a-number")
	t.Setenv("READ_TIMEOUT", "10")
	t.Setenv("MAX_BODY_BYTES", "xyz")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.MaxPoints != 50_000 {
		t.Errorf("MaxPoints: got %d, want default 50000", cfg.MaxPoints)
	}
	if cfg.MaxSpeedKmh != 150.0 {
		t.Errorf("MaxSpeedKmh: got %f, want default 150", cfg.MaxSpeedKmh)
	}
	if cfg.ReadTimeout != 10*time.Second {
		t.Errorf("ReadTimeout: got %v, want default 10s", cfg.ReadTimeout)
	}
	if cfg.MaxBodyBytes != 10<<20 {
		t.Errorf("MaxBodyBytes: got %d, want default %d", cfg.MaxBodyBytes, 10<<20)
	}
}
