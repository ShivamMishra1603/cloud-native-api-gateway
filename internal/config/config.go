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
	Gateway        GatewayConfig        `yaml:"gateway"`
	Observability  ObservabilityConfig  `yaml:"observability"`
	Services       []ServiceConfig      `yaml:"services"`
	HealthCheck    HealthCheckConfig    `yaml:"health_checks"`
	Authentication AuthenticationConfig `yaml:"authentication"`
	RateLimit      RateLimitConfig      `yaml:"rate_limit"`
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

type AuthenticationConfig struct {
	JWT    JWTConfig    `yaml:"jwt"`
	APIKey APIKeyConfig `yaml:"api_key"`
}

type JWTConfig struct {
	Enabled   bool   `yaml:"enabled"`
	PublicKey string `yaml:"public_key"`
}

type APIKeyConfig struct {
	Enabled bool           `yaml:"enabled"`
	Header  string         `yaml:"header"`
	Keys    []APIKeyRecord `yaml:"keys"`
}

type APIKeyRecord struct {
	Key      string `yaml:"key"`
	Consumer string `yaml:"consumer"`
}

type RateLimitConfig struct {
	Enabled           bool    `yaml:"enabled"`
	KeyBy             string  `yaml:"key_by"` // "ip" or "consumer"
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	Burst             int     `yaml:"burst"`
}

type ServiceConfig struct {
	Name         string           `yaml:"name"`
	LoadBalancer string           `yaml:"load_balancer,omitempty"`
	Routes       []RouteConfig    `yaml:"routes,omitempty"`
	Upstreams    []UpstreamConfig `yaml:"upstreams"`
	Auth         AuthConfig       `yaml:"auth,omitempty"`
	RateLimit    RateLimitConfig  `yaml:"rate_limit,omitempty"`
}

type AuthConfig struct {
	Enabled          bool     `yaml:"enabled"`
	Type             string   `yaml:"type"` // "api_key" or "jwt"
	AllowedConsumers []string `yaml:"allowed_consumers,omitempty"`
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

	// Default and validate Authentication config
	if c.Authentication.APIKey.Enabled {
		if strings.TrimSpace(c.Authentication.APIKey.Header) == "" {
			c.Authentication.APIKey.Header = "X-API-Key"
		}
		if len(c.Authentication.APIKey.Keys) == 0 {
			return fmt.Errorf("api_key authentication is enabled but no keys are configured")
		}
		for i, r := range c.Authentication.APIKey.Keys {
			if strings.TrimSpace(r.Key) == "" {
				return fmt.Errorf("authentication.api_key.keys[%d].key cannot be empty", i)
			}
			if strings.TrimSpace(r.Consumer) == "" {
				return fmt.Errorf("authentication.api_key.keys[%d].consumer cannot be empty", i)
			}
		}
	}

	if c.Authentication.JWT.Enabled {
		if strings.TrimSpace(c.Authentication.JWT.PublicKey) == "" {
			return fmt.Errorf("jwt authentication is enabled but public_key file path is not configured")
		}
	}

	// Validate service-level authentication settings
	for _, svc := range c.Services {
		if svc.Auth.Enabled {
			t := strings.ToLower(strings.TrimSpace(svc.Auth.Type))
			if t == "" {
				t = "api_key"
			}
			if t != "api_key" && t != "jwt" {
				return fmt.Errorf("service %q has invalid auth.type: %q", svc.Name, svc.Auth.Type)
			}

			if t == "api_key" && !c.Authentication.APIKey.Enabled {
				return fmt.Errorf("service %q requires api_key authentication but global api_key authentication is disabled", svc.Name)
			}

			if t == "jwt" && !c.Authentication.JWT.Enabled {
				return fmt.Errorf("service %q requires jwt authentication but global jwt authentication is disabled", svc.Name)
			}
		}
	}

	// Validate Rate Limit Configurations
	if err := validateRateLimit(c.RateLimit, "rate_limit"); err != nil {
		return err
	}
	for _, svc := range c.Services {
		if err := validateRateLimit(svc.RateLimit, fmt.Sprintf("service %q.rate_limit", svc.Name)); err != nil {
			return err
		}
	}

	return nil
}

func validateRateLimit(rl RateLimitConfig, prefix string) error {
	if rl.Enabled {
		k := strings.ToLower(strings.TrimSpace(rl.KeyBy))
		if k != "" && k != "ip" && k != "consumer" {
			return fmt.Errorf("%s.key_by must be 'ip' or 'consumer'", prefix)
		}
		if rl.RequestsPerSecond <= 0 {
			return fmt.Errorf("%s.requests_per_second must be positive", prefix)
		}
		if rl.Burst <= 0 {
			return fmt.Errorf("%s.burst must be positive", prefix)
		}
	}
	return nil
}