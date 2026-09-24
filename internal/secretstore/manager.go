// Package secretstore reads environment variables and deploy tokens from
// external secret managers: HashiCorp Vault and OpenBao, Infisical, and
// Bitwarden Secrets Manager. Values are read when a process starts, a task
// runs or a deployment builds, kept in memory for a short while (so that a
// recycle of every instance asks the store once) and never written
// anywhere. When a store cannot be reached, the last value read from it is
// used; without one, the start fails with an explanation.
package secretstore

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
)

// provider reads secrets from one store.
type provider interface {
	// fetch reads the references (in the store's syntax); the results are
	// in the same order.
	fetch(ctx context.Context, refs []string) []result
	// test signs in and checks access, describing what it found.
	test(ctx context.Context) (string, error)
	// maintain keeps the session alive (token renewal); called every
	// minute or so.
	maintain(ctx context.Context) error
	// tokenExpires is when the current session ends, zero if unknown.
	tokenExpires() time.Time
}

type result struct {
	value string
	err   error
}

// broken stands for a store that cannot be used as configured.
type broken struct{ err error }

func (b broken) fetch(_ context.Context, refs []string) []result {
	out := make([]result, len(refs))
	for i := range out {
		out[i].err = b.err
	}
	return out
}
func (b broken) test(context.Context) (string, error) { return "", b.err }
func (b broken) maintain(context.Context) error       { return nil }
func (b broken) tokenExpires() time.Time              { return time.Time{} }

// Limits.
const (
	maxEntries    = 10000            // values held in memory, all stores together
	tickEvery     = 15 * time.Second // Run's clock
	maintainEvery = time.Minute
	pruneEvery    = time.Hour
	eventEvery    = 10 * time.Minute // at most one stale-value event per store this often
	recordGrace   = 10 * time.Minute // a start's record survives this long before its site must be running
)

type Options struct {
	Log *slog.Logger
	// Unseal decrypts a store's stored credentials.
	Unseal func(string) (string, error)
	// Event reports an event (level info, warning or error).
	Event func(level, typ, siteID, msg string)
	// Watched returns the references of every running site's processes
	// (site ID → references): those whose change recycles the site.
	Watched func() map[string][]model.SecretRef
	// Changed is called when secrets a running site started with have
	// changed in their store (refs in text form): the site is recycled.
	// It is called from Run's goroutine and must not block for long.
	Changed func(siteID string, refs []string)
	// References lists every reference in the configuration; values no
	// longer referenced are forgotten.
	References func() []model.SecretRef
	// Now is the clock (tests).
	Now func() time.Time
}

type refKey struct{ store, ref string }

type entry struct {
	value   string
	hash    [32]byte
	fetched time.Time // read from the store
	used    time.Time // last handed out
}

// storeState is a configured store. Fields other than prov and fetchMu
// are guarded by Manager.mu.
type storeState struct {
	cfg     model.SecretStore // as stored (sealed credentials)
	key     string            // what the provider was built from
	prov    provider
	fetchMu sync.Mutex // one read of the store at a time

	lastOK, lastErrAt time.Time
	lastErr           string
	nextMaintain      time.Time
	nextWatch         time.Time
}

type siteRecord struct {
	at     time.Time
	hashes map[refKey][32]byte
}

type Manager struct {
	opts    Options
	hashKey []byte // keys the in-memory hashes of values

	mu        sync.Mutex
	stores    map[string]*storeState
	cache     map[refKey]*entry
	started   map[string]siteRecord // site → the values its processes started with
	lastEvent map[string]time.Time
	lastPrune time.Time
}

func New(opts Options) *Manager {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Event == nil {
		opts.Event = func(string, string, string, string) {}
	}
	key := make([]byte, 32)
	rand.Read(key)
	return &Manager{
		opts: opts, hashKey: key,
		stores: map[string]*storeState{}, cache: map[refKey]*entry{},
		started: map[string]siteRecord{}, lastEvent: map[string]time.Time{},
	}
}

// providerKey is what a provider depends on: a change of anything else
// (cache and watch settings) keeps the provider, its session and the
// values in memory.
func providerKey(s model.SecretStore) string {
	s.ID, s.CacheTTLSec, s.WatchIntervalSec = "", 0, 0
	raw, _ := json.Marshal(s)
	return string(raw)
}

