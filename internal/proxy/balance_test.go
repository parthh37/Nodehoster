package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func localPool(t *testing.T, ready *atomic.Bool, localWeight int, servers ...model.Upstream) *upstreamPool {
	t.Helper()
	site := &model.Site{ID: "s1", Name: "app"}
	p := newUpstreamPool(site, poolConfig{
		upstreams: servers, strategy: "round_robin", localWeight: localWeight,
		localReady: ready.Load, localCount: func() int { return 1 },
	}, nil, nil)
	t.Cleanup(p.close)
	return p
}

func TestPoolLocalMemberShare(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	p := localPool(t, &ready, 2, model.Upstream{URL: "http://10.0.0.2", Weight: 1})

	local := 0
	for range 300 {
		if p.pick("").local {
			local++
		}
	}
	if local != 200 {
		t.Errorf("local got %d of 300 requests, want 200 (weight 2 of 3)", local)
	}

	// While the local instances restart, everything goes to the other server.
	ready.Store(false)
	for range 10 {
		if p.pick("").local {
			t.Fatal("picked the local member while it has no ready instances")
		}
	}
	if st := p.status(); !st[0].Local || st[0].Healthy {
		t.Errorf("status of not-ready local member = %+v", st[0])
	}
}

func TestBalancedNodeSite(t *testing.T) {
	var gotHost, gotHop atomic.Value
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost.Store(r.Host)
		gotHop.Store(r.Header.Get(hopHeader))
		io.WriteString(w, "remote")
	}))
	defer remote.Close()

	var ready atomic.Bool
	ready.Store(true)
	site := &model.Site{ID: "s1", Name: "app", Type: model.SiteNode, Node: &model.NodeConfig{}}
	srv := &Server{deps: Deps{Log: slog.New(slog.DiscardHandler)}, stdLog: slog.NewLogLogger(slog.DiscardHandler, slog.LevelDebug)}
	rt := &siteRuntime{srv: srv, site: site, transport: newTransport(false, 0)}
	rt.pool = localPool(t, &ready, 1, model.Upstream{URL: remote.URL})
	h := rt.upstreamHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "local")
	}))

	serve := func(hop bool) string {
		req := httptest.NewRequest("GET", "http://app.example.com/", nil)
		if hop {
			req.Header.Set(hopHeader, "1")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Body.String()
	}

	seen := map[string]int{}
	for range 4 {
		seen[serve(false)]++
	}
	if seen["local"] != 2 || seen["remote"] != 2 {
		t.Errorf("responses = %v, want 2 local and 2 remote", seen)
	}
	if h := gotHost.Load(); h != "app.example.com" {
		t.Errorf("remote saw Host %v, want the client's host so its binding matches", h)
	}
	if h := gotHop.Load(); h != "1" {
		t.Errorf("remote saw %s = %v, want 1", hopHeader, h)
	}

	// A request another balancer already forwarded is always answered here.
	for range 4 {
		if got := serve(true); got != "local" {
			t.Fatalf("forwarded request answered by %s, want local", got)
		}
	}
}
