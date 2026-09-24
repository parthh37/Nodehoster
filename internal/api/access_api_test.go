package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/auth"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// scoped stores a site-scoped user with the given grants.
func (e *env) scoped(name string, grants ...model.SiteGrant) *store.UserRecord {
	e.t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		e.t.Fatal(err)
	}
	u := &store.UserRecord{User: model.User{ID: uuid.NewString(), Username: name, Role: model.RoleSites, Sites: grants, PasswordHash: string(hash), CreatedAt: time.Now()}}
	if err := e.c.Store.PutUser(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
	return u
}

func grant(siteID string, role model.Role) model.SiteGrant {
	return model.SiteGrant{SiteID: siteID, Role: role}
}

// threeSites creates three sites, site-a, site-b and site-c.
func threeSites(e *env, admin []opt) (a, b, c siteResp) {
	return e.createSite(admin, redirectSite("site-a", 0)), e.createSite(admin, redirectSite("site-b", 0)), e.createSite(admin, redirectSite("site-c", 0))
}

func siteNames(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var names []string
	for _, s := range decodeJSON[[]siteResp](t, rec) {
		names = append(names, s.Name)
	}
	slices.Sort(names)
	return names
}

func TestSiteScopedUserSeesAndDoesOnlyWhatIsGranted(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a, b, c := threeSites(e, admin)
	u := e.scoped("agency", grant(a.ID, model.RoleOperator), grant(b.ID, model.RoleViewer))

	for _, via := range []string{"session", "token"} {
		opts := session(e.login("agency"))
		if via == "token" {
			opts = []opt{withBearer(e.token(u))}
		}

		rec := e.do(http.MethodGet, "/api/sites", nil, opts...)
		expect(t, rec, http.StatusOK)
		if got := siteNames(t, rec); !slices.Equal(got, []string{"site-a", "site-b"}) {
			t.Errorf("%s: sites = %v", via, got)
		}

		for _, tc := range []struct {
			method, path string
			want         int
		}{
			{"GET", "/api/sites/" + a.ID, 200},
			{"GET", "/api/sites/" + b.ID, 200},
			{"GET", "/api/sites/" + b.ID + "/status", 200},
			{"GET", "/api/sites/" + b.ID + "/logs", 200},
			{"GET", "/api/sites/" + b.ID + "/deployments", 200},
			{"GET", "/api/sites/" + b.ID + "/metrics", 200},
			// Operator actions: allowed on a, refused on b (visible), and c
			// does not exist as far as this user knows.
			{"POST", "/api/sites/" + a.ID + "/stop", 200},
			{"POST", "/api/sites/" + a.ID + "/restart", 200},
			{"POST", "/api/sites/" + b.ID + "/stop", 403},
			{"POST", "/api/sites/" + b.ID + "/logs/clear", 403},
			{"POST", "/api/sites/" + b.ID + "/deploy/git", 403},
			// Configuration is a server administrator's, even on a granted site.
			{"PUT", "/api/sites/" + a.ID, 403},
			{"DELETE", "/api/sites/" + a.ID, 403},
			{"GET", "/api/sites/" + c.ID, 404},
			{"GET", "/api/sites/" + c.ID + "/status", 404},
			{"GET", "/api/sites/" + c.ID + "/logs", 404},
			{"GET", "/api/sites/" + c.ID + "/logs/download", 404},
			{"GET", "/api/sites/" + c.ID + "/deployments", 404},
			{"GET", "/api/sites/" + c.ID + "/deployments/d/log", 404},
			{"POST", "/api/sites/" + c.ID + "/start", 404},
			{"POST", "/api/sites/" + c.ID + "/deploy/git", 404},
			{"PUT", "/api/sites/" + c.ID, 404},
			{"DELETE", "/api/sites/" + c.ID, 404},
			{"GET", "/api/sites/does-not-exist", 404},
		} {
			rec := e.do(tc.method, tc.path, nil, opts...)
			if rec.Code != tc.want {
				t.Errorf("%s: %s %s = %d, want %d (%s)", via, tc.method, tc.path, rec.Code, tc.want, strings.TrimSpace(rec.Body.String()))
			}
		}
	}
	if len(e.c.Sites()) != 3 {
		t.Error("a site-scoped user changed the set of sites")
	}
	if !contains(e.auditActions(), "agency:site.stop") {
		t.Errorf("site action not audited: %v", e.auditActions())
	}
}

