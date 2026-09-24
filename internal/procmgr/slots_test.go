package procmgr

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// slotApp answers with its release (the folder it runs from), DB and pid;
// /slow takes a while. It refuses to start when CRASH_WITH equals DB.
const slotApp = `
const http = require('http'), path = require('path');
const release = path.basename(__dirname);
if (process.env.CRASH_WITH && process.env.CRASH_WITH === process.env.DB) process.exit(3);
http.createServer((req, res) => {
  if (req.url === '/slow') return setTimeout(() => res.end('slow ' + release), 1500);
  res.end(release + ' ' + process.env.DB + ' ' + process.pid + ' ' + req.headers.host);
}).listen(process.env.PORT);
`

// slotSite has releases rA (production) and rB (staging) under the
// manager's sites folder. DB is a slot setting.
func slotTestSite(t *testing.T, app string) *model.Site {
	t.Helper()
	sites := filepath.Join(filepath.Dir(app), "sites")
	for _, r := range []string{"rA", "rB"} {
		dir := model.ReleaseDir(sites, "s1", r)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "server.js"), []byte(slotApp), 0o644)
	}
	site := &model.Site{ID: "s1", Name: "shop", Type: model.SiteNode, ActiveRelease: "rA",
		Bindings: []model.Binding{{Protocol: "http", Port: 80, Host: "www.example.com"}, {Protocol: "http", Port: 80, Host: "staging.example.com", Slot: "staging"}},
		Node: &model.NodeConfig{Script: "server.js", Instances: 2, ShutdownTimeoutSec: 5,
			Env: []model.EnvVar{{Name: "DB", Value: "prod", SlotSetting: true}}},
		Slots: []model.DeploymentSlot{{Name: "staging", Instances: 1, ActiveRelease: "rB", Env: []model.EnvVar{{Name: "DB", Value: "staging"}}}},
	}
	site.ApplyDefaults()
	return site
}

// answers asks every ready instance of a key and returns what they said,
// without the pid and host.
func answers(t *testing.T, m *Manager, key string) []string {
	t.Helper()
	var out []string
	for _, b := range m.Backends(key) {
		f := strings.Fields(get(t, "http://"+b.Addr+"/"))
		out = append(out, f[0]+" "+f[1])
	}
	return out
}

func pids(m *Manager, key string) map[int]bool {
	st, _ := m.Status(key)
	out := map[int]bool{}
	for _, in := range st.Instances {
		out[in.PID] = true
	}
	return out
}

