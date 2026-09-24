package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/remote"
	"github.com/parthh37/nodehoster/internal/secrets"
)

// remoteServer is another NodeHoster server, its web console served over
// TLS (a self-signed certificate, as admin listeners often have), with an
// administrator "hub" whose token the connections use.
type remoteServer struct {
	*env
	srv   *httptest.Server
	hubID string
	token string
}

func newRemote(t *testing.T) *remoteServer {
	t.Helper()
	e := newEnv(t)
	srv := httptest.NewTLSServer(e.h)
	t.Cleanup(srv.Close)
	hub := e.user("hub", model.RoleAdmin, false)
	return &remoteServer{env: e, srv: srv, hubID: hub.ID, token: e.token(hub)}
}

func (r *remoteServer) fingerprint() string { return remote.Fingerprint(r.srv.Certificate().Raw) }

// connect adds a connection as an administrator and returns its ID.
func (e *env) connect(admin []opt, body map[string]any) string {
	e.t.Helper()
	rec := e.do("POST", "/api/servers", body, admin...)
	expect(e.t, rec, http.StatusCreated)
	return decodeJSON[model.ServerView](e.t, rec).ID
}

func TestServerConnectionEndpoints(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	site := e.createSite(admin, redirectSite("s", 0))
	e.user("viewer", model.RoleViewer, false)
	e.user("operator", model.RoleOperator, false)
	e.scoped("siteop", grant(site.ID, model.RoleOperator))
	viewer, operator, siteop := session(e.login("viewer")), session(e.login("operator")), session(e.login("siteop"))

	body := map[string]any{"name": "web02", "url": "https://web02.example:8484", "token": "nh_remote_secret", "minRole": "operator"}
	for _, who := range [][]opt{viewer, operator, siteop} {
		expect(t, e.do("POST", "/api/servers", body, who...), http.StatusForbidden)
	}
	rec := e.do("POST", "/api/servers", body, admin...)
	expect(t, rec, http.StatusCreated)
	v := decodeJSON[model.ServerView](t, rec)
	if v.Token != secrets.Mask || strings.Contains(rec.Body.String(), "nh_remote_secret") || v.MinRole != model.RoleOperator {
		t.Fatalf("created %s", rec.Body)
	}
	adminOnly := e.connect(admin, map[string]any{"name": "web03", "url": "https://web03.example", "token": "t2"})

	// Each sees the connections their role may use; site-scoped callers none.
	for _, tc := range []struct {
		who  []opt
		want int
	}{{admin, 2}, {operator, 1}, {viewer, 0}} {
		list := decodeJSON[[]model.ServerView](t, e.do("GET", "/api/servers", nil, tc.who...))
		if len(list) != tc.want {
			t.Errorf("sees %d connections, want %d", len(list), tc.want)
		}
		for _, s := range list {
			if s.Token != secrets.Mask {
				t.Errorf("token %q", s.Token)
			}
		}
	}
	expect(t, e.do("GET", "/api/servers", nil, siteop...), http.StatusForbidden)
	expect(t, e.do("GET", "/api/servers/"+adminOnly, nil, operator...), http.StatusNotFound)
	expect(t, e.do("GET", "/api/servers/"+v.ID, nil, operator...), http.StatusOK)
	expect(t, e.do("GET", "/api/servers/"+adminOnly+"/proxy/sites", nil, operator...), http.StatusNotFound)
	expect(t, e.do("GET", "/api/servers/"+v.ID+"/proxy/sites", nil, siteop...), http.StatusForbidden)

	// Updating keeps the masked token; the test endpoint is for admins.
	v.Name = "Web 02"
	expect(t, e.do("PUT", "/api/servers/"+v.ID, v.ServerConnection, operator...), http.StatusForbidden)
	rec = e.do("PUT", "/api/servers/"+v.ID, v.ServerConnection, admin...)
	expect(t, rec, http.StatusOK)
	if got := decodeJSON[model.ServerView](t, rec); got.Name != "Web 02" || got.Token != secrets.Mask {
		t.Fatalf("updated %+v", got)
	}
	v.URL = "https://elsewhere.example"
	rec = e.do("PUT", "/api/servers/"+v.ID, v.ServerConnection, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if !strings.Contains(rec.Body.String(), `"field":"token"`) {
		t.Fatalf("masked token to another host: %s", rec.Body)
	}
	expect(t, e.do("POST", "/api/servers/test", map[string]any{"url": "https://x", "token": "t"}, operator...), http.StatusForbidden)
	expect(t, e.do("POST", "/api/servers", map[string]any{"name": "x", "url": "http://web04:8484", "token": "t"}, admin...), http.StatusUnprocessableEntity)

	expect(t, e.do("DELETE", "/api/servers/"+adminOnly, nil, operator...), http.StatusForbidden)
	expect(t, e.do("DELETE", "/api/servers/"+adminOnly, nil, admin...), http.StatusNoContent)
	expect(t, e.do("DELETE", "/api/servers/"+adminOnly, nil, admin...), http.StatusNotFound)
	actions := e.auditActions()
	for _, a := range []string{"root:server.add", "root:server.update", "root:server.remove"} {
		if !contains(actions, a) {
			t.Errorf("no %s in %v", a, actions)
		}
	}

	// Backups carry the connection with its token sealed.
	rec = e.do("GET", "/api/backup", nil, admin...)
	expect(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"name": "Web 02"`) || strings.Contains(rec.Body.String(), "nh_remote_secret") {
		t.Fatalf("backup: %s", rec.Body)
	}
}

// TestServerProxy drives a real remote server through a connection: the
// remote token's rights, capped at the local user's role.
func TestServerProxy(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	r := newRemote(t)
	remoteAdmin := []opt{withBearer(r.token)}
	rsite := r.createSite(remoteAdmin, redirectSite("remote-site", 0))

	// Testing shows the self-signed certificate; pinned, the token works.
	res := decodeJSON[model.ServerTestResult](t, e.do("POST", "/api/servers/test", map[string]any{"url": r.srv.URL, "token": r.token}, admin...))
	if res.Trusted || res.Certificate == nil || res.Certificate.Fingerprint != r.fingerprint() || res.Health.Reachable {
		t.Fatalf("unpinned test: %+v", res)
	}
	res = decodeJSON[model.ServerTestResult](t, e.do("POST", "/api/servers/test",
		map[string]any{"url": r.srv.URL, "token": r.token, "fingerprint": model.FormatFingerprint(r.fingerprint())}, admin...))
	if !res.Trusted || !res.Health.Reachable || res.Health.User != "hub" || res.Health.Sites != 1 {
		t.Fatalf("pinned test: %+v", res)
	}

	id := e.connect(admin, map[string]any{"name": "web02", "url": r.srv.URL, "token": r.token, "fingerprint": r.fingerprint(), "minRole": "viewer"})
	p := "/api/servers/" + id + "/proxy"
	e.user("viewer", model.RoleViewer, false)
	e.user("operator", model.RoleOperator, false)
	viewer, operator := session(e.login("viewer")), session(e.login("operator"))

	list := decodeJSON[[]siteResp](t, e.do("GET", p+"/sites", nil, operator...))
	if len(list) != 1 || list[0].ID != rsite.ID {
		t.Fatalf("remote sites: %+v", list)
	}
	// The health check reads the same server.
	rec := e.do("POST", "/api/servers/"+id+"/check", nil, viewer...)
	expect(t, rec, http.StatusOK)
	if h := decodeJSON[model.ServerView](t, rec).Health; !h.Reachable || h.Sites != 1 || h.Version == "" || !h.RoleLimits {
		t.Fatalf("health %+v", h)
	}

	// Viewers read only; operators operate; neither administers, whatever
	// the token could do.
	expect(t, e.do("GET", p+"/sites/"+rsite.ID, nil, viewer...), http.StatusOK)
	expect(t, e.do("POST", p+"/sites/"+rsite.ID+"/start", nil, viewer...), http.StatusForbidden)
	expect(t, e.do("POST", p+"/sites/"+rsite.ID+"/start", nil, operator...), http.StatusOK)
	expect(t, e.do("GET", p+"/settings", nil, operator...), http.StatusForbidden)
	expect(t, e.do("GET", p+"/settings", nil, admin...), http.StatusOK)
	me := decodeJSON[struct {
		User   model.User `json:"user"`
		Access struct {
			Role model.Role `json:"role"`
		} `json:"access"`
	}](t, e.do("GET", p+"/auth/me", nil, operator...))
	if me.User.Username != "hub" || me.Access.Role != model.RoleOperator {
		t.Fatalf("remote identity for a local operator: %+v", me)
	}
	// The cookie session's CSRF rule still applies here.
	expect(t, e.do("POST", p+"/sites/"+rsite.ID+"/stop", nil, withCookie(e.login("operator"))), http.StatusForbidden)

	// The account behind the token is out of reach.
	expect(t, e.do("POST", p+"/tokens", map[string]any{"name": "x"}, admin...), http.StatusForbidden)
	expect(t, e.do("GET", p+"/tokens", nil, admin...), http.StatusForbidden)
	expect(t, e.do("POST", p+"/auth/password", map[string]any{}, admin...), http.StatusForbidden)
	expect(t, e.do("POST", p+"/auth%2Flogout", nil, admin...), http.StatusForbidden)

	// Changes are audited here (who, which server, what) and there (the
	// token's user).
	if actions := e.auditActions(); !contains(actions, "operator:server.proxy") {
		t.Errorf("local audit %v", actions)
	}
	local, _ := e.c.Store.ListAudit(context.Background(), 100, 0)
	found := false
	for _, a := range local {
		if a.Action == "server.proxy" && a.Target == "web02" && a.Detail == "POST /api/sites/"+rsite.ID+"/start (200)" {
			found = true
		}
	}
	if !found {
		t.Errorf("no detailed proxy entry in %+v", local)
	}
	if actions := r.auditActions(); !contains(actions, "hub:site.start") {
		t.Errorf("remote audit %v", actions)
	}

	// A revoked token: a 502 of this server, not a 401 that would end the
	// caller's own session.
	toks, _ := r.c.Store.ListTokens(context.Background(), r.hubID)
	for _, tk := range toks {
		r.c.Store.DeleteToken(context.Background(), tk.UserID, tk.ID)
	}
	rec = e.do("GET", p+"/sites", nil, admin...)
	expect(t, rec, http.StatusBadGateway)
	if !strings.Contains(rec.Body.String(), "refused the connection's API token") {
		t.Fatalf("revoked token: %s", rec.Body)
	}
}

// capture is a fake remote console recording what reaches it.
type capture struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []byte
}

func (c *capture) last() (*http.Request, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reqs) == 0 {
		return nil, nil
	}
	return c.reqs[len(c.reqs)-1], c.body
}

func TestServerProxyFiltering(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	cp := &capture{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		cp.mu.Lock()
		cp.reqs, cp.body = append(cp.reqs, r), data
		cp.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/unauthorized"):
			w.WriteHeader(http.StatusUnauthorized)
		case strings.HasSuffix(r.URL.Path, "/api/redirect"):
			http.Redirect(w, r, "https://elsewhere.example/", http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "/api/newer-feature"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"no such endpoint"}`))
		default:
			http.SetCookie(w, &http.Cookie{Name: "remote", Value: "x"})
			w.Header().Set("X-Remote-Internal", "1")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", `attachment; filename="a.log"`)
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer fake.Close()
	// Plain HTTP is accepted to this computer only; a path prefix is kept.
	id := e.connect(admin, map[string]any{"name": "fake", "url": fake.URL + "/console/", "token": "nh_fake"})
	p := "/api/servers/" + id + "/proxy"

	ck := e.login("root")
	rec := e.do("POST", p+"/bans/2001:db8::%2F48?x=1&y=2", map[string]any{"a": 1}, withCookie(ck), withCSRF(),
		withHeader("X-Forwarded-For", "10.0.0.1"), withHeader("Accept", "application/json"), withHeader("X-Backup-Passphrase", "pp"))
	expect(t, rec, http.StatusOK)
	got, body := cp.last()
	if got.URL.EscapedPath() != "/console/api/bans/2001:db8::%2F48" || got.URL.RawQuery != "x=1&y=2" || string(body) != `{"a":1}` {
		t.Fatalf("forwarded %s ? %s: %s", got.URL.EscapedPath(), got.URL.RawQuery, body)
	}
	if got.Header.Get("Authorization") != "Bearer nh_fake" || got.Header.Get(model.RoleLimitHeader) != "admin" ||
		got.Header.Get("Cookie") != "" || got.Header.Get("X-Requested-With") != "" || got.Header.Get("X-Forwarded-For") != "" ||
		got.Header.Get("Accept") != "application/json" || got.Header.Get("X-Backup-Passphrase") != "pp" || got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("forwarded headers %v", got.Header)
	}
	if rec.Header().Get("Set-Cookie") != "" || rec.Header().Get("X-Remote-Internal") != "" ||
		rec.Header().Get("Content-Disposition") == "" || rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("returned headers %v", rec.Header())
	}

	// Answers that are this server's business, and one that is not.
	expect(t, e.do("GET", p+"/unauthorized", nil, admin...), http.StatusBadGateway)
	rec = e.do("GET", p+"/redirect", nil, admin...)
	expect(t, rec, http.StatusBadGateway)
	if !strings.Contains(rec.Body.String(), "redirect") {
		t.Fatalf("redirect: %s", rec.Body)
	}
	rec = e.do("GET", p+"/newer-feature", nil, admin...)
	expect(t, rec, http.StatusNotFound) // an older remote: the console explains
	if !strings.Contains(rec.Body.String(), "no such endpoint") {
		t.Fatalf("404: %s", rec.Body)
	}

	// Nothing outside the remote API.
	n := len(cp.reqs)
	for _, bad := range []string{p + "/", p + "/../x", p + "/sites/%2e%2e/%2e%2e/x", p + "/a%2F..%2F..%2Fx", p + "/a%2F..%2Fb", p + "/a//b", p + "/a%5Cb", p + "/a%00b"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.URL = &url.URL{Path: mustUnescape(t, bad), RawPath: bad}
		for _, o := range admin {
			o(req)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if len(cp.reqs) != n {
		t.Fatalf("a path outside the API reached the server: %s", cp.reqs[len(cp.reqs)-1].URL)
	}

	// No connection upgrades (the API has no WebSockets).
	expect(t, e.do("GET", p+"/stream", nil, append(admin, withHeader("Connection", "Upgrade"), withHeader("Upgrade", "websocket"))...), http.StatusBadRequest)

	// An upload streams through.
	zip := []byte(strings.Repeat("PK", 50_000))
	rec = e.upload(p+"/sites/x/deploy/zip", "file", "app.zip", zip, admin...)
	expect(t, rec, http.StatusOK)
	got, body = cp.last()
	if !strings.HasPrefix(got.Header.Get("Content-Type"), "multipart/form-data") || len(body) < len(zip) {
		t.Fatalf("upload: %s, %d bytes", got.Header.Get("Content-Type"), len(body))
	}

	// A server that is gone.
	fake.Close()
	rec = e.do("GET", p+"/sites", nil, admin...)
	expect(t, rec, http.StatusBadGateway)
	if !strings.Contains(rec.Body.String(), "cannot reach fake") {
		t.Fatalf("gone: %s", rec.Body)
	}
}

func mustUnescape(t *testing.T, s string) string {
	t.Helper()
	u, err := url.PathUnescape(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestServerProxyStream follows a remote server's live status through the
// proxy, over real connections, and ends it when the connection goes.
func TestServerProxyStream(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	r := newRemote(t)
	id := e.connect(admin, map[string]any{"name": "web02", "url": r.srv.URL, "token": r.token, "fingerprint": r.fingerprint()})
	hub := httptest.NewServer(e.h)
	defer hub.Close()

	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(hub.URL)
	jar.SetCookies(u, []*http.Cookie{e.login("root")})
	client := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	resp, err := client.Get(hub.URL + "/api/servers/" + id + "/proxy/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %s %v", resp.Status, resp.Header)
	}
	sc := bufio.NewScanner(resp.Body)
	sawStatus := false
	for sc.Scan() {
		if sc.Text() == "event: status" {
			sawStatus = true
			break
		}
	}
	if !sawStatus {
		t.Fatalf("no status event: %v", sc.Err())
	}

	// Removing the connection ends the stream within a few seconds.
	expect(t, e.do("DELETE", "/api/servers/"+id, nil, admin...), http.StatusNoContent)
	done := make(chan struct{})
	go func() {
		for sc.Scan() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream outlived its connection")
	}
}

// TestRoleLimitHeader: any client may narrow its own access, never widen it.
func TestRoleLimitHeader(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.user("root", model.RoleAdmin, false)
	tok := e.token(e.user("op", model.RoleOperator, false))
	adminTok := e.token(e.user("adm", model.RoleAdmin, false))
	expect(t, e.do("GET", "/api/settings", nil, withBearer(adminTok)), http.StatusOK)
	expect(t, e.do("GET", "/api/settings", nil, withBearer(adminTok), withHeader(model.RoleLimitHeader, "operator")), http.StatusForbidden)
	expect(t, e.do("GET", "/api/server/metrics", nil, withBearer(adminTok), withHeader(model.RoleLimitHeader, "viewer")), http.StatusOK)
	expect(t, e.do("GET", "/api/settings", nil, withBearer(tok), withHeader(model.RoleLimitHeader, "admin")), http.StatusForbidden)
	expect(t, e.do("GET", "/api/sites", nil, withBearer(tok), withHeader(model.RoleLimitHeader, "root")), http.StatusBadRequest)

	// A limited request says the limit was applied (the proxy relays a
	// non-administrator's request only then); others say nothing.
	rec := e.do("GET", "/api/sites", nil, withBearer(adminTok), withHeader(model.RoleLimitHeader, "viewer"))
	expect(t, rec, http.StatusOK)
	if got := rec.Header().Get(model.RoleLimitAppliedHeader); got != "viewer" {
		t.Errorf("applied limit %q, want viewer", got)
	}
	if got := e.do("GET", "/api/sites", nil, withBearer(adminTok)).Header().Get(model.RoleLimitAppliedHeader); got != "" {
		t.Errorf("applied limit %q without one asked", got)
	}
}

// oldServer is a fake remote console that answers like a NodeHoster server
// with the token's full (administrator) rights; echo decides whether it
// says it applied the role limit: never (a server older than the limit),
// always, or only to the health check (one that stopped applying it
// since).
type oldServer struct {
	*httptest.Server
	mu    sync.Mutex
	echo  string // "never", "always", "check"
	paths []string
}

func newOldServer(t *testing.T, echo string) *oldServer {
	t.Helper()
	o := &oldServer{echo: echo}
	o.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.paths = append(o.paths, r.Method+" "+r.URL.Path)
		echo := o.echo
		o.mu.Unlock()
		if lim := r.Header.Get(model.RoleLimitHeader); lim != "" && (echo == "always" || echo == "check" && r.URL.Path == "/api/auth/me") {
			w.Header().Set(model.RoleLimitAppliedHeader, lim)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/server/info":
			w.Write([]byte(`{"version":"1.0.0","hostname":"OLD"}`))
		case "/api/auth/me":
			w.Write([]byte(`{"user":{"username":"hub","role":"admin"},"access":{"role":"admin"}}`))
		case "/api/sites":
			w.Write([]byte(`[]`))
		case "/api/settings":
			w.Write([]byte(`{"secret":"admin-only"}`))
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(o.Close)
	return o
}

func (o *oldServer) setEcho(echo string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.echo = echo
}

// reached reports whether a request for method and path reached the server.
func (o *oldServer) reached(method, path string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Contains(o.paths, method+" "+path)
}

// TestServerProxyOldServer: a remote server that ignores the role limit
// would give the connection token's full rights to anyone, so only
// administrators may use it.
func TestServerProxyOldServer(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	e.user("viewer", model.RoleViewer, false)
	e.user("operator", model.RoleOperator, false)
	viewer, operator := session(e.login("viewer")), session(e.login("operator"))

	old := newOldServer(t, "never")
	id := e.connect(admin, map[string]any{"name": "old", "url": old.URL, "token": "nh_old", "minRole": "viewer"})
	p := "/api/servers/" + id + "/proxy"

	// Not checked yet: the proxy checks it first, and refuses.
	rec := e.do("GET", p+"/settings", nil, viewer...)
	expect(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "too old for role limits") {
		t.Fatalf("viewer on an old server: %s", rec.Body)
	}
	expect(t, e.do("GET", p+"/backup", nil, viewer...), http.StatusForbidden)
	expect(t, e.do("PUT", p+"/users/x", map[string]any{"role": "admin"}, operator...), http.StatusForbidden)
	expect(t, e.do("POST", p+"/restore", "{}", operator...), http.StatusForbidden)
	for _, r := range []string{"GET /api/settings", "GET /api/backup", "PUT /api/users/x", "POST /api/restore"} {
		method, path, _ := strings.Cut(r, " ")
		if old.reached(method, path) {
			t.Errorf("%s of a non-administrator reached the old server", r)
		}
	}
	// The health says so, for the console to explain.
	v := decodeJSON[model.ServerView](t, e.do("GET", "/api/servers/"+id, nil, viewer...))
	if !v.Health.Reachable || v.Health.User != "hub" || v.Health.RoleLimits {
		t.Fatalf("health of an old server: %+v", v.Health)
	}
	// Administrators, whom the token's rights are meant for, still use it.
	rec = e.do("GET", p+"/settings", nil, admin...)
	expect(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "admin-only") {
		t.Fatalf("admin: %s", rec.Body)
	}
	expect(t, e.do("PUT", p+"/users/x", map[string]any{"role": "admin"}, admin...), http.StatusOK)

	// A server that applies the limit: its users get through, capped there.
	cur := newOldServer(t, "always")
	cid := e.connect(admin, map[string]any{"name": "current", "url": cur.URL, "token": "nh_cur", "minRole": "viewer"})
	expect(t, e.do("GET", "/api/servers/"+cid+"/proxy/sites", nil, viewer...), http.StatusOK)
	expect(t, e.do("POST", "/api/servers/"+cid+"/proxy/sites/x/start", nil, operator...), http.StatusOK)
	if v := decodeJSON[model.ServerView](t, e.do("GET", "/api/servers/"+cid, nil, viewer...)); !v.Health.RoleLimits {
		t.Fatalf("health of a current server: %+v", v.Health)
	}

	// One that stopped applying it since its last check: the answer does
	// not say the limit was applied, and is not relayed.
	cur.setEcho("check")
	rec = e.do("GET", "/api/servers/"+cid+"/proxy/settings", nil, viewer...)
	expect(t, rec, http.StatusBadGateway)
	if strings.Contains(rec.Body.String(), "admin-only") || !strings.Contains(rec.Body.String(), "too old for role limits") {
		t.Fatalf("unlimited answer relayed: %s", rec.Body)
	}
	expect(t, e.do("GET", "/api/servers/"+cid+"/proxy/settings", nil, admin...), http.StatusOK)
}
