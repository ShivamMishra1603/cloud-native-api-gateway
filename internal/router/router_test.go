package router

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
)

func TestRouterMatch(t *testing.T) {
	cfg := &config.Config{
		Services: []config.ServiceConfig{
			{
				Name: "catalog-service",
				Routes: []config.RouteConfig{
					{Path: "/catalog/*", StripPrefix: true},
					{Path: "/catalog/special/*", StripPrefix: false},
				},
			},
			{
				Name: "order-service",
				Routes: []config.RouteConfig{
					{Path: "/orders", StripPrefix: true},
				},
			},
			{
				Name: "root-service",
				Routes: []config.RouteConfig{
					{Path: "/"},
				},
			},
		},
	}

	r := New(cfg)

	tests := []struct {
		name        string
		path        string
		wantService string
		wantStrip   bool
		wantErr     error
	}{
		{
			name:        "exact match orders",
			path:        "/orders",
			wantService: "order-service",
			wantStrip:   true,
			wantErr:     nil,
		},
		{
			name:        "child match orders",
			path:        "/orders/123",
			wantService: "order-service",
			wantStrip:   true,
			wantErr:     nil,
		},
		{
			name:        "exact match catalog prefix",
			path:        "/catalog",
			wantService: "catalog-service",
			wantStrip:   true,
			wantErr:     nil,
		},
		{
			name:        "catalog prefix slash match",
			path:        "/catalog/",
			wantService: "catalog-service",
			wantStrip:   true,
			wantErr:     nil,
		},
		{
			name:        "catalog prefix child match",
			path:        "/catalog/products",
			wantService: "catalog-service",
			wantStrip:   true,
			wantErr:     nil,
		},
		{
			name:        "longest prefix match wins (special)",
			path:        "/catalog/special/items",
			wantService: "catalog-service", // defined in catalog-service, but the specific route
			wantStrip:   false,            // special route does not strip prefix
			wantErr:     nil,
		},
		{
			name:        "no match fallback to root",
			path:        "/other",
			wantService: "root-service",
			wantStrip:   false,
			wantErr:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				URL: &url.URL{
					Path: tt.path,
				},
			}
			matched, err := r.Match(req)
			if err != tt.wantErr {
				t.Fatalf("expected error %v, got %v", tt.wantErr, err)
			}
			if err == nil {
				if matched.ServiceName != tt.wantService {
					t.Errorf("expected service %s, got %s", tt.wantService, matched.ServiceName)
				}
				if matched.StripPrefix != tt.wantStrip {
					t.Errorf("expected strip prefix %t, got %t", tt.wantStrip, matched.StripPrefix)
				}
			}
		})
	}
}

func TestRouterNoMatch(t *testing.T) {
	// Config without root fallback
	cfg := &config.Config{
		Services: []config.ServiceConfig{
			{
				Name: "catalog-service",
				Routes: []config.RouteConfig{
					{Path: "/catalog/*"},
				},
			},
		},
	}

	r := New(cfg)

	req := &http.Request{
		URL: &url.URL{
			Path: "/other",
		},
	}

	_, err := r.Match(req)
	if err != ErrNoRouteMatched {
		t.Errorf("expected ErrNoRouteMatched, got %v", err)
	}
}
