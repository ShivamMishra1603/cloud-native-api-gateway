package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/health"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/server"
)

func TestHealthCheckIntegration(t *testing.T) {
	// 1. Set up two dynamic mock upstreams
	var b1Healthy int32 = 1 // 1 = healthy, 0 = unhealthy
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&b1Healthy) == 1 {
			w.Header().Set("X-Backend", "b1")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("backend-1"))
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("unhealthy"))
		}
	}))
	defer b1.Close()

	b2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// b2 remains healthy throughout the test
		w.Header().Set("X-Backend", "b2")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend-2"))
	}))
	defer b2.Close()

	// 2. Configure the API Gateway with rapid health checks
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Port:         8080,
			ReadTimeout:  2 * time.Second,
			WriteTimeout: 2 * time.Second,
			IdleTimeout:  2 * time.Second,
		},
		HealthCheck: config.HealthCheckConfig{
			Enabled:          true,
			Interval:         50 * time.Millisecond,
			Timeout:          20 * time.Millisecond,
			Path:             "/healthz",
			FailureThreshold: 2,
			SuccessThreshold: 2,
		},
		Services: []config.ServiceConfig{
			{
				Name:         "health-service",
				LoadBalancer: "round_robin",
				Routes: []config.RouteConfig{
					{Path: "/*"},
				},
				Upstreams: []config.UpstreamConfig{
					{URL: b1.URL},
					{URL: b2.URL},
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

	// 3. Start Health Checker background tasks
	healthCtx, cancelHealth := context.WithCancel(context.Background())
	defer cancelHealth()

	checker := health.NewChecker(cfg.HealthCheck, reg)
	go checker.Start(healthCtx)

	client := &http.Client{
		Timeout: 1 * time.Second,
	}

	// 4. Initially, both upstreams are healthy
	time.Sleep(100 * time.Millisecond) // Let initial checks run

	// We expect alternating hits (b1 -> b2 -> b1 -> b2)
	expectedSequence := []string{"b1", "b2", "b1", "b2"}
	for i, exp := range expectedSequence {
		resp, err := client.Get(gateway.URL + "/test")
		if err != nil {
			t.Fatalf("initial request %d failed: %v", i, err)
		}
		defer resp.Body.Close()

		backend := resp.Header.Get("X-Backend")
		if backend != exp {
			t.Errorf("initial request %d: expected %s, got %s", i, exp, backend)
		}
	}

	// 5. Make b1 return 500 error
	atomic.StoreInt32(&b1Healthy, 0)

	// Wait enough time for b1 to fail 2 consecutive checks: 2 * 50ms = 100ms
	time.Sleep(150 * time.Millisecond)

	// Now b1 should be marked unhealthy. All traffic should route to b2.
	for i := 0; i < 4; i++ {
		resp, err := client.Get(gateway.URL + "/test")
		if err != nil {
			t.Fatalf("outage request %d failed: %v", i, err)
		}
		defer resp.Body.Close()

		backend := resp.Header.Get("X-Backend")
		if backend != "b2" {
			t.Errorf("outage request %d: expected routing to bypass b1 and hit b2, got backend %s", i, backend)
		}
	}

	// 6. Recover b1 to return 200 OK
	atomic.StoreInt32(&b1Healthy, 1)

	// Wait enough time for b1 to pass 2 consecutive checks: 2 * 50ms = 100ms
	time.Sleep(150 * time.Millisecond)

	// b1 should be restored. Alternating hits should resume (first hit should be b1 or b2, but both should be hit).
	hits := make(map[string]int)
	for i := 0; i < 6; i++ {
		resp, err := client.Get(gateway.URL + "/test")
		if err != nil {
			t.Fatalf("recovery request %d failed: %v", i, err)
		}
		defer resp.Body.Close()

		backend := resp.Header.Get("X-Backend")
		hits[backend]++
	}

	if hits["b1"] == 0 || hits["b2"] == 0 {
		t.Errorf("expected traffic to resume balancing across both recovered backends, got hits distribution: %+v", hits)
	}
}
