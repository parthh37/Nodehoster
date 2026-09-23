package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func cacheCfg(mutate ...func(*model.CacheConfig)) model.CacheConfig {
	c := model.CacheConfig{Enabled: true, MaxMemoryMB: 1, MaxObjectKB: 64, VaryByQuery: "all"}
	for _, m := range mutate {
		m(&c)
	}
	return c
}

// TestCacheStorable is the table of HTTP's caching rules for a shared cache.
func TestCacheStorable(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	type tc struct {
		name       string
		method     string
		status     int
		hdr        map[string]string
		cookie     []string
		creds      bool
		defaultTTL time.Duration
		ttl        time.Duration // 0 = not storable
		pass       bool          // remembered as uncacheable
	}
	cases := []tc{
		{name: "max-age", hdr: map[string]string{"Cache-Control": "max-age=60"}, ttl: time.Minute},
		{name: "s-maxage wins", hdr: map[string]string{"Cache-Control": "max-age=60, s-maxage=30"}, ttl: 30 * time.Second},
		{name: "public max-age", hdr: map[string]string{"Cache-Control": "public, max-age=10"}, ttl: 10 * time.Second},
		{name: "no-store", hdr: map[string]string{"Cache-Control": "no-store, max-age=60"}, pass: true},
		{name: "private", hdr: map[string]string{"Cache-Control": "private, max-age=60"}, pass: true},
		{name: "no-cache", hdr: map[string]string{"Cache-Control": "no-cache"}, pass: true},
		{name: "max-age=0", hdr: map[string]string{"Cache-Control": "max-age=0"}, pass: true},
		{name: "no freshness", pass: true},
		{name: "no freshness, default ttl", defaultTTL: 5 * time.Second, ttl: 5 * time.Second},
		{name: "default ttl does not override no-store", defaultTTL: 5 * time.Second, hdr: map[string]string{"Cache-Control": "no-store"}, pass: true},
		{name: "expires", hdr: map[string]string{"Date": now.Format(http.TimeFormat), "Expires": now.Add(2 * time.Minute).Format(http.TimeFormat)}, ttl: 2 * time.Minute},
		{name: "expires in the past", hdr: map[string]string{"Expires": now.Add(-time.Minute).Format(http.TimeFormat)}, pass: true},
		{name: "invalid expires", hdr: map[string]string{"Expires": "0"}, pass: true},
		{name: "max-age beats expires", hdr: map[string]string{"Cache-Control": "max-age=10", "Expires": now.Add(time.Hour).Format(http.TimeFormat)}, ttl: 10 * time.Second},
		{name: "age deducted", hdr: map[string]string{"Cache-Control": "max-age=60", "Age": "20"}, ttl: 40 * time.Second},
		{name: "already stale", hdr: map[string]string{"Cache-Control": "max-age=60", "Age": "70"}, pass: true},
		{name: "203", status: 203, hdr: map[string]string{"Cache-Control": "max-age=60"}, ttl: time.Minute},
		{name: "301", status: 301, hdr: map[string]string{"Cache-Control": "max-age=60"}, ttl: time.Minute},
		{name: "404", status: 404, hdr: map[string]string{"Cache-Control": "max-age=60"}, ttl: time.Minute},
		{name: "410", status: 410, hdr: map[string]string{"Cache-Control": "max-age=60"}, ttl: time.Minute},
		{name: "302", status: 302, hdr: map[string]string{"Cache-Control": "max-age=60"}, pass: true},
		{name: "500", status: 500, hdr: map[string]string{"Cache-Control": "max-age=60"}, pass: true},
		{name: "206", status: 206, hdr: map[string]string{"Cache-Control": "max-age=60"}, pass: true},
		{name: "304 is not a verdict", status: 304, hdr: map[string]string{"Cache-Control": "max-age=60"}},
		{name: "HEAD", method: http.MethodHead, hdr: map[string]string{"Cache-Control": "max-age=60"}},
		{name: "set-cookie", hdr: map[string]string{"Cache-Control": "max-age=60"}, cookie: []string{"session=abc; Path=/"}, pass: true},
		{name: "set-cookie public", hdr: map[string]string{"Cache-Control": "public, max-age=60"}, cookie: []string{"session=abc"}, ttl: time.Minute},
		{name: "affinity cookie only", hdr: map[string]string{"Cache-Control": "max-age=60"}, cookie: []string{"NHAffinity=x; Path=/; HttpOnly"}, ttl: time.Minute},
		{name: "vary star", hdr: map[string]string{"Cache-Control": "max-age=60", "Vary": "*"}, pass: true},
		{name: "vary header", hdr: map[string]string{"Cache-Control": "max-age=60", "Vary": "Accept-Language"}, ttl: time.Minute},
		{name: "event stream", hdr: map[string]string{"Cache-Control": "max-age=60", "Content-Type": "text/event-stream"}, pass: true},
		{name: "too big", hdr: map[string]string{"Cache-Control": "max-age=60", "Content-Length": "999999"}, pass: true},
		{name: "credentials, not public", creds: true, hdr: map[string]string{"Cache-Control": "max-age=60"}},
		{name: "credentials, public", creds: true, hdr: map[string]string{"Cache-Control": "public, max-age=60"}, ttl: time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rc := newResponseCache(cacheCfg(func(cfg *model.CacheConfig) { cfg.DefaultTTLSec = int(c.defaultTTL / time.Second) }), false, model.DefaultAffinityCookie)
			rc.now = func() time.Time { return now }
			method := c.method
			if method == "" {
				method = http.MethodGet
			}
			status := c.status
			if status == 0 {
				status = 200
			}
			h := http.Header{}
			for k, v := range c.hdr {
				h.Set(k, v)
			}
			for _, v := range c.cookie {
				h.Add("Set-Cookie", v)
			}
			ttl, _, _, ok, pass := rc.storable(httptest.NewRequest(method, "/", nil), status, h, c.creds)
			if ok != (c.ttl > 0) || (ok && ttl != c.ttl) {
				t.Fatalf("storable = %v, ttl %s; want ttl %s", ok, ttl, c.ttl)
			}
			if !ok && pass != c.pass {
				t.Fatalf("pass = %v, want %v", pass, c.pass)
			}
		})
	}
}