func TestSiteScopedUserIsRefusedServerWideEndpoints(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a := e.createSite(admin, redirectSite("site-a", 0))
	// Even the strongest grant does not reach the server.
	u := e.scoped("agency", grant(a.ID, model.RoleOperator))
	for _, opts := range [][]opt{session(e.login("agency")), {withBearer(e.token(u))}} {
		for _, ep := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/api/server/metrics", nil},
			{"GET", "/api/certificates", nil},
			{"GET", "/api/certificates/x", nil},
			{"POST", "/api/certificates/x/renew", nil},
			{"POST", "/api/certificates/selfsigned", map[string]any{}},
			{"GET", "/api/node/available", nil},
			{"POST", "/api/node/versions", map[string]any{"version": "22"}},
			{"GET", "/api/mail/status", nil},
			{"GET", "/api/mail/queue", nil},
			{"GET", "/api/mail/health", nil},
			{"POST", "/api/mail/queue/retry", nil},
			{"POST", "/api/sites", redirectSite("sneaky", 0)},
			{"GET", "/api/settings", nil},
			{"PUT", "/api/settings", map[string]any{}},
			{"GET", "/api/settings/admin", nil},
			{"POST", "/api/rewrite/import", map[string]any{}},
			{"GET", "/api/users", nil},
			{"POST", "/api/users", map[string]any{}},
			{"GET", "/api/audit", nil},
			{"GET", "/api/backup", nil},
			{"POST", "/api/restore", nil},
		} {
			if rec := e.do(ep.method, ep.path, ep.body, opts...); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s = %d, want 403", ep.method, ep.path, rec.Code)
			}
		}
		// Catalogs the site pages need are harmless.
		for _, path := range []string{"/api/node/versions", "/api/mime/defaults", "/api/settings/dns-catalog"} {
			expect(t, e.do(http.MethodGet, path, nil, opts...), http.StatusOK)
		}
		// Server information is cut down to what the console's frame shows.
		rec := e.do(http.MethodGet, "/api/server/info", nil, opts...)
		expect(t, rec, http.StatusOK)
		info := decodeJSON[model.ServerInfo](t, rec)
		if info.Version == "" || info.DataDir != "" || info.AdminURL != "" || info.MemTotal != 0 {
			t.Errorf("server info for a site-scoped user = %+v", info)
		}
	}
	if len(e.c.Sites()) != 1 {
		t.Error("a site-scoped user created a site")
	}
}

func TestMeReportsAccess(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a := e.createSite(admin, redirectSite("site-a", 0))
	e.scoped("agency", grant(a.ID, model.RoleOperator))
	type me struct {
		User   model.User  `json:"user"`
		Access auth.Access `json:"access"`
	}
	rec := e.do(http.MethodGet, "/api/auth/me", nil, session(e.login("agency"))...)
	expect(t, rec, http.StatusOK)
	got := decodeJSON[me](t, rec)
	want := auth.Access{Role: model.RoleSites, Sites: []model.SiteGrant{grant(a.ID, model.RoleOperator)}}
	if got.User.Role != model.RoleSites || len(got.User.Sites) != 1 || got.Access.Role != want.Role || !slices.Equal(got.Access.Sites, want.Sites) {
		t.Errorf("me = %+v", got)
	}
	rec = e.do(http.MethodGet, "/api/auth/me", nil, admin...)
	if got := decodeJSON[me](t, rec); got.Access.Role != model.RoleAdmin || got.Access.Sites != nil {
		t.Errorf("admin me = %+v", got)
	}
}

