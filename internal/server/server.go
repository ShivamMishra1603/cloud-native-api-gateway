package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/proxy"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/router"
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

	// Initialize the Router
	r := router.New(cfg)

	// Build a map of service name -> proxy handler
	proxies := make(map[string]*proxy.ProxyHandler)
	for _, svc := range cfg.Services {
		// Milestone 3 uses the first upstream replica for proxying
		targetURL := svc.Upstreams[0].URL
		slog.Info("initializing proxy target", "service", svc.Name, "url", targetURL)

		pHandler, err := proxy.New(targetURL)
		if err != nil {
			return nil, fmt.Errorf("create proxy handler for service %q: %w", svc.Name, err)
		}
		proxies[svc.Name] = pHandler
	}

	// Mount the router handler as the catch-all endpoint
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		matched, err := r.Match(req)
		if err != nil {
			slog.Warn("no route matched", "method", req.Method, "path", req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("404 Not Found - No matching route registered\n"))
			return
		}

		pHandler, ok := proxies[matched.ServiceName]
		if !ok {
			slog.Error("matched service proxy not initialized", "service", matched.ServiceName)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("500 Internal Server Error\n"))
			return
		}

		// Strip prefix if required
		if matched.StripPrefix {
			originalPath := req.URL.Path
			stripped := strings.TrimPrefix(req.URL.Path, matched.PathPrefix)
			if !strings.HasPrefix(stripped, "/") {
				stripped = "/" + stripped
			}
			req.URL.Path = stripped
			slog.Debug("route matched, prefix stripped",
				"service", matched.ServiceName,
				"original_path", originalPath,
				"stripped_path", req.URL.Path,
			)
		} else {
			slog.Debug("route matched", "service", matched.ServiceName, "path", req.URL.Path)
		}

		pHandler.ServeHTTP(w, req)
	})

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