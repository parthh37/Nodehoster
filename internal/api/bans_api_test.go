package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
)

func fromAddr(addr string) opt { return func(r *http.Request) { r.RemoteAddr = addr } }

func (e *env) enableBanning(mutate func(*model.IPBanSettings)) {
	e.t.Helper()
	s := e.c.Settings()
	s.IPBan.Enabled = true
	if mutate != nil {
		mutate(&s.IPBan)
	}
	if _, err := e.c.UpdateSettings(context.Background(), s); err != nil {
		e.t.Fatal(err)
	}
}

func TestBanEndpointsAuthorization(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	site := e.createSite(admin, redirectSite("s", 0))
	e.user("viewer", model.RoleViewer, false)
	e.user("operator", model.RoleOperator, false)
	e.scoped("siteop", grant(site.ID, model.RoleOperator))
	viewer, operator, siteop := session(e.login("viewer")), session(e.login("operator")), session(e.login("siteop"))

	ban := map[string]any{"address": "203.0.113.0/24", "minutes": 60, "reason": "abuse report"}
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   any
		who    []opt
		want   int
	}{
		{"viewer lists", "GET", "/api/bans", nil, viewer, http.StatusForbidden},
		{"site operator lists", "GET", "/api/bans", nil, siteop, http.StatusForbidden},
		{"operator lists", "GET", "/api/bans", nil, operator, http.StatusOK},
		{"operator bans", "POST", "/api/bans", ban, operator, http.StatusForbidden},
		{"site operator bans", "POST", "/api/bans", ban, siteop, http.StatusForbidden},
		{"admin bans", "POST", "/api/bans", ban, admin, http.StatusCreated},
		{"operator unbans", "DELETE", "/api/bans/203.0.113.7", nil, operator, http.StatusForbidden},
		{"admin unbans", "DELETE", "/api/bans/203.0.113.7", nil, admin, http.StatusNoContent},
		{"unban again", "DELETE", "/api/bans/203.0.113.7", nil, admin, http.StatusNotFound},
		{"loopback", "POST", "/api/bans", map[string]any{"address": "127.0.0.1"}, admin, http.StatusUnprocessableEntity},
		{"nonsense", "POST", "/api/bans", map[string]any{"address": "example.com"}, admin, http.StatusUnprocessableEntity},
	} {
		if rec := e.do(tc.method, tc.path, tc.body, tc.who...); rec.Code != tc.want {
			t.Errorf("%s: %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body)
		}
	}
	actions := e.auditActions()
	if !contains(actions, "root:ban.add") || !contains(actions, "root:ban.remove") {
		t.Errorf("audit = %v", actions)
	}

	// An IPv6 range: its "/" travels escaped; any address in it unbans it.
	expect(t, e.do("POST", "/api/bans", map[string]any{"address": "2001:db8:1::/48"}, admin...), http.StatusCreated)
	expect(t, e.do("POST", "/api/bans", map[string]any{"address": "2001:db8:2::/48"}, admin...), http.StatusCreated)
	expect(t, e.do("DELETE", "/api/bans/2001:db8:1::%2F48", nil, admin...), http.StatusNoContent)
	expect(t, e.do("DELETE", "/api/bans/2001:db8:2:ffff::1", nil, admin...), http.StatusNoContent)
	if list := decodeJSON[[]model.Ban](t, e.do("GET", "/api/bans", nil, admin...)); len(list) != 0 {
		t.Fatalf("bans left: %+v", list)
	}
}

// TestConsoleLoginFailuresBan: guessing web console passwords bans the
// address from the console (and every site); loopback and the Manager's
// pipe are never locked out.
func TestConsoleLoginFailuresBan(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	e.enableBanning(func(s *model.IPBanSettings) { s.AuthFailures = model.BanRule{Threshold: 3, WindowSec: 60} })
	attacker := fromAddr("203.0.113.66:4000")
	bad := map[string]string{"username": "root", "password": "wrong"}
	for i := range 3 {
		expect(t, e.do("POST", "/api/auth/login", bad, attacker), http.StatusUnauthorized)
		if i < 2 && e.c.Bans.Banned([]byte{203, 0, 113, 66}) {
			t.Fatalf("banned after %d failures", i+1)
		}
	}
	rec := e.do("POST", "/api/auth/login", map[string]string{"username": "root", "password": testPassword}, attacker)
	expect(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "banned") {
		t.Fatalf("body %s", rec.Body)
	}
	expect(t, e.do("GET", "/", nil, attacker), http.StatusForbidden) // the console itself

	// Others, and the server itself, are unaffected.
	expect(t, e.do("GET", "/api/auth/me", nil, admin...), http.StatusOK)
	for range 10 {
		e.do("POST", "/api/auth/login", bad, fromAddr("127.0.0.1:5000"))
	}
	expect(t, e.do("GET", "/api/bans", nil, append(admin, fromAddr("127.0.0.1:5000"))...), http.StatusOK)

	list := decodeJSON[[]model.Ban](t, e.do("GET", "/api/bans", nil, admin...))
	if len(list) != 1 || list[0].Address != "203.0.113.66" || !strings.Contains(list[0].Reason, "authentication failures") {
		t.Fatalf("bans = %+v", list)
	}
	evs, err := e.c.Store.ListEvents(context.Background(), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Type == events.SecurityBanned && ev.Level == "warning" && strings.Contains(ev.Message, "203.0.113.66") {
			found = true
		}
	}
	if !found {
		t.Errorf("no security.banned event in %+v", evs)
	}

	// The Manager's pipe is not guarded by bans, and can lift them.
	local := LocalHandler(e.c)
	req := httptest.NewRequest("DELETE", "/api/bans/203.0.113.66", nil)
	req.RemoteAddr = "203.0.113.66:1"
	req = req.WithContext(WithLocalUser(req.Context(), `WEB01\Administrator`))
	lrec := httptest.NewRecorder()
	local.ServeHTTP(lrec, req)
	expect(t, lrec, http.StatusNoContent)
	expect(t, e.do("POST", "/api/auth/login", map[string]string{"username": "root", "password": testPassword}, attacker), http.StatusOK)
}

