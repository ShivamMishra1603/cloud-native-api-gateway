package server

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/loadbalancer"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/proxy"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/ratelimit"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/router"
)

func New(ctx context.Context, cfg *config.Config, reg *registry.Registry) (*http.Server, error) {
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

	// Load global JWT public key if enabled
	var jwtPublicKey crypto.PublicKey
	if cfg.Authentication.JWT.Enabled {
		keyBytes, err := os.ReadFile(cfg.Authentication.JWT.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("read jwt public key PEM file: %w", err)
		}
		block, _ := pem.Decode(keyBytes)
		if block == nil {
			return nil, fmt.Errorf("failed to decode PEM block from jwt public key")
		}
		pubKey, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			pubKey, err = x509.ParsePKCS1PublicKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("failed to parse jwt public key: %w", err)
			}
		}
		jwtPublicKey = pubKey
	}

	// Initialize global and service rate limiters
	var globalLimiter *ratelimit.Limiter
	if cfg.RateLimit.Enabled {
		globalLimiter = ratelimit.NewLimiter(ctx, cfg.RateLimit.RequestsPerSecond, cfg.RateLimit.Burst)
	}

	serviceLimiters := make(map[string]*ratelimit.Limiter)
	for _, svc := range cfg.Services {
		if svc.RateLimit.Enabled {
			serviceLimiters[svc.Name] = ratelimit.NewLimiter(ctx, svc.RateLimit.RequestsPerSecond, svc.RateLimit.Burst)
		}
	}

	// Build a map of service name -> proxy handler
	proxies := make(map[string]*proxy.ProxyHandler)
	for _, svc := range cfg.Services {
		regSvc, ok := reg.GetService(svc.Name)
		if !ok {
			return nil, fmt.Errorf("service %q not found in registry", svc.Name)
		}

		// Instantiate correct load balancer strategy
		var lb loadbalancer.LoadBalancer
		strategy := strings.ToLower(strings.TrimSpace(regSvc.LoadBalancer))
		switch strategy {
		case "least_connections":
			lb = loadbalancer.NewLeastConnections()
			slog.Info("initializing load balancer", "service", svc.Name, "strategy", "least_connections")
		default:
			lb = loadbalancer.NewRoundRobin()
			slog.Info("initializing load balancer", "service", svc.Name, "strategy", "round_robin")
		}

		pHandler := proxy.New(svc.Name, regSvc.Upstreams, lb)
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

		regSvc, ok := reg.GetService(matched.ServiceName)
		if !ok {
			slog.Error("matched service not found in registry", "service", matched.ServiceName)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("500 Internal Server Error\n"))
			return
		}

		pHandler, ok := proxies[matched.ServiceName]
		if !ok {
			slog.Error("matched service proxy not initialized", "service", matched.ServiceName)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("500 Internal Server Error\n"))
			return
		}

		var consumer string

		// Apply API Key or JWT Authentication if enabled for this service
		if regSvc.Auth.Enabled {
			authType := strings.ToLower(strings.TrimSpace(regSvc.Auth.Type))
			if authType == "" {
				authType = "api_key"
			}

			var authenticated bool

			if authType == "api_key" {
				headerName := cfg.Authentication.APIKey.Header
				if headerName == "" {
					headerName = "X-API-Key"
				}

				apiKey := req.Header.Get(headerName)
				if apiKey == "" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte("401 Unauthorized - Missing API Key\n"))
					return
				}

				// Validate key and identify consumer
				for _, record := range cfg.Authentication.APIKey.Keys {
					if record.Key == apiKey {
						consumer = record.Consumer
						authenticated = true
						break
					}
				}

				if !authenticated {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte("401 Unauthorized - Invalid API Key\n"))
					return
				}
			} else if authType == "jwt" {
				authHeader := req.Header.Get("Authorization")
				if authHeader == "" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte("401 Unauthorized - Missing Authorization Header\n"))
					return
				}

				if !strings.HasPrefix(authHeader, "Bearer ") {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte("401 Unauthorized - Authorization header must start with Bearer\n"))
					return
				}

				tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

				// Parse and validate token using cached public key
				token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
					// Verify signing method alg bounds
					if _, ok := token.Method.(*jwt.SigningMethodRSA); ok {
						return jwtPublicKey, nil
					}
					if _, ok := token.Method.(*jwt.SigningMethodECDSA); ok {
						return jwtPublicKey, nil
					}
					return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
				})

				if err != nil || !token.Valid {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(fmt.Sprintf("401 Unauthorized - Invalid JWT: %v\n", err)))
					return
				}

				claims, ok := token.Claims.(jwt.MapClaims)
				if !ok {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte("401 Unauthorized - Invalid JWT claims\n"))
					return
				}

				sub, _ := claims["sub"].(string)
				if sub == "" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte("401 Unauthorized - JWT sub claim is missing\n"))
					return
				}

				consumer = sub
				authenticated = true
			}

			// Validate service-level consumer access controls (allowed_consumers)
			if authenticated && len(regSvc.Auth.AllowedConsumers) > 0 {
				authorized := false
				for _, allowed := range regSvc.Auth.AllowedConsumers {
					if allowed == consumer {
						authorized = true
						break
					}
				}
				if !authorized {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte("403 Forbidden - Insufficient permissions\n"))
					return
				}
			}

			// Propagate consumer identification header to downstream upstreams
			req.Header.Set("X-Consumer", consumer)
		}

		// Apply Rate Limiting
		var keyBy string
		if regSvc.RateLimit.Enabled {
			keyBy = strings.ToLower(strings.TrimSpace(regSvc.RateLimit.KeyBy))
		} else if cfg.RateLimit.Enabled {
			keyBy = strings.ToLower(strings.TrimSpace(cfg.RateLimit.KeyBy))
		}
		if keyBy == "" {
			keyBy = "ip"
		}

		clientIP, _, err := net.SplitHostPort(req.RemoteAddr)
		if err != nil {
			clientIP = req.RemoteAddr
		}

		var rlKey string
		if keyBy == "consumer" && consumer != "" {
			rlKey = consumer
		} else {
			rlKey = clientIP
		}

		// Check service-level rate limit
		if regSvc.RateLimit.Enabled {
			sLimiter, ok := serviceLimiters[regSvc.Name]
			if ok && sLimiter != nil {
				if !sLimiter.Allow(rlKey) {
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte("429 Too Many Requests - Service rate limit exceeded\n"))
					return
				}
			}
		} else if cfg.RateLimit.Enabled {
			// Check global rate limit
			if globalLimiter != nil {
				if !globalLimiter.Allow(rlKey) {
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte("429 Too Many Requests - Global rate limit exceeded\n"))
					return
				}
			}
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