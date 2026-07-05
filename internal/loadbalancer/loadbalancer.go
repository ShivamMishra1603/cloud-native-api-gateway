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

// Select picks the next upstream in round-robin order.
func (rr *RoundRobin) Select(upstreams []*registry.Upstream) (*registry.Upstream, error) {
	n := len(upstreams)
	if n == 0 {
		return nil, ErrNoUpstreamAvailable
	}

	val := atomic.AddUint64(&rr.counter, 1)
	idx := int((val - 1) % uint64(n))
	return upstreams[idx], nil
}

// LeastConnections distributes traffic to the upstream with the lowest active connections count.
type LeastConnections struct{}

// NewLeastConnections creates a LeastConnections instance.
func NewLeastConnections() *LeastConnections {
	return &LeastConnections{}
}

// Select picks the upstream with the lowest connection count.
func (lc *LeastConnections) Select(upstreams []*registry.Upstream) (*registry.Upstream, error) {
	n := len(upstreams)
	if n == 0 {
		return nil, ErrNoUpstreamAvailable
	}

	best := upstreams[0]
	minConns := best.Connections()

	for i := 1; i < n; i++ {
		conns := upstreams[i].Connections()
		if conns < minConns {
			best = upstreams[i]
			minConns = conns
		}
	}

	return best, nil
}
