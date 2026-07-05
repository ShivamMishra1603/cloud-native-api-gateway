package integration

import (
	"bytes"
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

func TestProxyIntegration(t *testing.T) {
	// 1. Set up the mock upstream (backend) server
	var lastReceivedMethod string
	var lastReceivedPath string
	var lastReceivedQuery string
	var lastReceivedBody []byte
	var lastReceivedHeaders http.Header

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastReceivedMethod = r.Method
		lastReceivedPath = r.URL.Path
		lastReceivedQuery = r.URL.RawQuery
		lastReceivedHeaders = r.Header

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body at backend: %v", err)
		}
		lastReceivedBody = body

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Response-Header", "from-backend")
		w.WriteHeader(http.StatusAccepted) // Return 202 Accepted
		_, _ = w.Write([]byte(`{"message":"hello from upstream"}`))
	}))
	defer backend.Close()

	// 2. Configure the API Gateway pointing to the mock upstream
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
					{Path: "/*", StripPrefix: false},
				},
				Upstreams: []config.UpstreamConfig{
					{URL: backend.URL},
				},
			},
		},
	}

	reg, err := registry.New(cfg)
	if err != nil {
		t.Fatalf("failed to initialize registry: %v", err)
	}

	// 3. Start the API Gateway on a test listener
	gwSrv, err := server.New(context.Background(), cfg, reg)
	if err != nil {
		t.Fatalf("failed to initialize gateway server: %v", err)
	}

	gateway := httptest.NewServer(gwSrv.Handler)
	defer gateway.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	t.Run("GET request with query params and headers is forwarded correctly", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, gateway.URL+"/api/v1/products?category=books&limit=10", nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("X-Custom-Request-Header", "from-client")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to send request through gateway: %v", err)
		}
		defer resp.Body.Close()

		// Assert response is preserved
		if resp.StatusCode != http.StatusAccepted {
			t.Errorf("expected status 202, got %d", resp.StatusCode)
		}
		if resp.Header.Get("X-Custom-Response-Header") != "from-backend" {
			t.Errorf("expected response header to be forwarded, got: %s", resp.Header.Get("X-Custom-Response-Header"))
		}

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read response body: %v", err)
		}
		if string(respBody) != `{"message":"hello from upstream"}` {
			t.Errorf("expected response body, got %s", string(respBody))
		}

		// Assert request properties received by backend
		if lastReceivedMethod != http.MethodGet {
			t.Errorf("expected GET, got %s", lastReceivedMethod)
		}
		if lastReceivedPath != "/api/v1/products" {
			t.Errorf("expected path /api/v1/products, got %s", lastReceivedPath)
		}
		if lastReceivedQuery != "category=books&limit=10" {
			t.Errorf("expected query params, got %s", lastReceivedQuery)
		}
		if lastReceivedHeaders.Get("X-Custom-Request-Header") != "from-client" {
			t.Errorf("expected request header to be forwarded, got: %s", lastReceivedHeaders.Get("X-Custom-Request-Header"))
		}
		if lastReceivedHeaders.Get("X-Forwarded-For") == "" {
			t.Error("expected X-Forwarded-For header to be appended by proxy")
		}
	})

	t.Run("POST request with body is forwarded correctly", func(t *testing.T) {
		postBody := []byte(`{"name":"item","price":99.99}`)
		req, err := http.NewRequest(http.MethodPost, gateway.URL+"/api/v1/orders", bytes.NewReader(postBody))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to send request through gateway: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusAccepted {
			t.Errorf("expected status 202, got %d", resp.StatusCode)
		}
		if lastReceivedMethod != http.MethodPost {
			t.Errorf("expected POST, got %s", lastReceivedMethod)
		}
		if lastReceivedPath != "/api/v1/orders" {
			t.Errorf("expected path /api/v1/orders, got %s", lastReceivedPath)
		}
		if !bytes.Equal(lastReceivedBody, postBody) {
			t.Errorf("expected body %s, got %s", string(postBody), string(lastReceivedBody))
		}
	})

	t.Run("internal healthz is handled by gateway directly", func(t *testing.T) {
		resp, err := client.Get(gateway.URL + "/healthz")
		if err != nil {
			t.Fatalf("failed to send request to healthz: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read response body: %v", err)
		}
		if string(respBody) != "ok\n" {
			t.Errorf("expected ok response, got %s", string(respBody))
		}
	})

	t.Run("upstream server down returns 502 Bad Gateway", func(t *testing.T) {
		// Close backend server to simulate upstream failure
		backend.Close()

		resp, err := client.Get(gateway.URL + "/api/v1/products")
		if err != nil {
			t.Fatalf("failed to send request through gateway: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("expected status 502, got %d", resp.StatusCode)
		}

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read response body: %v", err)
		}
		if string(respBody) != "502 Bad Gateway - Upstream service is unreachable\n" {
			t.Errorf("expected 502 body, got: %s", string(respBody))
		}
	})
}