func TestEventsAreFilteredForSiteScopedUsers(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a, b, c := threeSites(e, admin)
	e.scoped("agency", grant(a.ID, model.RoleViewer), grant(b.ID, model.RoleViewer))
	ck := session(e.login("agency"))
	ctx := context.Background()
	for _, ev := range []model.Event{
		{SiteID: a.ID, Message: "a"}, {SiteID: c.ID, Message: "c"}, {Message: "server"}, {SiteID: b.ID, Message: "b"},
	} {
		ev.Time, ev.Level, ev.Type = time.Now(), "info", "test"
		if err := e.c.Store.AddEvent(ctx, &ev); err != nil {
			t.Fatal(err)
		}
	}
	messages := func(rec *httptest.ResponseRecorder) []string {
		expect(t, rec, http.StatusOK)
		var out []string
		for _, ev := range decodeJSON[[]model.Event](t, rec) {
			if ev.Type == "test" {
				out = append(out, ev.Message)
			}
		}
		return out
	}
	if got := messages(e.do(http.MethodGet, "/api/events", nil, ck...)); !slices.Equal(got, []string{"b", "a"}) {
		t.Errorf("scoped events = %v", got)
	}
	if got := messages(e.do(http.MethodGet, "/api/events?siteId="+a.ID, nil, ck...)); !slices.Equal(got, []string{"a"}) {
		t.Errorf("scoped events of a = %v", got)
	}
	if got := messages(e.do(http.MethodGet, "/api/events?siteId="+c.ID, nil, ck...)); len(got) != 0 {
		t.Errorf("scoped events of an ungranted site = %v", got)
	}
	if got := messages(e.do(http.MethodGet, "/api/events", nil, admin...)); !slices.Equal(got, []string{"b", "server", "c", "a"}) {
		t.Errorf("admin events = %v", got)
	}
}

func TestEventVisible(t *testing.T) {
	t.Parallel()
	scoped := auth.Access{Role: model.RoleSites, Sites: []model.SiteGrant{grant("a", model.RoleViewer)}}
	for _, tc := range []struct {
		acc  auth.Access
		site string
		want bool
	}{
		{scoped, "a", true},
		{scoped, "b", false},
		{scoped, "", false}, // server-wide events are hidden
		{auth.Access{Role: model.RoleViewer}, "b", true},
		{auth.Access{Role: model.RoleViewer}, "", true},
		{auth.Access{Role: model.RoleSites}, "a", false},
	} {
		if got := eventVisible(tc.acc, model.Event{SiteID: tc.site}); got != tc.want {
			t.Errorf("eventVisible(%+v, %q) = %v, want %v", tc.acc, tc.site, got, tc.want)
		}
	}
	sites := []*model.Site{{ID: "a"}, {ID: "b"}}
	if got := visibleSites(scoped, sites); len(got) != 1 || got[0].ID != "a" {
		t.Errorf("visibleSites = %v", got)
	}
	if got := visibleSites(auth.Access{Role: model.RoleViewer}, sites); len(got) != 2 {
		t.Errorf("visibleSites for a viewer = %v", got)
	}
}

// TestStreamIsFilteredForSiteScopedUsers follows the live stream: status
// and events of other sites, and server-wide events, never arrive.
func TestStreamIsFilteredForSiteScopedUsers(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a, _, c := threeSites(e, admin)
	u := e.scoped("agency", grant(a.ID, model.RoleViewer))

	srv := httptest.NewServer(e.h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stream", nil)
	req.Header.Set("Authorization", "Bearer "+e.token(u))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	next := func() (string, string) {
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				return name, strings.TrimPrefix(line, "data: ")
			}
		}
		t.Fatalf("stream ended: %v", sc.Err())
		return "", ""
	}

	name, data := next()
	var status []model.SiteStatus
	if err := json.Unmarshal([]byte(data), &status); name != "status" || err != nil {
		t.Fatalf("first message %s %s", name, data)
	}
	if len(status) != 1 || status[0].SiteID != a.ID {
		t.Errorf("status carries %+v, want only site a", status)
	}

	e.c.Bus.Info("test.hidden", c.ID, "other site")
	e.c.Bus.Info("test.hidden", "", "server-wide")
	e.c.Bus.Info("test.visible", a.ID, "granted site")
	for {
		name, data := next()
		if name != "event" {
			continue
		}
		var ev model.Event
		json.Unmarshal([]byte(data), &ev)
		if ev.Type == "test.hidden" {
			t.Fatalf("stream delivered %+v", ev)
		}
		if ev.Type == "test.visible" {
			break
		}
	}
}

