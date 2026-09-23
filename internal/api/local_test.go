package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

// The local handler trusts the identity the pipe server attaches to the
// connection; without one it must refuse, whatever else the request has.
func TestLocalHandlerRequiresPipeIdentity(t *testing.T) {
	e := newEnv(t)
	h := LocalHandler(e.c)
	admin := e.user("root", model.RoleAdmin, false)

	for name, req := range map[string]*http.Request{
		"no credentials": httptest.NewRequest(http.MethodGet, "/api/sites", nil),
		"bearer token": func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/api/sites", nil)
			r.Header.Set("Authorization", "Bearer "+e.token(admin))
			return r
		}(),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sites", nil)
	req = req.WithContext(WithLocalUser(req.Context(), `WEB01\Administrator`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	expect(t, rec, http.StatusOK)

	// The web console is not served over the pipe.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithLocalUser(req.Context(), `WEB01\Administrator`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	expect(t, rec, http.StatusNotFound)
}

func TestAdminResetsLostTOTP(t *testing.T) {
	e := newEnv(t)
	admin := e.adminSession()
	u := e.user("phone-lost", model.RoleOperator, false)
	u.TOTPEnabled, u.TOTPSecret = true, "sealed-secret"
	if err := e.c.Store.PutUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	ck := e.login2FAless(u.ID)

	expect(t, e.do(http.MethodPut, "/api/users/"+u.ID, map[string]any{"resetTotp": true}, admin...), http.StatusOK)

	got, err := e.c.Store.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TOTPEnabled || got.TOTPSecret != "" {
		t.Fatalf("TOTP still set: enabled=%v secret=%q", got.TOTPEnabled, got.TOTPSecret)
	}
	// Their sessions end, so the reset takes effect everywhere.
	expect(t, e.do(http.MethodGet, "/api/auth/me", nil, withCookie(ck)), http.StatusUnauthorized)
	if !contains(e.auditActions(), "root:user.update") {
		t.Fatalf("no audit entry: %v", e.auditActions())
	}
}

// login2FAless opens a session for a user directly (their TOTP secret in
// these tests is not a real one, so they cannot sign in through the API).
func (e *env) login2FAless(userID string) *http.Cookie {
	e.t.Helper()
	u, err := e.c.Store.GetUser(context.Background(), userID)
	if err != nil {
		e.t.Fatal(err)
	}
	u.TOTPEnabled = false
	if err := e.c.Store.PutUser(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
	ck := e.login(u.Username)
	u.TOTPEnabled = true
	if err := e.c.Store.PutUser(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
	return ck
}
