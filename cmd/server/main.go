// Command server — боевой сервис очистки GPS-маршрутов.
//
// Работа асинхронная: HTTP и gRPC только принимают задачу в очередь Redis и
// отдают ключ, а чистит её пул воркеров в этом же процессе. Синхронного пути
// здесь нет — прогон занимает десятки секунд, соединение столько не живёт.
//
// Порядок остановки важен и выдержан ниже: снять готовность → перестать
// принимать → дать воркерам дописать начатое → закрыть Redis. Задача, чей
// результат не успел лечь в Redis, для заказчика потеряна.
package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "ariadne/swagger"

	"ariadne/internal/api"
	"ariadne/internal/callback"
	"ariadne/internal/codec"
	"ariadne/internal/config"
	"ariadne/internal/grpcapi"
	"ariadne/internal/service"
	"ariadne/internal/taskstore"
	"ariadne/internal/worker"
)

// @title Ariadne API
// @version 1.0
// @description Сервис устранения коллизий GPS-маршрутов
// @host localhost:8080
// @BasePath /

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		slog.Warn("invalid LOG_LEVEL, using default INFO", "value", cfg.LogLevel, "error", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	// Redis — хранилище задач и результатов. Соединение ленивое, поэтому
	// сразу проверяем связь: если Redis недоступен, стартовать нет смысла.
	store := taskstore.New(cfg.RedisAddr, cfg.RedisDB, cfg.RedisPassword, cfg.ResultTTL)
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := store.Ping(pingCtx); err != nil {
		pingCancel()
		logger.Error("redis unavailable", "addr", cfg.RedisAddr, "error", err)
		os.Exit(1)
	}
	// Очередь — поток с группой потребителей; без группы читать нечем. Создаём
	// на старте и падаем, если не вышло: сервис, который принимает задачи и не
	// может их раздать, хуже упавшего — снаружи он выглядит рабочим.
	if err := store.EnsureQueue(pingCtx); err != nil {
		pingCancel()
		logger.Error("task queue init failed", "error", err)
		os.Exit(1)
	}
	pingCancel()
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("redis close error", "error", err)
		}
	}()
	logger.Info("redis connected", "addr", cfg.RedisAddr, "db", cfg.RedisDB)

	svc := service.New(cfg, service.NewRouter(cfg, logger))

	// Callback-клиент: уведомляет Laravel по готовности задачи. Пустой
	// CALLBACK_URL → коллбэки выключены (no-op).
	notifier := callback.New(cfg.CallbackURL, cfg.CallbackRetries, cfg.CallbackTimeout, logger)
	if cfg.CallbackURL == "" {
		logger.Warn("CALLBACK_URL is empty, callbacks disabled")
	}

	// Воркер-пул: N горутин разбирают очередь задач из Redis и пишут результат
	// обратно в карточку. workerCtx отменяем отдельно — при выключении сначала
	// гасим воркеров, потом ждём, пока допишут текущие задачи.
	pool := worker.New(store, svc, notifier, logger, cfg.WorkerCount, cfg.ResolveTimeout, codec.Limits{DecompressedBytes: cfg.MaxDecompressedBytes, Points: cfg.MaxPoints})
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	pool.Start(workerCtx)
	logger.Info("worker pool started", "workers", cfg.WorkerCount)

	// HTTP
	httpHandler := api.NewHandler(store)
	router := api.NewRouter(httpHandler, logger, cfg.MaxBodyBytes, cfg.SwaggerEnabled, pool.QueueBroken)

	httpSrv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	// gRPC (async-only: синхронный ResolveCollisions убран)
	grpcHandler := grpcapi.NewHandler(store)
	grpcSrv := grpcapi.NewServer(grpcHandler, logger, cfg.GRPCMaxRecvMsgSize, cfg.GRPCReflection)

	errCh := make(chan error, 2)

	go func() {
		logger.Info("http server starting", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	go func() {
		addr := ":" + cfg.GRPCPort
		lis, err := net.Listen("tcp", addr)
		if err != nil {
			errCh <- err
			return
		}
		logger.Info("grpc server starting", "addr", addr)
		if err := grpcSrv.Serve(lis); err != nil {
			errCh <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		logger.Error("server failed, shutting down", "error", err)
	case sig := <-quit:
		logger.Info("shutting down", "signal", sig.String())
	}

	api.SetReady(false)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	// Воркерам нужен ОТДЕЛЬНЫЙ, больший бюджет, чем HTTP/gRPC: задача в полёте
	// должна успеть дописать результат в Redis до store.Close() (defer выше),
	// иначе запись уйдёт в закрытый клиент и результат потеряется (A-M2). Худший
	// путь = чтение+обработка+запись; HTTP/gRPC довольствуются ShutdownTimeout.
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), pool.DrainBudget())
	defer cancelDrain()

	gracefulStop(
		// 1. Перестаём ПРИНИМАТЬ. HTTP и gRPC гасим параллельно — они друг
		// друга не ждут.
		func() {
			var wg sync.WaitGroup
			wg.Add(2)

			go func() {
				defer wg.Done()
				done := make(chan struct{})
				go func() { grpcSrv.GracefulStop(); close(done) }()
				select {
				case <-done:
					logger.Info("grpc server stopped gracefully")
				case <-ctx.Done():
					logger.Warn("grpc graceful stop timed out, forcing")
					grpcSrv.Stop()
				}
			}()

			go func() {
				defer wg.Done()
				if err := httpSrv.Shutdown(ctx); err != nil {
					logger.Error("http shutdown error", "error", err)
				} else {
					logger.Info("http server stopped gracefully")
				}
			}()

			wg.Wait()
		},

		// 2. И только теперь дорабатываем взятое.
		func() {
			cancelWorkers()
			pool.Shutdown(drainCtx)
			logger.Info("worker pool stopped")
		},
	)

	logger.Info("servers stopped")
}

// gracefulStop останавливает сервис в единственно верном порядке: сначала
// перестаём ПРИНИМАТЬ задачи, потом дорабатываем уже взятые.
//
// Раньше было наоборот: воркеров гасили первыми, а HTTP с gRPC ещё несколько
// мгновений принимали. Задача, попавшая в это окно, получала ответ «принято» и
// номерок — а разбирать её было уже некому. Клиент опрашивал её впустую до
// следующего запуска сервиса.
//
// Отдельной функцией ради одного: порядок здесь — это и есть содержание, и он
// закреплён тестом. Внутри одного длинного main такую перестановку не заметит
// никто, включая автора правки.
func gracefulStop(stopAccepting, drainWorkers func()) {
	stopAccepting()
	drainWorkers()
}