// origin counts requests and answers with a body naming the request.
type origin struct {
	hits    atomic.Int64
	handler func(w http.ResponseWriter, r *http.Request)
	lastAE  atomic.Value
}

func (o *origin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := o.hits.Add(1)
	o.lastAE.Store(r.Header.Get("Accept-Encoding"))
	if o.handler != nil {
		o.handler(w, r)
		return
	}
	w.Header().Set("Cache-Control", "max-age=60")
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "%s #%d", r.URL.RequestURI(), n)
}

type cacheReq struct {
	method string
	target string
	hdr    []string
}

func (c *responseCache) do(t *testing.T, next http.Handler, req cacheReq) *httptest.ResponseRecorder {
	t.Helper()
	method := req.method
	if method == "" {
		method = http.MethodGet
	}
	r := httptest.NewRequest(method, "http://site.example"+req.target, nil)
	for i := 0; i+1 < len(req.hdr); i += 2 {
		r.Header.Add(req.hdr[i], req.hdr[i+1])
	}
	rec := httptest.NewRecorder()
	c.handler(next).ServeHTTP(rec, r)
	return rec
}

func expectCache(t *testing.T, rec *httptest.ResponseRecorder, xcache, body string) {
	t.Helper()
	if got := rec.Header().Get("X-Cache"); got != xcache {
		t.Fatalf("X-Cache = %q, want %q (body %q)", got, xcache, rec.Body.String())
	}
	if body != "" && rec.Body.String() != body {
		t.Fatalf("body = %q, want %q", rec.Body.String(), body)
	}
}

