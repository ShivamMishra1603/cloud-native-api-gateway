package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/gateway"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
)

// New initializes the http.Server catching traffic and handing request routing/handling to the orchestrator.
func New(ctx context.Context, cfg *config.Config, reg *registry.Registry) (*http.Server, error) {
	// Initialize core orchestrator (gateway handler)
	gw, err := gateway.New(ctx, cfg, reg)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()

	// Internal health check endpoint
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	// Mount Prometheus metrics scraping endpoint
	mux.Handle("/metrics", promhttp.Handler())

	// catch-all catch and pass to core gateway handler
	mux.Handle("/", gw)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Gateway.Port),
		Handler:      mux,
		ReadTimeout:  cfg.Gateway.ReadTimeout,
		WriteTimeout: cfg.Gateway.WriteTimeout,
		IdleTimeout:  cfg.Gateway.IdleTimeout,
	}

	return srv, nil
}

// ShutdownTimeout specifies time allowed for clean exits before forceful kills.
func ShutdownTimeout() time.Duration {
	return 10 * time.Second
}