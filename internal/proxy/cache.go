package proxy

import (
	"bufio"
	"container/list"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// The response cache is IIS output caching / ARR's cache in memory: GET
// responses that HTTP's caching rules allow a shared cache to keep are
// stored per site and answered from memory until they expire. It sits
// after the request pipeline (IP restrictions, basic authentication, URL
// rewrite), so a cached response is only ever given to a request that
// would have been allowed to reach the application.

const (
	// coalesceWait is how long concurrent misses for the same URL wait for
	// the first one's response before going to the application themselves.
	coalesceWait = 10 * time.Second
	// passFor remembers that a URL's response could not be cached, so its
	// requests stop waiting on each other (Varnish's hit-for-pass).
	passFor = 30 * time.Second
	maxPass = 10000
	// entryOverhead approximates what an entry costs beyond its body and
	// headers, for the memory budget.
	entryOverhead = 256
)

// cacheableStatus are the statuses stored: final, and meaningful to repeat.
var cacheableStatus = map[int]bool{200: true, 203: true, 301: true, 404: true, 410: true}

type responseCache struct {
	maxBytes    int64
	maxObject   int64
	defaultTTL  time.Duration
	varyByQuery string
	params      map[string]bool
	varyHeaders []string
	bypass      []string
	stripAE     bool   // the site compresses: fetch identity, compress per client
	affinity    string // the session affinity cookie, ignored by the cookie rules
	now         func() time.Time
	wait        time.Duration // coalesceWait

	mu      sync.Mutex
	entries map[string]*list.Element // full key -> *cacheEntry
	vary    map[string]*varySet      // base key -> the response's Vary names
	lru     *list.List               // front = most recently used
	bytes   int64
	pass    map[string]time.Time
	flights map[string]chan struct{}

	hits, misses atomic.Int64
}

// varySet is what a URL's response varies on, kept while any variant of
// it is stored: dropped with its last variant, so that one-off URLs (random
// query strings) cannot grow it beyond the entries the budget allows.
type varySet struct {
	names    []string
	variants int
}

type cacheEntry struct {
	key, base, path string
	status          int
	header          http.Header
	body            []byte
	stored          time.Time
	age             time.Duration // Age the response already had when stored
	expires         time.Time
	public          bool // explicitly public: may answer requests with credentials
	size            int64
}

func newResponseCache(cfg model.CacheConfig, compression bool, affinityCookie string) *responseCache {
	c := &responseCache{
		maxBytes:    int64(cfg.MaxMemoryMB) << 20,
		maxObject:   int64(cfg.MaxObjectKB) << 10,
		defaultTTL:  time.Duration(cfg.DefaultTTLSec) * time.Second,
		varyByQuery: cfg.VaryByQuery,
		params:      map[string]bool{},
		bypass:      cfg.BypassPaths,
		stripAE:     compression,
		affinity:    affinityCookie,
		now:         time.Now,
		wait:        coalesceWait,
		entries:     map[string]*list.Element{},
		vary:        map[string]*varySet{},
		lru:         list.New(),
		pass:        map[string]time.Time{},
		flights:     map[string]chan struct{}{},
	}
	for _, p := range cfg.QueryParams {
		c.params[strings.TrimSpace(p)] = true
	}
	for _, h := range cfg.VaryHeaders {
		c.varyHeaders = append(c.varyHeaders, http.CanonicalHeaderKey(h))
	}
	slices.Sort(c.varyHeaders)
	return c
}

// handler puts the cache in front of next.
func (c *responseCache) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			next.ServeHTTP(w, r)
			return
		}
		if c.bypassed(r) {
			w.Header().Set("X-Cache", "BYPASS")
			next.ServeHTTP(w, r)
			return
		}
		creds := c.hasCredentials(r)
		base := c.baseKey(r)
		reqCC := parseCacheControl(r.Header.Values("Cache-Control"))
		// A client asking for a fresh copy (reload) gets one, which also
		// refreshes the cache.
		refresh := reqCC.has("no-cache") || reqCC["max-age"] == "0" ||
			(len(reqCC) == 0 && strings.Contains(strings.ToLower(r.Header.Get("Pragma")), "no-cache"))
		if !refresh {
			if e := c.lookup(base, r, creds); e != nil {
				c.serveHit(w, r, e)
				return
			}
		}
		c.misses.Add(1)
		if r.Method == http.MethodHead {
			w.Header().Set("X-Cache", "MISS")
			next.ServeHTTP(w, r)
			return
		}
		// Coalesce: the first miss for a URL fetches it, the others wait for
		// its response instead of all reaching the application. Requests
		// with credentials may get personal answers, and conditional ones
		// a 304 that is not stored, so neither waits nor is waited for.
		flightKey := c.variantBase(base, r.Header)
		conditional := r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != ""
		var flight chan struct{}
		if !creds && !conditional && !c.passing(flightKey) {
			if done, leader := c.join(flightKey); leader {
				flight = done
			} else {
				t := time.NewTimer(c.wait)
				select {
				case <-done:
				case <-t.C:
				case <-r.Context().Done():
				}
				t.Stop()
				if e := c.lookup(base, r, creds); e != nil {
					c.misses.Add(-1) // counted as the hit it turned out to be
					c.serveHit(w, r, e)
					return
				}
			}
		}
		// The request as it is now selects the variant: the site's handler
		// may still change it (a location strips its prefix).
		cw := &cacheWriter{ResponseWriter: w, c: c, r: r, reqHeader: r.Header.Clone(), path: r.URL.Path,
			base: base, flightKey: flightKey, creds: creds, flight: flight}
		defer cw.land() // however the request ends, a panic included
		if c.stripAE {
			r.Header.Del("Accept-Encoding")
		}
		next.ServeHTTP(cw, r)
		cw.finish()
	})
}

