package proxy

import (
	"context"
	"hash/fnv"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/procmgr"
)

// pickBackend chooses a Node.js instance: least active requests, with a
// rotating start so equal loads are spread round-robin.
func pickBackend(list []*procmgr.Backend, rr *atomic.Uint64) *procmgr.Backend {
	n := len(list)
	if n == 0 {
		return nil
	}
	start := int(rr.Add(1) % uint64(n))
	best := list[start]
	for i := 1; i < n; i++ {
		b := list[(start+i)%n]
		if b.Active.Load() < best.Active.Load() {
			best = b
		}
	}
	return best
}

// upstream is one target of a reverse-proxy site.
type upstream struct {
	url     *url.URL
	weight  int
	healthy atomic.Bool
	active  atomic.Int64
	fails   atomic.Int32
	downAt  atomic.Int64 // unix nano of passive failure

	mu      sync.Mutex
	lastErr string
}

type upstreamPool struct {
	siteID   string
	siteName string
	list     []*upstream
	strategy string
	hc       model.HealthCheck
	rr       atomic.Uint64
	bus      *events.Bus
	stop     chan struct{}
}

func newUpstreamPool(site *model.Site, bus *events.Bus, prev *upstreamPool) *upstreamPool {
	p := &upstreamPool{siteID: site.ID, siteName: site.Name, strategy: site.Proxy.LoadBalancing, hc: site.Proxy.HealthCheck, bus: bus, stop: make(chan struct{})}
	for _, u := range site.Proxy.Upstreams {
		parsed, err := url.Parse(u.URL)
		if err != nil {
			continue
		}
		up := &upstream{url: parsed, weight: max(u.Weight, 1)}
		up.healthy.Store(true)
		// Keep health state across configuration reloads.
		if prev != nil {
			for _, old := range prev.list {
				if old.url.String() == parsed.String() {
					up.healthy.Store(old.healthy.Load())
					up.fails.Store(old.fails.Load())
				}
			}
		}
		p.list = append(p.list, up)
	}
	if p.hc.Enabled {
		go p.healthLoop(site.Proxy.InsecureSkipVerify)
	}
	return p
}

func (p *upstreamPool) close() { close(p.stop) }

// available returns healthy upstreams, or all of them if none are healthy:
// trying a possibly-down upstream beats refusing every request.
func (p *upstreamPool) available() []*upstream {
	var out []*upstream
	now := time.Now().UnixNano()
	for _, u := range p.list {
		if !u.healthy.Load() {
			continue
		}
		// Passive failure: skip for 10s unless active checks manage health.
		if !p.hc.Enabled && now-u.downAt.Load() < int64(10*time.Second) {
			continue
		}
		out = append(out, u)
	}
	if len(out) == 0 {
		return p.list
	}
	return out
}

func (p *upstreamPool) pick(clientIP string) *upstream {
	list := p.available()
	if len(list) == 0 {
		return nil
	}
	switch p.strategy {
	case "least_conn":
		best := list[0]
		for _, u := range list[1:] {
			if float64(u.active.Load())/float64(u.weight) < float64(best.active.Load())/float64(best.weight) {
				best = u
			}
		}
		return best
	case "ip_hash":
		h := fnv.New32a()
		h.Write([]byte(clientIP))
		return list[int(h.Sum32())%len(list)]
	case "random":
		return weighted(list, rand.IntN(totalWeight(list)))
	default: // weighted round robin
		return weighted(list, int(p.rr.Add(1)%uint64(totalWeight(list))))
	}
}

func totalWeight(list []*upstream) int {
	t := 0
	for _, u := range list {
		t += u.weight
	}
	return max(t, 1)
}

func weighted(list []*upstream, n int) *upstream {
	for _, u := range list {
		if n < u.weight {
			return u
		}
		n -= u.weight
	}
	return list[len(list)-1]
}

// passiveFailure records a failed proxy attempt.
func (p *upstreamPool) passiveFailure(u *upstream, err error) {
	u.mu.Lock()
	u.lastErr = err.Error()
	u.mu.Unlock()
	if !p.hc.Enabled {
		u.downAt.Store(time.Now().UnixNano())
	}
}

func (p *upstreamPool) healthLoop(insecure bool) {
	client := &http.Client{
		Transport:     newTransport(insecure, 0),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	interval := time.Duration(p.hc.IntervalSec) * time.Second
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for _, u := range p.list {
			p.check(client, u)
		}
		select {
		case <-p.stop:
			return
		case <-t.C:
		}
	}
}

func (p *upstreamPool) check(client *http.Client, u *upstream) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.hc.TimeoutSec)*time.Second)
	defer cancel()
	target := *u.url
	target.Path = p.hc.Path
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	req.Header.Set("User-Agent", "NodeHoster-HealthCheck")
	resp, err := client.Do(req)
	ok := err == nil && resp.StatusCode < 500
	if resp != nil {
		resp.Body.Close()
	}
	if ok {
		u.fails.Store(0)
		if !u.healthy.Swap(true) {
			p.bus.Info(events.UpstreamUp, p.siteID, "%s upstream %s is healthy again", p.siteName, u.url)
		}
		return
	}
	msg := "server error"
	if err != nil {
		msg = err.Error()
	} else {
		msg = resp.Status
	}
	u.mu.Lock()
	u.lastErr = msg
	u.mu.Unlock()
	if int(u.fails.Add(1)) >= p.hc.UnhealthyThreshold && u.healthy.Swap(false) {
		p.bus.Warn(events.UpstreamDown, p.siteID, "%s upstream %s is down: %s", p.siteName, u.url, msg)
	}
}

func (p *upstreamPool) status() []model.UpstreamStatus {
	out := make([]model.UpstreamStatus, 0, len(p.list))
	now := time.Now().UnixNano()
	for _, u := range p.list {
		u.mu.Lock()
		le := u.lastErr
		u.mu.Unlock()
		healthy := u.healthy.Load() && (p.hc.Enabled || now-u.downAt.Load() >= int64(10*time.Second))
		out = append(out, model.UpstreamStatus{URL: u.url.String(), Healthy: healthy, ActiveConns: u.active.Load(), LastError: le})
	}
	return out
}