// Apply installs the configured stores. A store whose connection settings
// changed starts over: its session and its values in memory are dropped.
func (m *Manager) Apply(list []model.SecretStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make(map[string]*storeState, len(list))
	for _, s := range list {
		key := providerKey(s)
		if old := m.stores[s.Name]; old != nil && old.key == key {
			old.cfg = s
			next[s.Name] = old
			continue
		}
		next[s.Name] = &storeState{cfg: s, key: key, prov: m.build(s)}
		m.dropStoreLocked(s.Name)
	}
	for name := range m.stores {
		if next[name] == nil {
			m.dropStoreLocked(name)
		}
	}
	m.stores = next
}

func (m *Manager) dropStoreLocked(name string) {
	for k := range m.cache {
		if k.store == name {
			delete(m.cache, k)
		}
	}
}

// build makes the provider of a store from its stored configuration.
func (m *Manager) build(s model.SecretStore) provider {
	p, err := m.newProvider(s)
	if err != nil {
		return broken{fmt.Errorf("secret store %q cannot be used: %w", s.Name, err)}
	}
	return p
}

func (m *Manager) newProvider(s model.SecretStore) (provider, error) {
	plain, err := m.unseal(s)
	if err != nil {
		return nil, err
	}
	hc, err := newHTTPClient(plain.CACert)
	if err != nil {
		return nil, err
	}
	switch plain.Type {
	case model.SecretStoreVault:
		if plain.Vault == nil {
			break
		}
		return newVault(plain, hc, m.opts.Now), nil
	case model.SecretStoreInfisical:
		if plain.Infisical == nil {
			break
		}
		return newInfisical(plain, hc, m.opts.Now), nil
	case model.SecretStoreBitwarden:
		if plain.Bitwarden == nil {
			break
		}
		return newBitwarden(plain, hc, m.opts.Now)
	}
	return nil, fmt.Errorf("unknown or incomplete store type %q", s.Type)
}

// Credentials returns pointers to the secret fields of a store, which must
// have its own copies of its sections (see Clone).
func Credentials(s *model.SecretStore) []*string {
	var out []*string
	if s.Vault != nil {
		out = append(out, &s.Vault.Token, &s.Vault.SecretID)
	}
	if s.Infisical != nil {
		out = append(out, &s.Infisical.ClientSecret)
	}
	if s.Bitwarden != nil {
		out = append(out, &s.Bitwarden.AccessToken)
	}
	return out
}

// Clone copies a store deeply (its sections are pointers).
func Clone(s model.SecretStore) model.SecretStore {
	if s.Vault != nil {
		v := *s.Vault
		s.Vault = &v
	}
	if s.Infisical != nil {
		v := *s.Infisical
		s.Infisical = &v
	}
	if s.Bitwarden != nil {
		v := *s.Bitwarden
		s.Bitwarden = &v
	}
	return s
}

func (m *Manager) unseal(s model.SecretStore) (model.SecretStore, error) {
	s = Clone(s)
	if m.opts.Unseal == nil {
		return s, nil
	}
	for _, p := range Credentials(&s) {
		v, err := m.opts.Unseal(*p)
		if err != nil {
			return s, errors.New("a credential cannot be decrypted with this server's key; enter it again")
		}
		*p = v
	}
	return s, nil
}

// ResolveOptions say why values are read.
type ResolveOptions struct {
	// Fresh reads every value from its store even when a recent one is in
	// memory (checks and tests); an unreachable store still falls back.
	Fresh bool
	// Record remembers the values as those SiteID's processes run with,
	// for recycling the site when one changes.
	Record bool
	SiteID string
}

// ResolveError is a reference that could not be read.
type ResolveError struct {
	Ref model.SecretRef
	Err error
}

func (e *ResolveError) Error() string { return fmt.Sprintf("%s: %v", e.Ref, e.Err) }
func (e *ResolveError) Unwrap() error { return e.Err }

