package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Gateway GatewayConfig `yaml:"gateway"`
}

type GatewayConfig struct {
	Port         int           `yaml:"port"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	IdleTimeout  time.Duration `yaml:"idle_timeout"`
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

	return nil
}