// TestBansAreSaved: bans are kept in the database, for restarts.
func TestBansAreSaved(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	expect(t, e.do("POST", "/api/bans", map[string]any{"address": "198.51.100.9", "minutes": 0}, admin...), http.StatusCreated)
	var saved []model.Ban
	if err := e.c.Store.GetDoc(context.Background(), "ipban.bans", &saved); err != nil || len(saved) != 1 || saved[0].ExpiresAt != nil {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
}

// TestBanEnforcementInProxy drives real site listeners behind a trusted
// proxy (loopback, whose X-Forwarded-For is believed): bans hit the client
// address it reports, every site refuses a banned client except those
// that opted out, and only the answers the rules describe count.
func TestBanEnforcementInProxy(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	s := e.c.Settings()
	s.Proxy.TrustedProxies = []string{"127.0.0.1"}
	s.IPBan.Enabled = true
	s.IPBan.NotFound = model.BanRule{Threshold: 3, WindowSec: 60}
	s.IPBan.AuthFailures = model.BanRule{Threshold: 3, WindowSec: 60}
	if _, err := e.c.UpdateSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/missing") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer up.Close()
	mk := func(name string, routing map[string]any) int {
		port := freePort(t)
		e.createSite(admin, map[string]any{
			"name": name, "type": "proxy", "autoStart": true,
			"bindings": []map[string]any{{"protocol": "http", "ip": "127.0.0.1", "port": port}},
			"proxy":    map[string]any{"upstreams": []map[string]any{{"url": up.URL}}},
			"routing":  routing,
		})
		return port
	}
	normal := mk("normal", map[string]any{})
	exempt := mk("exempt", map[string]any{"banning": map[string]any{"exempt": true}})
	wordpress := mk("wordpress", map[string]any{"banning": map[string]any{"allowTrapPaths": true}})
	private := mk("private", map[string]any{"basicAuth": map[string]any{"enabled": true, "users": []map[string]any{{"username": "u", "password": "right"}}}})

	client := &http.Client{Timeout: 5 * time.Second}
	get := func(port int, path, from string, hdr ...string) int {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
		if from != "" {
			req.Header.Set("X-Forwarded-For", from)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		var res *http.Response
		var err error
		for range 50 { // listeners open asynchronously
			if res, err = client.Do(req); err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	// banned asks until the client is refused (or 5s pass) and returns the
	// last answer: the proxy counts an answer after sending it, so the
	// next request, on a new connection, can arrive before the count does.
	banned := func(port int, from string, hdr ...string) int {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			got := get(port, "/", from, hdr...)
			if got == http.StatusForbidden || time.Now().After(deadline) {
				return got
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// A trap path bans at once, on every site but the exempt one.
	if got := get(normal, "/wp-login.php", "203.0.113.5"); got != http.StatusForbidden {
		t.Fatalf("trap path: %d", got)
	}
	for port, want := range map[int]int{normal: 403, wordpress: 403, exempt: 200} {
		if got := get(port, "/", "203.0.113.5"); got != want {
			t.Errorf("banned client on port %d: %d, want %d", port, got, want)
		}
	}
	// The trusted proxy itself is never banned, nor is a site's own trap
	// path opt-out a ban.
	if got := get(normal, "/", ""); got != http.StatusOK {
		t.Fatalf("the proxy's own requests: %d", got)
	}
	if got := get(wordpress, "/wp-login.php", "203.0.113.6"); got != http.StatusOK || get(normal, "/", "203.0.113.6") != http.StatusOK {
		t.Fatal("a trap path on a site that serves it banned the client")
	}

	// 404s count, but not on the exempt site.
	for range 5 {
		get(exempt, "/missing", "203.0.113.8")
	}
	if got := get(normal, "/", "203.0.113.8"); got != http.StatusOK {
		t.Fatalf("404s on an exempt site counted: %d", got)
	}
	for range 3 {
		get(normal, "/missing", "203.0.113.7")
	}
	if got := banned(normal, "203.0.113.7"); got != http.StatusForbidden {
		t.Fatalf("after 3 404s: %d", got)
	}

	// Being asked to sign in is not a failure; wrong credentials are.
	for range 5 {
		if got := get(private, "/", "203.0.113.9"); got != http.StatusUnauthorized {
			t.Fatalf("challenge: %d", got)
		}
	}
	if got := get(private, "/", "203.0.113.9", "Authorization", "Basic dTpyaWdodA=="); got != http.StatusOK {
		t.Fatalf("right password after challenges: %d", got)
	}
	for range 3 {
		get(private, "/", "203.0.113.9", "Authorization", "Basic dTp3cm9uZw==")
	}
	if got := banned(private, "203.0.113.9", "Authorization", "Basic dTpyaWdodA=="); got != http.StatusForbidden {
		t.Fatalf("after 3 wrong passwords: %d", got)
	}

	list := decodeJSON[[]model.Ban](t, e.do("GET", "/api/bans", nil, admin...))
	got := map[string]bool{}
	for _, b := range list {
		got[b.Address] = true
	}
	if len(list) != 3 || !got["203.0.113.5"] || !got["203.0.113.7"] || !got["203.0.113.9"] {
		t.Fatalf("bans = %+v", list)
	}
}
