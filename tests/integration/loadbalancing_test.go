package integration

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/server"
)

func TestLoadBalancingIntegration(t *testing.T) {
	// 1. Set up three mock upstreams
	var backend1Calls int
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend1Calls++
		w.Header().Set("X-Backend", "b1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend-1"))
	}))
	defer b1.Close()

	var backend2Calls int
	b2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend2Calls++
		w.Header().Set("X-Backend", "b2")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend-2"))
	}))
	defer b2.Close()

	var backend3Calls int
	b3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend3Calls++
		w.Header().Set("X-Backend", "b3")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend-3"))
	}))
	defer b3.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	t.Run("Round Robin distributes traffic sequentially", func(t *testing.T) {
		backend1Calls, backend2Calls, backend3Calls = 0, 0, 0

		// Configure gateway with Round Robin load balancer
		cfg := &config.Config{
			Gateway: config.GatewayConfig{
				Port:         8080,
				ReadTimeout:  5 * time.Second,
				WriteTimeout: 5 * time.Second,
				IdleTimeout:  5 * time.Second,
			},
			Services: []config.ServiceConfig{
				{
					Name:         "rr-service",
					LoadBalancer: "round_robin",
					Routes: []config.RouteConfig{
						{Path: "/*"},
					},
					Upstreams: []config.UpstreamConfig{
						{URL: b1.URL},
						{URL: b2.URL},
						{URL: b3.URL},
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
			t.Fatalf("failed to create server: %v", err)
		}
		gateway := httptest.NewServer(gwSrv.Handler)
		defer gateway.Close()

		// Send 6 requests, expect 2 hits per backend
		expectedSequence := []string{"b1", "b2", "b3", "b1", "b2", "b3"}
		for i, exp := range expectedSequence {
			resp, err := client.Get(gateway.URL + "/test")
			if err != nil {
				t.Fatalf("request %d failed: %v", i, err)
			}
			defer resp.Body.Close()

			backendHeader := resp.Header.Get("X-Backend")
			if backendHeader != exp {
				t.Errorf("request %d: expected backend %s, got %s", i, exp, backendHeader)
			}
		}

		if backend1Calls != 2 || backend2Calls != 2 || backend3Calls != 2 {
			t.Errorf("unexpected hit counts. b1=%d, b2=%d, b3=%d", backend1Calls, backend2Calls, backend3Calls)
		}
	})

	t.Run("Least Connections routes around busy backends", func(t *testing.T) {
		// Set up block channels for upstreams 1 & 2
		b1BlockChan := make(chan struct{})
		b2BlockChan := make(chan struct{})

		var b1Active, b2Active, b3Active int
		var mu sync.Mutex

		// Create mock servers with custom handlers that we can block
		ups1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			b1Active++
			mu.Unlock()

			<-b1BlockChan // blocks request

			w.Header().Set("X-Backend", "ups1")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ups1"))
		}))
		defer ups1.Close()

		ups2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			b2Active++
			mu.Unlock()

			<-b2BlockChan // blocks request

			w.Header().Set("X-Backend", "ups2")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ups2"))
		}))
		defer ups2.Close()

		ups3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			b3Active++
			mu.Unlock()

			w.Header().Set("X-Backend", "ups3")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ups3"))
		}))
		defer ups3.Close()

		// Configure Gateway with Least Connections load balancer
		cfg := &config.Config{
			Gateway: config.GatewayConfig{
				Port:         8080,
				ReadTimeout:  5 * time.Second,
				WriteTimeout: 5 * time.Second,
				IdleTimeout:  5 * time.Second,
			},
			Services: []config.ServiceConfig{
				{
					Name:         "lc-service",
					LoadBalancer: "least_connections",
					Routes: []config.RouteConfig{
						{Path: "/*"},
					},
					Upstreams: []config.UpstreamConfig{
						{URL: ups1.URL},
						{URL: ups2.URL},
						{URL: ups3.URL},
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
			t.Fatalf("failed to create server: %v", err)
		}
		gateway := httptest.NewServer(gwSrv.Handler)
		defer gateway.Close()

		// Send two requests asynchronously to consume connections on ups1 and ups2
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			resp, err := client.Get(gateway.URL + "/block-1")
			if err != nil {
				t.Errorf("async request 1 failed: %v", err)
				return
			}
			resp.Body.Close()
		}()

		go func() {
			defer wg.Done()
			resp, err := client.Get(gateway.URL + "/block-2")
			if err != nil {
				t.Errorf("async request 2 failed: %v", err)
				return
			}
			resp.Body.Close()
		}()

		// Wait briefly to ensure both connections reach the backend and block
		time.Sleep(100 * time.Millisecond)

		// At this point, ups1 and ups2 each have 1 active connection. ups3 has 0 active connections.
		// A new request should route to ups3.
		resp, err := client.Get(gateway.URL + "/test-least")
		if err != nil {
			t.Fatalf("failed to send test request: %v", err)
		}
		defer resp.Body.Close()

		backendHeader := resp.Header.Get("X-Backend")
		if backendHeader != "ups3" {
			t.Errorf("expected request to route to ups3, got %s", backendHeader)
		}

		body, _ := io.ReadAll(resp.Body)
		if string(body) != "ups3" {
			t.Errorf("expected response from ups3, got %s", string(body))
		}

		// Clean up: unblock upstream 1 & 2
		close(b1BlockChan)
		close(b2BlockChan)

		// Wait for goroutines to complete cleanly
		wg.Wait()

		mu.Lock()
		defer mu.Unlock()
		if b1Active != 1 || b2Active != 1 || b3Active != 1 {
			t.Errorf("unexpected hits distribution. ups1=%d, ups2=%d, ups3=%d", b1Active, b2Active, b3Active)
		}
	})
}
