package server

import (
	"fmt"
	"net/http"
	"time"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
)

func New(cfg *config.Config) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	return &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Gateway.Port),
		Handler:      mux,
		ReadTimeout:  cfg.Gateway.ReadTimeout,
		WriteTimeout: cfg.Gateway.WriteTimeout,
		IdleTimeout:  cfg.Gateway.IdleTimeout,
	}
}

func ShutdownTimeout() time.Duration {
	return 10 * time.Second
}