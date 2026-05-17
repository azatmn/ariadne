package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
	MaxBodyBytes    int64

	DedupDistanceMeters float64
	DedupTimeGap        time.Duration
	SimplifyMinMeters   float64
	MaxPoints           int
	IntersectMaxIter    int
	MaxSpeedKmh         float64
	MaxLoopMeters       float64
	MaxLoopSeconds      float64

	UseOSRM bool
	OSRMURL string

	LogLevel string
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:            envStr("PORT", "8080"),
		ReadTimeout:     envDuration("READ_TIMEOUT", 10*time.Second),
		WriteTimeout:    envDuration("WRITE_TIMEOUT", 30*time.Second),
		ShutdownTimeout: envDuration("SHUTDOWN_TIMEOUT", 15*time.Second),
		MaxBodyBytes:    envInt64("MAX_BODY_BYTES", 10<<20),

		DedupDistanceMeters: envFloat("DEDUP_DISTANCE_METERS", 2.0),
		DedupTimeGap:        envDuration("DEDUP_TIME_GAP", 60*time.Second),
		// SimplifyMinMeters:   envFloat("SIMPLIFY_MIN_METERS", 2.0),
		MaxPoints:        envInt("MAX_POINTS", 50_000),
		IntersectMaxIter: envInt("INTERSECT_MAX_ITER", 10_000),
		MaxSpeedKmh:      envFloat("MAX_SPEED_KMH", 150),
		MaxLoopMeters:    envFloat("MAX_LOOP_METERS", 100),
		MaxLoopSeconds:   envFloat("MAX_LOOP_SECONDS", 10),

		// UseOSRM: envBool("USE_OSRM", false),
		// OSRMURL: envStr("OSRM_URL", ""),

		LogLevel: envStr("LOG_LEVEL", "info"),
	}

	if cfg.UseOSRM && cfg.OSRMURL == "" {
		return nil, fmt.Errorf("config: USE_OSRM=true but OSRM_URL is empty")
	}

	return cfg, nil
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envInt64(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