func TestPrometheusIsFilteredForSiteScopedUsers(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a, _, _ := threeSites(e, admin)
	if rec := e.do(http.MethodPost, "/api/certificates/selfsigned", map[string]any{"name": "cert", "domains": []string{"example.test"}}, admin...); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("self-signed certificate: %d %s", rec.Code, rec.Body)
	}
	u := e.scoped("agency", grant(a.ID, model.RoleViewer))

	rec := e.do(http.MethodGet, "/metrics", nil, withBearer(e.token(u)))
	expect(t, rec, http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, `site="site-a"`) || strings.Contains(body, `site="site-b"`) || strings.Contains(body, `site="site-c"`) {
		t.Errorf("scoped metrics:\n%s", body)
	}
	if strings.Contains(body, "nodehoster_certificate_expiry_seconds") {
		t.Error("scoped metrics include server-wide certificate metrics")
	}
	rec = e.do(http.MethodGet, "/metrics", nil, admin...)
	if body := rec.Body.String(); !strings.Contains(body, `site="site-c"`) || !strings.Contains(body, "nodehoster_certificate_expiry_seconds{") {
		t.Errorf("admin metrics:\n%s", body)
	}
}

func TestRestrictedTokens(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a, b, c := threeSites(e, admin)

	mint := func(opts []opt, body map[string]any) (string, model.APIToken) {
		t.Helper()
		body["name"] = "ci"
		rec := e.do(http.MethodPost, "/api/tokens", body, opts...)
		expect(t, rec, http.StatusCreated)
		out := decodeJSON[struct {
			Token string         `json:"token"`
			Info  model.APIToken `json:"info"`
		}](t, rec)
		return out.Token, out.Info
	}

	// An administrator's CI token that can only deploy site a.
	raw, info := mint(admin, map[string]any{"siteIds": []string{a.ID}})
	if info.Role != "" || !slices.Equal(info.SiteIDs, []string{a.ID}) {
		t.Errorf("token info = %+v", info)
	}
	ci := withBearer(raw)
	expect(t, e.do(http.MethodPost, "/api/sites/"+a.ID+"/stop", nil, ci), http.StatusOK)
	expect(t, e.do(http.MethodPut, "/api/sites/"+a.ID, redirectSite("site-a", 0), ci), http.StatusForbidden)
	expect(t, e.do(http.MethodGet, "/api/sites/"+b.ID, nil, ci), http.StatusNotFound)
	expect(t, e.do(http.MethodGet, "/api/settings", nil, ci), http.StatusForbidden)
	expect(t, e.do(http.MethodGet, "/api/users", nil, ci), http.StatusForbidden)
	if got := siteNames(t, e.do(http.MethodGet, "/api/sites", nil, ci)); !slices.Equal(got, []string{"site-a"}) {
		t.Errorf("token sees sites %v", got)
	}
	// It cannot mint itself something better, or touch the account.
	for _, ep := range []struct{ method, path string }{
		{"POST", "/api/tokens"}, {"GET", "/api/tokens"}, {"DELETE", "/api/tokens/" + info.ID},
		{"POST", "/api/auth/password"}, {"POST", "/api/auth/totp/setup"},
	} {
		expect(t, e.do(ep.method, ep.path, map[string]any{"name": "escape"}, ci), http.StatusForbidden)
	}
	expect(t, e.do(http.MethodGet, "/api/auth/me", nil, ci), http.StatusOK)
	if detail := lastAudit(t, e, "token.create"); !strings.Contains(detail, "site-a") {
		t.Errorf("token restriction not audited: %q", detail)
	}

	// A read-only token of an administrator.
	raw, _ = mint(admin, map[string]any{"role": "viewer"})
	ro := withBearer(raw)
	expect(t, e.do(http.MethodGet, "/api/certificates", nil, ro), http.StatusOK)
	expect(t, e.do(http.MethodGet, "/api/settings", nil, ro), http.StatusForbidden)
	expect(t, e.do(http.MethodPost, "/api/sites/"+a.ID+"/start", nil, ro), http.StatusForbidden)
	if got := siteNames(t, e.do(http.MethodGet, "/api/sites", nil, ro)); len(got) != 3 {
		t.Errorf("read-only token sees %v", got)
	}

	// The token follows its owner down: an operator's site token stops
	// working for operator actions once the owner is made a viewer.
	op := e.user("olga", model.RoleOperator, false)
	raw, _ = mint(session(e.login("olga")), map[string]any{"role": "operator", "siteIds": []string{a.ID, b.ID}})
	opTok := withBearer(raw)
	expect(t, e.do(http.MethodPost, "/api/sites/"+b.ID+"/stop", nil, opTok), http.StatusOK)
	expect(t, e.do(http.MethodPut, "/api/users/"+op.ID, map[string]any{"role": "viewer"}, admin...), http.StatusOK)
	expect(t, e.do(http.MethodPost, "/api/sites/"+b.ID+"/stop", nil, opTok), http.StatusForbidden)
	expect(t, e.do(http.MethodGet, "/api/sites/"+b.ID, nil, opTok), http.StatusOK)
	// And loses a site the owner loses.
	expect(t, e.do(http.MethodPut, "/api/users/"+op.ID, map[string]any{"role": "sites", "sites": []model.SiteGrant{grant(a.ID, model.RoleOperator)}}, admin...), http.StatusOK)
	expect(t, e.do(http.MethodGet, "/api/sites/"+b.ID, nil, opTok), http.StatusNotFound)
	expect(t, e.do(http.MethodPost, "/api/sites/"+a.ID+"/stop", nil, opTok), http.StatusOK)

	// A site-scoped user's token narrows further.
	e.scoped("agency", grant(a.ID, model.RoleOperator), grant(b.ID, model.RoleViewer))
	agency := session(e.login("agency"))
	raw, _ = mint(agency, map[string]any{"role": "viewer", "siteIds": []string{a.ID}})
	expect(t, e.do(http.MethodPost, "/api/sites/"+a.ID+"/stop", nil, withBearer(raw)), http.StatusForbidden)
	expect(t, e.do(http.MethodGet, "/api/sites/"+b.ID, nil, withBearer(raw)), http.StatusNotFound)

	// Restrictions must be within the caller's access.
	e.user("vera", model.RoleViewer, false)
	vera := session(e.login("vera"))
	for _, tc := range []struct {
		opts  []opt
		body  map[string]any
		field string
	}{
		{vera, map[string]any{"role": "operator"}, "role"},
		{vera, map[string]any{"role": "admin"}, "role"},
		{agency, map[string]any{"role": "admin"}, "role"},
		{admin, map[string]any{"role": "root"}, "role"},
		{admin, map[string]any{"role": "sites"}, "role"},
		{admin, map[string]any{"role": "admin", "siteIds": []string{a.ID}}, "role"},
		{admin, map[string]any{"siteIds": []string{"no-such-site"}}, "siteIds[0]"},
		{agency, map[string]any{"siteIds": []string{a.ID, c.ID}}, "siteIds[1]"},
	} {
		tc.body["name"] = "bad"
		rec := e.do(http.MethodPost, "/api/tokens", tc.body, tc.opts...)
		expect(t, rec, http.StatusUnprocessableEntity)
		if f := decodeJSON[map[string]string](t, rec)["field"]; f != tc.field {
			t.Errorf("%v: field = %q, want %q", tc.body, f, tc.field)
		}
	}
}

