package secretstore

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
)

// fakeProvider serves values from a map; down makes every read fail as
// an outage would.
type fakeProvider struct {
	mu     sync.Mutex
	values map[string]string
	down   bool
	delay  time.Duration
	reads  atomic.Int32 // fetch calls
	asked  atomic.Int32 // references read
	maint  atomic.Int32
}

func (p *fakeProvider) fetch(ctx context.Context, refs []string) []result {
	p.reads.Add(1)
	p.asked.Add(int32(len(refs)))
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]result, len(refs))
	for i, r := range refs {
		switch v, ok := p.values[r]; {
		case p.down:
			out[i].err = errors.New("connection refused")
		case !ok:
			out[i].err = notFound("%s was not found", r)
		default:
			out[i].value = v
		}
	}
	return out
}
func (p *fakeProvider) test(context.Context) (string, error) { return "ok", nil }
func (p *fakeProvider) maintain(context.Context) error       { p.maint.Add(1); return nil }
func (p *fakeProvider) tokenExpires() time.Time              { return time.Time{} }

func (p *fakeProvider) set(ref, v string) {
	p.mu.Lock()
	p.values[ref] = v
	p.mu.Unlock()
}

func (p *fakeProvider) setDown(down bool) {
	p.mu.Lock()
	p.down = down
	p.mu.Unlock()
}

type recorded struct {
	mu     sync.Mutex
	events []string
}

func (r *recorded) add(level, typ, site, msg string) {
	r.mu.Lock()
	r.events = append(r.events, level+" "+typ+" "+msg)
	r.mu.Unlock()
}

func (r *recorded) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

// testManager has one store "s" (Vault type, served by a fake provider).
func testManager(t *testing.T, opts Options) (*Manager, *fakeProvider, *fakeClock) {
	t.Helper()
	clock := newClock()
	opts.Now = clock.Now
	m := New(opts)
	m.Apply([]model.SecretStore{vaultStore("https://vault.invalid", model.VaultStore{Token: "t"})})
	s := m.stores["vault"]
	s.cfg.CacheTTLSec = 300
	p := &fakeProvider{values: map[string]string{"app#A": "a1", "app#B": "b1"}}
	s.prov = p
	return m, p, clock
}

func ref(r string) model.SecretRef { return model.SecretRef{Store: "vault", Ref: r} }

func TestResolveCachesForTheTTL(t *testing.T) {
	m, p, clock := testManager(t, Options{})
	ctx := context.Background()
	got, err := m.Resolve(ctx, []model.SecretRef{ref("app#A"), ref("app#B"), ref("app#A")}, ResolveOptions{})
	if err != nil || got[ref("app#A")] != "a1" || got[ref("app#B")] != "b1" {
		t.Fatalf("resolve = %v, %v", got, err)
	}
	if p.reads.Load() != 1 || p.asked.Load() != 2 {
		t.Fatalf("reads %d, refs asked %d", p.reads.Load(), p.asked.Load())
	}
	p.set("app#A", "a2")
	clock.Add(4 * time.Minute)
	got, _ = m.Resolve(ctx, []model.SecretRef{ref("app#A")}, ResolveOptions{})
	if got[ref("app#A")] != "a1" || p.reads.Load() != 1 {
		t.Fatalf("within the TTL: %v after %d reads", got, p.reads.Load())
	}
	clock.Add(2 * time.Minute)
	got, _ = m.Resolve(ctx, []model.SecretRef{ref("app#A")}, ResolveOptions{})
	if got[ref("app#A")] != "a2" || p.reads.Load() != 2 {
		t.Fatalf("after the TTL: %v after %d reads", got, p.reads.Load())
	}
	// Fresh always reads.
	m.Resolve(ctx, []model.SecretRef{ref("app#A")}, ResolveOptions{Fresh: true})
	if p.reads.Load() != 3 {
		t.Fatalf("fresh did not read")
	}
}

// Many instances starting together read the store once.
func TestResolveConcurrentStartsReadOnce(t *testing.T) {
	m, p, _ := testManager(t, Options{})
	p.delay = 50 * time.Millisecond
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			got, err := m.Resolve(context.Background(), []model.SecretRef{ref("app#A")}, ResolveOptions{})
			if err != nil || got[ref("app#A")] != "a1" {
				t.Errorf("resolve = %v, %v", got, err)
			}
		})
	}
	wg.Wait()
	if n := p.reads.Load(); n != 1 {
		t.Errorf("%d reads for 10 simultaneous starts", n)
	}
}

