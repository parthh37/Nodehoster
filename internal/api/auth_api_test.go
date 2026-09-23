package api

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/auth"
	"github.com/parthh37/nodehoster/internal/model"
)

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/api/auth/me", nil)
	h := rec.Header()
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "same-origin",
		"Cache-Control":          "no-store",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("Content-Security-Policy = %q", csp)
	}
	if h.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS sent over plain HTTP")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.TLS = &tls.ConnectionState{}
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Error("HSTS missing over TLS")
	}
}

func TestUnknownAPIEndpointIsJSON404(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/api/definitely-not-here", nil)
	expect(t, rec, http.StatusNotFound)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestAuthenticationRequired(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.user("viewer", model.RoleViewer, false)

	endpoints := []struct{ method, path string }{
		{"GET", "/api/auth/me"},
		{"POST", "/api/auth/logout"},
		{"POST", "/api/auth/password"},
		{"GET", "/api/tokens"},
		{"POST", "/api/tokens"},
		{"GET", "/api/sites"},
		{"POST", "/api/sites"},
		{"GET", "/api/sites/x"},
		{"PUT", "/api/sites/x"},
		{"DELETE", "/api/sites/x"},
		{"POST", "/api/sites/x/start"},
		{"POST", "/api/sites/x/deploy/zip"},
		{"GET", "/api/events"},
		{"GET", "/api/settings"},
		{"PUT", "/api/settings"},
		{"GET", "/api/users"},
		{"POST", "/api/users"},
		{"GET", "/api/audit"},
		{"GET", "/api/backup"},
		{"POST", "/api/restore"},
		{"GET", "/api/certificates"},
		{"GET", "/metrics"},
	}
	credentials := []struct {
		name string
		opts []opt
	}{
		{"none", nil},
		{"unknown session cookie", []opt{withCookie(&http.Cookie{Name: auth.SessionCookie, Value: "forged"}), withCSRF()}},
		{"empty session cookie", []opt{withCookie(&http.Cookie{Name: auth.SessionCookie, Value: ""}), withCSRF()}},
		{"unknown bearer token", []opt{withBearer("nh_notARealTokenAtAll")}},
		{"bearer without prefix", []opt{withBearer("abc")}},
		{"basic auth", []opt{withHeader("Authorization", "Basic dmlld2VyOmNvcnJlY3QgaG9yc2UgYmF0dGVyeQ==")}},
	}
	for _, cred := range credentials {
		for _, ep := range endpoints {
			rec := e.do(ep.method, ep.path, nil, cred.opts...)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with %s: status %d, want 401", ep.method, ep.path, cred.name, rec.Code)
			}
		}
	}
}

func TestLoginAndLogout(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.user("alice", model.RoleOperator, false)

	// Wrong password.
	rec := e.do(http.MethodPost, "/api/auth/login", map[string]string{"username": "alice", "password": "wrong password!"})
	expect(t, rec, http.StatusUnauthorized)
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a failed login set a cookie")
	}
	if !contains(e.auditActions(), "alice:login.failed") {
		t.Errorf("failed login not audited: %v", e.auditActions())
	}

	// Malformed body.
	expect(t, e.do(http.MethodPost, "/api/auth/login", "{not json"), http.StatusBadRequest)

	// Success.
	rec = e.do(http.MethodPost, "/api/auth/login", map[string]string{"username": "alice", "password": testPassword})
	expect(t, rec, http.StatusOK)
	var ck *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			ck = c
		}
	}
	if ck == nil || ck.Value == "" {
		t.Fatal("no session cookie")
	}
	if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" || ck.Secure {
		t.Errorf("cookie attributes: HttpOnly=%v SameSite=%v Path=%q Secure=%v", ck.HttpOnly, ck.SameSite, ck.Path, ck.Secure)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "hash") || strings.Contains(rec.Body.String(), "$2a$") {
		t.Errorf("login response leaks the password hash: %s", rec.Body)
	}
	body := decodeJSON[struct {
		User               model.User `json:"user"`
		MustChangePassword bool       `json:"mustChangePassword"`
	}](t, rec)
	if body.User.Username != "alice" || body.User.Role != model.RoleOperator || body.MustChangePassword {
		t.Errorf("login body = %+v", body)
	}
	if !contains(e.auditActions(), "alice:login") {
		t.Errorf("login not audited: %v", e.auditActions())
	}

	// The session works.
	rec = e.do(http.MethodGet, "/api/auth/me", nil, withCookie(ck))
	expect(t, rec, http.StatusOK)
	if me := decodeJSON[struct{ User model.User }](t, rec); me.User.Username != "alice" {
		t.Errorf("me = %+v", me)
	}

	// Logout clears the cookie and revokes the session server-side.
	rec = e.do(http.MethodPost, "/api/auth/logout", nil, session(ck)...)
	expect(t, rec, http.StatusNoContent)
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.MaxAge < 0 && c.Value == "" {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout did not clear the cookie")
	}
	expect(t, e.do(http.MethodGet, "/api/auth/me", nil, withCookie(ck)), http.StatusUnauthorized)
}