// bypassed reports requests the cache does not handle at all.
func (c *responseCache) bypassed(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return true
	}
	if r.Header.Get("Range") != "" || parseCacheControl(r.Header.Values("Cache-Control")).has("no-store") {
		return true
	}
	if len(c.bypass) == 0 {
		return false
	}
	// Bypassing is the safe side: a path under a prefix as sent, or in
	// either of the forms applications read it in (/x/../api, /API), is
	// bypassed, and so is a path those forms cannot be made of.
	literal, resolved, ok := pathForms(r.URL.Path, r.URL.RawPath)
	if !ok {
		return true
	}
	for _, p := range c.bypass {
		if strings.HasPrefix(r.URL.Path, p) {
			return true
		}
		if _, px, ok := pathForms(p, ""); ok {
			px = strings.TrimSuffix(px, "/")
			if underPrefix(literal, px) || underPrefix(resolved, px) {
				return true
			}
		}
	}
	return false
}

// hasCredentials: requests with Authorization or cookies may get answers
// meant for one user; only responses marked public are shared with them.
// The session affinity cookie is NodeHoster's own and does not count. A
// client certificate is a credential too, verified or not: the answer to
// one that failed verification may say why (clients cannot send these
// headers themselves).
func (c *responseCache) hasCredentials(r *http.Request) bool {
	if r.Header.Get("Authorization") != "" || r.Header.Get(hdrClientCert) != "" {
		return true
	}
	if v := r.Header.Get(hdrClientVerify); v != "" && v != "NONE" {
		return true
	}
	for _, ck := range r.Cookies() {
		if c.affinity == "" || (ck.Name != c.affinity && ck.Name != c.affinity+hopCookieSuffix) {
			return true
		}
	}
	return false
}

// baseKey identifies the resource: the binding (protocol, address and
// port: bindings of a site on other ports may have other client
// certificate policies, and applications may answer them differently),
// host, path and the query as configured.
func (c *responseCache) baseKey(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	binding := ""
	if rt, ok := r.Context().Value(routeKey{}).(*route); ok {
		binding = rt.binding.String() + " "
	}
	q := ""
	switch c.varyByQuery {
	case "none":
	case "listed":
		vals := url.Values{}
		for k, v := range r.URL.Query() {
			if c.params[k] {
				vals[k] = v
			}
		}
		q = vals.Encode()
	default:
		q = r.URL.Query().Encode() // sorted, so ?a=1&b=2 and ?b=2&a=1 share an entry
	}
	return binding + scheme + "://" + strings.ToLower(r.Host) + r.URL.EscapedPath() + "?" + q
}

