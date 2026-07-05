package loadbalancer

import (
	"errors"
	"sync/atomic"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
)

var ErrNoUpstreamAvailable = errors.New("no upstream available")

// LoadBalancer defines the interface for backend upstream selection strategies.
type LoadBalancer interface {
	Select(upstreams []*registry.Upstream) (*registry.Upstream, error)
}

// RoundRobin distributes traffic sequentially across available upstreams.
type RoundRobin struct {
	counter uint64
}

// NewRoundRobin creates a RoundRobin instance.
func NewRoundRobin() *RoundRobin {
	return &RoundRobin{}
}

// Select picks the next healthy upstream in round-robin order.
func (rr *RoundRobin) Select(upstreams []*registry.Upstream) (*registry.Upstream, error) {
	var healthy []*registry.Upstream
	for _, u := range upstreams {
		if u.IsHealthy() {
			healthy = append(healthy, u)
		}
	}

	n := len(healthy)
	if n == 0 {
		return nil, ErrNoUpstreamAvailable
	}

	val := atomic.AddUint64(&rr.counter, 1)
	idx := int((val - 1) % uint64(n))
	return healthy[idx], nil
}

// LeastConnections distributes traffic to the upstream with the lowest active connections count.
type LeastConnections struct{}

// NewLeastConnections creates a LeastConnections instance.
func NewLeastConnections() *LeastConnections {
	return &LeastConnections{}
}

// Select picks the healthy upstream with the lowest connection count.
func (lc *LeastConnections) Select(upstreams []*registry.Upstream) (*registry.Upstream, error) {
	var healthy []*registry.Upstream
	for _, u := range upstreams {
		if u.IsHealthy() {
			healthy = append(healthy, u)
		}
	}

	n := len(healthy)
	if n == 0 {
		return nil, ErrNoUpstreamAvailable
	}

	best := healthy[0]
	minConns := best.Connections()

	for i := 1; i < n; i++ {
		conns := healthy[i].Connections()
		if conns < minConns {
			best = healthy[i]
			minConns = conns
		}
	}

	return best, nil
}
