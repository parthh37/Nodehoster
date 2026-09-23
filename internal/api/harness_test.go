package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/auth"
	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// testPassword is every seeded user's password. Seeded users get a
// minimum-cost bcrypt hash so that logging in stays fast.
const testPassword = "correct horse battery"

// env is a complete NodeHoster core (store, secrets, proxy, process
// manager, deployer) over a private data directory, with the admin API
// handler in front of it. Nothing listens until a site is started.
type env struct {
	t    *testing.T
	c    *core.Core
	h    http.Handler
	root string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	// A short path: on Unix the process manager's agent socket lives in the
	// data directory and socket paths are limited to ~104 bytes.
	root, err := os.MkdirTemp("", "nhapi")
	if err != nil {
		t.Fatal(err)
	}
	// Registered first so it runs last, after Shutdown released every file.
	t.Cleanup(func() { os.RemoveAll(root) })

	boot := config.DefaultBootstrap()
	boot.Admin.Listen = "127.0.0.1:0"
	c, err := core.Open(config.NewPaths(root), boot, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("core.Open: %v", err)
	}
	t.Cleanup(c.Shutdown)

	// Point the default Node.js version at one that is not installed, so
	// nothing (for example a deployment's install step) ever executes a
	// node binary that happens to be on PATH.
	s := c.Settings()
	s.DefaultNodeVersion = "99.0.0-test"
	if _, err := c.UpdateSettings(context.Background(), s); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	return &env{t: t, c: c, h: Handler(c), root: root}
}

// user stores a user directly (bypassing the slow cost-12 hash).
func (e *env) user(name string, role model.Role, mustChange bool) *store.UserRecord {
	e.t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		e.t.Fatal(err)
	}
	u := &store.UserRecord{
		User:       model.User{ID: uuid.NewString(), Username: name, Role: role, PasswordHash: string(hash), CreatedAt: time.Now()},
		MustChange: mustChange,
	}
	if err := e.c.Store.PutUser(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
	return u
}

// token mints an API token for a user.
func (e *env) token(u *store.UserRecord) string {
	e.t.Helper()
	raw, _, err := e.c.Auth.CreateToken(context.Background(), u.ID, "test", 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return raw
}

type opt func(*http.Request)

func withCookie(ck *http.Cookie) opt { return func(r *http.Request) { r.AddCookie(ck) } }
func withCSRF() opt {
	return func(r *http.Request) { r.Header.Set("X-Requested-With", "NodeHoster") }
}
func withBearer(tok string) opt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}
func withHeader(k, v string) opt { return func(r *http.Request) { r.Header.Set(k, v) } }

// session is how a signed-in browser calls the API: cookie plus the CSRF
// header the UI always sends.
func session(ck *http.Cookie) []opt { return []opt{withCookie(ck), withCSRF()} }

// do sends a request. body may be nil, a string/[]byte (sent raw) or any
// value (sent as JSON).
func (e *env) do(method, path string, body any, opts ...opt) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// upload posts a multipart form with one file field.
func (e *env) upload(path, field, filename string, data []byte, opts ...opt) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if field != "" {
		fw, err := mw.CreateFormFile(field, filename)
		if err != nil {
			e.t.Fatal(err)
		}
		fw.Write(data)
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// login signs in through the API and returns the session cookie.
func (e *env) login(name string) *http.Cookie {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/api/auth/login", map[string]string{"username": name, "password": testPassword})
	if rec.Code != http.StatusOK {
		e.t.Fatalf("login %s: %d %s", name, rec.Code, rec.Body)
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == auth.SessionCookie {
			return ck
		}
	}
	e.t.Fatalf("login %s set no session cookie", name)
	return nil
}

// adminSession seeds an administrator and signs in.
func (e *env) adminSession() []opt {
	e.t.Helper()
	e.user("root", model.RoleAdmin, false)
	return session(e.login("root"))
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func expect(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, code, rec.Body)
	}
}

// freePort returns a loopback TCP port that was free a moment ago.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type siteResp struct {
	model.Site
	Status model.SiteStatus `json:"status"`
}

func redirectSite(name string, port int) map[string]any {
	s := map[string]any{
		"name": name, "type": "redirect", "autoStart": false,
		"redirect": map[string]any{"targetUrl": "https://example.com", "statusCode": 301, "preservePath": true},
	}
	if port > 0 {
		s["bindings"] = []map[string]any{{"protocol": "http", "ip": "127.0.0.1", "port": port}}
	}
	return s
}

// createSite creates a site as an admin and returns it.
func (e *env) createSite(admin []opt, body map[string]any) siteResp {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/api/sites", body, admin...)
	expect(e.t, rec, http.StatusCreated)
	return decodeJSON[siteResp](e.t, rec)
}

func (e *env) auditActions() []string {
	e.t.Helper()
	list, err := e.c.Store.ListAudit(context.Background(), 1000, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, a := range list {
		out = append(out, a.User+":"+a.Action)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