// variantBase adds the configured vary headers to the base key.
func (c *responseCache) variantBase(base string, h http.Header) string {
	return base + c.headerValues(c.varyHeaders, h)
}

func (c *responseCache) headerValues(names []string, h http.Header) string {
	var b strings.Builder
	for _, n := range names {
		b.WriteString("\x00" + n + "=")
		switch n {
		case "Accept-Encoding":
			// Normalized to what matters, not every browser's spelling.
			// When the site compresses, the application always gets
			// identity requests, so there is one variant.
			if !c.stripAE {
				b.WriteString(negotiateEncoding(h.Get(n)))
			}
		case "Cookie":
			for _, ck := range (&http.Request{Header: h}).Cookies() {
				if ck.Name != c.affinity && ck.Name != c.affinity+hopCookieSuffix {
					b.WriteString(ck.Name + "=" + ck.Value + ";")
				}
			}
		default:
			b.WriteString(strings.Join(h.Values(n), ","))
		}
	}
	return b.String()
}

func (c *responseCache) lookup(base string, r *http.Request, creds bool) *cacheEntry {
	vb := c.variantBase(base, r.Header)
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.vary[vb]
	if !ok {
		return nil
	}
	el := c.entries[vb+c.headerValues(v.names, r.Header)]
	if el == nil {
		return nil
	}
	e := el.Value.(*cacheEntry)
	if !c.now().Before(e.expires) {
		c.removeLocked(el)
		return nil
	}
	if creds && !e.public {
		return nil
	}
	c.lru.MoveToFront(el)
	return e
}

func (c *responseCache) serveHit(w http.ResponseWriter, r *http.Request, e *cacheEntry) {
	c.hits.Add(1)
	h := w.Header()
	for k, v := range e.header {
		h[k] = slices.Clone(v)
	}
	h.Set("Age", strconv.Itoa(int((c.now().Sub(e.stored)+e.age)/time.Second)))
	h.Set("X-Cache", "HIT")
	if notModified(r, e.header) {
		h.Del("Content-Type")
		h.Del("Content-Length")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(e.body)))
	w.WriteHeader(e.status)
	if r.Method != http.MethodHead {
		w.Write(e.body)
	}
}

// notModified evaluates a conditional request against a stored response.
func notModified(r *http.Request, h http.Header) bool {
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		et := strings.TrimPrefix(h.Get("ETag"), "W/")
		if et == "" {
			return false
		}
		for _, t := range strings.Split(inm, ",") {
			t = strings.TrimSpace(t)
			if t == "*" || strings.TrimPrefix(t, "W/") == et {
				return true
			}
		}
		return false
	}
	if ims, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil {
		if lm, err := http.ParseTime(h.Get("Last-Modified")); err == nil {
			return !lm.After(ims)
		}
	}
	return false
}

// ---- coalescing and hit-for-pass

func (c *responseCache) join(key string) (chan struct{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if done, ok := c.flights[key]; ok {
		return done, false
	}
	done := make(chan struct{})
	c.flights[key] = done
	return done, true
}

func (c *responseCache) leave(key string, done chan struct{}) {
	c.mu.Lock()
	if c.flights[key] == done {
		delete(c.flights, key)
	}
	c.mu.Unlock()
	close(done)
}

func (c *responseCache) passing(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.pass[key]
	if ok && !c.now().Before(until) {
		delete(c.pass, key)
		return false
	}
	return ok
}

func (c *responseCache) markPass(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.pass) >= maxPass {
		for k, until := range c.pass {
			if !now.Before(until) {
				delete(c.pass, k)
			}
		}
		for k := range c.pass { // still full: forget arbitrary ones
			if len(c.pass) < maxPass/2 {
				break
			}
			delete(c.pass, k)
		}
	}
	c.pass[key] = now.Add(passFor)
}

// ---- storing

