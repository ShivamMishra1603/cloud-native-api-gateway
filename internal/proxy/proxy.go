package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/loadbalancer"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
)

type selectedUpstreamKey struct{}

// ProxyHandler wraps httputil.ReverseProxy and distributes requests across a pool of upstreams.
type ProxyHandler struct {
	serviceName  string
	upstreams    []*registry.Upstream
	loadBalancer loadbalancer.LoadBalancer
	resiliency   config.ResiliencyConfig
	proxy        *httputil.ReverseProxy
}

// New creates a new ProxyHandler linked to a service's upstream pool and load-balancer strategy.
func New(serviceName string, upstreams []*registry.Upstream, lb loadbalancer.LoadBalancer, res config.ResiliencyConfig) *ProxyHandler {
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
			if r.Context().Err() == context.DeadlineExceeded || err == context.DeadlineExceeded || strings.Contains(err.Error(), "context deadline exceeded") {
				w.WriteHeader(http.StatusGatewayTimeout)
				_, _ = w.Write([]byte("504 Gateway Timeout - Upstream request timed out\n"))
				return
			}
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("502 Bad Gateway - Upstream service is unreachable\n"))
		},
	}

	return &ProxyHandler{
		serviceName:  serviceName,
		upstreams:    upstreams,
		loadBalancer: lb,
		resiliency:   res,
		proxy:        rp,
	}
}

// ServeHTTP selects an upstream, tracks active requests, and forwards using the reverse proxy.
func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Buffer request body if present
	var bodyBytes []byte
	if req.Body != nil && req.Body != http.NoBody {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("400 Bad Request - Failed to read request body\n"))
			return
		}
		_ = req.Body.Close()
	}

	maxAttempts := 1
	if p.resiliency.Retry.Enabled {
		maxAttempts = p.resiliency.Retry.MaxAttempts
		if maxAttempts < 1 {
			maxAttempts = 1
		}
	}

	var lastBufWriter *bufferingResponseWriter

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// 1. Filter upstreams by eligibility (healthy + circuit breaker check)
		var eligible []*registry.Upstream
		for _, u := range p.upstreams {
			if u.IsEligible(p.resiliency.CircuitBreaker) {
				eligible = append(eligible, u)
			}
		}

		if len(eligible) == 0 {
			slog.Error("no eligible upstreams available for service", "service", p.serviceName)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("503 Service Unavailable - No upstream available\n"))
			return
		}

		// 2. Select the target upstream using the load balancer from eligible ones
		upstream, err := p.loadBalancer.Select(eligible)
		if err != nil {
			slog.Error("load balancer selection failed", "service", p.serviceName, "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("503 Service Unavailable - No upstream available\n"))
			return
		}

		// 3. Track connection metrics
		upstream.Increment()

		// 4. Setup timeout context on the request if configured
		var cancel context.CancelFunc
		ctx := req.Context()
		timeout := p.resiliency.Timeout.RequestTimeout
		if timeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, timeout)
		}

		// 5. Inject selected upstream and clone request
		ctx = context.WithValue(ctx, selectedUpstreamKey{}, upstream)
		attemptReq := req.Clone(ctx)
		if len(bodyBytes) > 0 {
			attemptReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		} else {
			attemptReq.Body = http.NoBody
		}

		// 6. Forward using the reverse proxy to our buffering writer
		bufWriter := newBufferingResponseWriter()
		p.proxy.ServeHTTP(bufWriter, attemptReq)

		if cancel != nil {
			cancel()
		}

		upstream.Decrement()
		lastBufWriter = bufWriter

		// Determine success outcome for the Circuit Breaker
		success := true
		if isRetryable(bufWriter.code, p.resiliency.Retry.StatusCodes) || bufWriter.code >= 500 {
			success = false
		}

		// Record result in circuit breaker
		if req.Context().Err() == context.Canceled {
			upstream.DecrementHalfOpen()
		} else {
			upstream.RecordResult(success, p.resiliency.CircuitBreaker)
		}

		// Check if we need to retry
		if attempt < maxAttempts {
			retryableStatus := isRetryable(bufWriter.code, p.resiliency.Retry.StatusCodes)
			methodAllowed := isMethodAllowed(req.Method, p.resiliency.Retry.AllowedMethods)

			if retryableStatus && methodAllowed {
				slog.Warn("upstream request failed, retrying",
					"service", p.serviceName,
					"attempt", attempt,
					"max_attempts", maxAttempts,
					"status_code", bufWriter.code,
					"backoff", p.resiliency.Retry.Backoff,
				)
				time.Sleep(p.resiliency.Retry.Backoff)
				continue
			}
		}
		break
	}

	// Flush the final response buffer to the client
	if lastBufWriter != nil {
		for k, vv := range lastBufWriter.header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(lastBufWriter.code)
		_, _ = w.Write(lastBufWriter.body.Bytes())
	}
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

type bufferingResponseWriter struct {
	header      http.Header
	code        int
	body        *bytes.Buffer
	wroteHeader bool
}

func newBufferingResponseWriter() *bufferingResponseWriter {
	return &bufferingResponseWriter{
		header: make(http.Header),
		code:   http.StatusOK,
		body:   new(bytes.Buffer),
	}
}

func (w *bufferingResponseWriter) Header() http.Header {
	return w.header
}

func (w *bufferingResponseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.code = code
		w.wroteHeader = true
	}
}

func (w *bufferingResponseWriter) Write(buf []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(buf)
}

func isRetryable(code int, statusCodes []int) bool {
	for _, sc := range statusCodes {
		if code == sc {
			return true
		}
	}
	return false
}

func isMethodAllowed(method string, allowedMethods []string) bool {
	for _, m := range allowedMethods {
		if strings.EqualFold(method, m) {
			return true
		}
	}
	return false
}