func lastAudit(t *testing.T, e *env, action string) string {
	t.Helper()
	list, err := e.c.Store.ListAudit(context.Background(), 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.Action == action {
			return a.Detail
		}
	}
	t.Fatalf("no %s in the audit log", action)
	return ""
}

func TestSiteScopedUserManagement(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a, b, _ := threeSites(e, admin)
	pw := "long enough password"

	for _, tc := range []struct {
		body  map[string]any
		field string
	}{
		{map[string]any{"username": "x1", "password": pw, "role": "sites"}, "sites"},
		{map[string]any{"username": "x2", "password": pw, "role": "sites", "sites": []model.SiteGrant{}}, "sites"},
		{map[string]any{"username": "x3", "password": pw, "role": "sites", "sites": []model.SiteGrant{grant(a.ID, model.RoleAdmin)}}, "sites[0].role"},
		{map[string]any{"username": "x4", "password": pw, "role": "sites", "sites": []model.SiteGrant{grant(a.ID, model.RoleViewer), grant("gone", model.RoleViewer)}}, "sites[1].siteId"},
		{map[string]any{"username": "x5", "password": pw, "role": "sites", "sites": []model.SiteGrant{grant(a.ID, model.RoleViewer), grant(a.ID, model.RoleOperator)}}, "sites[1].siteId"},
		{map[string]any{"username": "x6", "password": pw, "role": "operator", "sites": []model.SiteGrant{grant(a.ID, model.RoleViewer)}}, "sites"},
	} {
		rec := e.do(http.MethodPost, "/api/users", tc.body, admin...)
		expect(t, rec, http.StatusUnprocessableEntity)
		if f := decodeJSON[map[string]string](t, rec)["field"]; f != tc.field {
			t.Errorf("%v: field = %q, want %q", tc.body, f, tc.field)
		}
	}

	rec := e.do(http.MethodPost, "/api/users", map[string]any{"username": "agency", "password": pw, "role": "sites",
		"sites": []model.SiteGrant{grant(a.ID, model.RoleOperator), grant(b.ID, model.RoleViewer)}}, admin...)
	expect(t, rec, http.StatusCreated)
	u := decodeJSON[model.User](t, rec)
	if u.Role != model.RoleSites || len(u.Sites) != 2 {
		t.Fatalf("created = %+v", u)
	}
	if d := lastAudit(t, e, "user.create"); !strings.Contains(d, "site-a: operator") || !strings.Contains(d, "site-b: viewer") {
		t.Errorf("user.create detail = %q", d)
	}

	// Grants alone can be replaced, and are validated.
	path := "/api/users/" + u.ID
	expect(t, e.do(http.MethodPut, path, map[string]any{"sites": []model.SiteGrant{grant(b.ID, model.RoleOperator)}}, admin...), http.StatusOK)
	if d := lastAudit(t, e, "user.update"); !strings.Contains(d, "site-a: operator, site-b: viewer -> sites: site-b: operator") {
		t.Errorf("grant change audit detail = %q", d)
	}
	expect(t, e.do(http.MethodPut, path, map[string]any{"sites": []model.SiteGrant{}}, admin...), http.StatusUnprocessableEntity)
	expect(t, e.do(http.MethodPut, path, map[string]any{"sites": []model.SiteGrant{grant(b.ID, "admin")}}, admin...), http.StatusUnprocessableEntity)
	// Other changes keep the grants.
	expect(t, e.do(http.MethodPut, path, map[string]any{"disabled": false}, admin...), http.StatusOK)
	if got, _ := e.c.Store.GetUser(context.Background(), u.ID); len(got.Sites) != 1 || got.Sites[0] != grant(b.ID, model.RoleOperator) {
		t.Errorf("grants after unrelated update = %+v", got.Sites)
	}

	// A server-wide role replaces the grants; they cannot be sent with it.
	expect(t, e.do(http.MethodPut, path, map[string]any{"role": "viewer", "sites": []model.SiteGrant{grant(a.ID, model.RoleViewer)}}, admin...), http.StatusUnprocessableEntity)
	rec = e.do(http.MethodPut, path, map[string]any{"role": "viewer"}, admin...)
	expect(t, rec, http.StatusOK)
	if got := decodeJSON[model.User](t, rec); got.Role != model.RoleViewer || len(got.Sites) != 0 {
		t.Errorf("after making viewer = %+v", got)
	}
	// Grants on a server-wide user are refused; going back to sites needs them.
	expect(t, e.do(http.MethodPut, path, map[string]any{"sites": []model.SiteGrant{grant(a.ID, model.RoleViewer)}}, admin...), http.StatusUnprocessableEntity)
	expect(t, e.do(http.MethodPut, path, map[string]any{"role": "sites"}, admin...), http.StatusUnprocessableEntity)
	expect(t, e.do(http.MethodPut, path, map[string]any{"role": "sites", "sites": []model.SiteGrant{grant(a.ID, model.RoleViewer)}}, admin...), http.StatusOK)

	rec = e.do(http.MethodGet, "/api/users", nil, admin...)
	for _, lu := range decodeJSON[[]model.User](t, rec) {
		if lu.ID == u.ID && (lu.Role != model.RoleSites || len(lu.Sites) != 1 || lu.Sites[0].SiteID != a.ID) {
			t.Errorf("listed = %+v", lu)
		}
	}
}

