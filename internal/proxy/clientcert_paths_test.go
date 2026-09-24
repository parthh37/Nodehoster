package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestPathForms(t *testing.T) {
	for _, c := range []struct {
		path, raw         string
		literal, resolved string
		ok                bool
	}{
		{path: "/", literal: "/", resolved: "/", ok: true},
		{path: "/admin/x", literal: "/admin/x", resolved: "/admin/x", ok: true},
		{path: "//admin//x/", literal: "/admin/x", resolved: "/admin/x", ok: true},
		{path: "/ADMIN/X", literal: "/admin/x", resolved: "/admin/x", ok: true},
		{path: "/x/../admin/x", literal: "/x/../admin/x", resolved: "/admin/x", ok: true},
		{path: "/./admin", literal: "/./admin", resolved: "/admin", ok: true},
		{path: "/../../admin", literal: "/../../admin", resolved: "/admin", ok: true},
		{path: "/admin/..", literal: "/admin/..", resolved: "/", ok: true},
		{path: "/admin;jsessionid=1/x", literal: "/admin/x", resolved: "/admin/x", ok: true},
		{path: "/x/..;/admin", literal: "/x/../admin", resolved: "/admin", ok: true},
		{path: "/admin::$INDEX_ALLOCATION/x", literal: "/admin/x", resolved: "/admin/x", ok: true},
		{path: "/admin. . /x", literal: "/admin/x", resolved: "/admin/x", ok: true},
		{path: "/admin/x", raw: "/%61dmin/x", literal: "/admin/x", resolved: "/admin/x", ok: true},
		{path: "/100%-off", literal: "/100%-off", resolved: "/100%-off", ok: true},
		{path: "/admin/x", raw: "/admin%2Fx"},
		{path: "/admin/x", raw: "/admin%2fx"},
		{path: `/admin\x`, raw: "/admin%5cx"},
		{path: `/admin\x`},
		{path: "/%61dmin"}, // double-encoded
		{path: "/a\x00"},
		{path: "/.../admin"},
		{path: "/x/.. /admin"},
	} {
		lit, res, ok := pathForms(c.path, c.raw)
		if ok != c.ok || lit != c.literal || res != c.resolved {
			t.Errorf("pathForms(%q, %q) = %q, %q, %v; want %q, %q, %v", c.path, c.raw, lit, res, ok, c.literal, c.resolved, c.ok)
		}
	}
}

func TestRequirePathsMatching(t *testing.T) {
	ca := newClientCA(t, "Devices CA")
	for _, x := range []string{"/admin", "/admin/", "/Admin", "//admin//"} {
		p, err := compileClientPolicy(&model.ClientCertPolicy{Mode: "accept", CAPEM: ca.pem, RequirePaths: []string{x}})
		if err != nil {
			t.Fatal(err)
		}
		for path, want := range map[string]bool{
			"/admin": true, "/admin/": true, "/admin/x": true, "/ADMIN": true, "/x/../admin": true,
			"/admin/../x": true, // an application that does not resolve dot segments routes it to /admin
			"/":           false, "/administrator": false, "/x/admin": false, "/adm": false,
		} {
			if got, ok := p.requiredFor(path, ""); !ok || got != want {
				t.Errorf("%q: %q required = %v (ok %v), want %v", x, path, got, ok, want)
			}
		}
	}
	root, _ := compileClientPolicy(&model.ClientCertPolicy{Mode: "accept", CAPEM: ca.pem, RequirePaths: []string{"/"}})
	if got, _ := root.requiredFor("/anything", ""); !got {
		t.Error(`"/" does not cover everything`)
	}
	// An entry that does not validate (an older version's) covers
	// everything rather than nothing.
	bad, _ := compileClientPolicy(&model.ClientCertPolicy{Mode: "accept", CAPEM: ca.pem, RequirePaths: []string{`/a\b`}})
	if got, _ := bad.requiredFor("/x", ""); !got {
		t.Error("an unreadable entry leaves paths open")
	}
}

// applySites loads sites as they are on the env.
func (e *tlsEnv) applySites(sites ...*model.Site) {
	e.t.Helper()
	for _, s := range sites {
		s.ApplyDefaults()
		if err := s.Validate(); err != nil {
			e.t.Fatal(err)
		}
	}
	e.s.Reload(sites, func(*model.Site) bool { return true })
}

