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

func TestRoutingIntegration(t *testing.T) {
	// 1. Set up mock catalog backend
	var catalogReceivedPath string
	catalogBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		catalogReceivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"catalog","path":"` + r.URL.Path + `"}`))
	}))
	defer catalogBackend.Close()

	// 2. Set up mock order backend
	var orderReceivedPath string
	orderBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orderReceivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"orders","path":"` + r.URL.Path + `"}`))
	}))
	defer orderBackend.Close()

	// 3. Configure the Gateway with both services and routes
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Port:         8080,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  5 * time.Second,
		},
		Services: []config.ServiceConfig{
			{
				Name: "catalog-service",
				Routes: []config.RouteConfig{
					{Path: "/catalog/*", StripPrefix: true},
				},
				Upstreams: []config.UpstreamConfig{
					{URL: catalogBackend.URL},
				},
			},
			{
				Name: "order-service",
				Routes: []config.RouteConfig{
					{Path: "/orders/*", StripPrefix: true},
				},
				Upstreams: []config.UpstreamConfig{
					{URL: orderBackend.URL},
				},
			},
		},
	}

	reg, err := registry.New(cfg)
	if err != nil {
		t.Fatalf("failed to initialize registry: %v", err)
	}

	// 4. Start the API Gateway
	gwSrv, err := server.New(context.Background(), cfg, reg)
	if err != nil {
		t.Fatalf("failed to initialize gateway server: %v", err)
	}

	gateway := httptest.NewServer(gwSrv.Handler)
	defer gateway.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	t.Run("route to catalog-service and strip prefix", func(t *testing.T) {
		resp, err := client.Get(gateway.URL + "/catalog/items/123")
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if string(body) != `{"service":"catalog","path":"/items/123"}` {
			t.Errorf("unexpected body: %s", string(body))
		}

		if catalogReceivedPath != "/items/123" {
			t.Errorf("expected catalog backend path /items/123, got %s", catalogReceivedPath)
		}
	})

	t.Run("route to order-service and strip prefix", func(t *testing.T) {
		resp, err := client.Get(gateway.URL + "/orders/list?page=2")
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if string(body) != `{"service":"orders","path":"/list"}` {
			t.Errorf("unexpected body: %s", string(body))
		}

		if orderReceivedPath != "/list" {
			t.Errorf("expected order backend path /list, got %s", orderReceivedPath)
		}
	})

	t.Run("route exact match prefix catalog handles stripping correctly", func(t *testing.T) {
		resp, err := client.Get(gateway.URL + "/catalog")
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if string(body) != `{"service":"catalog","path":"/"}` {
			t.Errorf("unexpected body: %s", string(body))
		}

		if catalogReceivedPath != "/" {
			t.Errorf("expected catalog backend path /, got %s", catalogReceivedPath)
		}
	})

	t.Run("unmatched route returns 404 Not Found", func(t *testing.T) {
		resp, err := client.Get(gateway.URL + "/unknown/route")
		if err != nil {
			t.Fatalf("failed to send request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if string(body) != "404 Not Found - No matching route registered\n" {
			t.Errorf("unexpected 404 body: %s", string(body))
		}
	})

	t.Run("health check endpoint still functions directly", func(t *testing.T) {
		resp, err := client.Get(gateway.URL + "/healthz")
		if err != nil {
			t.Fatalf("failed to send healthcheck: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
	})
}