// Resolve reads references. Values in memory younger than their store's
// cache TTL are used as they are; others are read from the store, and when
// that fails for any reason other than the secret not existing, the last
// value read is used (with a secret.stale event). Without one, the
// reference fails. The first failure is returned.
func (m *Manager) Resolve(ctx context.Context, refs []model.SecretRef, o ResolveOptions) (map[model.SecretRef]string, error) {
	byStore := map[string][]string{}
	for _, r := range refs {
		if !slices.Contains(byStore[r.Store], r.Ref) {
			byStore[r.Store] = append(byStore[r.Store], r.Ref)
		}
	}
	names := make([]string, 0, len(byStore))
	for n := range byStore {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make(map[model.SecretRef]string, len(refs))
	for _, name := range names {
		vals, err := m.resolveStore(ctx, name, byStore[name], o.Fresh)
		if err != nil {
			return nil, err
		}
		for ref, v := range vals {
			out[model.SecretRef{Store: name, Ref: ref}] = v
		}
	}
	if o.Record && o.SiteID != "" {
		m.record(o.SiteID, out)
	}
	return out, nil
}

func (m *Manager) resolveStore(ctx context.Context, name string, refs []string, fresh bool) (map[string]string, error) {
	vals := make(map[string]string, len(refs))
	missing := func(st *storeState) []string {
		now := m.opts.Now()
		ttl := time.Duration(st.cfg.CacheTTLSec) * time.Second
		var need []string
		for _, ref := range refs {
			if _, ok := vals[ref]; ok {
				continue
			}
			if e := m.cache[refKey{name, ref}]; e != nil && !fresh && now.Sub(e.fetched) < ttl {
				vals[ref] = e.value
				e.used = now
				continue
			}
			need = append(need, ref)
		}
		return need
	}

	m.mu.Lock()
	st := m.stores[name]
	if st == nil {
		m.mu.Unlock()
		return nil, &ResolveError{model.SecretRef{Store: name, Ref: refs[0]}, fmt.Errorf("secret store %q does not exist", name)}
	}
	need := missing(st)
	m.mu.Unlock()
	if len(need) == 0 {
		return vals, nil
	}

	// One read of a store at a time: instances starting together wait
	// for the first read and then find its values in memory.
	st.fetchMu.Lock()
	if !fresh {
		m.mu.Lock()
		need = missing(st)
		m.mu.Unlock()
	}
	var res []result
	if len(need) > 0 {
		fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
		res = st.prov.fetch(fctx, need)
		cancel()
	}
	st.fetchMu.Unlock()

	m.mu.Lock()
	now := m.opts.Now()
	current := m.stores[name] == st // not reconfigured meanwhile
	var stale []string
	var outage error
	for i, ref := range need {
		k := refKey{name, ref}
		r := res[i]
		switch {
		case r.err == nil:
			if current {
				m.putLocked(k, r.value, now)
				st.lastOK = now
			}
			vals[ref] = r.value
		case IsNotFound(r.err):
			delete(m.cache, k)
			m.mu.Unlock()
			return nil, &ResolveError{model.SecretRef{Store: name, Ref: ref}, r.err}
		default:
			outage = r.err
			if e := m.cache[k]; e != nil && current {
				e.used = now
				vals[ref] = e.value
				stale = append(stale, ref)
				continue
			}
			if current {
				st.lastErr, st.lastErrAt = r.err.Error(), now
			}
			m.mu.Unlock()
			return nil, &ResolveError{model.SecretRef{Store: name, Ref: ref}, fmt.Errorf("store %q could not be read and no earlier value is known: %w", name, r.err)}
		}
	}
	if outage != nil && current {
		st.lastErr, st.lastErrAt = outage.Error(), now
	}
	report := len(stale) > 0 && m.allowEventLocked("stale\x00"+name, now)
	m.mu.Unlock()
	if report {
		m.opts.Log.Warn("secret store unreachable; using last known values", "store", name, "refs", len(stale), "err", outage)
		m.opts.Event("warning", events.SecretStale, "", fmt.Sprintf("Secret store %q could not be read (%v); the last known value of %d secret(s) was used", name, outage, len(stale)))
	}
	return vals, nil
}

func (m *Manager) hash(v string) [32]byte {
	h := hmac.New(sha256.New, m.hashKey)
	h.Write([]byte(v))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// putLocked stores a value, forgetting the least recently used one when
// memory is full.
func (m *Manager) putLocked(k refKey, v string, now time.Time) {
	e := m.cache[k]
	if e == nil {
		if len(m.cache) >= maxEntries {
			var oldest refKey
			var at time.Time
			first := true
			for ok, oe := range m.cache {
				if first || oe.used.Before(at) {
					oldest, at, first = ok, oe.used, false
				}
			}
			delete(m.cache, oldest)
		}
		e = &entry{}
		m.cache[k] = e
	}
	e.value, e.hash, e.fetched, e.used = v, m.hash(v), now, now
}

func (m *Manager) allowEventLocked(key string, now time.Time) bool {
	if last, ok := m.lastEvent[key]; ok && now.Sub(last) < eventEvery {
		return false
	}
	m.lastEvent[key] = now
	return true
}

// record remembers what a site's processes started with.
func (m *Manager) record(siteID string, vals map[model.SecretRef]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hs := make(map[refKey][32]byte, len(vals))
	for r, v := range vals {
		hs[refKey{r.Store, r.Ref}] = m.hash(v)
	}
	m.started[siteID] = siteRecord{at: m.opts.Now(), hashes: hs}
}

// Forget drops what is remembered about a site (deleted or stopped).
func (m *Manager) Forget(siteID string) {
	m.mu.Lock()
	delete(m.started, siteID)
	m.mu.Unlock()
}

// Test checks a store's configuration (sealed credentials, as stored) by
// signing in, and reads ref with it when one is given. Nothing is kept:
// not the session, not the value, which is never returned.
func (m *Manager) Test(ctx context.Context, s model.SecretStore, ref string) model.SecretTestResult {
	p, err := m.newProvider(s)
	if err != nil {
		return model.SecretTestResult{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	detail, err := p.test(ctx)
	if err != nil {
		return model.SecretTestResult{Error: err.Error()}
	}
	if ref != "" {
		if err := model.ValidateSecretRef(s.Type, ref); err != nil {
			return model.SecretTestResult{Error: err.Error(), Detail: detail}
		}
		r := p.fetch(ctx, []string{ref})[0]
		if r.err != nil {
			return model.SecretTestResult{Error: r.err.Error(), Detail: detail}
		}
		detail += fmt.Sprintf("; %s was read (%d characters)", ref, len([]rune(r.value)))
	}
	return model.SecretTestResult{OK: true, Detail: detail}
}

// Status describes every store.
func (m *Manager) Status() []model.SecretStoreStatus {
	var refs []model.SecretRef
	if m.opts.References != nil {
		refs = m.opts.References()
	}
	m.mu.Lock()
	type snap struct {
		st   *storeState
		stat model.SecretStoreStatus
	}
	var list []snap
	for name, st := range m.stores {
		s := model.SecretStoreStatus{Name: name, Type: st.cfg.Type, LastError: st.lastErr}
		if !st.lastOK.IsZero() {
			t := st.lastOK
			s.LastSuccess = &t
		}
		if !st.lastErrAt.IsZero() {
			t := st.lastErrAt
			s.LastErrorAt = &t
		}
		for k := range m.cache {
			if k.store == name {
				s.Cached++
			}
		}
		for _, r := range refs {
			if r.Store == name {
				s.References++
			}
		}
		list = append(list, snap{st, s})
	}
	m.mu.Unlock()
	out := make([]model.SecretStoreStatus, 0, len(list))
	for _, x := range list {
		if t := x.st.prov.tokenExpires(); !t.IsZero() {
			x.stat.TokenExpires = &t
		}
		out = append(out, x.stat)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run renews the stores' sessions, watches for changed secrets and
// forgets values no longer referenced, until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.tick(ctx)
		}
	}
}

func (m *Manager) tick(ctx context.Context) {
	now := m.opts.Now()
	type job struct {
		name     string
		st       *storeState
		maintain bool
		watch    bool
	}
	var jobs []job
	prune := false
	m.mu.Lock()
	for name, st := range m.stores {
		j := job{name: name, st: st}
		if !now.Before(st.nextMaintain) {
			st.nextMaintain, j.maintain = now.Add(maintainEvery), true
		}
		if w := st.cfg.WatchIntervalSec; w > 0 && !now.Before(st.nextWatch) {
			if !st.nextWatch.IsZero() {
				j.watch = true
			}
			st.nextWatch = now.Add(time.Duration(w) * time.Second)
		}
		if j.maintain || j.watch {
			jobs = append(jobs, j)
		}
	}
	if now.Sub(m.lastPrune) >= pruneEvery {
		m.lastPrune, prune = now, true
	}
	m.mu.Unlock()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].name < jobs[j].name })

	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		if j.maintain {
			mctx, cancel := context.WithTimeout(ctx, fetchTimeout)
			err := j.st.prov.maintain(mctx)
			cancel()
			if err != nil {
				m.opts.Log.Warn("secret store session", "store", j.name, "err", err)
				m.mu.Lock()
				j.st.lastErr, j.st.lastErrAt = err.Error(), m.opts.Now()
				m.mu.Unlock()
			}
		}
		if j.watch {
			m.watch(ctx, j.name, j.st)
		}
	}
	if prune && m.opts.References != nil {
		m.prune(m.opts.References())
	}
}

