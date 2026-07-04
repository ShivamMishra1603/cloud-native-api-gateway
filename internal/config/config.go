package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Gateway       GatewayConfig       `yaml:"gateway"`
	Observability ObservabilityConfig `yaml:"observability"`
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

	return nil
}