func TestResolveFallsBackToLastKnownValue(t *testing.T) {
	ev := &recorded{}
	m, p, clock := testManager(t, Options{Event: ev.add})
	ctx := context.Background()
	if _, err := m.Resolve(ctx, []model.SecretRef{ref("app#A")}, ResolveOptions{}); err != nil {
		t.Fatal(err)
	}
	p.setDown(true)
	clock.Add(time.Hour)
	got, err := m.Resolve(ctx, []model.SecretRef{ref("app#A")}, ResolveOptions{})
	if err != nil || got[ref("app#A")] != "a1" {
		t.Fatalf("with the store down = %v, %v", got, err)
	}
	m.Resolve(ctx, []model.SecretRef{ref("app#A")}, ResolveOptions{})
	evs := ev.all()
	if len(evs) != 1 || !strings.Contains(evs[0], events.SecretStale) || !strings.Contains(evs[0], "connection refused") {
		t.Fatalf("events = %q (one stale event, rate limited)", evs)
	}
	for _, e := range evs {
		if strings.Contains(e, "a1") {
			t.Errorf("an event contains the value: %s", e)
		}
	}
	// Nothing known about B: the start fails, saying why.
	_, err = m.Resolve(ctx, []model.SecretRef{ref("app#B")}, ResolveOptions{})
	var re *ResolveError
	if !errors.As(err, &re) || re.Ref != ref("app#B") || !strings.Contains(err.Error(), "no earlier value") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("unknown value with the store down: %v", err)
	}
	st := m.Status()
	if len(st) != 1 || st[0].LastError == "" || st[0].Cached != 1 {
		t.Errorf("status = %+v", st)
	}
}

// A secret deleted from the store fails the start even with an older
// value in memory: that is not an outage.
func TestResolveNotFoundDoesNotFallBack(t *testing.T) {
	m, p, clock := testManager(t, Options{})
	ctx := context.Background()
	m.Resolve(ctx, []model.SecretRef{ref("app#A")}, ResolveOptions{})
	p.mu.Lock()
	delete(p.values, "app#A")
	p.mu.Unlock()
	clock.Add(time.Hour)
	if _, err := m.Resolve(ctx, []model.SecretRef{ref("app#A")}, ResolveOptions{}); err == nil || !IsNotFound(err) {
		t.Fatalf("deleted secret: %v", err)
	}
	if _, err := m.Resolve(ctx, []model.SecretRef{{Store: "nope", Ref: "x"}}, ResolveOptions{}); err == nil || !strings.Contains(err.Error(), `"nope" does not exist`) {
		t.Errorf("unknown store: %v", err)
	}
}

func TestApplyKeepsOrDropsValues(t *testing.T) {
	m, p, _ := testManager(t, Options{})
	ctx := context.Background()
	m.Resolve(ctx, []model.SecretRef{ref("app#A")}, ResolveOptions{})

	// Cache and watch settings: the provider and values stay.
	s := vaultStore("https://vault.invalid", model.VaultStore{Token: "t"})
	s.CacheTTLSec, s.WatchIntervalSec = 60, 120
	m.Apply([]model.SecretStore{s})
	if m.stores["vault"].prov != p || len(m.cache) != 1 {
		t.Fatal("a cache setting change dropped the store")
	}
	// Another server: values read from the old one are forgotten.
	s.URL = "https://other.invalid"
	m.Apply([]model.SecretStore{s})
	if m.stores["vault"].prov == p || len(m.cache) != 0 {
		t.Fatal("a new URL kept the old values")
	}
	m.Apply(nil)
	if len(m.stores) != 0 {
		t.Fatal("a removed store remains")
	}
}

// A credential that cannot be decrypted makes the store unusable, with an
// explanation, without affecting the others.
func TestBrokenStore(t *testing.T) {
	m := New(Options{Unseal: func(v string) (string, error) {
		if v == "sealed-elsewhere" {
			return "", errors.New("cannot decrypt")
		}
		return v, nil
	}})
	m.Apply([]model.SecretStore{vaultStore("https://vault.invalid", model.VaultStore{Token: "sealed-elsewhere"})})
	_, err := m.Resolve(context.Background(), []model.SecretRef{ref("app#A")}, ResolveOptions{})
	if err == nil || !strings.Contains(err.Error(), "enter it again") {
		t.Fatalf("broken store: %v", err)
	}
}