// cacheWriter passes a miss through to the client as it comes (streaming
// is never delayed) and keeps a copy while the response may be stored.
type cacheWriter struct {
	http.ResponseWriter
	c         *responseCache
	r         *http.Request
	reqHeader http.Header
	path      string
	base      string
	flightKey string
	creds     bool
	flight    chan struct{} // the misses waiting on this one; nil if none

	status   int
	store    bool
	pass     bool // not storable, for reasons that will hold for a while
	header   http.Header
	ttl, age time.Duration
	public   bool
	body     []byte
}

func (w *cacheWriter) WriteHeader(code int) {
	if code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status == 0 {
		w.status = code
		w.header = w.Header().Clone()
		w.ttl, w.age, w.public, w.store, w.pass = w.c.storable(w.r, code, w.header, w.creds)
		w.Header().Set("X-Cache", "MISS")
		if !w.store {
			w.giveUp()
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

// giveUp is called as soon as the response is known not to be stored: the
// misses waiting for it go to the application now rather than wait for the
// end of a response that may never end (an event stream) or take long (a
// large download).
func (w *cacheWriter) giveUp() {
	if w.pass && !w.creds {
		w.c.markPass(w.flightKey)
	}
	w.land()
}

// land releases the misses waiting on this one.
func (w *cacheWriter) land() {
	if w.flight != nil {
		w.c.leave(w.flightKey, w.flight)
		w.flight = nil
	}
}

func (w *cacheWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	if w.store {
		if int64(len(w.body)+n) > w.c.maxObject {
			w.store, w.pass, w.body = false, true, nil
			w.giveUp()
		} else {
			w.body = append(w.body, b[:n]...)
		}
	}
	return n, err
}

func (w *cacheWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *cacheWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.store = false
	w.land()
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *cacheWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// finish stores a complete response. It is not reached when the handler
// panicked (an aborted upstream body), so nothing partial is ever stored.
func (w *cacheWriter) finish() {
	c := w.c
	if !w.store {
		if w.pass && !w.creds {
			c.markPass(w.flightKey)
		}
		return
	}
	names, _ := varyNames(w.header)
	hdr := w.header
	for _, k := range []string{"Set-Cookie", "X-Cache", "Age", "Date", "Content-Length", "Connection", "Keep-Alive",
		"Transfer-Encoding", "Trailer", "Proxy-Connection", "Upgrade"} {
		hdr.Del(k)
	}
	now := c.now()
	e := &cacheEntry{
		base: w.flightKey, path: w.path, status: w.status, header: hdr, body: w.body,
		stored: now, age: w.age, expires: now.Add(w.ttl), public: w.public,
	}
	e.key = e.base + c.headerValues(names, w.reqHeader)
	e.size = int64(len(e.body)+len(e.key)) + entryOverhead
	for k, v := range hdr {
		e.size += int64(len(k))
		for _, s := range v {
			e.size += int64(len(s))
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[e.key]; ok {
		c.removeLocked(el)
	}
	if old, ok := c.vary[e.base]; ok && !slices.Equal(old.names, names) {
		// The application changed what the response varies on: variants
		// stored under the old names could never be found again.
		for el := c.lru.Front(); el != nil; {
			next := el.Next()
			if el.Value.(*cacheEntry).base == e.base {
				c.removeLocked(el)
			}
			el = next
		}
	}
	v := c.vary[e.base]
	if v == nil {
		v = &varySet{names: names}
		c.vary[e.base] = v
	}
	v.variants++
	c.entries[e.key] = c.lru.PushFront(e)
	c.bytes += e.size
	delete(c.pass, w.flightKey)
	for c.bytes > c.maxBytes && c.lru.Len() > 0 {
		c.removeLocked(c.lru.Back())
	}
}

// storable applies HTTP's caching rules for a shared cache to a response:
// how long it stays fresh, and whether it may be stored at all. pass means
// the answer will not change on the next request (as opposed to, say, a
// 304 to a conditional request).
func (c *responseCache) storable(r *http.Request, status int, h http.Header, creds bool) (ttl, age time.Duration, public, ok, pass bool) {
	if status == http.StatusNotModified || r.Method != http.MethodGet {
		return 0, 0, false, false, false
	}
	pass = true
	if !cacheableStatus[status] {
		return
	}
	cc := parseCacheControl(h.Values("Cache-Control"))
	if cc.has("no-store") || cc.has("private") || cc.has("no-cache") {
		return
	}
	public = cc.has("public")
	if creds && !public {
		return 0, 0, false, false, false
	}
	// A cookie is for one client. The affinity cookie is NodeHoster's own
	// and never stored; with an explicit public, others are dropped from
	// the stored copy (see finish).
	for _, line := range h.Values("Set-Cookie") {
		ck, err := http.ParseSetCookie(line)
		ours := err == nil && c.affinity != "" && (ck.Name == c.affinity || ck.Name == c.affinity+hopCookieSuffix)
		if !ours && !public {
			return
		}
	}
	if _, star := varyNames(h); star {
		return
	}
	mt, _, _ := strings.Cut(h.Get("Content-Type"), ";")
	if strings.EqualFold(strings.TrimSpace(mt), "text/event-stream") {
		return
	}
	if n, err := strconv.ParseInt(h.Get("Content-Length"), 10, 64); err == nil && n > c.maxObject {
		return
	}
	now := c.now()
	switch {
	case cc["s-maxage"] != "":
		ttl = seconds(cc["s-maxage"])
	case cc["max-age"] != "":
		ttl = seconds(cc["max-age"])
	case h.Get("Expires") != "":
		exp, err := http.ParseTime(h.Get("Expires"))
		if err != nil {
			return // an invalid Expires means already expired
		}
		base := now
		if d, err := http.ParseTime(h.Get("Date")); err == nil {
			base = d
		}
		ttl = exp.Sub(base)
	default:
		ttl = c.defaultTTL
	}
	if a := h.Get("Age"); a != "" {
		age = seconds(a)
		ttl -= age
	}
	if ttl <= 0 {
		return
	}
	return ttl, age, public, true, true
}

func seconds(v string) time.Duration {
	n, err := strconv.ParseInt(strings.Trim(v, `"`), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return time.Duration(min(n, 1<<31)) * time.Second
}

// cacheControl holds Cache-Control directives, lower-case, with values.
type cacheControl map[string]string

func parseCacheControl(values []string) cacheControl {
	cc := cacheControl{}
	for _, v := range values {
		for _, d := range strings.Split(v, ",") {
			k, val, _ := strings.Cut(strings.TrimSpace(d), "=")
			if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
				cc[k] = strings.TrimSpace(val)
			}
		}
	}
	return cc
}

func (cc cacheControl) has(k string) bool { _, ok := cc[k]; return ok }

// ---- maintenance

func (c *responseCache) removeLocked(el *list.Element) {
	e := c.lru.Remove(el).(*cacheEntry)
	delete(c.entries, e.key)
	c.bytes -= e.size
	if v := c.vary[e.base]; v != nil {
		if v.variants--; v.variants <= 0 {
			delete(c.vary, e.base)
		}
	}
}

// purge removes entries whose path starts with prefix ("" = all).
func (c *responseCache) purge(prefix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for el := c.lru.Front(); el != nil; {
		next := el.Next()
		if strings.HasPrefix(el.Value.(*cacheEntry).path, prefix) {
			c.removeLocked(el)
			n++
		}
		el = next
	}
	if prefix == "" {
		c.vary = map[string]*varySet{}
	}
	c.pass = map[string]time.Time{}
	return n
}

func (c *responseCache) stats() model.CacheStats {
	c.mu.Lock()
	st := model.CacheStats{Entries: c.lru.Len(), Bytes: c.bytes}
	c.mu.Unlock()
	st.Hits, st.Misses = c.hits.Load(), c.misses.Load()
	if t := st.Hits + st.Misses; t > 0 {
		st.HitRatio = float64(st.Hits) / float64(t)
	}
	return st
}

// PurgeCache empties a site's response cache, or the part of it under a
// path prefix. It returns how many entries were removed.
func (s *Server) PurgeCache(id, prefix string) int {
	if rt := s.runtime(id); rt != nil && rt.cache != nil {
		return rt.cache.purge(prefix)
	}
	return 0
}

// CacheStats describes a site's response cache; nil when it has none.
func (s *Server) CacheStats(id string) *model.CacheStats {
	if rt := s.runtime(id); rt != nil && rt.cache != nil {
		st := rt.cache.stats()
		return &st
	}
	return nil
}