// plainGet requests path with Host host over plain HTTP on port.
func plainGet(t *testing.T, port int, host, path string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	req.Host = host
	c := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bypassPaths are ways to spell /admin/secret.txt that applications or
// the file system read as that path.
var bypassPaths = []string{
	"/admin", "/admin/", "/admin/secret.txt",
	"//admin/secret.txt", "/x/../admin/secret.txt", "/./admin/secret.txt", "/x/%2e%2e/admin/secret.txt",
	"/ADMIN/secret.txt", "/Admin", "/%61dmin/secret.txt", "/admin;v=1/secret.txt", "/x/..;/admin/secret.txt",
	"/admin./secret.txt", "/admin%20/secret.txt",
}

// uncheckablePaths cannot be compared safely and get 400.
var uncheckablePaths = []string{"/admin%2Fsecret.txt", "/admin%5Csecret.txt", "/%2561dmin/secret.txt", "/.../admin/secret.txt"}

// TestClientCertRequirePathsCanonical: requirePaths hold however the path
// is spelled, on a static site (whose files the file system finds by any
// spelling) and a proxy site (whose application may route any spelling).
func TestClientCertRequirePathsCanonical(t *testing.T) {
	e := newTLSEnv(t)
	ca := newClientCA(t, "Devices CA")
	cert := e.serverCert("files.example.com", "app.example.com", "slash.example.com")
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "index.html"), "home")
	writeFile(t, filepath.Join(root, "admin", "secret.txt"), "top secret")
	policy := func(paths ...string) *model.ClientCertPolicy {
		return &model.ClientCertPolicy{Mode: "accept", CAPEM: ca.pem, RequirePaths: paths}
	}
	e.applySites(
		&model.Site{ID: "files", Name: "files", Type: model.SiteStatic, Static: &model.StaticConfig{Root: root},
			Bindings: []model.Binding{e.binding("files.example.com", cert, policy("/admin"))}},
		&model.Site{ID: "app", Name: "app", Type: model.SiteProxy, Proxy: &model.ProxyConfig{Upstreams: []model.Upstream{{URL: e.up.URL}}},
			Bindings: []model.Binding{e.binding("app.example.com", cert, policy("/admin"))}},
		// Configured with a trailing slash, the path itself is covered too.
		&model.Site{ID: "slash", Name: "slash", Type: model.SiteStatic, Static: &model.StaticConfig{Root: root},
			Bindings: []model.Binding{e.binding("slash.example.com", cert, policy("/Admin/"))}},
	)

	for _, host := range []string{"files.example.com", "app.example.com", "slash.example.com"} {
		anon := e.client(host, nil)
		for _, p := range bypassPaths {
			r := e.mustGet(anon, host, p)
			if r.status != http.StatusForbidden || r.seen != nil || strings.Contains(r.body, "top secret") {
				t.Errorf("%s%s without a certificate: %d %q (application reached: %v)", host, p, r.status, r.body, r.seen != nil)
			}
		}
		for _, p := range uncheckablePaths {
			if r := e.mustGet(anon, host, p); r.status != http.StatusBadRequest || r.seen != nil {
				t.Errorf("%s%s: %d, want 400", host, p, r.status)
			}
		}
		for _, p := range []string{"/", "/administrator", "/x/admin"} {
			if r := e.mustGet(anon, host, p); r.status == http.StatusForbidden || r.status == http.StatusBadRequest {
				t.Errorf("%s%s is not under /admin: %d", host, p, r.status)
			}
		}
	}

	// With a valid certificate every spelling is served as it would be
	// without mTLS (nothing is refused as uncheckable either).
	good := e.client("files.example.com", ca.issue(t, "device-1", time.Time{}))
	for _, p := range []string{"/admin/secret.txt", "//admin/secret.txt", "/ADMIN/secret.txt"} {
		if r := e.mustGet(good, "files.example.com", p); r.status != 200 || r.body != "top secret" {
			t.Errorf("%s with a certificate: %d %q", p, r.status, r.body)
		}
	}
	if r := e.mustGet(e.client("app.example.com", ca.issue(t, "device-1", time.Time{})), "app.example.com", "/admin%2Fx"); r.status != 200 {
		t.Errorf("encoded slash with a certificate: %d", r.status)
	}
}

