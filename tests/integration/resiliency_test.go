package integration

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/server"
)

func TestRequestTimeoutIntegration(t *testing.T) {
	// 1. Set up a slow backend upstream server
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep for 1 second to simulate high latency/delay
		time.Sleep(1 * time.Second)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("slow-response"))
	}))
	defer backend.Close()

	// 2. Configure Gateway service with a short 200ms request timeout
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Port:         8080,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  5 * time.Second,
		},
		Services: []config.ServiceConfig{
			{
				Name: "slow-service",
				Resiliency: config.ResiliencyConfig{
					Timeout: config.TimeoutConfig{
						RequestTimeout: 200 * time.Millisecond,
					},
				},
				Routes: []config.RouteConfig{
					{Path: "/slow/*", StripPrefix: true},
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
		t.Fatalf("failed to initialize gateway server: %v", err)
	}

	gateway := httptest.NewServer(gwSrv.Handler)
	defer gateway.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// 3. Send request and verify it is terminated after 200ms with a 504 Gateway Timeout
	startTime := time.Now()
	resp, err := client.Get(gateway.URL + "/slow/data")
	duration := time.Since(startTime)

	if err != nil {
		t.Fatalf("gateway call failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("expected status 504 Gateway Timeout, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytesContain(body, "504 Gateway Timeout") {
		t.Errorf("expected timeout message, got '%s'", string(body))
	}

	// Request should have timed out around 200ms, definitely well before 1s
	if duration >= 800*time.Millisecond {
		t.Errorf("request took too long (%v), timeout did not trigger correctly", duration)
	}
}

func TestRetryPolicyIntegration(t *testing.T) {
	// 1. Set up a backend that fails on first request, but succeeds subsequently
	var calls int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		val := atomic.AddInt32(&calls, 1)
		if val == 1 {
			w.WriteHeader(http.StatusBadGateway) // 502
			_, _ = w.Write([]byte("error-attempt-1"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success-on-retry"))
	}))
	defer backend.Close()

	// 2. Configure Gateway with retries enabled
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Port:         8080,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  5 * time.Second,
		},
		Services: []config.ServiceConfig{
			{
				Name: "retry-service",
				Resiliency: config.ResiliencyConfig{
					Retry: config.RetryConfig{
						Enabled:     true,
						MaxAttempts: 3,
						Backoff:     10 * time.Millisecond,
					},
				},
				Routes: []config.RouteConfig{
					{Path: "/retry/*", StripPrefix: true},
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
		t.Fatalf("failed to initialize gateway: %v", err)
	}

	gateway := httptest.NewServer(gwSrv.Handler)
	defer gateway.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	t.Run("retryable idempotent method GET succeeds on second attempt", func(t *testing.T) {
		atomic.StoreInt32(&calls, 0)

		resp, err := client.Get(gateway.URL + "/retry/data")
		if err != nil {
			t.Fatalf("gateway request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200 OK, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if string(body) != "success-on-retry" {
			t.Errorf("expected body 'success-on-retry', got '%s'", string(body))
		}

		totalCalls := atomic.LoadInt32(&calls)
		if totalCalls != 2 {
			t.Errorf("expected exactly 2 requests to backend, got %d", totalCalls)
		}
	})

	t.Run("non-idempotent method POST does not retry by default", func(t *testing.T) {
		atomic.StoreInt32(&calls, 0)

		req, _ := http.NewRequest(http.MethodPost, gateway.URL+"/retry/data", bytes.NewBuffer([]byte("post-body")))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("gateway request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("expected status 502 Bad Gateway, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if string(body) != "error-attempt-1" {
			t.Errorf("expected body 'error-attempt-1', got '%s'", string(body))
		}

		totalCalls := atomic.LoadInt32(&calls)
		if totalCalls != 1 {
			t.Errorf("expected exactly 1 request to backend (no retries), got %d", totalCalls)
		}
	})
}

func TestCircuitBreakerIntegration(t *testing.T) {
	// 1. Set up a mock backend server that counts incoming requests
	var calls int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		val := atomic.AddInt32(&calls, 1)
		// Fail for first 2 requests, succeed subsequently
		if val <= 2 {
			w.WriteHeader(http.StatusBadGateway) // 502
			_, _ = w.Write([]byte("backend-failed"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend-recovered"))
	}))
	defer backend.Close()

	// 2. Configure Gateway service with a failure threshold of 2 and open timeout of 200ms
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Port:         8080,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  5 * time.Second,
		},
		Services: []config.ServiceConfig{
			{
				Name: "cb-service",
				Resiliency: config.ResiliencyConfig{
					CircuitBreaker: config.CircuitBreakerConfig{
						Enabled:             true,
						FailureThreshold:    2,
						OpenTimeout:         200 * time.Millisecond,
						HalfOpenMaxRequests: 1,
					},
				},
				Routes: []config.RouteConfig{
					{Path: "/cb/*", StripPrefix: true},
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
		t.Fatalf("failed to initialize gateway: %v", err)
	}

	gateway := httptest.NewServer(gwSrv.Handler)
	defer gateway.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Request 1: Should fail (goes to backend)
	resp1, err := client.Get(gateway.URL + "/cb/data")
	if err != nil {
		t.Fatalf("request 1 failed: %v", err)
	}
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusBadGateway {
		t.Errorf("expected status 502, got %d", resp1.StatusCode)
	}

	// Request 2: Should fail and trip the circuit breaker (goes to backend)
	resp2, err := client.Get(gateway.URL + "/cb/data")
	if err != nil {
		t.Fatalf("request 2 failed: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadGateway {
		t.Errorf("expected status 502, got %d", resp2.StatusCode)
	}

	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("expected exactly 2 calls to backend, got %d", atomic.LoadInt32(&calls))
	}

	// Request 3: Should fail immediately with 503 (Blocked by Circuit Breaker)
	resp3, err := client.Get(gateway.URL + "/cb/data")
	if err != nil {
		t.Fatalf("request 3 failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected status 503 Service Unavailable, got %d", resp3.StatusCode)
	}

	body3, _ := io.ReadAll(resp3.Body)
	if !bytesContain(body3, "503 Service Unavailable") {
		t.Errorf("expected circuit breaker blocked error, got '%s'", string(body3))
	}

	// Backend calls must still be 2 (proves that request 3 was blocked on gateway level)
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("expected calls to remain 2, got %d", atomic.LoadInt32(&calls))
	}

	// Wait for open timeout of 200ms to expire so breaker transitions to Half-Open
	time.Sleep(250 * time.Millisecond)

	// Request 4: Trial request should pass and close the breaker (goes to backend)
	resp4, err := client.Get(gateway.URL + "/cb/data")
	if err != nil {
		t.Fatalf("request 4 failed: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 OK after recovery, got %d", resp4.StatusCode)
	}

	body4, _ := io.ReadAll(resp4.Body)
	if string(body4) != "backend-recovered" {
		t.Errorf("expected body 'backend-recovered', got '%s'", string(body4))
	}

	// Backend calls should now be 3
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("expected calls to be 3 after successful trial, got %d", atomic.LoadInt32(&calls))
	}
}
