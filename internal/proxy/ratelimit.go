package proxy

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ipLimiter is a token bucket per client address.
type ipLimiter struct {
	rps   rate.Limit
	burst int

	mu      sync.Mutex
	clients map[string]*clientBucket
	sweep   time.Time
}

type clientBucket struct {
	lim  *rate.Limiter
	seen time.Time
}

func newIPLimiter(rps float64, burst int) *ipLimiter {
	return &ipLimiter{rps: rate.Limit(rps), burst: burst, clients: map[string]*clientBucket{}, sweep: time.Now()}
}

func (l *ipLimiter) allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.sweep) > time.Minute {
		for k, c := range l.clients {
			if now.Sub(c.seen) > 5*time.Minute {
				delete(l.clients, k)
			}
		}
		l.sweep = now
	}
	c, ok := l.clients[ip]
	if !ok {
		c = &clientBucket{lim: rate.NewLimiter(l.rps, l.burst)}
		l.clients[ip] = c
	}
	c.seen = now
	return c.lim.AllowN(now, 1)
}
