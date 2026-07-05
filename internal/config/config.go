package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Gateway       GatewayConfig       `yaml:"gateway"`
	Observability ObservabilityConfig `yaml:"observability"`
	Services      []ServiceConfig     `yaml:"services"`
	HealthCheck   HealthCheckConfig   `yaml:"health_checks"`
}

type GatewayConfig struct {
	Port         int           `yaml:"port"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	IdleTimeout  time.Duration `yaml:"idle_timeout"`
}

type ObservabilityConfig struct {
	Logging LoggingConfig `yaml:"logging"`
}

type LoggingConfig struct {
	Level string `yaml:"level"`
}

type HealthCheckConfig struct {
	Enabled          bool          `yaml:"enabled"`
	Interval         time.Duration `yaml:"interval"`
	Timeout          time.Duration `yaml:"timeout"`
	Path             string        `yaml:"path"`
	FailureThreshold int           `yaml:"failure_threshold"`
	SuccessThreshold int           `yaml:"success_threshold"`
}

type ServiceConfig struct {
	Name         string           `yaml:"name"`
	LoadBalancer string           `yaml:"load_balancer,omitempty"`
	Routes       []RouteConfig    `yaml:"routes,omitempty"`
	Upstreams    []UpstreamConfig `yaml:"upstreams"`
}

type RouteConfig struct {
	Path        string `yaml:"path"`
	StripPrefix bool   `yaml:"strip_prefix"`
}

type UpstreamConfig struct {
	URL string `yaml:"url"`
}


func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Gateway.Port <= 0 || c.Gateway.Port > 65535 {
		return fmt.Errorf("gateway.port must be between 1 and 65535")
	}

	if c.Gateway.ReadTimeout <= 0 {
		return fmt.Errorf("gateway.read_timeout must be positive")
	}

	if c.Gateway.WriteTimeout <= 0 {
		return fmt.Errorf("gateway.write_timeout must be positive")
	}

	if c.Gateway.IdleTimeout <= 0 {
		return fmt.Errorf("gateway.idle_timeout must be positive")
	}

	if c.Observability.Logging.Level == "" {
		c.Observability.Logging.Level = "info"
	} else {
		lvl := strings.ToLower(strings.TrimSpace(c.Observability.Logging.Level))
		if lvl != "debug" && lvl != "info" && lvl != "warn" && lvl != "warning" && lvl != "error" {
			return fmt.Errorf("invalid logging level: %s", c.Observability.Logging.Level)
		}
	}

	if len(c.Services) == 0 {
		return fmt.Errorf("at least one service must be configured")
	}

	for i, svc := range c.Services {
		if strings.TrimSpace(svc.Name) == "" {
			return fmt.Errorf("service[%d].name cannot be empty", i)
		}
		lb := strings.ToLower(strings.TrimSpace(svc.LoadBalancer))
		if lb != "" && lb != "round_robin" && lb != "least_connections" {
			return fmt.Errorf("service %q has invalid load_balancer strategy: %q", svc.Name, svc.LoadBalancer)
		}
		if len(svc.Routes) == 0 {
			return fmt.Errorf("service %q must have at least one route", svc.Name)
		}
		for j, r := range svc.Routes {
			p := strings.TrimSpace(r.Path)
			if p == "" {
				return fmt.Errorf("service %q route[%d].path cannot be empty", svc.Name, j)
			}
			if !strings.HasPrefix(p, "/") {
				return fmt.Errorf("service %q route[%d].path %q must start with '/'", svc.Name, j, p)
			}
		}
		if len(svc.Upstreams) == 0 {
			return fmt.Errorf("service %q must have at least one upstream", svc.Name)
		}
		for j, ups := range svc.Upstreams {
			if strings.TrimSpace(ups.URL) == "" {
				return fmt.Errorf("service %q upstream[%d].url cannot be empty", svc.Name, j)
			}
			parsed, err := url.Parse(ups.URL)
			if err != nil {
				return fmt.Errorf("service %q upstream[%d].url is invalid: %w", svc.Name, j, err)
			}
			if parsed.Scheme != "http" && parsed.Scheme != "https" {
				return fmt.Errorf("service %q upstream[%d].url scheme must be http or https", svc.Name, j)
			}
			if parsed.Host == "" {
				return fmt.Errorf("service %q upstream[%d].url must specify a host", svc.Name, j)
			}
		}
	}

	if c.HealthCheck.Enabled {
		if c.HealthCheck.Interval <= 0 {
			c.HealthCheck.Interval = 10 * time.Second
		}
		if c.HealthCheck.Timeout <= 0 {
			c.HealthCheck.Timeout = 2 * time.Second
		}
		if strings.TrimSpace(c.HealthCheck.Path) == "" {
			c.HealthCheck.Path = "/healthz"
		} else if !strings.HasPrefix(c.HealthCheck.Path, "/") {
			return fmt.Errorf("health_checks.path %q must start with '/'", c.HealthCheck.Path)
		}
		if c.HealthCheck.FailureThreshold <= 0 {
			c.HealthCheck.FailureThreshold = 3
		}
		if c.HealthCheck.SuccessThreshold <= 0 {
			c.HealthCheck.SuccessThreshold = 2
		}
	}

	return nil
}