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

	// Пороги упаковки — трёх стадий, сжимающих уже очищенный трек. Всё
	// остальное в чистке и дорисовке задано константами в коде намеренно: те
	// числа выверены по эталону на Python и проверяются золотыми тестами, а
	// ручка в окружении дала бы бою тихо разойтись с эталоном.
	//
	// Порогов четырёх снятых фильтров (якорь, телепорты, скорость, ускорение)
	// здесь НЕТ. Они пережили сами стадии и ещё какое-то время доезжали до
	// pipeline.Params, где их никто не читал: настройка, которая выглядит
	// рабочей и молча ничего не делает.
	DedupDistanceMeters float64
	DedupTimeGap        time.Duration
	SimplifyMinMeters   float64
	MaxPoints           int
	StopRadiusMeters    float64
	StopMinPoints       int

	// OSRM — маршрутизатор, на котором держится вся чистка: снэпы дают вес
	// точке, расстояния по дорогам проверяют переходы, геометрия дорисовывает
	// дыры. Пустой адрес — чистка и дорисовка пропускают трек насквозь с
	// предупреждением, сервис при этом работает.
	OSRMURL         string
	OSRMTimeout     time.Duration
	OSRMMaxParallel int

	// OSRMRetries — сколько раз повторяем запрос, упавший по ВРЕМЕННОЙ причине
	// (сеть моргнула, 5xx, таймаут). Отказы по существу (400, 404, 414) не
	// повторяются никогда: ответ от этого не изменится.
	//
	// Ноль означает «не повторять вовсе», и это опасное значение по умолчанию:
	// один моргнувший запрос оставляет дыру недорисованной, километраж
	// занижается, и никто об этом не узнаёт. Пауза между попытками растёт
	// (0.2 с, 0.4 с) со случайной добавкой, так что двух повторов хватает.
	OSRMRetries int

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

		// Упаковка
		DedupDistanceMeters: envFloat("DEDUP_DISTANCE_METERS", 2.0),
		DedupTimeGap:        envDuration("DEDUP_TIME_GAP", 60*time.Second),
		StopRadiusMeters:    envFloat("STOP_RADIUS_METERS", 50),
		StopMinPoints:       envInt("STOP_MIN_POINTS", 5),
		SimplifyMinMeters:   envFloat("SIMPLIFY_MIN_METERS", 5.0),

		OSRMURL:         envStr("OSRM_URL", ""),
		OSRMTimeout:     envDuration("OSRM_TIMEOUT", 30*time.Second),
		OSRMMaxParallel: envInt("OSRM_MAX_PARALLEL", 16),
		OSRMRetries:     envInt("OSRM_RETRIES", 2),

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
