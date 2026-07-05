package registry

import (
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
)

// Upstream represents a single backend replica destination.
type Upstream struct {
	URL            *url.URL
	activeRequests int64 // private tracker for active connections

	mu                   sync.RWMutex
	unhealthy            bool
	consecutiveFailures  int
	consecutiveSuccesses int
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

// Service represents a logical backend service holding a list of upstream replicas.
type Service struct {
	Name         string
	Upstreams    []*Upstream
	LoadBalancer string
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
