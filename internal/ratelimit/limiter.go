package ratelimit

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// Limiter manages dynamic Token Bucket rate limiters keyed by client identifiers.
type Limiter struct {
	mu      sync.Mutex
	clients map[string]*clientLimiter
	rps     rate.Limit
	burst   int
}

// NewLimiter creates a new Limiter instance and starts a background sweeper loop
// to purge inactive client limiters and prevent memory leaks.
func NewLimiter(ctx context.Context, rps float64, burst int) *Limiter {
	l := &Limiter{
		clients: make(map[string]*clientLimiter),
		rps:     rate.Limit(rps),
		burst:   burst,
	}

	go l.startCleanupLoop(ctx)

	return l
}

// Allow checks if the request is permitted for the given key (IP or consumer).
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	client, ok := l.clients[key]
	if !ok {
		client = &clientLimiter{
			limiter: rate.NewLimiter(l.rps, l.burst),
		}
		l.clients[key] = client
	}
	client.lastSeen = time.Now()

	return client.limiter.Allow()
}

func (l *Limiter) startCleanupLoop(ctx context.Context) {
	// Sweep maps every 5 minutes
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.mu.Lock()
			now := time.Now()
			for key, client := range l.clients {
				// Purge clients inactive for more than 1 hour
				if now.Sub(client.lastSeen) > 1*time.Hour {
					delete(l.clients, key)
				}
			}
			l.mu.Unlock()
		}
	}
}