func TestCacheHitMissBypass(t *testing.T) {
	o := &origin{}
	c := newResponseCache(cacheCfg(func(cfg *model.CacheConfig) { cfg.BypassPaths = []string{"/api"} }), false, model.DefaultAffinityCookie)
	now := time.Now()
	c.now = func() time.Time { return now }

	rec := c.do(t, o, cacheReq{target: "/page"})
	expectCache(t, rec, "MISS", "/page #1")
	now = now.Add(5 * time.Second)
	rec = c.do(t, o, cacheReq{target: "/page"})
	expectCache(t, rec, "HIT", "/page #1")
	if rec.Header().Get("Age") != "5" || rec.Header().Get("Content-Length") != "8" || rec.Header().Get("Cache-Control") != "max-age=60" {
		t.Fatalf("hit headers: %v", rec.Header())
	}
	rec = c.do(t, o, cacheReq{method: http.MethodHead, target: "/page"})
	expectCache(t, rec, "HIT", "")
	if rec.Body.Len() != 0 {
		t.Fatal("HEAD hit has a body")
	}

	for _, req := range []cacheReq{
		{method: http.MethodPost, target: "/page"},
		{target: "/api/users"},
		{target: "/page", hdr: []string{"Range", "bytes=0-1"}},
		{target: "/page", hdr: []string{"Cache-Control", "no-store"}},
	} {
		expectCache(t, c.do(t, o, req), "BYPASS", "")
	}
	// A reload fetches a fresh copy and refreshes the cache.
	expectCache(t, c.do(t, o, cacheReq{target: "/page", hdr: []string{"Cache-Control", "no-cache"}}), "MISS", "")
	fresh := o.hits.Load()
	expectCache(t, c.do(t, o, cacheReq{target: "/page"}), "HIT", fmt.Sprintf("/page #%d", fresh))

	// Expired entries are fetched again.
	now = now.Add(2 * time.Minute)
	expectCache(t, c.do(t, o, cacheReq{target: "/page"}), "MISS", "")

	if st := c.stats(); st.Entries != 1 || st.Hits != 3 || st.Misses != 3 || st.Bytes <= 0 || st.HitRatio != 0.5 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestCacheConditionalHit(t *testing.T) {
	o := &origin{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", "Mon, 01 Sep 2025 00:00:00 GMT")
		io.WriteString(w, "content")
	}}
	c := newResponseCache(cacheCfg(), false, "")
	c.do(t, o, cacheReq{target: "/x"})
	rec := c.do(t, o, cacheReq{target: "/x", hdr: []string{"If-None-Match", `W/"abc"`}})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || rec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("If-None-Match: %d %q", rec.Code, rec.Body.String())
	}
	rec = c.do(t, o, cacheReq{target: "/x", hdr: []string{"If-Modified-Since", "Tue, 02 Sep 2025 00:00:00 GMT"}})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("If-Modified-Since: %d", rec.Code)
	}
	rec = c.do(t, o, cacheReq{target: "/x", hdr: []string{"If-None-Match", `"other"`}})
	if rec.Code != http.StatusOK || rec.Body.String() != "content" {
		t.Fatalf("other ETag: %d", rec.Code)
	}
	if o.hits.Load() != 1 {
		t.Fatalf("origin hit %d times", o.hits.Load())
	}
}

