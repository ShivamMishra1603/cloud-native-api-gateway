package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/auth"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/loadbalancer"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/proxy"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/ratelimit"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/router"
)

type Gateway struct {
	cfg             *config.Config
	reg             *registry.Registry
	router          *router.Router
	authenticator   *auth.Authenticator
	globalLimiter   *ratelimit.Limiter
	serviceLimiters map[string]*ratelimit.Limiter
	proxies         map[string]*proxy.ProxyHandler
}

// New creates and configures the orchestrating API Gateway routing, rate limiter, and auth.
func New(ctx context.Context, cfg *config.Config, reg *registry.Registry) (*Gateway, error) {
	// Initialize Router
	r := router.New(cfg)

	// Initialize Authenticator
	authn, err := auth.NewAuthenticator(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to load authentication: %w", err)
	}

	// Initialize global limiter
	var globalLimiter *ratelimit.Limiter
	if cfg.RateLimit.Enabled {
		globalLimiter = ratelimit.NewLimiter(ctx, cfg.RateLimit.RequestsPerSecond, cfg.RateLimit.Burst)
	}

	// Initialize service limiters
	serviceLimiters := make(map[string]*ratelimit.Limiter)
	for _, svc := range cfg.Services {
		if svc.RateLimit.Enabled {
			serviceLimiters[svc.Name] = ratelimit.NewLimiter(ctx, svc.RateLimit.RequestsPerSecond, svc.RateLimit.Burst)
		}
	}

	// Build proxies map
	proxies := make(map[string]*proxy.ProxyHandler)
	for _, svc := range cfg.Services {
		regSvc, ok := reg.GetService(svc.Name)
		if !ok {
			return nil, fmt.Errorf("service %q not found in registry", svc.Name)
		}

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

		proxies[svc.Name] = proxy.New(svc.Name, regSvc.Upstreams, lb, regSvc.Resiliency)
	}

	return &Gateway{
		cfg:             cfg,
		reg:             reg,
		router:          r,
		authenticator:   authn,
		globalLimiter:   globalLimiter,
		serviceLimiters: serviceLimiters,
		proxies:         proxies,
	}, nil
}

// ServeHTTP routes matching requests, processes auth, rate limits, strips prefixes, and proxies downstreams.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Match route
	matched, err := g.router.Match(req)
	if err != nil {
		slog.Warn("no route matched", "method", req.Method, "path", req.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("404 Not Found - No matching route registered\n"))
		return
	}

	regSvc, ok := g.reg.GetService(matched.ServiceName)
	if !ok {
		slog.Error("matched service not found in registry", "service", matched.ServiceName)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("500 Internal Server Error\n"))
		return
	}

	pHandler, ok := g.proxies[matched.ServiceName]
	if !ok {
		slog.Error("matched service proxy not initialized", "service", matched.ServiceName)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("500 Internal Server Error\n"))
		return
	}

	// 1. Authenticate Request
	consumer, status, err := g.authenticator.Authenticate(req, regSvc)
	if err != nil {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(fmt.Sprintf("%d %s - %s\n", status, http.StatusText(status), err.Error())))
		return
	}
	if consumer != "" {
		req.Header.Set("X-Consumer", consumer)
	}

	// 2. Evaluate Rate Limits
	var keyBy string
	if regSvc.RateLimit.Enabled {
		keyBy = strings.ToLower(strings.TrimSpace(regSvc.RateLimit.KeyBy))
	} else if g.cfg.RateLimit.Enabled {
		keyBy = strings.ToLower(strings.TrimSpace(g.cfg.RateLimit.KeyBy))
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

	if regSvc.RateLimit.Enabled {
		sLimiter, ok := g.serviceLimiters[regSvc.Name]
		if ok && sLimiter != nil {
			if !sLimiter.Allow(rlKey) {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte("429 Too Many Requests - Service rate limit exceeded\n"))
				return
			}
		}
	} else if g.cfg.RateLimit.Enabled {
		if g.globalLimiter != nil {
			if !g.globalLimiter.Allow(rlKey) {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte("429 Too Many Requests - Global rate limit exceeded\n"))
				return
			}
		}
	}

	// 3. Strip prefix if required
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

	// 4. Proxy request
	pHandler.ServeHTTP(w, req)
}
