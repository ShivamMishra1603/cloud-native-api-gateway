package registry

import (
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
)

type CircuitBreakerState int

const (
	StateClosed CircuitBreakerState = iota
	StateOpen
	StateHalfOpen
)

// Upstream represents a single backend replica destination.
type Upstream struct {
	URL            *url.URL
	activeRequests int64 // private tracker for active connections

	mu                   sync.RWMutex
	unhealthy            bool
	consecutiveFailures  int
	consecutiveSuccesses int

	// circuit breaker state
	cbState            CircuitBreakerState
	cbFailureCount     int
	cbOpenUntil        time.Time
	cbHalfOpenRequests int
}

// Increment increases the active connection counter.
func (u *Upstream) Increment() {
	atomic.AddInt64(&u.activeRequests, 1)
}

// Decrement decreases the active connection counter.
func (u *Upstream) Decrement() {
	atomic.AddInt64(&u.activeRequests, -1)
}

// Connections returns the current active connection count.
func (u *Upstream) Connections() int64 {
	return atomic.LoadInt64(&u.activeRequests)
}

// IsHealthy returns true if the upstream is considered healthy.
func (u *Upstream) IsHealthy() bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return !u.unhealthy
}

// IsEligible returns true if the upstream is healthy and not blocked by the circuit breaker.
func (u *Upstream) IsEligible(cbCfg config.CircuitBreakerConfig) bool {
	u.mu.Lock()
	defer u.mu.Unlock()

	// If health checks marked it unhealthy, skip it
	if u.unhealthy {
		return false
	}

	if !cbCfg.Enabled {
		return true
	}

	if u.cbState == StateOpen {
		if time.Now().After(u.cbOpenUntil) {
			// Transition Open -> Half-Open
			u.cbState = StateHalfOpen
			u.cbFailureCount = 0
			u.cbHalfOpenRequests = 0
		} else {
			return false // still blocked
		}
	}

	if u.cbState == StateHalfOpen {
		maxRequests := cbCfg.HalfOpenMaxRequests
		if maxRequests <= 0 {
			maxRequests = 1
		}
		if u.cbHalfOpenRequests >= maxRequests {
			return false // throttle trial requests
		}
		u.cbHalfOpenRequests++
	}

	return true
}

// RecordResult updates the circuit breaker state based on the outcome of a request.
func (u *Upstream) RecordResult(success bool, cbCfg config.CircuitBreakerConfig) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if !cbCfg.Enabled {
		return
	}

	if success {
		if u.cbState == StateHalfOpen {
			// Success in Half-Open transitions back to Closed
			u.cbState = StateClosed
			u.cbFailureCount = 0
			u.cbHalfOpenRequests = 0
		} else if u.cbState == StateClosed {
			u.cbFailureCount = 0
		}
	} else {
		// Request failed
		if u.cbState == StateClosed {
			u.cbFailureCount++
			threshold := cbCfg.FailureThreshold
			if threshold <= 0 {
				threshold = 5
			}
			if u.cbFailureCount >= threshold {
				u.cbState = StateOpen
				openTimeout := cbCfg.OpenTimeout
				if openTimeout <= 0 {
					openTimeout = 10 * time.Second
				}
				u.cbOpenUntil = time.Now().Add(openTimeout)
			}
		} else if u.cbState == StateHalfOpen {
			// Any failure in Half-Open immediately trips back to Open
			u.cbState = StateOpen
			openTimeout := cbCfg.OpenTimeout
			if openTimeout <= 0 {
				openTimeout = 10 * time.Second
			}
			u.cbOpenUntil = time.Now().Add(openTimeout)
			u.cbHalfOpenRequests = 0
		}
	}
}

// DecrementHalfOpen decreases the active trial request counter if we remain in Half-Open.
func (u *Upstream) DecrementHalfOpen() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cbState == StateHalfOpen && u.cbHalfOpenRequests > 0 {
		u.cbHalfOpenRequests--
	}
}

// ReportFailure increments consecutive failures. If it crosses the threshold,
// the upstream transitions to unhealthy. Returns true if state transitioned.
func (u *Upstream) ReportFailure(threshold int) bool {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.consecutiveSuccesses = 0
	u.consecutiveFailures++

	if !u.unhealthy && u.consecutiveFailures >= threshold {
		u.unhealthy = true
		return true // transitioned
	}
	return false
}

// ReportSuccess increments consecutive successes. If it crosses the threshold,
// the upstream transitions to healthy. Returns true if state transitioned.
func (u *Upstream) ReportSuccess(threshold int) bool {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.consecutiveFailures = 0
	u.consecutiveSuccesses++

	if u.unhealthy && u.consecutiveSuccesses >= threshold {
		u.unhealthy = false
		return true // transitioned
	}
	return false
}

type Service struct {
	Name         string
	Upstreams    []*Upstream
	LoadBalancer string
	Auth         Auth // Service-level authentication policy
	RateLimit    config.RateLimitConfig
	Resiliency   config.ResiliencyConfig
}

type Auth struct {
	Enabled          bool
	Type             string
	AllowedConsumers []string
}

// Registry maintains service configuration maps.
type Registry struct {
	mu       sync.RWMutex
	services map[string]*Service
}

// New compiles a Registry instance populated from configuration.
func New(cfg *config.Config) (*Registry, error) {
	services := make(map[string]*Service)
	for _, svc := range cfg.Services {
		var upstreams []*Upstream
		for _, ups := range svc.Upstreams {
			parsed, err := url.Parse(ups.URL)
			if err != nil {
				return nil, fmt.Errorf("service %q upstream %q is invalid: %w", svc.Name, ups.URL, err)
			}
			upstreams = append(upstreams, &Upstream{
				URL: parsed,
			})
		}

		lb := svc.LoadBalancer
		if lb == "" {
			lb = "round_robin"
		}

		services[svc.Name] = &Service{
			Name:         svc.Name,
			Upstreams:    upstreams,
			LoadBalancer: lb,
			Auth: Auth{
				Enabled:          svc.Auth.Enabled,
				Type:             svc.Auth.Type,
				AllowedConsumers: svc.Auth.AllowedConsumers,
			},
			RateLimit:  svc.RateLimit,
			Resiliency: svc.Resiliency,
		}
	}

	return &Registry{
		services: services,
	}, nil
}

// GetService queries the registry and returns the requested service, hiding the map implementation.
func (r *Registry) GetService(name string) (*Service, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	svc, ok := r.services[name]
	return svc, ok
}

// Services returns a slice of all compiled Services, used by the health checker.
func (r *Registry) Services() []*Service {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var svcs []*Service
	for _, svc := range r.services {
		svcs = append(svcs, svc)
	}
	return svcs
}