func TestCacheCredentials(t *testing.T) {
	o := &origin{}
	c := newResponseCache(cacheCfg(), false, model.DefaultAffinityCookie)
	c.do(t, o, cacheReq{target: "/p"})

	// Requests with credentials never get a shared, non-public copy, and
	// their answers are not stored.
	for _, hdr := range [][]string{{"Authorization", "Basic dTpw"}, {"Cookie", "session=1"}, {"Cookie", "NHAffinity=x; session=1"}} {
		rec := c.do(t, o, cacheReq{target: "/p", hdr: hdr})
		expectCache(t, rec, "MISS", "")
		if rec.Body.String() == "/p #1" {
			t.Fatalf("%v got the shared copy", hdr)
		}
	}
	// The affinity cookie alone is not a credential.
	expectCache(t, c.do(t, o, cacheReq{target: "/p", hdr: []string{"Cookie", "NHAffinity=x"}}), "HIT", "/p #1")

	// Explicitly public responses are shared with them.
	pub := &origin{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Add("Set-Cookie", "tracking=1")
		io.WriteString(w, "public")
	}}
	c2 := newResponseCache(cacheCfg(), false, "")
	rec := c2.do(t, pub, cacheReq{target: "/p", hdr: []string{"Authorization", "Bearer x"}})
	if rec.Header().Get("Set-Cookie") == "" {
		t.Fatal("the fetching client lost its Set-Cookie")
	}
	rec = c2.do(t, pub, cacheReq{target: "/p", hdr: []string{"Cookie", "a=b"}})
	expectCache(t, rec, "HIT", "public")
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatal("a stored Set-Cookie was replayed to another client")
	}
}

func TestCacheVary(t *testing.T) {
	o := &origin{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Vary", "Accept-Language")
		io.WriteString(w, "lang="+r.Header.Get("Accept-Language"))
	}}
	c := newResponseCache(cacheCfg(), false, "")
	for range 2 {
		for _, lang := range []string{"en", "fr", ""} {
			rec := c.do(t, o, cacheReq{target: "/", hdr: []string{"Accept-Language", lang}})
			if rec.Body.String() != "lang="+lang {
				t.Fatalf("Accept-Language %q answered %q", lang, rec.Body.String())
			}
		}
	}
	if o.hits.Load() != 3 {
		t.Fatalf("origin hit %d times for 3 variants", o.hits.Load())
	}

	star := &origin{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Vary", "*")
		io.WriteString(w, "x")
	}}
	c = newResponseCache(cacheCfg(), false, "")
	c.do(t, star, cacheReq{target: "/"})
	expectCache(t, c.do(t, star, cacheReq{target: "/"}), "MISS", "")

	// Configured vary headers select variants even when the application
	// does not say so.
	dev := &origin{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		io.WriteString(w, "device="+r.Header.Get("X-Device"))
	}}
	c = newResponseCache(cacheCfg(func(cfg *model.CacheConfig) { cfg.VaryHeaders = []string{"x-device"} }), false, "")
	c.do(t, dev, cacheReq{target: "/", hdr: []string{"X-Device", "mobile"}})
	expectCache(t, c.do(t, dev, cacheReq{target: "/", hdr: []string{"X-Device", "desktop"}}), "MISS", "device=desktop")
	expectCache(t, c.do(t, dev, cacheReq{target: "/", hdr: []string{"X-Device", "mobile"}}), "HIT", "device=mobile")
}

func TestCacheQueryString(t *testing.T) {
	cases := []struct {
		mode   string
		params []string
		first  string
		second string
		hit    bool
	}{
		{"all", nil, "/p?a=1&b=2", "/p?b=2&a=1", true},
		{"all", nil, "/p?a=1", "/p?a=2", false},
		{"none", nil, "/p?a=1", "/p?a=2", true},
		{"listed", []string{"page"}, "/p?page=1&utm_source=x", "/p?utm_source=y&page=1", true},
		{"listed", []string{"page"}, "/p?page=1", "/p?page=2", false},
	}
	for _, tc := range cases {
		c := newResponseCache(cacheCfg(func(cfg *model.CacheConfig) { cfg.VaryByQuery, cfg.QueryParams = tc.mode, tc.params }), false, "")
		o := &origin{}
		c.do(t, o, cacheReq{target: tc.first})
		want := "MISS"
		if tc.hit {
			want = "HIT"
		}
		if got := c.do(t, o, cacheReq{target: tc.second}).Header().Get("X-Cache"); got != want {
			t.Errorf("%s %v: %s then %s = %s, want %s", tc.mode, tc.params, tc.first, tc.second, got, want)
		}
	}
}

