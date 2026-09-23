package proxy

import (
	"context"
	"fmt"
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

// pickBackendIn chooses a Node.js instance, preferring the slot a session
// affinity cookie pins (noSlot = none). A pinned slot that has no ready
// instance, because it is restarting or the site was scaled down, falls
// back to the normal choice.
func pickBackendIn(list []*procmgr.Backend, rr *atomic.Uint64, slot int) *procmgr.Backend {
	if slot != noSlot {
		// While a slot is being recycled, its old and new instances are
		// both listed; either may answer.
		var best *procmgr.Backend
		for _, b := range list {
			if b.Slot == slot && (best == nil || b.Active.Load() < best.Active.Load()) {
				best = b
			}
		}
		if best != nil {
			return best
		}
	}
	return pickBackend(list, rr)
}

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

// upstream is one target of a reverse-proxy site, or of a load-balanced
// node site where one member stands for the site's own local instances.
type upstream struct {
	url     *url.URL // nil for the local member
	local   bool
	weight  int
	healthy atomic.Bool
	active  atomic.Int64
	fails   atomic.Int32
	downAt  atomic.Int64 // unix nano of passive failure
	affID   affinityID   // how session affinity cookies name it

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
	once     sync.Once

	// localReady reports whether the local member can take requests;
	// nil when the pool has no local member.
	localReady func() bool
	localCount func() int
}

type poolConfig struct {
	upstreams []model.Upstream
	strategy  string
	hc        model.HealthCheck
	insecure  bool

	// For a load-balanced node site: the local instances' share and state.
	localWeight int
	localReady  func() bool
	localCount  func() int
}

func proxyPoolConfig(site *model.Site) poolConfig {
	p := site.Proxy
	return poolConfig{upstreams: p.Upstreams, strategy: p.LoadBalancing, hc: p.HealthCheck, insecure: p.InsecureSkipVerify}
}

func newUpstreamPool(site *model.Site, cfg poolConfig, bus *events.Bus, prev *upstreamPool) *upstreamPool {
	p := &upstreamPool{
		siteID: site.ID, siteName: site.Name, strategy: cfg.strategy, hc: cfg.hc, bus: bus, stop: make(chan struct{}),
		localReady: cfg.localReady, localCount: cfg.localCount,
	}
	if cfg.localReady != nil {
		p.list = append(p.list, &upstream{local: true, weight: max(cfg.localWeight, 1)})
	}
	for _, u := range cfg.upstreams {
		parsed, err := url.Parse(u.URL)
		if err != nil {
			continue
		}
		up := &upstream{url: parsed, weight: max(u.Weight, 1)}
		up.healthy.Store(true)
		// Keep health state across configuration reloads.
		if prev != nil {
			for _, old := range prev.list {
				if !old.local && old.url.String() == parsed.String() {
					up.healthy.Store(old.healthy.Load())
					up.fails.Store(old.fails.Load())
				}
			}
		}
		p.list = append(p.list, up)
	}
	if p.hc.Enabled {
		go p.healthLoop(cfg.insecure)
	}
	return p
}

func (p *upstreamPool) close() { p.once.Do(func() { close(p.stop) }) }

// setAffinity names the members for session affinity cookies.
func (p *upstreamPool) setAffinity(a *affinityRuntime) {
	if a == nil {
		return
	}
	for _, u := range p.list {
		if u.local {
			u.affID = a.localID
		} else {
			u.affID = a.memberID(u.url.String())
		}
	}
}

// pinned returns the member a session affinity cookie names if it can take
// requests now; nil sends the client to the strategy's choice (and a new
// cookie) instead.
func (p *upstreamPool) pinned(id affinityID) *upstream {
	for _, u := range p.available() {
		if u.affID == id {
			return u
		}
	}
	return nil
}

// available returns healthy upstreams, or all of them if none are healthy:
// trying a possibly-down upstream beats refusing every request.
func (p *upstreamPool) available() []*upstream {
	var out []*upstream
	now := time.Now().UnixNano()
	for _, u := range p.list {
		if u.local {
			if p.localReady() {
				out = append(out, u)
			}
			continue
		}
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
			if !u.local {
				p.check(client, u)
			}
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
		if u.local {
			out = append(out, model.UpstreamStatus{
				URL: fmt.Sprintf("this server (%d ready)", p.localCount()), Local: true,
				Healthy: p.localReady(), ActiveConns: u.active.Load(),
			})
			continue
		}
		u.mu.Lock()
		le := u.lastErr
		u.mu.Unlock()
		healthy := u.healthy.Load() && (p.hc.Enabled || now-u.downAt.Load() >= int64(10*time.Second))
		out = append(out, model.UpstreamStatus{URL: u.url.String(), Healthy: healthy, ActiveConns: u.active.Load(), LastError: le})
	}
	return out
}

// canRetryLocally decides whether a request that failed on a remote server
// of a load-balanced node site may be answered by the local instances
// instead of returning 502 to the client. It runs before anything has been
// written to the client, but the transport has already closed r.Body.
func canRetryLocally(r *http.Request, err error) bool {
	// TODO(user): decide which failed requests are safe to replay locally.
	return false
}
