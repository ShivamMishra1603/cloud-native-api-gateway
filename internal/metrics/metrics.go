package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// RequestsTotal tracks request throughput and status distributions.
	RequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_requests_total",
			Help: "Total number of HTTP requests processed by the gateway.",
		},
		[]string{"service", "method", "status"},
	)

	// RequestDuration tracks overall client request latencies.
	RequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gateway_request_duration_seconds",
			Help:    "Overall request latencies in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"service"},
	)

	// UpstreamDuration tracks latencies of calls forwarded to specific upstream destinations.
	UpstreamDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gateway_upstream_duration_seconds",
			Help:    "Upstream response latencies in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"service", "upstream"},
	)

	// ActiveRequests tracks concurrent execution count.
	ActiveRequests = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gateway_active_requests",
			Help: "Number of active requests currently being processed by the gateway.",
		},
		[]string{"service"},
	)

	// RateLimitedRequests tracks global and service-specific rate limit rejections.
	RateLimitedRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_rate_limited_requests_total",
			Help: "Total number of requests rejected by rate limiting.",
		},
		[]string{"service", "limiter_type"},
	)

	// AuthenticationFailures tracks API Key or JWT authentication errors.
	AuthenticationFailures = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_authentication_failures_total",
			Help: "Total number of request authentication failures.",
		},
		[]string{"service", "auth_type"},
	)

	// CircuitBreakerState tracks upstream circuit breaker states (0=closed, 1=open, 2=half-open).
	CircuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gateway_circuit_breaker_state",
			Help: "Circuit breaker state of upstreams (0=closed, 1=open, 2=half-open).",
		},
		[]string{"service", "upstream"},
	)

	// UpstreamHealthStatus tracks upstream health states (1=healthy, 0=unhealthy).
	UpstreamHealthStatus = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gateway_upstream_health_status",
			Help: "Upstream health status (1=healthy, 0=unhealthy).",
		},
		[]string{"service", "upstream"},
	)
)