func TestCacheCoalescesMisses(t *testing.T) {
	release := make(chan struct{})
	o := &origin{handler: func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Cache-Control", "max-age=60")
		io.WriteString(w, "slow")
	}}
	c := newResponseCache(cacheCfg(), false, "")
	h := c.handler(o)
	const n = 10
	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://site/slow", nil))
			results[i] = rec
		}()
	}
	// Let every request arrive before the first is answered.
	deadline := time.Now().Add(5 * time.Second)
	for o.hits.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if o.hits.Load() != 1 {
		t.Fatalf("origin reached %d times by %d concurrent misses", o.hits.Load(), n)
	}
	counts := map[string]int{}
	for _, rec := range results {
		if rec.Body.String() != "slow" {
			t.Fatalf("body %q", rec.Body.String())
		}
		counts[rec.Header().Get("X-Cache")]++
	}
	if counts["MISS"] != 1 || counts["HIT"] != n-1 {
		t.Fatalf("X-Cache counts %v", counts)
	}
	if st := c.stats(); st.Hits != n-1 || st.Misses != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// TestCacheCoalescingUncacheable: when the first response cannot be stored
// the waiting requests go to the application, and later requests for that
// URL stop waiting on each other.
func TestCacheCoalescingUncacheable(t *testing.T) {
	var inFlight, peak atomic.Int64
	o := &origin{handler: func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, "dynamic")
	}}
	c := newResponseCache(cacheCfg(), false, "")
	wave := func() {
		var wg sync.WaitGroup
		for range 5 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rec := c.do(t, o, cacheReq{target: "/d"})
				if rec.Body.String() != "dynamic" || rec.Header().Get("X-Cache") != "MISS" {
					t.Errorf("got %q %s", rec.Body.String(), rec.Header().Get("X-Cache"))
				}
			}()
		}
		wg.Wait()
	}
	wave()
	if !c.passing(c.variantBase("http://site.example/d?", http.Header{})) {
		t.Fatal("uncacheable URL not remembered")
	}
	peak.Store(0)
	wave()
	if peak.Load() < 2 {
		t.Fatalf("requests to an uncacheable URL were serialized (peak %d in flight)", peak.Load())
	}
}

func TestCacheCoalescingTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int64
	o := &origin{handler: func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			<-release // the first request hangs
		}
		w.Header().Set("Cache-Control", "max-age=60")
		io.WriteString(w, "ok")
	}}
	c := newResponseCache(cacheCfg(), false, "")
	c.wait = 50 * time.Millisecond
	go c.do(t, o, cacheReq{target: "/hang"})
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	rec := c.do(t, o, cacheReq{target: "/hang"})
	if rec.Body.String() != "ok" || time.Since(start) > 2*time.Second {
		t.Fatalf("waiting request: %q after %s", rec.Body.String(), time.Since(start))
	}
}

func TestCacheLRUEviction(t *testing.T) {
	o := &origin{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.Write(make([]byte, 1000))
	}}
	c := newResponseCache(cacheCfg(), false, "")
	for _, p := range []string{"/1", "/2", "/3"} {
		c.do(t, o, cacheReq{target: p})
	}
	// Room for three entries.
	c.mu.Lock()
	c.maxBytes = c.bytes
	c.mu.Unlock()
	expectCache(t, c.do(t, o, cacheReq{target: "/1"}), "HIT", "") // /1 is now the most recent
	c.do(t, o, cacheReq{target: "/4"})                            // evicts /2, the least recently used
	expectCache(t, c.do(t, o, cacheReq{target: "/1"}), "HIT", "")
	expectCache(t, c.do(t, o, cacheReq{target: "/3"}), "HIT", "")
	expectCache(t, c.do(t, o, cacheReq{target: "/2"}), "MISS", "")
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bytes > c.maxBytes || c.lru.Len() != 3 {
		t.Fatalf("%d bytes in %d entries, budget %d", c.bytes, c.lru.Len(), c.maxBytes)
	}
}