// TestClientCertRequirePathsAfterRewriteAndLocation: a rewrite rule or a
// location that strips its prefix cannot turn a path outside requirePaths
// into one under them.
func TestClientCertRequirePathsAfterRewriteAndLocation(t *testing.T) {
	e := newTLSEnv(t)
	ca := newClientCA(t, "Devices CA")
	cert := e.serverCert("a.example.com")
	mounted := t.TempDir()
	writeFile(t, filepath.Join(mounted, "admin", "secret.txt"), "top secret")
	site := &model.Site{ID: "a", Name: "a", Type: model.SiteProxy, Proxy: &model.ProxyConfig{Upstreams: []model.Upstream{{URL: e.up.URL}}},
		Bindings: []model.Binding{e.binding("a.example.com", cert, &model.ClientCertPolicy{Mode: "accept", CAPEM: ca.pem, RequirePaths: []string{"/admin"}})},
		Routing: model.RoutingConfig{
			Rewrites: []model.RewriteRule{{Enabled: true, IgnoreCase: true, Match: "^/portal/(.*)", Action: "rewrite", Target: "/admin/{R:1}"}},
			Locations: []model.Location{
				{Path: "/files", Kind: "static", Root: mounted, StripPrefix: true},
				{Path: "/app", Kind: "site", SiteID: "inner", StripPrefix: true},
				{Path: "/kept", Kind: "static", Root: mounted},
			},
		}}
	inner := &model.Site{ID: "inner", Name: "inner", Type: model.SiteProxy, Proxy: &model.ProxyConfig{Upstreams: []model.Upstream{{URL: e.up.URL}}}}
	e.applySites(site, inner)

	anon := e.client("a.example.com", nil)
	for _, p := range []string{"/portal/users", "/PORTAL/users", "/files/admin/secret.txt", "/files//ADMIN/secret.txt", "/app/admin", "/app/admin/x"} {
		r := e.mustGet(anon, "a.example.com", p)
		if r.status != http.StatusForbidden || r.seen != nil || strings.Contains(r.body, "top secret") {
			t.Errorf("%s without a certificate: %d %q", p, r.status, r.body)
		}
	}
	for _, p := range []string{"/app/x", "/files/", "/kept/admin/secret.txt"} {
		if r := e.mustGet(anon, "a.example.com", p); r.status == http.StatusForbidden {
			t.Errorf("%s: 403", p)
		}
	}
	good := e.client("a.example.com", ca.issue(t, "device-1", time.Time{}))
	if r := e.mustGet(good, "a.example.com", "/files/admin/secret.txt"); r.status != 200 || r.body != "top secret" {
		t.Errorf("with a certificate: %d %q", r.status, r.body)
	}
	if r := e.mustGet(good, "a.example.com", "/portal/users"); r.status != 200 || r.seen.Get(hdrClientVerify) != "SUCCESS" {
		t.Errorf("rewritten, with a certificate: %d %v", r.status, r.seen)
	}
}

// TestClientCertPlainHTTP: an http binding of the same site and host name
// does not serve what the https binding requires a certificate for.
func TestClientCertPlainHTTP(t *testing.T) {
	e := newTLSEnv(t)
	ca := newClientCA(t, "Devices CA")
	cert := e.serverCert("a.example.com")
	httpPort := freePort(t)
	site := func(mode string, redirect bool) *model.Site {
		return &model.Site{ID: "a", Name: "a", Type: model.SiteProxy, Proxy: &model.ProxyConfig{Upstreams: []model.Upstream{{URL: e.up.URL}}},
			Bindings: []model.Binding{
				e.binding("a.example.com", cert, &model.ClientCertPolicy{Mode: mode, CAPEM: ca.pem, RequirePaths: []string{"/admin"}}),
				{ID: "plain", Protocol: "http", IP: "127.0.0.1", Port: httpPort, Host: "a.example.com"},
				{ID: "other", Protocol: "http", IP: "127.0.0.1", Port: httpPort, Host: "b.example.com"},
			},
			Routing: model.RoutingConfig{HTTPSRedirect: redirect}}
	}
	e.applySites(site("accept", false))
	for p, want := range map[string]int{"/": 200, "/admin": 403, "/ADMIN/x": 403, "//admin": 403, "/admin%2Fx": 400} {
		if st, body, _ := plainGet(t, httpPort, "a.example.com", p); st != want {
			t.Errorf("accept, http %s: %d %s", p, st, body)
		} else if st == 403 && !strings.Contains(body, "HTTPS") {
			t.Errorf("accept, http %s: %s", p, body)
		}
	}
	if st, _, _ := plainGet(t, httpPort, "b.example.com", "/admin"); st != 200 {
		t.Errorf("another host name's http binding: %d", st)
	}
	for len(e.seen) > 0 {
		<-e.seen
	}
	if st, _, _ := plainGet(t, httpPort, "a.example.com", "/"); st != 200 {
		t.Fatal(st)
	}
	if h := <-e.seen; h.Get(hdrClientVerify) != "" {
		t.Errorf("plain HTTP told the application %q", h.Get(hdrClientVerify))
	}

	e.applySites(site("require", false))
	if st, _, _ := plainGet(t, httpPort, "a.example.com", "/"); st != http.StatusForbidden {
		t.Errorf("require, http /: %d", st)
	}
	if st, _, _ := plainGet(t, httpPort, "b.example.com", "/"); st != 200 {
		t.Errorf("require, another host name: %d", st)
	}
	// A site that redirects to HTTPS redirects first.
	e.applySites(site("require", true))
	if st, _, h := plainGet(t, httpPort, "a.example.com", "/admin"); st != http.StatusMovedPermanently || !strings.HasPrefix(h.Get("Location"), "https://") {
		t.Errorf("redirecting site: %d %v", st, h)
	}
}