func TestDeletingSiteRemovesItsGrants(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a, b, _ := threeSites(e, admin)
	u := e.scoped("agency", grant(a.ID, model.RoleOperator), grant(b.ID, model.RoleViewer))
	ck := session(e.login("agency"))
	expect(t, e.do(http.MethodGet, "/api/sites/"+a.ID, nil, ck...), http.StatusOK)

	expect(t, e.do(http.MethodDelete, "/api/sites/"+a.ID, nil, admin...), http.StatusNoContent)
	got, err := e.c.Store.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sites) != 1 || got.Sites[0].SiteID != b.ID {
		t.Errorf("grants after delete = %+v", got.Sites)
	}
	expect(t, e.do(http.MethodGet, "/api/sites/"+a.ID, nil, ck...), http.StatusNotFound)
	if names := siteNames(t, e.do(http.MethodGet, "/api/sites", nil, ck...)); !slices.Equal(names, []string{"site-b"}) {
		t.Errorf("sites = %v", names)
	}
}

func TestSiteScopedUserMustChangePassword(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a := e.createSite(admin, redirectSite("site-a", 0))
	u := e.scoped("agency", grant(a.ID, model.RoleOperator))
	u.MustChange = true
	if err := e.c.Store.PutUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	ck := session(e.login("agency"))
	for _, p := range []string{"/api/sites", "/api/sites/" + a.ID} {
		rec := e.do(http.MethodGet, p, nil, ck...)
		expect(t, rec, http.StatusForbidden)
		if !strings.Contains(rec.Body.String(), "change your password") {
			t.Errorf("%s: %s", p, rec.Body)
		}
	}
}

