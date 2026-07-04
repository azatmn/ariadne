package config

import (
	"log/slog"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port                 string
	GRPCPort             string
	GRPCMaxRecvMsgSize   int
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	IdleTimeout          time.Duration
	ShutdownTimeout      time.Duration
	MaxBodyBytes         int64
	MaxDecompressedBytes int64
	ResolveTimeout       time.Duration

	DedupDistanceMeters   float64
	DedupTimeGap          time.Duration
	SimplifyMinMeters     float64
	MaxPoints             int
	MaxSpeedKmh           float64
	MaxAccelKmhPerSec     float64
	TeleportJumpMeters    float64
	TeleportReturnMeters  float64
	TeleportMaxSpanMeters float64
	StopRadiusMeters      float64
	StopMinPoints         int
	AnchorToleranceMeters float64

	// Redis (async: очередь задач + хранилище результатов)
	RedisAddr     string
	RedisDB       int
	RedisPassword string
	WorkerCount   int
	ResultTTL     time.Duration

	// Callback (Go → Laravel по готовности задачи)
	CallbackURL     string // шаблон с плейсхолдером {taskKey}; пустой → коллбэки выключены
	CallbackRetries int
	CallbackTimeout time.Duration

	SwaggerEnabled bool
	GRPCReflection bool

	LogLevel string
}

func Load() (*Config, error) {
	cfg := &Config{
		// Server
		Port:               envStr("PORT", "8080"),
		GRPCPort:           envStr("GRPC_PORT", "9090"),
		GRPCMaxRecvMsgSize: envInt("GRPC_MAX_RECV_MSG_SIZE", 10<<20), // 10 MB
		ReadTimeout:        envDuration("READ_TIMEOUT", 10*time.Second),
		WriteTimeout:       envDuration("WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:        envDuration("IDLE_TIMEOUT", 2*time.Minute),
		ShutdownTimeout:    envDuration("SHUTDOWN_TIMEOUT", 15*time.Second),

		// Limits
		MaxBodyBytes:         envInt64("MAX_BODY_BYTES", 10<<20),         // 10 MB
		MaxDecompressedBytes: envInt64("MAX_DECOMPRESSED_BYTES", 20<<20), // 20 MB
		MaxPoints:            envInt("MAX_POINTS", 50_000),
		ResolveTimeout:       envDuration("RESOLVE_TIMEOUT", 25*time.Second),

		// Pipeline
		DedupDistanceMeters:   envFloat("DEDUP_DISTANCE_METERS", 2.0),
		DedupTimeGap:          envDuration("DEDUP_TIME_GAP", 60*time.Second),
		MaxSpeedKmh:           envFloat("MAX_SPEED_KMH", 150),
		MaxAccelKmhPerSec:     envFloat("MAX_ACCEL_KMH_PER_SEC", 20),
		TeleportJumpMeters:    envFloat("TELEPORT_JUMP_METERS", 15000),
		TeleportReturnMeters:  envFloat("TELEPORT_RETURN_METERS", 2000),
		TeleportMaxSpanMeters: envFloat("TELEPORT_MAX_SPAN_METERS", 5000),
		StopRadiusMeters:      envFloat("STOP_RADIUS_METERS", 50),
		StopMinPoints:         envInt("STOP_MIN_POINTS", 5),
		SimplifyMinMeters:     envFloat("SIMPLIFY_MIN_METERS", 5.0),
		AnchorToleranceMeters: envFloat("ANCHOR_BACKTRACK_TOLERANCE_METERS", 0), // 0 = якорный фильтр выключен

		// Redis (async: очередь + хранилище результатов)
		RedisAddr:     envStr("REDIS_ADDR", "localhost:6379"),
		RedisDB:       envInt("REDIS_DB", 10),
		RedisPassword: envStr("REDIS_PASSWORD", ""),
		WorkerCount:   envInt("WORKER_COUNT", 4),
		ResultTTL:     envDuration("RESULT_TTL", time.Hour),

		// Callback (Go → Laravel)
		CallbackURL:     envStr("CALLBACK_URL", ""),
		CallbackRetries: envInt("CALLBACK_RETRIES", 3),
		CallbackTimeout: envDuration("CALLBACK_TIMEOUT", 5*time.Second),

		// Swagger
		SwaggerEnabled: envBool("SWAGGER_ENABLED", false),
		GRPCReflection: envBool("GRPC_REFLECTION", false),

		// Logging
		LogLevel: envStr("LOG_LEVEL", "info"),
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
		slog.Warn("invalid env value, using default", "key", key, "value", v, "default", def, "error", err)
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
		slog.Warn("invalid env value, using default", "key", key, "value", v, "default", def, "error", err)
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
		slog.Warn("invalid env value, using default", "key", key, "value", v, "default", def, "error", err)
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
		slog.Warn("invalid env value, using default", "key", key, "value", v, "default", def, "error", err)
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
		slog.Warn("invalid env value, using default", "key", key, "value", v, "default", def, "error", err)
		return def
	}
	return d
}
