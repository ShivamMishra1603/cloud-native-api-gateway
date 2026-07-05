package health

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/metrics"
	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
)

// Checker handles background periodic health monitoring for upstreams.
type Checker struct {
	cfg    config.HealthCheckConfig
	client *http.Client
	reg    *registry.Registry
}

// NewChecker instantiates a Checker with validation defaults.
func NewChecker(cfg config.HealthCheckConfig, reg *registry.Registry) *Checker {
	return &Checker{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
		reg: reg,
	}
}

// Start launches a periodic health check monitor loop for each upstream replica.
func (c *Checker) Start(ctx context.Context) {
	if !c.cfg.Enabled {
		slog.Info("health checker: disabled in config")
		return
	}

	services := c.reg.Services()
	slog.Info("health checker: starting monitoring background tasks", "services_count", len(services))

	for _, svc := range services {
		for _, upstream := range svc.Upstreams {
			go c.checkLoop(ctx, svc.Name, upstream)
		}
	}
}

func (c *Checker) checkLoop(ctx context.Context, serviceName string, upstream *registry.Upstream) {
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	slog.Info("health checker: worker started", "service", serviceName, "url", upstream.URL.String())

	// Run initial check immediately
	c.checkUpstream(serviceName, upstream)

	for {
		select {
		case <-ctx.Done():
			slog.Info("health checker: worker stopped", "service", serviceName, "url", upstream.URL.String())
			return
		case <-ticker.C:
			c.checkUpstream(serviceName, upstream)
		}
	}
}

func (c *Checker) checkUpstream(serviceName string, upstream *registry.Upstream) {
	// Build health url path
	healthURL := *upstream.URL
	healthURL.Path = singleJoiningSlash(healthURL.Path, c.cfg.Path)

	resp, err := c.client.Get(healthURL.String())
	if err != nil {
		transitioned := upstream.ReportFailure(c.cfg.FailureThreshold)
		if transitioned {
			metrics.UpstreamHealthStatus.WithLabelValues(serviceName, upstream.URL.String()).Set(0)
			slog.Warn("upstream state transition: UNHEALTHY",
				"service", serviceName,
				"url", upstream.URL.String(),
				"reason", err.Error(),
			)
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		transitioned := upstream.ReportSuccess(c.cfg.SuccessThreshold)
		if transitioned {
			metrics.UpstreamHealthStatus.WithLabelValues(serviceName, upstream.URL.String()).Set(1)
			slog.Info("upstream state transition: HEALTHY (recovered)",
				"service", serviceName,
				"url", upstream.URL.String(),
				"status", resp.Status,
			)
		}
	} else {
		transitioned := upstream.ReportFailure(c.cfg.FailureThreshold)
		if transitioned {
			metrics.UpstreamHealthStatus.WithLabelValues(serviceName, upstream.URL.String()).Set(0)
			slog.Warn("upstream state transition: UNHEALTHY",
				"service", serviceName,
				"url", upstream.URL.String(),
				"status", resp.Status,
			)
		}
	}
}

// singleJoiningSlash safely joins base path and health check subpath.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		if a == "" {
			return b
		}
		return a + "/" + b
	}
	return a + b
}
