package proxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/procmgr"
)

// affServer is a proxy Server with no listeners, a fixed affinity key and
// no node instances unless a test provides them.
func affServer(t *testing.T) *Server {
	t.Helper()
	s := testServer(model.DefaultSettings())
	s.deps.AffinityKey = bytes.Repeat([]byte{7}, 32)
	s.backends = func(string) []*procmgr.Backend { return nil }
	s.table.Store(&routeTable{byPort: map[int][]*route{}, sites: map[string]*siteRuntime{}})
	return s
}

func compileTest(t *testing.T, s *Server, site *model.Site) *siteRuntime {
	t.Helper()
	site.ApplyDefaults()
	if err := site.Validate(); err != nil {
		t.Fatal(err)
	}
	rt := s.compileSite(site, nil)
	t.Cleanup(func() { rt.release(false) })
	return rt
}

// named starts a backend that answers with its name.
func named(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, name)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func proxySite(strategy string, affinity bool, upstreams ...*httptest.Server) *model.Site {
	site := &model.Site{ID: "p1", Name: "proxy", Type: model.SiteProxy, Proxy: &model.ProxyConfig{LoadBalancing: strategy}}
	for _, u := range upstreams {
		site.Proxy.Upstreams = append(site.Proxy.Upstreams, model.Upstream{URL: u.URL})
	}
	site.Routing.Affinity.Enabled = affinity
	return site
}

type result struct {
	body   string
	status int
	cookie *http.Cookie // Set-Cookie of the affinity cookie, nil if none
	raw    string       // its Set-Cookie header line
}