func TestCacheMaxObjectSize(t *testing.T) {
	o := &origin{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		for range 100 { // 100 KB without a Content-Length, over the 64 KB limit
			w.Write(make([]byte, 1024))
		}
	}}
	c := newResponseCache(cacheCfg(), false, "")
	rec := c.do(t, o, cacheReq{target: "/big"})
	if rec.Body.Len() != 100*1024 {
		t.Fatalf("client got %d bytes", rec.Body.Len())
	}
	expectCache(t, c.do(t, o, cacheReq{target: "/big"}), "MISS", "")
	if st := c.stats(); st.Entries != 0 {
		t.Fatalf("stored an object over the limit: %+v", st)
	}
}

func TestCachePurge(t *testing.T) {
	o := &origin{}
	c := newResponseCache(cacheCfg(), false, "")
	for _, p := range []string{"/blog/a", "/blog/b", "/shop"} {
		c.do(t, o, cacheReq{target: p})
	}
	if n := c.purge("/blog"); n != 2 {
		t.Fatalf("purged %d, want 2", n)
	}
	expectCache(t, c.do(t, o, cacheReq{target: "/shop"}), "HIT", "")
	expectCache(t, c.do(t, o, cacheReq{target: "/blog/a"}), "MISS", "")
	if n := c.purge(""); n != 2 {
		t.Fatalf("purged %d, want 2", n)
	}
	if st := c.stats(); st.Entries != 0 || st.Bytes != 0 {
		t.Fatalf("after purging everything: %+v", st)
	}
}

// TestCacheAndCompression: the cache keeps one uncompressed copy, fetched
// without Accept-Encoding, and every client gets the encoding it accepts.
func TestCacheAndCompression(t *testing.T) {
	body := strings.Repeat("cached and compressed ", 100)
	o := &origin{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, body)
	}}
	site := &model.Site{ID: "c", Name: "cached", Type: model.SiteProxy, Proxy: &model.ProxyConfig{}}
	srv := httptest.NewServer(o)
	defer srv.Close()
	site.Proxy.Upstreams = []model.Upstream{{URL: srv.URL}}
	site.Routing.Compression = true
	site.Routing.Cache = cacheCfg()
	rt := compileTest(t, affServer(t), site)
	for i, accept := range []string{"br", "gzip", "", "gzip, br"} {
		rec := get(t, rt, "http://site.example/page", "Accept-Encoding", accept)
		want := map[string]string{"br": "br", "gzip": "gzip", "": "", "gzip, br": "br"}[accept]
		if rec.Header().Get("Content-Encoding") != want || decodeBody(t, want, rec.Body.Bytes()) != body {
			t.Fatalf("accept %q: Content-Encoding %q", accept, rec.Header().Get("Content-Encoding"))
		}
		wantCache := "HIT"
		if i == 0 {
			wantCache = "MISS"
		}
		if rec.Header().Get("X-Cache") != wantCache || !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
			t.Fatalf("accept %q: X-Cache %s, Vary %q", accept, rec.Header().Get("X-Cache"), rec.Header().Get("Vary"))
		}
	}
	// (Go's transport may add its own gzip, which it decodes itself.)
	if ae := o.lastAE.Load().(string); o.hits.Load() != 1 || strings.Contains(ae, "br") {
		t.Fatalf("origin hit %d times, last Accept-Encoding %q", o.hits.Load(), ae)
	}

	// A new configuration starts empty; an unchanged one keeps its entries.
	rt2 := rt.srv.compileSite(site, rt)
	if rt2.cache != rt.cache {
		t.Fatal("recompiling an unchanged site dropped its cache")
	}
	changed := *site
	rt3 := rt.srv.compileSite(&changed, rt)
	if rt3.cache == rt.cache {
		t.Fatal("a changed site kept the old cache")
	}
}
