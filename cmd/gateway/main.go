package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/health"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/logging"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/server"
)

func main() {
	configPath := flag.String("config", "configs/gateway.yaml", "path to gateway config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Initialize the structured JSON logger
	logging.Init(cfg.Observability.Logging.Level)

	slog.Info("config loaded successfully", "port", cfg.Gateway.Port)

	// Initialize the Service Registry
	reg, err := registry.New(cfg)
	if err != nil {
		slog.Error("failed to initialize service registry", "error", err)
		os.Exit(1)
	}

	// Initialize the HTTP Server
	srv, err := server.New(cfg, reg)
	if err != nil {
		slog.Error("failed to initialize server", "error", err)
		os.Exit(1)
	}

	// Start Health Checker background tasks
	healthCtx, cancelHealth := context.WithCancel(context.Background())
	defer cancelHealth()

	checker := health.NewChecker(cfg.HealthCheck, reg)
	go checker.Start(healthCtx)

	go func() {
		slog.Info("gateway listening", "addr", srv.Addr)

		if err := srv.ListenAndServe(); err != nil {
			if err.Error() != "http: Server closed" {
				slog.Error("server error", "error", err)
				os.Exit(1)
			}
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	<-stop
	slog.Info("shutdown signal received")

	// Terminate health check workers
	cancelHealth()

	ctx, cancel := context.WithTimeout(context.Background(), server.ShutdownTimeout())
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}

	slog.Info("gateway stopped")
}