func fetch(t *testing.T, h http.Handler, ck *http.Cookie, opts ...func(*http.Request)) result {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	if ck != nil {
		req.AddCookie(ck)
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := result{body: rec.Body.String(), status: rec.Code}
	for _, line := range rec.Result().Header.Values("Set-Cookie") {
		if c, err := http.ParseSetCookie(line); err == nil && strings.HasPrefix(c.Name, model.DefaultAffinityCookie) {
			res.cookie, res.raw = c, line
		}
	}
	return res
}

// jar keeps the affinity cookie like a browser would.
func jar(t *testing.T, h http.Handler, ck **http.Cookie) string {
	t.Helper()
	r := fetch(t, h, *ck)
	if r.cookie != nil {
		*ck = &http.Cookie{Name: r.cookie.Name, Value: r.cookie.Value}
	}
	return r.body
}

func TestAffinityStickiness(t *testing.T) {
	a, b, c := named(t, "a"), named(t, "b"), named(t, "c")
	s := affServer(t)
	rt := compileTest(t, s, proxySite("round_robin", true, a, b, c))

	// Clients without the cookie are balanced by the strategy.
	seen := map[string]bool{}
	for range 6 {
		seen[fetch(t, rt, nil).body] = true
	}
	if len(seen) != 3 {
		t.Fatalf("without a cookie requests went to %v, want all three upstreams", seen)
	}

	var ck *http.Cookie
	first := jar(t, rt, &ck)
	if ck == nil {
		t.Fatal("no affinity cookie issued")
	}
	for range 20 {
		r := fetch(t, rt, ck)
		if r.body != first {
			t.Fatalf("pinned to %s, answered by %s", first, r.body)
		}
		if r.cookie != nil {
			t.Fatalf("cookie re-issued although the backend did not change: %s", r.raw)
		}
	}
}

func TestAffinityCookieAttributes(t *testing.T) {
	s := affServer(t)
	site := proxySite("round_robin", true, named(t, "a"), named(t, "b"))
	rt := compileTest(t, s, site)
	r := fetch(t, rt, nil)
	for _, want := range []string{"Path=/", "HttpOnly", "SameSite=Lax"} {
		if !strings.Contains(r.raw, want) {
			t.Errorf("Set-Cookie %q lacks %s", r.raw, want)
		}
	}
	if strings.Contains(r.raw, "Secure") || strings.Contains(r.raw, "Max-Age") {
		t.Errorf("plain HTTP session cookie: %q", r.raw)
	}

	site.Routing.Affinity.LifetimeSec = 3600
	site.Routing.Affinity.CookieName = "Sticky"
	rt = compileTest(t, s, site)
	req := httptest.NewRequest(http.MethodGet, "https://app.example.com/", nil)
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	line := rec.Result().Header.Get("Set-Cookie")
	for _, want := range []string{"Sticky=", "Secure", "Max-Age=3600"} {
		if !strings.Contains(line, want) {
			t.Errorf("Set-Cookie %q lacks %s", line, want)
		}
	}
}

// TestAffinityOpaque: the cookie reveals nothing about the backend.
func TestAffinityOpaque(t *testing.T) {
	a, b := named(t, "a"), named(t, "b")
	rt := compileTest(t, affServer(t), proxySite("round_robin", true, a, b))
	r := fetch(t, rt, nil)
	raw, err := base64.RawURLEncoding.DecodeString(r.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []*httptest.Server{a, b} {
		host := strings.TrimPrefix(u.URL, "http://")
		_, port, _ := net.SplitHostPort(host)
		if strings.Contains(r.cookie.Value, host) || strings.Contains(string(raw), host) || strings.Contains(string(raw), port) {
			t.Fatalf("cookie %q reveals upstream %s", r.cookie.Value, host)
		}
	}
}

func TestAffinityTamperResistance(t *testing.T) {
	a, b := named(t, "a"), named(t, "b")
	s := affServer(t)
	rt := compileTest(t, s, proxySite("round_robin", true, a, b))
	good := fetch(t, rt, nil).cookie
	if _, ok := rt.affinity.decode(good.Value); !ok {
		t.Fatal("genuine cookie rejected")
	}

	// Every single-character change is detected.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for i := range good.Value {
		v := []byte(good.Value)
		v[i] = alphabet[(strings.IndexByte(alphabet, v[i])+1)%len(alphabet)]
		if _, ok := rt.affinity.decode(string(v)); ok {
			t.Fatalf("tampered cookie %q (position %d) accepted", v, i)
		}
	}
	for _, v := range []string{"", "x", "not base64!", good.Value + "AA", good.Value[:10]} {
		if _, ok := rt.affinity.decode(v); ok {
			t.Fatalf("cookie %q accepted", v)
		}
	}

	// Forging one for the other upstream needs the key.
	other := newAffinity(bytes.Repeat([]byte{8}, 32), "p1", model.DefaultAffinityCookie, 0)
	forged := other.encode(affinityCookie{member: other.memberID(b.URL), slot: noSlot, issued: time.Now().Unix()})
	if _, ok := rt.affinity.decode(forged); ok {
		t.Fatal("cookie signed with another key accepted")
	}
	// A cookie from another site does not apply here either.
	site2 := newAffinity(s.affinityKey(), "p2", model.DefaultAffinityCookie, 0)
	if _, ok := rt.affinity.decode(site2.encode(affinityCookie{member: site2.memberID(b.URL), slot: noSlot})); ok {
		t.Fatal("another site's cookie accepted")
	}

	// A rejected cookie is served normally and replaced.
	r := fetch(t, rt, &http.Cookie{Name: model.DefaultAffinityCookie, Value: forged})
	if r.status != http.StatusOK || r.cookie == nil {
		t.Fatalf("forged cookie: status %d, new cookie %v", r.status, r.cookie)
	}
}

func TestAffinityFallbackWhenBackendDisappears(t *testing.T) {
	a, b := named(t, "a"), named(t, "b")
	rt := compileTest(t, affServer(t), proxySite("round_robin", true, a, b))
	var ck *http.Cookie
	first := jar(t, rt, &ck)
	gone, other := a, "b"
	if first == "b" {
		gone, other = b, "a"
	}
	gone.Close()

	// The request in flight when it went away fails...
	if r := fetch(t, rt, ck); r.status != http.StatusBadGateway || r.cookie != nil {
		t.Fatalf("request to a closed upstream: status %d, cookie %v", r.status, r.cookie)
	}
	// ...then the client moves to the other upstream with a new cookie,
	// and stays there.
	r := fetch(t, rt, ck)
	if r.body != other || r.cookie == nil {
		t.Fatalf("after the failure: answered by %q, new cookie %v", r.body, r.cookie)
	}
	ck = &http.Cookie{Name: r.cookie.Name, Value: r.cookie.Value}
	for range 5 {
		if r := fetch(t, rt, ck); r.body != other || r.cookie != nil {
			t.Fatalf("re-pinned client answered by %q (cookie %v)", r.body, r.cookie)
		}
	}
}

func TestAffinitySingleBackend(t *testing.T) {
	rt := compileTest(t, affServer(t), proxySite("round_robin", true, named(t, "only")))
	if r := fetch(t, rt, nil); r.body != "only" || r.cookie != nil {
		t.Fatalf("single upstream: body %q, cookie %v", r.body, r.cookie)
	}
	// A cookie from when there were more upstreams (or a bogus one) is
	// ignored gracefully.
	if r := fetch(t, rt, &http.Cookie{Name: model.DefaultAffinityCookie, Value: "garbage"}); r.body != "only" || r.cookie != nil {
		t.Fatalf("stale cookie: body %q, cookie %v", r.body, r.cookie)
	}
}

func TestAffinityDisabled(t *testing.T) {
	rt := compileTest(t, affServer(t), proxySite("round_robin", false, named(t, "a"), named(t, "b")))
	if r := fetch(t, rt, nil); r.cookie != nil {
		t.Fatalf("cookie set with affinity off: %s", r.raw)
	}
}

// TestAffinityOverridesStrategy: a valid cookie wins over every strategy,
// which only decides for new clients and after a fallback.
func TestAffinityOverridesStrategy(t *testing.T) {
	a, b := named(t, "a"), named(t, "b")
	for _, strategy := range []string{"round_robin", "least_conn", "ip_hash", "random"} {
		t.Run(strategy, func(t *testing.T) {
			rt := compileTest(t, affServer(t), proxySite(strategy, true, a, b))
			byName := map[string]*upstream{"a": rt.pool.list[0], "b": rt.pool.list[1]}
			// ip_hash sends this client to one upstream; pin it to the other.
			natural := fetch(t, rt, nil).body
			pin := "a"
			if natural == "a" {
				pin = "b"
			}
			if strategy == "least_conn" {
				// The pinned upstream is the busier one.
				byName[pin].active.Add(10)
				defer byName[pin].active.Add(-10)
			}
			ck := &http.Cookie{Name: model.DefaultAffinityCookie, Value: rt.affinity.encode(affinityCookie{
				member: byName[pin].affID, slot: noSlot, issued: time.Now().Unix(),
			})}
			for range 10 {
				if r := fetch(t, rt, ck); r.body != pin || r.cookie != nil {
					t.Fatalf("pinned to %s, answered by %s (cookie %v)", pin, r.body, r.cookie)
				}
			}
		})
	}
}

func nodeSite(instances int) *model.Site {
	site := &model.Site{ID: "n1", Name: "node", Type: model.SiteNode,
		Node: &model.NodeConfig{AppRoot: "/srv/app", Script: "server.js", Instances: instances}}
	site.Routing.Affinity.Enabled = true
	return site
}

// instances is a fake process manager's list of ready instances.
type instances struct {
	mu   sync.Mutex
	list []*procmgr.Backend
}

func (in *instances) set(t *testing.T, names ...string) {
	var list []*procmgr.Backend
	for i, n := range names {
		list = append(list, &procmgr.Backend{Addr: strings.TrimPrefix(named(t, n).URL, "http://"), Slot: i})
	}
	in.mu.Lock()
	in.list = list
	in.mu.Unlock()
}

func (in *instances) get(string) []*procmgr.Backend {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.list
}

func TestAffinityNodeInstancesSurviveRecycle(t *testing.T) {
	s := affServer(t)
	var in instances
	s.backends = in.get
	in.set(t, "i0", "i1", "i2")
	rt := compileTest(t, s, nodeSite(3))

	var ck *http.Cookie
	first := jar(t, rt, &ck)
	if ck == nil {
		t.Fatal("no cookie for a site with three instances")
	}
	for range 10 {
		if r := fetch(t, rt, ck); r.body != first {
			t.Fatalf("pinned to %s, answered by %s", first, r.body)
		}
	}
	slot := int(first[1] - '0')

	// A recycle puts a new process (new port) in the same slot, first next
	// to the old one, then alone.
	in.mu.Lock()
	next := &procmgr.Backend{Addr: strings.TrimPrefix(named(t, "new").URL, "http://"), Slot: slot}
	in.list = append(append([]*procmgr.Backend{}, in.list...), next)
	in.mu.Unlock()
	for range 5 {
		if r := fetch(t, rt, ck); r.body != first && r.body != "new" {
			t.Fatalf("during the recycle of slot %d, answered by %s", slot, r.body)
		}
	}
	in.mu.Lock()
	var after []*procmgr.Backend
	for _, b := range in.list {
		if b.Slot != slot || b == next {
			after = append(after, b)
		}
	}
	in.list = after
	in.mu.Unlock()
	for range 10 {
		r := fetch(t, rt, ck)
		if r.body != "new" {
			t.Fatalf("after recycling slot %d, answered by %s, want its replacement", slot, r.body)
		}
		if r.cookie != nil {
			t.Fatalf("cookie re-issued after a recycle: %s", r.raw)
		}
	}

	// Scaled down below the pinned slot: balanced normally, new cookie.
	in.set(t, "only0")
	if slot != 0 {
		if r := fetch(t, rt, ck); r.body != "only0" || r.cookie == nil {
			t.Fatalf("slot gone: answered by %s, new cookie %v", r.body, r.cookie)
		}
	}
}

func TestAffinitySingleInstanceNoCookie(t *testing.T) {
	s := affServer(t)
	var in instances
	s.backends = in.get
	in.set(t, "i0")
	rt := compileTest(t, s, nodeSite(1))
	if r := fetch(t, rt, nil); r.body != "i0" || r.cookie != nil {
		t.Fatalf("one instance: body %q, cookie %v", r.body, r.cookie)
	}
}

// TestAffinityLoadBalancedNode: one cookie pins both the server and, when
// that is this server, the instance.
func TestAffinityLoadBalancedNode(t *testing.T) {
	s := affServer(t)
	var in instances
	s.backends = in.get
	in.set(t, "local0", "local1")
	remote := named(t, "remote")
	site := nodeSite(2)
	site.Node.LoadBalancer = model.LoadBalancerConfig{Enabled: true, Servers: []model.Upstream{{URL: remote.URL}}}
	rt := compileTest(t, s, site)

	pins := map[string]int{}
	for range 12 {
		var ck *http.Cookie
		first := jar(t, rt, &ck)
		if ck == nil {
			t.Fatalf("no cookie (answered by %s)", first)
		}
		for range 4 {
			if r := fetch(t, rt, ck); r.body != first {
				t.Fatalf("pinned to %s, answered by %s", first, r.body)
			}
		}
		pins[first]++
	}
	if len(pins) != 3 {
		t.Fatalf("new clients were pinned to %v, want both instances and the remote server", pins)
	}

	// A request another balancer forwarded uses its own cookie name, so it
	// never overwrites the front server's cookie.
	r := fetch(t, rt, nil, func(r *http.Request) { r.Header.Set(hopHeader, "1") })
	if r.cookie == nil || r.cookie.Name != model.DefaultAffinityCookie+hopCookieSuffix {
		t.Fatalf("forwarded request got cookie %v", r.cookie)
	}
}

func TestAffinitySlidingRenewal(t *testing.T) {
	a, b := named(t, "a"), named(t, "b")
	site := proxySite("round_robin", true, a, b)
	site.Routing.Affinity.LifetimeSec = 3600
	rt := compileTest(t, affServer(t), site)
	u := rt.pool.list[0]
	fresh := rt.affinity.encode(affinityCookie{member: u.affID, slot: noSlot, issued: time.Now().Unix()})
	old := rt.affinity.encode(affinityCookie{member: u.affID, slot: noSlot, issued: time.Now().Add(-40 * time.Minute).Unix()})
	if r := fetch(t, rt, &http.Cookie{Name: model.DefaultAffinityCookie, Value: fresh}); r.body != "a" || r.cookie != nil {
		t.Fatalf("fresh cookie: body %q, re-issued %v", r.body, r.cookie)
	}
	if r := fetch(t, rt, &http.Cookie{Name: model.DefaultAffinityCookie, Value: old}); r.body != "a" || r.cookie == nil {
		t.Fatalf("cookie past half its lifetime: body %q, re-issued %v", r.body, r.cookie)
	}
}

// TestAffinityWebSocket: an upgrade request goes to the pinned backend.
func TestAffinityWebSocket(t *testing.T) {
	ws := func(name string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Upgrade") != "websocket" {
				io.WriteString(w, name)
				return
			}
			conn, brw, err := http.NewResponseController(w).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nX-Backend: %s\r\n\r\n", name)
			brw.Flush()
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	rt := compileTest(t, affServer(t), proxySite("round_robin", true, ws("a"), ws("b"), ws("c")))
	front := httptest.NewServer(rt)
	defer front.Close()

	var ck *http.Cookie
	first := jar(t, rt, &ck)
	for range 6 {
		conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "GET /socket.io/?EIO=4&transport=websocket HTTP/1.1\r\nHost: app.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nCookie: %s=%s\r\n\r\n", ck.Name, ck.Value)
		res, err := http.ReadResponse(bufio.NewReader(conn), nil)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusSwitchingProtocols || res.Header.Get("X-Backend") != first {
			t.Fatalf("upgrade: %d from %q, pinned to %s", res.StatusCode, res.Header.Get("X-Backend"), first)
		}
	}
}
