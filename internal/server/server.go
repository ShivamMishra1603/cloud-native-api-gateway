package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/proxy"
)

func New(cfg *config.Config) (*http.Server, error) {
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

	// Get target service upstream for Milestone 2 proxying
	// Config validation guarantees we have at least one service and one upstream
	targetURL := cfg.Services[0].Upstreams[0].URL
	slog.Info("initializing proxy target", "service", cfg.Services[0].Name, "url", targetURL)

	pHandler, err := proxy.New(targetURL)
	if err != nil {
		return nil, fmt.Errorf("create proxy handler: %w", err)
	}

	// Mount the proxy handler as the catch-all endpoint
	mux.Handle("/", pHandler)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Gateway.Port),
		Handler:      mux,
		ReadTimeout:  cfg.Gateway.ReadTimeout,
		WriteTimeout: cfg.Gateway.WriteTimeout,
		IdleTimeout:  cfg.Gateway.IdleTimeout,
	}

	return srv, nil
}

func ShutdownTimeout() time.Duration {
	return 10 * time.Second
}