func TestDeploymentLogStreamIsSiteBound(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a, _, c := threeSites(e, admin)
	if err := e.c.Store.PutDeployment(context.Background(), &model.Deployment{ID: "dep-c", SiteID: c.ID, Status: "succeeded", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	expect(t, e.do(http.MethodGet, "/api/sites/"+a.ID+"/deployments/dep-c/log/stream", nil, admin...), http.StatusNotFound)
}

// TestSiteStreamsEndWhenAccessIsLost follows a site's output as a
// site-scoped user: revoking the grant or disabling the account ends the
// stream within seconds, as it does /api/stream, instead of whenever the
// client goes away.
func TestSiteStreamsEndWhenAccessIsLost(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a, b, _ := threeSites(e, admin)
	srv := httptest.NewServer(e.h)
	defer srv.Close()

	for _, tc := range []struct {
		name, path string
		change     map[string]any
	}{
		{"grant revoked", "/logs/stream", map[string]any{"sites": []model.SiteGrant{grant(b.ID, model.RoleViewer)}}},
		{"account disabled", "/logs/stream?type=access", map[string]any{"disabled": true}},
	} {
		u := e.scoped("follower-"+strings.ReplaceAll(tc.name, " ", "-"), grant(a.ID, model.RoleViewer))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/sites/"+a.ID+tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+e.token(u))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: stream status %d", tc.name, resp.StatusCode)
		}
		ended := make(chan struct{})
		go func() { io.Copy(io.Discard, resp.Body); close(ended) }()
		expect(t, e.do(http.MethodPut, "/api/users/"+u.ID, tc.change, admin...), http.StatusOK)
		select {
		case <-ended:
		case <-time.After(8 * time.Second):
			t.Errorf("%s: the stream went on", tc.name)
		}
		cancel()
		resp.Body.Close()
	}
}