func TestLoginCookieIsSecureOverTLS(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.user("tls", model.RoleViewer, false)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"tls","password":"`+testPassword+`"}`))
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	expect(t, rec, http.StatusOK)
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie && !c.Secure {
			t.Error("session cookie over TLS is not Secure")
		}
	}
}

func TestLoginThrottling(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.user("bob", model.RoleViewer, false)
	for i := 0; i < 5; i++ {
		rec := e.do(http.MethodPost, "/api/auth/login", map[string]string{"username": "bob", "password": "nope nope nope"})
		expect(t, rec, http.StatusUnauthorized)
	}
	// Even the right password is refused while locked out.
	rec := e.do(http.MethodPost, "/api/auth/login", map[string]string{"username": "BOB", "password": testPassword})
	expect(t, rec, http.StatusTooManyRequests)
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			t.Fatal("a locked-out login set a session cookie")
		}
	}
}

func TestDisabledUserIsLockedOut(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.user("carol", model.RoleAdmin, false)
	ck := e.login("carol")
	tok := e.token(u)

	u.Disabled = true
	if err := e.c.Store.PutUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	expect(t, e.do(http.MethodGet, "/api/auth/me", nil, withCookie(ck)), http.StatusUnauthorized)
	expect(t, e.do(http.MethodGet, "/api/auth/me", nil, withBearer(tok)), http.StatusUnauthorized)
	rec := e.do(http.MethodPost, "/api/auth/login", map[string]string{"username": "carol", "password": testPassword})
	expect(t, rec, http.StatusUnauthorized)
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a disabled account received a session")
	}
}

func TestCSRFHeaderRequiredForCookieMutations(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.user("dave", model.RoleAdmin, false)
	ck := e.login("dave")

	// Cookie without the header: rejected before reaching the handler.
	rec := e.do(http.MethodPost, "/api/tokens", map[string]string{"name": "x"}, withCookie(ck))
	expect(t, rec, http.StatusForbidden)
	rec = e.do(http.MethodPost, "/api/sites", redirectSite("csrf", 0), withCookie(ck), withHeader("X-Requested-With", "XMLHttpRequest"))
	expect(t, rec, http.StatusForbidden)
	rec = e.do(http.MethodDelete, "/api/tokens/whatever", nil, withCookie(ck))
	expect(t, rec, http.StatusForbidden)
	if len(e.c.Sites()) != 0 {
		t.Fatal("a request without the CSRF header created a site")
	}
	// Safe methods do not need it.
	expect(t, e.do(http.MethodGet, "/api/sites", nil, withCookie(ck)), http.StatusOK)
	// With the header the request goes through.
	expect(t, e.do(http.MethodPost, "/api/tokens", map[string]string{"name": "x"}, session(ck)...), http.StatusCreated)
	// Bearer tokens cannot be sent by a browser cross-site, so they are exempt.
	expect(t, e.do(http.MethodPost, "/api/tokens", map[string]string{"name": "y"}, withBearer(e.token(u))), http.StatusCreated)
}

func TestRoleEnforcement(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sessions := map[model.Role][]opt{}
	tokens := map[model.Role]string{}
	for _, r := range []model.Role{model.RoleViewer, model.RoleOperator, model.RoleAdmin} {
		u := e.user("user-"+string(r), r, false)
		sessions[r] = session(e.login(u.Username))
		tokens[r] = e.token(u)
	}

	rank := map[model.Role]int{model.RoleViewer: 1, model.RoleOperator: 2, model.RoleAdmin: 3}
	endpoints := []struct {
		method, path string
		body         any
		need         model.Role
	}{
		{"GET", "/api/sites", nil, model.RoleViewer},
		{"GET", "/api/sites/missing", nil, model.RoleViewer},
		{"GET", "/api/sites/missing/deployments", nil, model.RoleViewer},
		{"GET", "/api/events", nil, model.RoleViewer},
		{"GET", "/api/certificates", nil, model.RoleViewer},
		{"GET", "/api/settings/dns-catalog", nil, model.RoleViewer},
		{"GET", "/metrics", nil, model.RoleViewer},
		{"POST", "/api/sites/missing/start", nil, model.RoleOperator},
		{"POST", "/api/sites/missing/stop", nil, model.RoleOperator},
		{"POST", "/api/sites/missing/restart", nil, model.RoleOperator},
		{"POST", "/api/sites/missing/recycle", nil, model.RoleOperator},
		{"POST", "/api/sites/missing/logs/clear", nil, model.RoleOperator},
		{"POST", "/api/sites/missing/deploy/git", nil, model.RoleOperator},
		{"POST", "/api/sites/missing/deploy/zip", nil, model.RoleOperator},
		{"POST", "/api/sites/missing/deployments/d/activate", nil, model.RoleOperator},
		{"POST", "/api/sites", map[string]any{}, model.RoleAdmin},
		{"PUT", "/api/sites/missing", map[string]any{}, model.RoleAdmin},
		{"DELETE", "/api/sites/missing", nil, model.RoleAdmin},
		{"GET", "/api/settings", nil, model.RoleAdmin},
		{"PUT", "/api/settings", map[string]any{}, model.RoleAdmin},
		{"POST", "/api/settings/webhooks/test", map[string]any{"url": "://invalid"}, model.RoleAdmin},
		{"GET", "/api/users", nil, model.RoleAdmin},
		{"POST", "/api/users", map[string]any{}, model.RoleAdmin},
		{"PUT", "/api/users/missing", map[string]any{}, model.RoleAdmin},
		{"DELETE", "/api/users/missing", nil, model.RoleAdmin},
		{"GET", "/api/audit", nil, model.RoleAdmin},
		{"GET", "/api/backup", nil, model.RoleAdmin},
		{"POST", "/api/restore", nil, model.RoleAdmin},
		{"POST", "/api/certificates/selfsigned", map[string]any{}, model.RoleAdmin},
		{"DELETE", "/api/certificates/missing", nil, model.RoleAdmin},
		{"POST", "/api/certificates/missing/export", map[string]any{}, model.RoleAdmin},
	}
	for _, role := range []model.Role{model.RoleViewer, model.RoleOperator, model.RoleAdmin} {
		for _, via := range []string{"session", "token"} {
			opts := sessions[role]
			if via == "token" {
				opts = []opt{withBearer(tokens[role])}
			}
			for _, ep := range endpoints {
				rec := e.do(ep.method, ep.path, ep.body, opts...)
				allowed := rank[role] >= rank[ep.need]
				switch {
				case rec.Code == http.StatusUnauthorized:
					t.Errorf("%s via %s: %s %s = 401", role, via, ep.method, ep.path)
				case !allowed && rec.Code != http.StatusForbidden:
					t.Errorf("%s via %s: %s %s = %d, want 403", role, via, ep.method, ep.path, rec.Code)
				case allowed && rec.Code == http.StatusForbidden:
					t.Errorf("%s via %s: %s %s = 403 (%s), want allowed", role, via, ep.method, ep.path, strings.TrimSpace(rec.Body.String()))
				}
			}
		}
	}
	if len(e.c.Sites()) != 0 {
		t.Error("role checks created a site")
	}
}

func TestMustChangePasswordBlocksEverythingElse(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.user("newbie", model.RoleAdmin, true)
	ck := e.login("newbie")

	rec := e.do(http.MethodGet, "/api/sites", nil, session(ck)...)
	expect(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "change your password") {
		t.Errorf("body = %s", rec.Body)
	}
	expect(t, e.do(http.MethodPost, "/api/sites", redirectSite("nope", 0), session(ck)...), http.StatusForbidden)

	rec = e.do(http.MethodGet, "/api/auth/me", nil, session(ck)...)
	expect(t, rec, http.StatusOK)
	if me := decodeJSON[struct {
		MustChangePassword bool `json:"mustChangePassword"`
	}](t, rec); !me.MustChangePassword {
		t.Error("me does not report mustChangePassword")
	}

	// Wrong current password, same password and a too-short one are refused.
	expect(t, e.do(http.MethodPost, "/api/auth/password", map[string]string{"current": "wrong", "new": "a brand new password"}, session(ck)...), http.StatusBadRequest)
	expect(t, e.do(http.MethodPost, "/api/auth/password", map[string]string{"current": testPassword, "new": testPassword}, session(ck)...), http.StatusBadRequest)
	expect(t, e.do(http.MethodPost, "/api/auth/password", map[string]string{"current": testPassword, "new": "short"}, session(ck)...), http.StatusBadRequest)

	// Changing it unlocks the account and keeps the current session.
	expect(t, e.do(http.MethodPost, "/api/auth/password", map[string]string{"current": testPassword, "new": "a brand new password"}, session(ck)...), http.StatusNoContent)
	expect(t, e.do(http.MethodGet, "/api/sites", nil, session(ck)...), http.StatusOK)
	if !contains(e.auditActions(), "newbie:password.change") {
		t.Errorf("password change not audited: %v", e.auditActions())
	}
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.user("erin", model.RoleViewer, false)
	laptop := e.login("erin")
	phone := e.login("erin")
	expect(t, e.do(http.MethodPost, "/api/auth/password", map[string]string{"current": testPassword, "new": "another long password"}, session(laptop)...), http.StatusNoContent)
	expect(t, e.do(http.MethodGet, "/api/auth/me", nil, withCookie(laptop)), http.StatusOK)
	expect(t, e.do(http.MethodGet, "/api/auth/me", nil, withCookie(phone)), http.StatusUnauthorized)
}

func TestMustChangePasswordCannotBeBypassedWithAPIToken(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.user("newbie", model.RoleAdmin, true)
	ck := e.login("newbie")
	rec := e.do(http.MethodPost, "/api/tokens", map[string]string{"name": "escape"}, session(ck)...)
	if rec.Code == http.StatusForbidden {
		return // minting is blocked: fine
	}
	expect(t, rec, http.StatusCreated)
	tok := decodeJSON[struct{ Token string }](t, rec).Token
	rec = e.do(http.MethodGet, "/api/sites", nil, withBearer(tok))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a MustChange user reached /api/sites with a self-minted token: %d", rec.Code)
	}
}

func TestAPITokenLifecycle(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.user("frank", model.RoleOperator, false)
	other := e.user("grace", model.RoleOperator, false)
	ck := e.login("frank")

	expect(t, e.do(http.MethodPost, "/api/tokens", map[string]string{"name": "   "}, session(ck)...), http.StatusBadRequest)

	rec := e.do(http.MethodPost, "/api/tokens", map[string]any{"name": "ci", "expiresDays": 30}, session(ck)...)
	expect(t, rec, http.StatusCreated)
	created := decodeJSON[struct {
		Token string         `json:"token"`
		Info  model.APIToken `json:"info"`
	}](t, rec)
	if !strings.HasPrefix(created.Token, "nh_") || created.Info.ID == "" || created.Info.ExpiresAt == nil {
		t.Fatalf("created = %+v", created)
	}
	if strings.Contains(rec.Body.String(), `"hash"`) {
		t.Error("token hash exposed")
	}
	if !strings.HasPrefix(created.Token, created.Info.Prefix) {
		t.Errorf("prefix %q does not identify token", created.Info.Prefix)
	}

	rec = e.do(http.MethodGet, "/api/tokens", nil, session(ck)...)
	expect(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), created.Token) {
		t.Error("listing tokens returns the raw token")
	}
	if list := decodeJSON[[]model.APIToken](t, rec); len(list) != 1 || list[0].Name != "ci" {
		t.Errorf("tokens = %+v", list)
	}

	rec = e.do(http.MethodGet, "/api/auth/me", nil, withBearer(created.Token))
	expect(t, rec, http.StatusOK)

	// Another user cannot see or revoke it.
	otherTok := e.token(other)
	rec = e.do(http.MethodGet, "/api/tokens", nil, withBearer(otherTok))
	if list := decodeJSON[[]model.APIToken](t, rec); len(list) != 1 || list[0].ID == created.Info.ID {
		t.Errorf("grace sees tokens %+v", list)
	}
	expect(t, e.do(http.MethodDelete, "/api/tokens/"+created.Info.ID, nil, withBearer(otherTok)), http.StatusNotFound)
	expect(t, e.do(http.MethodGet, "/api/auth/me", nil, withBearer(created.Token)), http.StatusOK)

	// The owner revokes it.
	expect(t, e.do(http.MethodDelete, "/api/tokens/"+created.Info.ID, nil, session(ck)...), http.StatusNoContent)
	expect(t, e.do(http.MethodGet, "/api/auth/me", nil, withBearer(created.Token)), http.StatusUnauthorized)
}

func TestExpiredTokenRejected(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.user("henry", model.RoleAdmin, false)
	raw, tok, err := e.c.Auth.CreateToken(context.Background(), u.ID, "old", 1)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	tok.ExpiresAt = &past
	if err := e.c.Store.DeleteToken(context.Background(), u.ID, tok.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Store.CreateToken(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	expect(t, e.do(http.MethodGet, "/api/sites", nil, withBearer(raw)), http.StatusUnauthorized)
}

func TestUserManagement(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	root := e.user("root", model.RoleAdmin, false)
	admin := session(e.login("root"))

	// Validation happens before any hashing.
	for _, tc := range []struct {
		body  map[string]any
		field string
	}{
		{map[string]any{"username": "x", "password": "long enough password", "role": "viewer"}, "username"},
		{map[string]any{"username": "ivan", "password": "long enough password", "role": "superuser"}, "role"},
		{map[string]any{"username": "ivan", "password": "short", "role": "viewer"}, "password"},
	} {
		rec := e.do(http.MethodPost, "/api/users", tc.body, admin...)
		expect(t, rec, http.StatusUnprocessableEntity)
		if f := decodeJSON[map[string]string](t, rec)["field"]; f != tc.field {
			t.Errorf("body %v: field = %q, want %q", tc.body, f, tc.field)
		}
	}

	// Create a user (through the real, slow hash) and check the result.
	rec := e.do(http.MethodPost, "/api/users", map[string]any{"username": "ivan", "password": "long enough password", "role": "operator"}, admin...)
	expect(t, rec, http.StatusCreated)
	ivan := decodeJSON[model.User](t, rec)
	if ivan.Role != model.RoleOperator || strings.Contains(rec.Body.String(), "$2a$") {
		t.Errorf("created user = %s", rec.Body)
	}
	stored, err := e.c.Store.GetUser(context.Background(), ivan.ID)
	if err != nil || !stored.MustChange {
		t.Errorf("a user created by an admin must change the password at first sign-in (%v)", err)
	}
	expect(t, e.do(http.MethodPost, "/api/users", map[string]any{"username": "ivan", "password": "long enough password", "role": "viewer"}, admin...), http.StatusUnprocessableEntity)

	// The last administrator is protected.
	expect(t, e.do(http.MethodPut, "/api/users/"+root.ID, map[string]any{"role": "viewer"}, admin...), http.StatusBadRequest)
	expect(t, e.do(http.MethodPut, "/api/users/"+root.ID, map[string]any{"disabled": true}, admin...), http.StatusBadRequest)
	expect(t, e.do(http.MethodDelete, "/api/users/"+root.ID, nil, admin...), http.StatusBadRequest)
	expect(t, e.do(http.MethodPut, "/api/users/"+root.ID, map[string]any{"role": "owner"}, admin...), http.StatusUnprocessableEntity)

	// Disabling a user ends their sessions immediately.
	judy := e.user("judy", model.RoleViewer, false)
	judyCk := e.login("judy")
	expect(t, e.do(http.MethodGet, "/api/sites", nil, session(judyCk)...), http.StatusOK)
	expect(t, e.do(http.MethodPut, "/api/users/"+judy.ID, map[string]any{"disabled": true}, admin...), http.StatusOK)
	expect(t, e.do(http.MethodGet, "/api/sites", nil, session(judyCk)...), http.StatusUnauthorized)

	// Promote ivan, after which root is no longer the last admin.
	expect(t, e.do(http.MethodPut, "/api/users/"+ivan.ID, map[string]any{"role": "admin"}, admin...), http.StatusOK)
	expect(t, e.do(http.MethodDelete, "/api/users/"+ivan.ID, nil, admin...), http.StatusNoContent)
	expect(t, e.do(http.MethodDelete, "/api/users/"+ivan.ID, nil, admin...), http.StatusNotFound)

	rec = e.do(http.MethodGet, "/api/users", nil, admin...)
	expect(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "$2a$") || strings.Contains(strings.ToLower(rec.Body.String()), "passwordhash") {
		t.Error("user list exposes password hashes")
	}
	for _, want := range []string{"root:user.create", "root:user.update", "root:user.delete"} {
		if !contains(e.auditActions(), want) {
			t.Errorf("audit log lacks %s: %v", want, e.auditActions())
		}
	}
}
