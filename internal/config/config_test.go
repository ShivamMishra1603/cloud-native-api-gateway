package config

import (
	"os"
	"testing"
	"time"
)

func TestConfigLoad(t *testing.T) {
	// Create a temporary YAML config file
	yamlContent := `
gateway:
  port: 9090
  read_timeout: 5s
  write_timeout: 5s
  idle_timeout: 30s
observability:
  logging:
    level: debug
services:
  - name: test-service
    upstreams:
      - url: http://localhost:8081
`
	tmpfile, err := os.CreateTemp("", "gateway-config-*.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write([]byte(yamlContent)); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatalf("failed to close temp file: %v", err)
	}

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Gateway.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Gateway.Port)
	}
	if cfg.Gateway.ReadTimeout != 5*time.Second {
		t.Errorf("expected read_timeout 5s, got %v", cfg.Gateway.ReadTimeout)
	}
	if cfg.Observability.Logging.Level != "debug" {
		t.Errorf("expected level debug, got %s", cfg.Observability.Logging.Level)
	}
	if len(cfg.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(cfg.Services))
	}
	if cfg.Services[0].Name != "test-service" {
		t.Errorf("expected service name test-service, got %s", cfg.Services[0].Name)
	}
	if len(cfg.Services[0].Upstreams) != 1 {
		t.Fatalf("expected 1 upstream, got %d", len(cfg.Services[0].Upstreams))
	}
	if cfg.Services[0].Upstreams[0].URL != "http://localhost:8081" {
		t.Errorf("expected upstream url http://localhost:8081, got %s", cfg.Services[0].Upstreams[0].URL)
	}
}

func TestConfigValidateErrors(t *testing.T) {
	validServices := []ServiceConfig{
		{
			Name: "test-service",
			Upstreams: []UpstreamConfig{
				{URL: "http://localhost:8081"},
			},
		},
	}

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid minimal",
			cfg: Config{
				Gateway: GatewayConfig{
					Port:         8080,
					ReadTimeout:  time.Second,
					WriteTimeout: time.Second,
					IdleTimeout:  time.Second,
				},
				Services: validServices,
			},
			wantErr: false,
		},
		{
			name: "invalid port low",
			cfg: Config{
				Gateway: GatewayConfig{
					Port:         0,
					ReadTimeout:  time.Second,
					WriteTimeout: time.Second,
					IdleTimeout:  time.Second,
				},
				Services: validServices,
			},
			wantErr: true,
		},
		{
			name: "invalid port high",
			cfg: Config{
				Gateway: GatewayConfig{
					Port:         70000,
					ReadTimeout:  time.Second,
					WriteTimeout: time.Second,
					IdleTimeout:  time.Second,
				},
				Services: validServices,
			},
			wantErr: true,
		},
		{
			name: "invalid read_timeout",
			cfg: Config{
				Gateway: GatewayConfig{
					Port:         8080,
					ReadTimeout:  -time.Second,
					WriteTimeout: time.Second,
					IdleTimeout:  time.Second,
				},
				Services: validServices,
			},
			wantErr: true,
		},
		{
			name: "invalid log level",
			cfg: Config{
				Gateway: GatewayConfig{
					Port:         8080,
					ReadTimeout:  time.Second,
					WriteTimeout: time.Second,
					IdleTimeout:  time.Second,
				},
				Observability: ObservabilityConfig{
					Logging: LoggingConfig{
						Level: "invalid-level",
					},
				},
				Services: validServices,
			},
			wantErr: true,
		},
		{
			name: "missing services",
			cfg: Config{
				Gateway: GatewayConfig{
					Port:         8080,
					ReadTimeout:  time.Second,
					WriteTimeout: time.Second,
					IdleTimeout:  time.Second,
				},
				Services: nil,
			},
			wantErr: true,
		},
		{
			name: "empty service name",
			cfg: Config{
				Gateway: GatewayConfig{
					Port:         8080,
					ReadTimeout:  time.Second,
					WriteTimeout: time.Second,
					IdleTimeout:  time.Second,
				},
				Services: []ServiceConfig{
					{
						Name: "",
						Upstreams: []UpstreamConfig{
							{URL: "http://localhost:8081"},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "missing upstreams",
			cfg: Config{
				Gateway: GatewayConfig{
					Port:         8080,
					ReadTimeout:  time.Second,
					WriteTimeout: time.Second,
					IdleTimeout:  time.Second,
				},
				Services: []ServiceConfig{
					{
						Name:      "test-service",
						Upstreams: nil,
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid upstream url scheme",
			cfg: Config{
				Gateway: GatewayConfig{
					Port:         8080,
					ReadTimeout:  time.Second,
					WriteTimeout: time.Second,
					IdleTimeout:  time.Second,
				},
				Services: []ServiceConfig{
					{
						Name: "test-service",
						Upstreams: []UpstreamConfig{
							{URL: "ftp://localhost:8081"},
						},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