// TestStreamsEndWhenTheCredentialIsWithdrawn follows /api/stream and a
// site's log stream with an API token and with a session: deleting the
// token or signing the session out ends the stream within seconds, as a
// revoked grant does, instead of whenever the client goes away.
func TestStreamsEndWhenTheCredentialIsWithdrawn(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a, _, _ := threeSites(e, admin)
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close) // after the parallel subtests

	for stream, path := range map[string]string{"stream": "/api/stream", "logs": "/api/sites/" + a.ID + "/logs/stream"} {
		for _, via := range []string{"token deleted", "signed out"} {
			name := stream + "-" + strings.ReplaceAll(via, " ", "-")
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				e.scoped(name, grant(a.ID, model.RoleViewer))
				sess := session(e.login(name))
				rec := e.do(http.MethodPost, "/api/tokens", map[string]any{"name": "follow"}, sess...)
				expect(t, rec, http.StatusCreated)
				tok := decodeJSON[struct {
					Token string         `json:"token"`
					Info  model.APIToken `json:"info"`
				}](t, rec)

				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
				withdraw := func() {
					expect(t, e.do(http.MethodDelete, "/api/tokens/"+tok.Info.ID, nil, sess...), http.StatusNoContent)
				}
				if via == "token deleted" {
					req.Header.Set("Authorization", "Bearer "+tok.Token)
				} else {
					for _, o := range sess {
						o(req)
					}
					withdraw = func() { expect(t, e.do(http.MethodPost, "/api/auth/logout", nil, sess...), http.StatusNoContent) }
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("stream status %d", resp.StatusCode)
				}
				ended := make(chan struct{})
				go func() { io.Copy(io.Discard, resp.Body); close(ended) }()

				// A credential still valid keeps the stream open past
				// the periodic check.
				select {
				case <-ended:
					t.Fatal("the stream ended with a valid credential")
				case <-time.After(3 * time.Second):
				}
				withdraw()
				select {
				case <-ended:
				case <-time.After(8 * time.Second):
					t.Error("the stream went on")
				}
			})
		}
	}
}

// TestRestrictedTokenCannotChangeItsOwnUser: the users list is an
// administrator's, but a restricted token must not use it to do to its
// owner's account what the account endpoints refuse it.
func TestRestrictedTokenCannotChangeItsOwnUser(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	rec := e.do(http.MethodPost, "/api/tokens", map[string]any{"name": "ops", "role": "admin"}, admin...)
	expect(t, rec, http.StatusCreated)
	tok := withBearer(decodeJSON[struct {
		Token string `json:"token"`
	}](t, rec).Token)
	me := decodeJSON[struct {
		User model.User `json:"user"`
	}](t, e.do(http.MethodGet, "/api/auth/me", nil, tok)).User

	for _, change := range []map[string]any{
		{"password": "a-new-password-1"},
		{"resetTotp": true},
		{"role": "viewer"},
		{"disabled": false},
	} {
		expect(t, e.do(http.MethodPut, "/api/users/"+me.ID, change, tok), http.StatusForbidden)
	}
	// Other users are still the token's to manage, and an unrestricted
	// token still manages its own account.
	other := e.user("olga", model.RoleOperator, false)
	expect(t, e.do(http.MethodPut, "/api/users/"+other.ID, map[string]any{"password": "a-new-password-2"}, tok), http.StatusOK)
	owner, err := e.c.Store.GetUser(context.Background(), me.ID)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, e.do(http.MethodPut, "/api/users/"+me.ID, map[string]any{"resetTotp": true}, withBearer(e.token(owner))), http.StatusOK)
}