// watch reads the store's secrets that running sites started with, and
// has every site whose value changed recycled.
func (m *Manager) watch(ctx context.Context, name string, st *storeState) {
	if m.opts.Watched == nil {
		return
	}
	watched := m.opts.Watched()
	now := m.opts.Now()
	var need []string
	m.mu.Lock()
	for site, rec := range m.started {
		if _, running := watched[site]; !running && now.Sub(rec.at) > recordGrace {
			delete(m.started, site)
		}
	}
	for site, refs := range watched {
		if _, ok := m.started[site]; !ok {
			continue
		}
		for _, r := range refs {
			if r.Store == name && !slices.Contains(need, r.Ref) {
				need = append(need, r.Ref)
			}
		}
	}
	m.mu.Unlock()
	if len(need) == 0 {
		return
	}
	sort.Strings(need)

	st.fetchMu.Lock()
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	res := st.prov.fetch(fctx, need)
	cancel()
	st.fetchMu.Unlock()

	changed := map[string][]string{}
	m.mu.Lock()
	if m.stores[name] != st {
		m.mu.Unlock()
		return
	}
	now = m.opts.Now()
	for i, ref := range need {
		if res[i].err != nil {
			st.lastErr, st.lastErrAt = res[i].err.Error(), now
			continue
		}
		k := refKey{name, ref}
		m.putLocked(k, res[i].value, now)
		st.lastOK = now
		h := m.cache[k].hash
		for site, refs := range watched {
			rec, ok := m.started[site]
			if !ok || !slices.Contains(refs, model.SecretRef{Store: name, Ref: ref}) {
				continue
			}
			if old, ok := rec.hashes[k]; ok && old != h {
				rec.hashes[k] = h // recycled once, even if the recycle fails
				changed[site] = append(changed[site], model.SecretRef{Store: name, Ref: ref}.String())
			}
		}
	}
	m.mu.Unlock()
	sites := make([]string, 0, len(changed))
	for s := range changed {
		sites = append(sites, s)
	}
	sort.Strings(sites)
	for _, s := range sites {
		if m.opts.Changed != nil {
			m.opts.Changed(s, changed[s])
		}
	}
}

