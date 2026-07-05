package integration

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/server"
)

func TestRateLimitingIntegration(t *testing.T) {
	// 1. Set up mock backend server
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend-response"))
	}))
	defer backend.Close()

	// 2. Configure Gateway with global and service-level rate limits
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Port:         8080,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  5 * time.Second,
		},
		// Global rate limit: 1 request/second, burst of 1
		RateLimit: config.RateLimitConfig{
			Enabled:           true,
			KeyBy:             "ip",
			RequestsPerSecond: 1.0,
			Burst:             1,
		},
		Authentication: config.AuthenticationConfig{
			APIKey: config.APIKeyConfig{
				Enabled: true,
				Header:  "X-API-Key",
				Keys: []config.APIKeyRecord{
					{Key: "alice-key", Consumer: "alice"},
					{Key: "bob-key", Consumer: "bob"},
				},
			},
		},
		Services: []config.ServiceConfig{
			{
				Name: "public-service",
				Routes: []config.RouteConfig{
					{Path: "/public/*", StripPrefix: true},
				},
				Upstreams: []config.UpstreamConfig{
					{URL: backend.URL},
				},
			},
			{
				Name: "custom-rate-service",
				// Service-level rate limit overrides: 5 requests/sec, burst of 2
				RateLimit: config.RateLimitConfig{
					Enabled:           true,
					KeyBy:             "ip",
					RequestsPerSecond: 5.0,
					Burst:             2,
				},
				Routes: []config.RouteConfig{
					{Path: "/custom/*", StripPrefix: true},
				},
				Upstreams: []config.UpstreamConfig{
					{URL: backend.URL},
				},
			},
			{
				Name: "consumer-rate-service",
				// Service-level rate limit, key-by consumer: 2 requests/sec, burst of 1
				Auth: config.AuthConfig{
					Enabled: true,
					Type:    "api_key",
				},
				RateLimit: config.RateLimitConfig{
					Enabled:           true,
					KeyBy:             "consumer",
					RequestsPerSecond: 2.0,
					Burst:             1,
				},
				Routes: []config.RouteConfig{
					{Path: "/consumer/*", StripPrefix: true},
				},
				Upstreams: []config.UpstreamConfig{
					{URL: backend.URL},
				},
			},
		},
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

	t.Run("global rate limiting blocks requests exceeding limits", func(t *testing.T) {
		// First request passes
		resp1, err := client.Get(gateway.URL + "/public/hello")
		if err != nil {
			t.Fatalf("request 1 failed: %v", err)
		}
		defer resp1.Body.Close()
		if resp1.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got %d", resp1.StatusCode)
		}

		// Second request immediately after fails (since rps=1, burst=1)
		resp2, err := client.Get(gateway.URL + "/public/hello")
		if err != nil {
			t.Fatalf("request 2 failed: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusTooManyRequests {
			t.Errorf("expected status 429 Too Many Requests, got %d", resp2.StatusCode)
		}

		body, _ := io.ReadAll(resp2.Body)
		if !bytesContain(body, "Global rate limit exceeded") {
			t.Errorf("expected global rate limit error, got '%s'", string(body))
		}
	})

	t.Run("service rate limit overrides global rate limit settings", func(t *testing.T) {
		// Wait for token bucket to refill
		time.Sleep(1 * time.Second)

		// /custom has service-level burst of 2
		resp1, err := client.Get(gateway.URL + "/custom/hello")
		if err != nil {
			t.Fatalf("request 1 failed: %v", err)
		}
		defer resp1.Body.Close()
		if resp1.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got %d", resp1.StatusCode)
		}

		resp2, err := client.Get(gateway.URL + "/custom/hello")
		if err != nil {
			t.Fatalf("request 2 failed: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Errorf("expected status 200 for second burst request, got %d", resp2.StatusCode)
		}

		resp3, err := client.Get(gateway.URL + "/custom/hello")
		if err != nil {
			t.Fatalf("request 3 failed: %v", err)
		}
		defer resp3.Body.Close()
		if resp3.StatusCode != http.StatusTooManyRequests {
			t.Errorf("expected status 429 Too Many Requests, got %d", resp3.StatusCode)
		}

		body, _ := io.ReadAll(resp3.Body)
		if !bytesContain(body, "Service rate limit exceeded") {
			t.Errorf("expected service rate limit error, got '%s'", string(body))
		}
	})

	t.Run("consumer key_by rate limits are tracked independently per authenticated consumer", func(t *testing.T) {
		// Wait for token buckets to refill
		time.Sleep(1 * time.Second)

		// Send request as Alice. Alice should pass (first request)
		req1, _ := http.NewRequest(http.MethodGet, gateway.URL+"/consumer/hello", nil)
		req1.Header.Set("X-API-Key", "alice-key")
		resp1, err := client.Do(req1)
		if err != nil {
			t.Fatalf("Alice request 1 failed: %v", err)
		}
		defer resp1.Body.Close()
		if resp1.StatusCode != http.StatusOK {
			t.Errorf("expected Alice status 200, got %d", resp1.StatusCode)
		}

		// Send second request immediately as Alice. Alice should be blocked (since burst=1)
		req2, _ := http.NewRequest(http.MethodGet, gateway.URL+"/consumer/hello", nil)
		req2.Header.Set("X-API-Key", "alice-key")
		resp2, err := client.Do(req2)
		if err != nil {
			t.Fatalf("Alice request 2 failed: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusTooManyRequests {
			t.Errorf("expected Alice status 429, got %d", resp2.StatusCode)
		}

		// Send request as Bob immediately. Bob should pass because rate limits are isolated by consumer name!
		req3, _ := http.NewRequest(http.MethodGet, gateway.URL+"/consumer/hello", nil)
		req3.Header.Set("X-API-Key", "bob-key")
		resp3, err := client.Do(req3)
		if err != nil {
			t.Fatalf("Bob request 1 failed: %v", err)
		}
		defer resp3.Body.Close()
		if resp3.StatusCode != http.StatusOK {
			t.Errorf("expected Bob status 200, got %d", resp3.StatusCode)
		}
	})
}
