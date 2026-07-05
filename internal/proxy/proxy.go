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
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/metrics"
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
			upstream, ok := req.Context().Value(selectedUpstreamKey{}).(*registry.Upstream)
			if !ok {
				slog.ErrorContext(req.Context(), "failed to extract upstream from context in proxy Director")
				return
			}

			// Rewrite target
			req.URL.Scheme = upstream.URL.Scheme
			req.URL.Host = upstream.URL.Host
			req.Host = upstream.URL.Host

			// Merge queries
			targetQuery := upstream.URL.RawQuery
			if targetQuery == "" || req.URL.RawQuery == "" {
				req.URL.RawQuery = targetQuery + req.URL.RawQuery
			} else {
				req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
			}

			// Join path
			req.URL.Path = singleJoiningSlash(upstream.URL.Path, req.URL.Path)

			// Propagate IP
			if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
				if prior, ok := req.Header["X-Forwarded-For"]; ok {
					clientIP = strings.Join(prior, ", ") + ", " + clientIP
				}
				req.Header.Set("X-Forwarded-For", clientIP)
			}

			slog.DebugContext(req.Context(), "request proxied",
				"service", serviceName,
				"method", req.Method,
				"path", req.URL.Path,
				"query", req.URL.RawQuery,
				"target", upstream.URL.String(),
				"active_conns", upstream.Connections(),
			)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.ErrorContext(r.Context(), "upstream connection error",
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

// ServeHTTP selects an upstream, tracks active requests, and forwards using streaming or buffering.
func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	retryEnabled := p.resiliency.Retry.Enabled && isMethodAllowed(req.Method, p.resiliency.Retry.AllowedMethods)

	if retryEnabled {
		// Read and buffer request body to allow re-submission across retry attempts
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

		maxAttempts := p.resiliency.Retry.MaxAttempts
		if maxAttempts < 1 {
			maxAttempts = 1
		}

		p.executeBufferedWithRetry(w, req, maxAttempts, bodyBytes)
	} else {
		// Streaming mode (zero in-memory buffering for response body or headers)
		p.executeStream(w, req)
	}
}

func (p *ProxyHandler) filterEligible() []*registry.Upstream {
	var eligible []*registry.Upstream
	for _, u := range p.upstreams {
		if u.IsEligible(p.resiliency.CircuitBreaker) {
			eligible = append(eligible, u)
		}
	}
	return eligible
}

func (p *ProxyHandler) selectUpstream(eligible []*registry.Upstream) (*registry.Upstream, error) {
	return p.loadBalancer.Select(eligible)
}

func (p *ProxyHandler) prepareRequest(req *http.Request, upstream *registry.Upstream) (*http.Request, context.CancelFunc) {
	var cancel context.CancelFunc
	ctx := req.Context()
	timeout := p.resiliency.Timeout.RequestTimeout
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}

	ctx = context.WithValue(ctx, selectedUpstreamKey{}, upstream)
	attemptReq := req.Clone(ctx)
	return attemptReq, cancel
}

func (p *ProxyHandler) executeStream(w http.ResponseWriter, req *http.Request) {
	eligible := p.filterEligible()
	if len(eligible) == 0 {
		slog.ErrorContext(req.Context(), "no eligible upstreams available for service", "service", p.serviceName)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("503 Service Unavailable - No upstream available\n"))
		return
	}

	upstream, err := p.selectUpstream(eligible)
	if err != nil {
		slog.ErrorContext(req.Context(), "load balancer selection failed", "service", p.serviceName, "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("503 Service Unavailable - No upstream available\n"))
		return
	}

	upstream.Increment()
	defer upstream.Decrement()

	attemptReq, cancel := p.prepareRequest(req, upstream)
	if cancel != nil {
		defer cancel()
	}

	upstreamStart := time.Now()
	tracker := &statusTrackingWriter{ResponseWriter: w}
	p.proxy.ServeHTTP(tracker, attemptReq)

	metrics.UpstreamDuration.WithLabelValues(p.serviceName, upstream.URL.String()).Observe(time.Since(upstreamStart).Seconds())

	// Determine result success
	success := true
	if tracker.statusCode >= 500 {
		success = false
	}

	// Update Circuit Breaker
	if req.Context().Err() == context.Canceled {
		upstream.DecrementHalfOpen()
	} else {
		upstream.RecordResult(success, p.resiliency.CircuitBreaker)
	}
	metrics.CircuitBreakerState.WithLabelValues(p.serviceName, upstream.URL.String()).Set(float64(upstream.CircuitBreakerState()))
}

func (p *ProxyHandler) executeBufferedWithRetry(w http.ResponseWriter, req *http.Request, maxAttempts int, bodyBytes []byte) {
	var lastBufWriter *bufferingResponseWriter

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		eligible := p.filterEligible()
		if len(eligible) == 0 {
			slog.ErrorContext(req.Context(), "no eligible upstreams available for service", "service", p.serviceName)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("503 Service Unavailable - No upstream available\n"))
			return
		}

		upstream, err := p.selectUpstream(eligible)
		if err != nil {
			slog.ErrorContext(req.Context(), "load balancer selection failed", "service", p.serviceName, "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("503 Service Unavailable - No upstream available\n"))
			return
		}

		upstream.Increment()

		attemptReq, cancel := p.prepareRequest(req, upstream)
		if len(bodyBytes) > 0 {
			attemptReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		} else {
			attemptReq.Body = http.NoBody
		}

		upstreamStart := time.Now()
		bufWriter := newBufferingResponseWriter()
		p.proxy.ServeHTTP(bufWriter, attemptReq)

		if cancel != nil {
			cancel()
		}

		metrics.UpstreamDuration.WithLabelValues(p.serviceName, upstream.URL.String()).Observe(time.Since(upstreamStart).Seconds())
		upstream.Decrement()
		lastBufWriter = bufWriter

		// Determine success outcome
		success := true
		if isRetryable(bufWriter.code, p.resiliency.Retry.StatusCodes) || bufWriter.code >= 500 {
			success = false
		}

		// Update Circuit Breaker
		if req.Context().Err() == context.Canceled {
			upstream.DecrementHalfOpen()
		} else {
			upstream.RecordResult(success, p.resiliency.CircuitBreaker)
		}
		metrics.CircuitBreakerState.WithLabelValues(p.serviceName, upstream.URL.String()).Set(float64(upstream.CircuitBreakerState()))

		// Check if we need to retry
		if attempt < maxAttempts {
			retryableStatus := isRetryable(bufWriter.code, p.resiliency.Retry.StatusCodes)
			if retryableStatus {
				slog.WarnContext(req.Context(), "upstream request failed, retrying",
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

type statusTrackingWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusTrackingWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusTrackingWriter) Write(buf []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	return w.ResponseWriter.Write(buf)
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