// TestClientCertHeadersForgedSpellings: underscore spellings of the
// identity headers never reach the application, and a client cannot have
// NodeHoster's own headers dropped by naming them in Connection, whichever
// way the request is forwarded.
func TestClientCertHeadersForgedSpellings(t *testing.T) {
	e := newTLSEnv(t)
	ca := newClientCA(t, "Devices CA")
	cert := e.serverCert("a.example.com")
	e.applySites(&model.Site{ID: "a", Name: "a", Type: model.SiteProxy, Proxy: &model.ProxyConfig{Upstreams: []model.Upstream{{URL: e.up.URL}}},
		Bindings: []model.Binding{e.binding("a.example.com", cert, &model.ClientCertPolicy{Mode: "accept", CAPEM: ca.pem})},
		Routing: model.RoutingConfig{
			Rewrites:  []model.RewriteRule{{Enabled: true, Match: "^/elsewhere/(.*)", Action: "rewrite", Target: e.up.URL + "/{R:1}"}},
			Locations: []model.Location{{Path: "/loc", Kind: "url", URL: e.up.URL}},
		}})
	device := ca.issue(t, "device-1", time.Time{})
	for _, p := range []string{"/", "/elsewhere/x", "/loc/x"} {
		for _, c := range []struct {
			cert   *tls.Certificate
			verify string
		}{{nil, "NONE"}, {device, "SUCCESS"}} {
			for len(e.seen) > 0 {
				<-e.seen
			}
			req, _ := http.NewRequest(http.MethodGet, "https://127.0.0.1:"+strconv.Itoa(e.port)+p, nil)
			req.Host = "a.example.com"
			req.Header["X_Client_Verify"] = []string{"SUCCESS"}
			req.Header["x_client_cert"] = []string{"forged"}
			req.Header["X_CLIENT_CERT_SUBJECT"] = []string{"CN=admin"}
			req.Header["X-Client-Cert_Fingerprint"] = []string{"AB"}
			req.Header["Connection"] = []string{"X-Client-Verify, x_client_cert_fingerprint", "X-Client-Cert-Subject, X-Client-Cert"}
			resp, err := e.client("a.example.com", c.cert).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			var seen http.Header
			select {
			case seen = <-e.seen:
			default:
			}
			if resp.StatusCode != 200 || seen == nil {
				t.Fatalf("%s: %d", p, resp.StatusCode)
			}
			for k, v := range seen {
				if isClientCertHeader(k) && !strings.Contains(k, "-") {
					t.Errorf("%s: %s: %v reached the application", p, k, v)
				}
			}
			if seen.Get(hdrClientVerify) != c.verify {
				t.Errorf("%s: X-Client-Verify = %q, want %q", p, seen.Get(hdrClientVerify), c.verify)
			}
			if c.cert != nil && (seen.Get(hdrClientFingerprint) != fingerprintOf(device) || seen.Get(hdrClientSubject) != "CN=device-1,O=Example" || seen.Get(hdrClientCert) == "") {
				t.Errorf("%s: identity headers dropped: %v", p, seen)
			}
			if c.cert == nil && (seen.Get(hdrClientCert) != "" || seen.Get(hdrClientSubject) != "" || seen.Get(hdrClientFingerprint) != "") {
				t.Errorf("%s: forged identity: %v", p, seen)
			}
		}
	}
}

