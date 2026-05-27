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

	_ "ariadne/swagger"

	"ariadne/internal/api"
	"ariadne/internal/config"
	"ariadne/internal/grpcapi"
	"ariadne/internal/service"
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

	svc := service.New(cfg)

	// HTTP
	httpHandler := api.NewHandler(svc, cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	router := api.NewRouter(httpHandler, logger, cfg.MaxBodyBytes, cfg.SwaggerEnabled)

	httpSrv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	// gRPC
	grpcHandler := grpcapi.NewHandler(svc, cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
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
	logger.Info("servers stopped")
}