// TestSlotSwap runs a production release and a staging slot side by side,
// prepares the slot with production's settings, warms it up and swaps:
// production traffic moves onto the very processes that were warmed up, a
// request running on old production finishes, and the old production
// release carries on in the slot with the slot's settings.
func TestSlotSwap(t *testing.T) {
	m, app := newRecoveryManager(t)
	site := slotTestSite(t, app)
	m.Apply(site)
	if err := m.Start("s1"); err != nil {
		t.Fatal(err)
	}
	if err := m.StartSlot("s1", "staging"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "production", func() bool { return len(m.Backends("s1")) == 2 })
	waitFor(t, "staging", func() bool { return len(m.Backends("s1@staging")) == 1 })
	if got := answers(t, m, "s1"); got[0] != "rA prod" || got[1] != "rA prod" {
		t.Fatalf("production = %v", got)
	}
	if got := answers(t, m, "s1@staging"); got[0] != "rB staging" {
		t.Fatalf("staging = %v", got)
	}

	// Phase 1: the slot restarts with production's settings (DB=prod) and
	// production's instance count.
	if _, err := m.PrepareSwap(context.Background(), model.SwapSite(site, "staging"), "staging"); err != nil {
		t.Fatal(err)
	}
	if got := answers(t, m, "s1@staging"); len(got) != 2 || got[0] != "rB prod" || got[1] != "rB prod" {
		t.Fatalf("prepared staging = %v", got)
	}
	// Apply leaves a slot being swapped alone.
	m.Apply(site)
	if got := answers(t, m, "s1@staging"); len(got) != 2 || got[0] != "rB prod" {
		t.Fatalf("staging reconfigured during the swap: %v", got)
	}

	// Phase 2: warm-up, with production's host name.
	var logged []string
	err := m.WarmUp(context.Background(), "s1", "staging", model.WarmupConfig{Paths: []string{"/", "/slow"}, Statuses: "200-399", TimeoutSec: 20},
		"www.example.com", "https", func(f string, a ...any) { logged = append(logged, f) })
	if err != nil {
		t.Fatal(err)
	}
	if len(logged) != 4 {
		t.Fatalf("warm-up log: %v", logged)
	}
	warmPIDs := pids(m, "s1@staging")

	// A request in flight on production while it swaps, counted the way
	// the proxy counts it.
	old := m.Backends("s1")[0]
	slow := make(chan string, 1)
	old.Active.Add(1)
	go func() {
		defer old.Active.Add(-1)
		resp, err := http.Get("http://" + old.Addr + "/slow")
		if err != nil {
			slow <- err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		slow <- string(b)
	}()
	time.Sleep(200 * time.Millisecond)

	// Phase 3: swap, then the new configuration.
	if err := m.CommitSwap("s1", "staging"); err != nil {
		t.Fatal(err)
	}
	next := *site
	next.ActiveRelease = "rB"
	next.Slots = []model.DeploymentSlot{site.Slots[0]}
	next.Slots[0].ActiveRelease = "rA"
	m.Apply(&next)

	if got := answers(t, m, "s1"); len(got) != 2 || got[0] != "rB prod" || got[1] != "rB prod" {
		t.Fatalf("production after swap = %v", got)
	}
	if got := pids(m, "s1"); len(got) != 2 || !got[firstKey(warmPIDs)] {
		t.Fatalf("production restarted after the swap: %v, warmed %v", got, warmPIDs)
	}
	if s := <-slow; s != "slow rA" {
		t.Fatalf("in-flight request = %q\n%s", s, logText(m, "s1"))
	}
	// The old production release is now the slot, recycled onto the
	// slot's settings and instance count.
	waitFor(t, "old production in staging", func() bool {
		got := m.Backends("s1@staging")
		if len(got) != 1 {
			return false
		}
		f := strings.Fields(get(t, "http://"+got[0].Addr+"/"))
		return f[0] == "rA" && f[1] == "staging"
	})

	// Logs are attributed to the slot the app was in when it wrote them.
	var staging, prod int
	for _, l := range m.Logs("s1").Recent(1000) {
		switch l.Slot {
		case "staging":
			staging++
		case "":
			prod++
		default:
			t.Fatalf("line from slot %q", l.Slot)
		}
	}
	if staging == 0 || prod == 0 {
		t.Fatalf("staging %d, production %d lines", staging, prod)
	}
	// Swapping again is the rollback.
	if _, err := m.PrepareSwap(context.Background(), model.SwapSite(&next, "staging"), "staging"); err != nil {
		t.Fatal(err)
	}
	if err := m.CommitSwap("s1", "staging"); err != nil {
		t.Fatal(err)
	}
	m.Apply(site)
	if got := answers(t, m, "s1"); got[0] != "rA prod" {
		t.Fatalf("after swapping back = %v", got)
	}
}

func firstKey(m map[int]bool) int {
	for k := range m {
		return k
	}
	return 0
}

// TestSwapPrepareFails: a slot whose release cannot run with production's
// settings fails phase 1; cancelling puts it back on its own settings.
func TestSwapPrepareFails(t *testing.T) {
	m, app := newRecoveryManager(t)
	site := slotTestSite(t, app)
	site.Node.StartupTimeoutSec = 5
	site.Slots[0].Env = append(site.Slots[0].Env, model.EnvVar{Name: "CRASH_WITH", Value: "prod"})
	m.Apply(site)
	m.Start("s1")
	m.StartSlot("s1", "staging")
	waitFor(t, "staging", func() bool { return len(m.Backends("s1@staging")) == 1 })

	prep := model.SwapSite(site, "staging")
	// Production's settings carry no CRASH_WITH; give it to them so the
	// slot's release cannot start with DB=prod.
	n := *prep.Node
	n.Env = append(append([]model.EnvVar(nil), n.Env...), model.EnvVar{Name: "CRASH_WITH", Value: "prod"})
	prep.Node = &n
	if _, err := m.PrepareSwap(context.Background(), prep, "staging"); err == nil {
		t.Fatal("prepare succeeded")
	}
	// The failed replacement left the slot's instance running.
	if got := answers(t, m, "s1@staging"); len(got) != 1 || got[0] != "rB staging" {
		t.Fatalf("staging = %v", got)
	}
	m.CancelSwap(site, "staging", false)
	m.mu.Lock()
	swapping := m.swapping["s1"]
	m.mu.Unlock()
	if swapping {
		t.Fatal("still swapping")
	}
	if got := answers(t, m, "s1"); len(got) != 2 || got[0] != "rA prod" {
		t.Fatalf("production = %v", got)
	}
}

// TestSlotLifecycle: slots are registered, started, stopped and removed
// with the site's configuration.
func TestSlotLifecycle(t *testing.T) {
	m, app := newRecoveryManager(t)
	site := slotTestSite(t, app)
	m.Apply(site)
	if err := m.StartSlot("s1", "staging"); err != nil {
		t.Fatal(err)
	}
	if err := m.StartSlot("s1", "qa"); err != ErrNoSlot {
		t.Fatalf("unknown slot: %v", err)
	}
	waitFor(t, "staging", func() bool { return len(m.Backends("s1@staging")) == 1 })
	if m.Running("s1") {
		t.Fatal("starting a slot started production")
	}
	pid := firstKey(pids(m, "s1@staging"))

	// Removing the slot stops it.
	gone := *site
	gone.Slots = nil
	gone.Bindings = gone.Bindings[:1]
	m.Apply(&gone)
	if _, ok := m.Status("s1@staging"); ok {
		t.Fatal("slot still registered")
	}
	waitFor(t, "slot process exit", func() bool { return !processAlive(pid) })

	// Removing the site removes its slots.
	m.Apply(site)
	m.StartSlot("s1", "staging")
	waitFor(t, "staging again", func() bool { return len(m.Backends("s1@staging")) == 1 })
	pid = firstKey(pids(m, "s1@staging"))
	m.Remove("s1")
	if _, ok := m.Status("s1@staging"); ok {
		t.Fatal("slot registered after the site was removed")
	}
	if processAlive(pid) {
		t.Fatal("slot process survived its site")
	}
}

func TestWarmUp(t *testing.T) {
	var calls atomic.Int32
	var host, proto atomic.Value
	cold := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host.Store(r.Host)
		proto.Store(r.Header.Get("X-Forwarded-Proto"))
		if r.URL.Path == "/login" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound) // 302 is accepted as is, not followed
			return
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("warm"))
	}))
	defer cold.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	accept, _ := model.ParseStatusRanges("200-399")
	addr := func(s *httptest.Server) string { return strings.TrimPrefix(s.URL, "http://") }
	logf := func(string, ...any) {}

	start := time.Now()
	err := warmUp(context.Background(), warmupClient, warmup{addrs: []string{addr(cold)}, paths: []string{"/", "/login"}, accept: accept,
		host: "www.example.com", proto: "https", timeout: 10 * time.Second, logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 2*warmupRetryDelay {
		t.Fatalf("done after %s: did not retry", d)
	}
	if host.Load() != "www.example.com" || proto.Load() != "https" {
		t.Fatalf("host %v, proto %v", host.Load(), proto.Load())
	}

	err = warmUp(context.Background(), warmupClient, warmup{addrs: []string{addr(cold), addr(broken)}, paths: []string{"/"}, accept: accept,
		timeout: 2 * time.Second, logf: logf})
	if err == nil || !strings.Contains(err.Error(), addr(broken)) || !strings.Contains(err.Error(), "HTTP 500") || strings.Contains(err.Error(), addr(cold)) {
		t.Fatalf("err = %v", err)
	}
	// A 500 can be accepted on purpose.
	all, _ := model.ParseStatusRanges("200-599")
	if err := warmUp(context.Background(), warmupClient, warmup{addrs: []string{addr(broken)}, paths: []string{"/"}, accept: all, timeout: time.Second, logf: logf}); err != nil {
		t.Fatal(err)
	}
	// Nothing listening: the error says so.
	l := httptest.NewServer(http.NotFoundHandler())
	dead := addr(l)
	l.Close()
	err = warmUp(context.Background(), warmupClient, warmup{addrs: []string{dead}, paths: []string{"/"}, accept: accept, timeout: 1500 * time.Millisecond, logf: logf})
	if err == nil || !strings.Contains(err.Error(), "refused") && !strings.Contains(err.Error(), "connect") {
		t.Fatalf("dead instance: %v", err)
	}
}