func TestStripClientCertHeaders(t *testing.T) {
	h := http.Header{
		"X_client_verify": {"SUCCESS"}, "X-Client-Cert": {"x"}, "Accept": {"*/*"},
		"Connection": {"Upgrade, X-CLIENT-VERIFY", "x_client_cert"},
	}
	stripClientCertHeaders(h)
	if len(h) != 2 || h.Get("Accept") != "*/*" || h.Get("Connection") != "Upgrade" {
		t.Fatalf("%v", h)
	}
	h = http.Header{"Connection": {"X-Client-Cert"}}
	stripClientCertHeaders(h)
	if _, ok := h["Connection"]; ok {
		t.Fatalf("%v", h)
	}
	h = http.Header{"Connection": {"keep-alive"}}
	stripClientCertHeaders(h)
	if h.Get("Connection") != "keep-alive" {
		t.Fatalf("%v", h)
	}
}

// TestClientCertRequirePathsHTTP3: the same checks over QUIC.
func TestClientCertRequirePathsHTTP3(t *testing.T) {
	e := newTLSEnv(t)
	ca := newClientCA(t, "Devices CA")
	cert := e.serverCert("a.example.com")
	e.setHTTP3(true)
	e.apply(e.binding("a.example.com", cert, &model.ClientCertPolicy{Mode: "accept", CAPEM: ca.pem, RequirePaths: []string{"/admin"}}))

	anon := e.h3Client("a.example.com", nil)
	for _, p := range []string{"/admin", "/ADMIN/x", "//admin/x", "/x/../admin", "/%61dmin"} {
		if r := e.mustGet(anon, "a.example.com", p); r.status != http.StatusForbidden || r.seen != nil {
			t.Errorf("HTTP/3 %s without a certificate: %d", p, r.status)
		}
	}
	if r := e.mustGet(anon, "a.example.com", "/admin%2Fx"); r.status != http.StatusBadRequest {
		t.Errorf("HTTP/3 encoded slash: %d", r.status)
	}
	if r := e.mustGet(anon, "a.example.com", "/"); r.status != 200 || r.seen.Get(hdrClientVerify) != "NONE" {
		t.Errorf("HTTP/3 /: %d %v", r.status, r.seen)
	}
	good := e.h3Client("a.example.com", ca.issue(t, "device-1", time.Time{}))
	if r := e.mustGet(good, "a.example.com", "/ADMIN/x"); r.status != 200 || r.seen.Get(hdrClientVerify) != "SUCCESS" {
		t.Errorf("HTTP/3 with a certificate: %d %v", r.status, r.seen)
	}
}

// TestCacheKeyPerBinding: bindings of a site on other ports (other client
// certificate policies) do not share cache entries.
func TestCacheKeyPerBinding(t *testing.T) {
	o := &origin{}
	c := newResponseCache(cacheCfg(), false, "")
	b443 := &route{binding: model.Binding{Protocol: "https", Port: 443, Host: "site.example"}}
	b8443 := &route{binding: model.Binding{Protocol: "https", Port: 8443, Host: "site.example"}}
	get := func(rt *route) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "https://site.example/page", nil)
		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, rt))
		rec := httptest.NewRecorder()
		c.handler(o).ServeHTTP(rec, r)
		return rec
	}
	expectCache(t, get(b443), "MISS", "/page #1")
	expectCache(t, get(b8443), "MISS", "/page #2")
	expectCache(t, get(b443), "HIT", "/page #1")
	expectCache(t, get(b8443), "HIT", "/page #2")
}

// TestCacheClientCertificateIsCredential: a request that presented a
// certificate, verified or not, is not answered from nor stored in the
// shared cache (unless the response is public).
func TestCacheClientCertificateIsCredential(t *testing.T) {
	c := newResponseCache(cacheCfg(), false, "")
	for hdr, want := range map[[2]string]bool{
		{hdrClientVerify, "NONE"}:                  false,
		{hdrClientVerify, "SUCCESS"}:               true,
		{hdrClientVerify, "FAILED:unknown issuer"}: true,
		{hdrClientCert, "x"}:                       true,
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set(hdr[0], hdr[1])
		if got := c.hasCredentials(r); got != want {
			t.Errorf("%s: %s = %v", hdr[0], hdr[1], got)
		}
	}
}
