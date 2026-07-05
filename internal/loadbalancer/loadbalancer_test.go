package loadbalancer

import (
	"net/url"
	"testing"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/registry"
)

func TestRoundRobin(t *testing.T) {
	u1, _ := url.Parse("http://localhost:8081")
	u2, _ := url.Parse("http://localhost:8082")
	u3, _ := url.Parse("http://localhost:8083")

	upstreams := []*registry.Upstream{
		{URL: u1},
		{URL: u2},
		{URL: u3},
	}

	rr := NewRoundRobin()

	// Verify cycle: 1 -> 2 -> 3 -> 1 -> 2
	expected := []string{
		"http://localhost:8081",
		"http://localhost:8082",
		"http://localhost:8083",
		"http://localhost:8081",
		"http://localhost:8082",
	}

	for i, exp := range expected {
		selected, err := rr.Select(upstreams)
		if err != nil {
			t.Fatalf("unexpected error at select[%d]: %v", i, err)
		}
		if selected.URL.String() != exp {
			t.Errorf("select[%d]: expected URL %s, got %s", i, exp, selected.URL.String())
		}
	}
}

func TestLeastConnections(t *testing.T) {
	u1, _ := url.Parse("http://localhost:8081")
	u2, _ := url.Parse("http://localhost:8082")
	u3, _ := url.Parse("http://localhost:8083")

	ups1 := &registry.Upstream{URL: u1}
	ups2 := &registry.Upstream{URL: u2}
	ups3 := &registry.Upstream{URL: u3}

	upstreams := []*registry.Upstream{ups1, ups2, ups3}

	lc := NewLeastConnections()

	// Initial selection (all have 0 conns, picks first)
	selected, err := lc.Select(upstreams)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != ups1 {
		t.Errorf("expected first upstream initially, got %s", selected.URL.String())
	}

	// Increment connection on ups1
	ups1.Increment() // conns: ups1=1, ups2=0, ups3=0

	selected, err = lc.Select(upstreams)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != ups2 {
		t.Errorf("expected ups2 (0 conns), got %s", selected.URL.String())
	}

	// Increment connection on ups2
	ups2.Increment() // conns: ups1=1, ups2=1, ups3=0

	selected, err = lc.Select(upstreams)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != ups3 {
		t.Errorf("expected ups3 (0 conns), got %s", selected.URL.String())
	}

	// Increment ups3 twice
	ups3.Increment()
	ups3.Increment() // conns: ups1=1, ups2=1, ups3=2

	// Now ups1 or ups2 have 1 connection (ups3 has 2). Should pick ups1
	selected, err = lc.Select(upstreams)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != ups1 && selected != ups2 {
		t.Errorf("expected ups1 or ups2 (1 conn), got %s", selected.URL.String())
	}
}

func TestLoadBalancersEmpty(t *testing.T) {
	var upstreams []*registry.Upstream

	rr := NewRoundRobin()
	_, err := rr.Select(upstreams)
	if err != ErrNoUpstreamAvailable {
		t.Errorf("expected ErrNoUpstreamAvailable, got %v", err)
	}

	lc := NewLeastConnections()
	_, err = lc.Select(upstreams)
	if err != ErrNoUpstreamAvailable {
		t.Errorf("expected ErrNoUpstreamAvailable, got %v", err)
	}
}