// prune forgets values that nothing references any more.
func (m *Manager) prune(refs []model.SecretRef) {
	keep := make(map[refKey]bool, len(refs))
	for _, r := range refs {
		keep[refKey{r.Store, r.Ref}] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.cache {
		if !keep[k] {
			delete(m.cache, k)
		}
	}
	now := m.opts.Now()
	for k, t := range m.lastEvent {
		if now.Sub(t) > eventEvery {
			delete(m.lastEvent, k)
		}
	}
}

// AllowEvent reports whether an event under key may be sent now: at most
// one every ten minutes per key, so that a site failing to start over and
// over does not flood the event log and webhooks.
func (m *Manager) AllowEvent(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allowEventLocked(key, m.opts.Now())
}

// Check reads refs from their stores now, with no value from memory and
// no fallback, and returns each one's error (nil: it resolves). Values
// read replace those in memory.
func (m *Manager) Check(ctx context.Context, refs []model.SecretRef) []error {
	errs := make([]error, len(refs))
	byStore := map[string][]int{}
	var names []string
	for i, r := range refs {
		if _, ok := byStore[r.Store]; !ok {
			names = append(names, r.Store)
		}
		byStore[r.Store] = append(byStore[r.Store], i)
	}
	sort.Strings(names)
	for _, name := range names {
		idx := byStore[name]
		m.mu.Lock()
		st := m.stores[name]
		m.mu.Unlock()
		if st == nil {
			for _, i := range idx {
				errs[i] = fmt.Errorf("secret store %q does not exist", name)
			}
			continue
		}
		var need []string
		for _, i := range idx {
			if !slices.Contains(need, refs[i].Ref) {
				need = append(need, refs[i].Ref)
			}
		}
		st.fetchMu.Lock()
		fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
		res := st.prov.fetch(fctx, need)
		cancel()
		st.fetchMu.Unlock()
		m.mu.Lock()
		now := m.opts.Now()
		current := m.stores[name] == st
		for j, ref := range need {
			switch {
			case res[j].err == nil && current:
				m.putLocked(refKey{name, ref}, res[j].value, now)
				st.lastOK = now
			case res[j].err != nil && current && !IsNotFound(res[j].err):
				st.lastErr, st.lastErrAt = res[j].err.Error(), now
			}
			for _, i := range idx {
				if refs[i].Ref == ref {
					errs[i] = res[j].err
				}
			}
		}
		m.mu.Unlock()
	}
	return errs
}
