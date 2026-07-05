package proxy

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/loadbalancer"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
)

type selectedUpstreamKey struct{}

// ProxyHandler wraps httputil.ReverseProxy and distributes requests across a pool of upstreams.
type ProxyHandler struct {
	serviceName  string
	upstreams    []*registry.Upstream
	loadBalancer loadbalancer.LoadBalancer
	proxy        *httputil.ReverseProxy
}

// New creates a new ProxyHandler linked to a service's upstream pool and load-balancer strategy.
func New(serviceName string, upstreams []*registry.Upstream, lb loadbalancer.LoadBalancer) *ProxyHandler {
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Extract selected upstream from context
			upstream, ok := req.Context().Value(selectedUpstreamKey{}).(*registry.Upstream)
			if !ok {
				slog.Error("failed to extract upstream from context in proxy Director")
				return
			}

			// Rewrite scheme and host targets
			req.URL.Scheme = upstream.URL.Scheme
			req.URL.Host = upstream.URL.Host
			req.Host = upstream.URL.Host

			// Merge raw queries correctly
			targetQuery := upstream.URL.RawQuery
			if targetQuery == "" || req.URL.RawQuery == "" {
				req.URL.RawQuery = targetQuery + req.URL.RawQuery
			} else {
				req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
			}

			// Clean and join URLs
			req.URL.Path = singleJoiningSlash(upstream.URL.Path, req.URL.Path)

			// Propagate client IP in X-Forwarded-For header
			if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
				if prior, ok := req.Header["X-Forwarded-For"]; ok {
					clientIP = strings.Join(prior, ", ") + ", " + clientIP
				}
				req.Header.Set("X-Forwarded-For", clientIP)
			}

			slog.Debug("request proxied",
				"service", serviceName,
				"method", req.Method,
				"path", req.URL.Path,
				"query", req.URL.RawQuery,
				"target", upstream.URL.String(),
				"active_conns", upstream.Connections(),
			)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("upstream connection error",
				"error", err,
				"service", serviceName,
				"path", r.URL.Path,
			)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("502 Bad Gateway - Upstream service is unreachable\n"))
		},
	}

	return &ProxyHandler{
		serviceName:  serviceName,
		upstreams:    upstreams,
		loadBalancer: lb,
		proxy:        rp,
	}
}

// ServeHTTP selects an upstream, tracks active requests, and forwards using the reverse proxy.
func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 1. Select the target upstream using the load balancer
	upstream, err := p.loadBalancer.Select(p.upstreams)
	if err != nil {
		slog.Error("no upstream available for service", "service", p.serviceName, "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("503 Service Unavailable - No upstream available\n"))
		return
	}

	// 2. Track connection metrics
	upstream.Increment()
	defer upstream.Decrement()

	// 3. Inject selected upstream into request context
	ctx := context.WithValue(req.Context(), selectedUpstreamKey{}, upstream)

	// 4. Forward using the reverse proxy
	p.proxy.ServeHTTP(w, req.WithContext(ctx))
}

// singleJoiningSlash safely joins target base path and request path.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		if a == "" {
			return b
		}
		return a + "/" + b
	}
	return a + b
}