// A running site whose secret changes is recycled once; a site that has
// not started with the value, or stopped, is not.
func TestWatchRecyclesChangedSites(t *testing.T) {
	var mu sync.Mutex
	watched := map[string][]model.SecretRef{"site1": {ref("app#A")}, "site2": {ref("app#B")}}
	var changed []string
	m, p, clock := testManager(t, Options{
		Watched: func() map[string][]model.SecretRef {
			mu.Lock()
			defer mu.Unlock()
			out := map[string][]model.SecretRef{}
			for k, v := range watched {
				out[k] = v
			}
			return out
		},
		Changed: func(site string, refs []string) {
			mu.Lock()
			changed = append(changed, site+":"+strings.Join(refs, ","))
			mu.Unlock()
		},
	})
	m.stores["vault"].cfg.WatchIntervalSec = 60
	ctx := context.Background()
	m.Resolve(ctx, []model.SecretRef{ref("app#A")}, ResolveOptions{Record: true, SiteID: "site1"})
	m.Resolve(ctx, []model.SecretRef{ref("app#B")}, ResolveOptions{}) // site2: not recorded

	m.tick(ctx) // schedules the first watch
	if p.maint.Load() != 1 {
		t.Errorf("maintain not called")
	}
	clock.Add(61 * time.Second)
	m.tick(ctx)
	if len(changed) != 0 {
		t.Fatalf("recycled without a change: %v", changed)
	}
	p.set("app#A", "a2")
	p.set("app#B", "b2")
	clock.Add(61 * time.Second)
	m.tick(ctx)
	mu.Lock()
	if len(changed) != 1 || changed[0] != "site1:secretref:vault/app#A" {
		t.Fatalf("changed = %v", changed)
	}
	mu.Unlock()
	// The new value is in memory for the recycle, and not recycled twice.
	got, _ := m.Resolve(ctx, []model.SecretRef{ref("app#A")}, ResolveOptions{})
	if got[ref("app#A")] != "a2" {
		t.Errorf("value after the watch = %q", got[ref("app#A")])
	}
	clock.Add(61 * time.Second)
	m.tick(ctx)
	if len(changed) != 1 {
		t.Fatalf("recycled twice: %v", changed)
	}

	// Stopped long enough: forgotten, never recycled.
	mu.Lock()
	delete(watched, "site1")
	mu.Unlock()
	clock.Add(recordGrace + time.Minute)
	m.tick(ctx)
	if _, ok := m.started["site1"]; ok {
		t.Error("the record of a stopped site was kept")
	}
}

func TestPruneForgetsUnreferencedValues(t *testing.T) {
	refs := []model.SecretRef{ref("app#A")}
	m, _, clock := testManager(t, Options{References: func() []model.SecretRef { return refs }})
	ctx := context.Background()
	m.Resolve(ctx, []model.SecretRef{ref("app#A"), ref("app#B")}, ResolveOptions{})
	clock.Add(2 * time.Hour)
	m.tick(ctx)
	if _, ok := m.cache[refKey{"vault", "app#B"}]; ok || len(m.cache) != 1 {
		t.Errorf("cache after prune: %d entries", len(m.cache))
	}
	if st := m.Status(); st[0].References != 1 || st[0].Cached != 1 {
		t.Errorf("status = %+v", st[0])
	}
}

func TestCacheIsBounded(t *testing.T) {
	m, _, clock := testManager(t, Options{})
	m.mu.Lock()
	for i := range maxEntries + 5 {
		clock.Add(time.Millisecond)
		m.putLocked(refKey{"vault", fmt.Sprint(i)}, "v", clock.Now())
	}
	n := len(m.cache)
	_, oldestKept := m.cache[refKey{"vault", "0"}]
	m.mu.Unlock()
	if n != maxEntries || oldestKept {
		t.Errorf("cache has %d entries (oldest kept: %v)", n, oldestKept)
	}
}

func TestCheckReportsEachReference(t *testing.T) {
	m, p, _ := testManager(t, Options{})
	errs := m.Check(context.Background(), []model.SecretRef{ref("app#A"), ref("app#NOPE"), {Store: "x", Ref: "y"}})
	if errs[0] != nil || !IsNotFound(errs[1]) || errs[2] == nil {
		t.Fatalf("errs = %v", errs)
	}
	p.setDown(true)
	// No fallback for a check: it says the store cannot be read.
	if errs := m.Check(context.Background(), []model.SecretRef{ref("app#A")}); errs[0] == nil {
		t.Error("a check with the store down succeeded")
	}
}

// Test signs in with a configuration and reads a reference, keeping
// nothing.
func TestManagerTestKeepsNothing(t *testing.T) {
	f := newFakeVault(t)
	srv := httptest.NewServer(f)
	defer srv.Close()
	m := New(Options{})
	res := m.Test(context.Background(), vaultStore(srv.URL, model.VaultStore{Token: "root-token"}), "app/prod#DB_PASSWORD")
	if !res.OK || !strings.Contains(res.Detail, "7 characters") || strings.Contains(res.Detail, "hunter2") {
		t.Fatalf("test = %+v", res)
	}
	if len(m.cache) != 0 || len(m.stores) != 0 {
		t.Error("the test kept something")
	}
	res = m.Test(context.Background(), vaultStore(srv.URL, model.VaultStore{Token: "root-token"}), "app/prod#NOPE")
	if res.OK || !strings.Contains(res.Error, "NOPE") {
		t.Errorf("missing key = %+v", res)
	}
	res = m.Test(context.Background(), vaultStore(srv.URL, model.VaultStore{Token: "root-token"}), "no-key")
	if res.OK || !strings.Contains(res.Error, "#") {
		t.Errorf("bad reference = %+v", res)
	}
}
