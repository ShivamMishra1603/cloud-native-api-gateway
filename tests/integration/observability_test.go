package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/logging"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/server"
)

func TestRequestIDPropagationAndLogging(t *testing.T) {
	// 1. Set up a backend that records the incoming request ID
	var mu sync.Mutex
	var lastReceivedRequestID string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastReceivedRequestID = r.Header.Get("X-Request-ID")
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	// 2. Configure Gateway
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Port:         8080,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  5 * time.Second,
		},
		Services: []config.ServiceConfig{
			{
				Name: "test-service",
				Routes: []config.RouteConfig{
					{Path: "/test/*", StripPrefix: true},
				},
				Upstreams: []config.UpstreamConfig{
					{URL: backend.URL},
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("failed to validate config: %v", err)
	}

	reg, err := registry.New(cfg)
	if err != nil {
		t.Fatalf("failed to create registry: %v", err)
	}

	gwSrv, err := server.New(context.Background(), cfg, reg)
	if err != nil {
		t.Fatalf("failed to create gateway server: %v", err)
	}

	gateway := httptest.NewServer(gwSrv.Handler)
	defer gateway.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	t.Run("generates request ID if missing and propagates downstream and to response", func(t *testing.T) {
		resp, err := client.Get(gateway.URL + "/test/hello")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		respID := resp.Header.Get("X-Request-ID")
		if respID == "" {
			t.Fatal("expected X-Request-ID in response headers, got empty")
		}

		mu.Lock()
		upstreamID := lastReceivedRequestID
		mu.Unlock()

		if upstreamID == "" {
			t.Fatal("expected X-Request-ID propagated to backend, got empty")
		}

		if respID != upstreamID {
			t.Errorf("expected matching request ID between response (%s) and backend (%s)", respID, upstreamID)
		}
	})

	t.Run("propagates client-specified request ID downstream and to response", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, gateway.URL+"/test/hello", nil)
		req.Header.Set("X-Request-ID", "custom-client-id-123")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		respID := resp.Header.Get("X-Request-ID")
		if respID != "custom-client-id-123" {
			t.Errorf("expected propagated X-Request-ID 'custom-client-id-123', got '%s'", respID)
		}

		mu.Lock()
		upstreamID := lastReceivedRequestID
		mu.Unlock()

		if upstreamID != "custom-client-id-123" {
			t.Errorf("expected propagated X-Request-ID 'custom-client-id-123' at backend, got '%s'", upstreamID)
		}
	})

	t.Run("logs contain request ID when logged with context", func(t *testing.T) {
		// Capture stdout to verify structured logging output
		oldStdout := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("failed to create pipe: %v", err)
		}
		os.Stdout = w

		// Initialize logger (which sets JSON handler writing to os.Stdout)
		logging.Init("info")

		// Write a log with context containing a request ID
		ctx := logging.WithRequestID(context.Background(), "log-test-request-id-456")
		slog.InfoContext(ctx, "observability integration test log message")

		// Restore stdout
		w.Close()
		os.Stdout = oldStdout

		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)

		var parsedLog map[string]interface{}
		if err := json.Unmarshal(buf.Bytes(), &parsedLog); err != nil {
			t.Fatalf("failed to parse log JSON: %v. Output was: %s", err, buf.String())
		}

		reqIDVal, ok := parsedLog["request_id"].(string)
		if !ok || reqIDVal != "log-test-request-id-456" {
			t.Errorf("expected request_id 'log-test-request-id-456' in log, got '%v'", parsedLog["request_id"])
		}

		msgVal, _ := parsedLog["msg"].(string)
		if msgVal != "observability integration test log message" {
			t.Errorf("expected message in log, got '%s'", msgVal)
		}
	})
}

func TestPrometheusMetricsCollection(t *testing.T) {
	// 1. Set up backend server
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	// 2. Configure Gateway with rate limiting enabled to trigger metrics counter increment
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Port:         8080,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  5 * time.Second,
		},
		RateLimit: config.RateLimitConfig{
			Enabled:           true,
			KeyBy:             "ip",
			RequestsPerSecond: 1,
			Burst:             1,
		},
		Services: []config.ServiceConfig{
			{
				Name: "metrics-service",
				Routes: []config.RouteConfig{
					{Path: "/metrics-svc/*", StripPrefix: true},
				},
				Upstreams: []config.UpstreamConfig{
					{URL: backend.URL},
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("failed to validate config: %v", err)
	}

	reg, err := registry.New(cfg)
	if err != nil {
		t.Fatalf("failed to create registry: %v", err)
	}

	gwSrv, err := server.New(context.Background(), cfg, reg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	gateway := httptest.NewServer(gwSrv.Handler)
	defer gateway.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// 3. Generate some request traffic (some allowed, some blocked by rate limit)
	for i := 0; i < 5; i++ {
		resp, _ := client.Get(gateway.URL + "/metrics-svc/data")
		if resp != nil {
			_ = resp.Body.Close()
		}
	}

	// 4. Scrape /metrics and verify expected metrics exist in output format
	resp, err := client.Get(gateway.URL + "/metrics")
	if err != nil {
		t.Fatalf("failed to scrape metrics endpoint: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected metrics status 200 OK, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read metrics body: %v", err)
	}

	metricsStr := string(body)

	// Define expected metrics
	expectedMetrics := []string{
		"gateway_requests_total",
		"gateway_request_duration_seconds_bucket",
		"gateway_upstream_duration_seconds_bucket",
		"gateway_active_requests",
		"gateway_rate_limited_requests_total",
		"gateway_circuit_breaker_state",
		"gateway_upstream_health_status",
	}

	for _, metricName := range expectedMetrics {
		if !bytesContain(body, metricName) {
			t.Errorf("expected prometheus metrics to include '%s', but was missing. Full body:\n%s", metricName, metricsStr)
		}
	}
}
