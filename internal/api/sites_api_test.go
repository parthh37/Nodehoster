package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
)

func TestSiteCRUD(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()

	body := redirectSite("Old Site", freePort(t))
	body["description"] = "moves to example.com"
	body["deploy"] = map[string]any{
		"webhookSecret": "hook-secret-value",
		"git":           map[string]any{"repo": "https://example.invalid/app.git", "branch": "main", "token": "git-token-value"},
	}
	rec := e.do(http.MethodPost, "/api/sites", body, admin...)
	expect(t, rec, http.StatusCreated)
	for _, secret := range []string{"hook-secret-value", "git-token-value"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("create response leaks %q", secret)
		}
	}
	created := decodeJSON[siteResp](t, rec)
	if created.ID == "" || created.Type != model.SiteRedirect || created.Name != "Old Site" {
		t.Fatalf("created = %+v", created.Site)
	}
	if created.Deploy.WebhookSecret != secrets.Mask || created.Deploy.Git.Token != secrets.Mask {
		t.Errorf("secrets not masked: %+v", created.Deploy)
	}
	if created.Status.State != model.StateStopped {
		t.Errorf("new site state = %q, want stopped (autoStart off)", created.Status.State)
	}
	if len(created.Bindings) != 1 || created.Bindings[0].ID == "" {
		t.Errorf("bindings = %+v", created.Bindings)
	}

	// Stored sealed, not in plaintext.
	stored, err := e.c.Site(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Deploy.WebhookSecret == "hook-secret-value" || e.c.Box.MustUnseal(stored.Deploy.WebhookSecret) != "hook-secret-value" {
		t.Errorf("webhook secret stored as %q", stored.Deploy.WebhookSecret)
	}

	// Read back.
	rec = e.do(http.MethodGet, "/api/sites/"+created.ID, nil, admin...)
	expect(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "hook-secret-value") || strings.Contains(rec.Body.String(), "enc:v1:") {
		t.Error("GET leaks a secret or its sealed form")
	}
	rec = e.do(http.MethodGet, "/api/sites", nil, admin...)
	expect(t, rec, http.StatusOK)
	if list := decodeJSON[[]siteResp](t, rec); len(list) != 1 || list[0].ID != created.ID {
		t.Errorf("list = %+v", list)
	}

	// Update, sending the masked secrets back unchanged.
	got := decodeJSON[map[string]any](t, e.do(http.MethodGet, "/api/sites/"+created.ID, nil, admin...))
	got["name"] = "New Site"
	got["description"] = "renamed"
	delete(got, "status")
	rec = e.do(http.MethodPut, "/api/sites/"+created.ID, got, admin...)
	expect(t, rec, http.StatusOK)
	if u := decodeJSON[siteResp](t, rec); u.Name != "New Site" || u.Description != "renamed" || !u.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("updated = %+v", u.Site)
	}
	stored, _ = e.c.Site(created.ID)
	if e.c.Box.MustUnseal(stored.Deploy.WebhookSecret) != "hook-secret-value" || e.c.Box.MustUnseal(stored.Deploy.Git.Token) != "git-token-value" {
		t.Error("sending the mask back did not keep the stored secrets")
	}

	// The type is immutable.
	got["type"] = "static"
	got["static"] = map[string]any{"root": "."}
	rec = e.do(http.MethodPut, "/api/sites/"+created.ID, got, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "type" {
		t.Errorf("field = %q", f)
	}

	// Delete, with files.
	siteDir := filepath.Join(e.c.Paths.Sites, created.ID)
	os.MkdirAll(siteDir, 0o750)
	os.WriteFile(filepath.Join(siteDir, "marker"), []byte("x"), 0o640)
	expect(t, e.do(http.MethodDelete, "/api/sites/"+created.ID+"?deleteFiles=true", nil, admin...), http.StatusNoContent)
	expect(t, e.do(http.MethodGet, "/api/sites/"+created.ID, nil, admin...), http.StatusNotFound)
	if _, err := os.Stat(siteDir); !os.IsNotExist(err) {
		t.Errorf("site files survived deleteFiles=true: %v", err)
	}
	expect(t, e.do(http.MethodDelete, "/api/sites/"+created.ID, nil, admin...), http.StatusNotFound)

	for _, want := range []string{"root:site.create", "root:site.update", "root:site.delete"} {
		if !contains(e.auditActions(), want) {
			t.Errorf("audit lacks %s", want)
		}
	}
}

func TestSiteValidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	port := freePort(t)
	e.createSite(admin, redirectSite("taken", port))

	mut := func(f func(m map[string]any)) map[string]any {
		m := redirectSite("candidate", 0)
		f(m)
		return m
	}
	cases := []struct {
		name  string
		body  any
		code  int
		field string
	}{
		{"invalid JSON", "{", http.StatusBadRequest, ""},
		{"empty name", mut(func(m map[string]any) { m["name"] = "" }), 422, "name"},
		{"bad name", mut(func(m map[string]any) { m["name"] = "../etc" }), 422, "name"},
		{"unknown type", mut(func(m map[string]any) { m["type"] = "php" }), 422, "type"},
		{"bad redirect status", mut(func(m map[string]any) {
			m["redirect"] = map[string]any{"targetUrl": "https://x.test", "statusCode": 200}
		}), 422, "redirect.statusCode"},
		{"bad redirect target", mut(func(m map[string]any) { m["redirect"] = map[string]any{"targetUrl": "javascript:alert(1)"} }), 422, "redirect.targetUrl"},
		{"bad port", mut(func(m map[string]any) {
			m["bindings"] = []map[string]any{{"protocol": "http", "port": 70000}}
		}), 422, "bindings[0].port"},
		{"bad protocol", mut(func(m map[string]any) {
			m["bindings"] = []map[string]any{{"protocol": "ftp", "port": 21}}
		}), 422, "bindings[0].protocol"},
		{"bad host", mut(func(m map[string]any) {
			m["bindings"] = []map[string]any{{"protocol": "http", "port": 8080, "host": "bad host!"}}
		}), 422, "bindings[0].host"},
		{"binding in use", mut(func(m map[string]any) {
			m["bindings"] = []map[string]any{{"protocol": "http", "ip": "127.0.0.1", "port": port}}
		}), 422, "bindings[0]"},
		{"missing certificate", mut(func(m map[string]any) {
			m["bindings"] = []map[string]any{{"protocol": "https", "port": 8443, "certMode": "certificate", "certificateId": "nope"}}
		}), 422, "bindings"},
		{"proxy without upstreams", map[string]any{"name": "p", "type": "proxy"}, 422, "proxy.upstreams"},
		{"static without root", map[string]any{"name": "s", "type": "static"}, 422, "static.root"},
		{"basic auth user without password", mut(func(m map[string]any) {
			m["routing"] = map[string]any{"basicAuth": map[string]any{"enabled": true, "users": []map[string]any{{"username": "u"}}}}
		}), 422, "routing.basicAuth.users[0].password"},
		{"duplicate name", redirectSite("taken", 0), http.StatusConflict, "name"},
	}
	for _, tc := range cases {
		rec := e.do(http.MethodPost, "/api/sites", tc.body, admin...)
		if rec.Code != tc.code {
			t.Errorf("%s: status %d, want %d (%s)", tc.name, rec.Code, tc.code, strings.TrimSpace(rec.Body.String()))
			continue
		}
		if tc.field != "" {
			if f := decodeJSON[map[string]string](t, rec)["field"]; f != tc.field {
				t.Errorf("%s: field = %q, want %q", tc.name, f, tc.field)
			}
		}
	}
	if n := len(e.c.Sites()); n != 1 {
		t.Errorf("%d sites exist after invalid creates, want 1", n)
	}
	expect(t, e.do(http.MethodPut, "/api/sites/does-not-exist", redirectSite("x", 0), admin...), http.StatusNotFound)
}

func TestSiteBasicAuthPasswordIsHashedAndHidden(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	body := redirectSite("guarded", 0)
	body["routing"] = map[string]any{"basicAuth": map[string]any{"enabled": true,
		"users": []map[string]any{{"username": "guest", "password": "guest-password-123"}}}}
	s := e.createSite(admin, body)
	rec := e.do(http.MethodGet, "/api/sites/"+s.ID, nil, admin...)
	if strings.Contains(rec.Body.String(), "guest-password-123") || strings.Contains(rec.Body.String(), "$2a$") {
		t.Errorf("basic auth credentials exposed: %s", rec.Body)
	}
	stored, _ := e.c.Site(s.ID)
	if u := stored.Routing.BasicAuth.Users[0]; u.Password != "" || !strings.HasPrefix(u.PasswordHash, "$2") {
		t.Errorf("stored basic auth user = %+v", u)
	}
	// Updating without a password keeps the existing hash.
	got := decodeJSON[map[string]any](t, rec)
	delete(got, "status")
	expect(t, e.do(http.MethodPut, "/api/sites/"+s.ID, got, admin...), http.StatusOK)
	updated, _ := e.c.Site(s.ID)
	if updated.Routing.BasicAuth.Users[0].PasswordHash != stored.Routing.BasicAuth.Users[0].PasswordHash {
		t.Error("the basic auth hash changed on an update without a new password")
	}
}

func TestSiteStartStopServesTraffic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	port := freePort(t)
	s := e.createSite(admin, redirectSite("live", port))

	e.user("op", model.RoleOperator, false)
	op := session(e.login("op"))
	e.user("view", model.RoleViewer, false)
	viewer := session(e.login("view"))

	expect(t, e.do(http.MethodPost, "/api/sites/"+s.ID+"/start", nil, viewer...), http.StatusForbidden)

	rec := e.do(http.MethodPost, "/api/sites/"+s.ID+"/start", nil, op...)
	expect(t, rec, http.StatusOK)
	if st := decodeJSON[model.SiteStatus](t, rec); st.State != model.StateRunning || st.SiteID != s.ID {
		t.Fatalf("status after start = %+v", st)
	}

	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/some/path?q=1", port))
	if err != nil {
		t.Fatalf("request to the started site: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently || !strings.HasPrefix(resp.Header.Get("Location"), "https://example.com/some/path") {
		t.Errorf("site answered %d Location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}

	rec = e.do(http.MethodGet, "/api/sites/"+s.ID+"/status", nil, viewer...)
	expect(t, rec, http.StatusOK)
	if st := decodeJSON[model.SiteStatus](t, rec); st.State != model.StateRunning || st.Traffic.Requests < 1 {
		t.Errorf("status = %+v, want running with the request counted", st)
	}
	rec = e.do(http.MethodGet, "/metrics", nil, viewer...)
	expect(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `nodehoster_site_up{site="live",type="redirect"} 1`) {
		t.Errorf("metrics lack the running site:\n%s", rec.Body)
	}

	rec = e.do(http.MethodPost, "/api/sites/"+s.ID+"/stop", nil, op...)
	expect(t, rec, http.StatusOK)
	if st := decodeJSON[model.SiteStatus](t, rec); st.State != model.StateStopped {
		t.Errorf("status after stop = %+v", st)
	}
	if resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port)); err == nil {
		resp.Body.Close()
		t.Errorf("stopped site still answers: %d", resp.StatusCode)
	}

	rec = e.do(http.MethodGet, "/api/events?siteId="+s.ID, nil, viewer...)
	expect(t, rec, http.StatusOK)
	evs := decodeJSON[[]model.Event](t, rec)
	var types []string
	for _, ev := range evs {
		types = append(types, ev.Type)
	}
	if !contains(types, "site.started") || !contains(types, "site.stopped") {
		t.Errorf("events = %v", types)
	}
	if !contains(e.auditActions(), "op:site.start") || !contains(e.auditActions(), "op:site.stop") {
		t.Errorf("start/stop not audited: %v", e.auditActions())
	}
}

// ---- push webhooks

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func TestVerifyWebhook(t *testing.T) {
	t.Parallel()
	body := []byte(`{"ref":"refs/heads/main"}`)
	const secret = "webhook-secret"
	good := sign(secret, body)
	cases := []struct {
		name   string
		header map[string]string
		query  string
		want   bool
	}{
		{"github", map[string]string{"X-Hub-Signature-256": "sha256=" + good}, "", true},
		{"github wrong", map[string]string{"X-Hub-Signature-256": "sha256=" + sign("other", body)}, "", false},
		{"github sha1 prefix", map[string]string{"X-Hub-Signature-256": "sha1=" + good}, "", false},
		{"gitea", map[string]string{"X-Gitea-Signature": good}, "", true},
		{"gogs", map[string]string{"X-Gogs-Signature": good}, "", true},
		{"gitlab", map[string]string{"X-Gitlab-Token": secret}, "", true},
		{"gitlab wrong", map[string]string{"X-Gitlab-Token": "guess"}, "", false},
		{"query", nil, "?secret=" + secret, true},
		{"query wrong", nil, "?secret=nope", false},
		{"bad signature beats good token", map[string]string{"X-Hub-Signature-256": "sha256=00", "X-Gitlab-Token": secret}, "", false},
		{"nothing", nil, "", false},
		{"empty signature", map[string]string{"X-Hub-Signature-256": "sha256="}, "", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodPost, "/hooks/deploy/x"+tc.query, bytes.NewReader(body))
		for k, v := range tc.header {
			r.Header.Set(k, v)
		}
		if got := verifyWebhook(r, body, secret); got != tc.want {
			t.Errorf("%s: verifyWebhook = %v, want %v", tc.name, got, tc.want)
		}
	}
	// A signature over a different body is rejected.
	r := httptest.NewRequest(http.MethodPost, "/hooks/deploy/x", nil)
	r.Header.Set("X-Hub-Signature-256", "sha256="+good)
	if verifyWebhook(r, []byte(`{"ref":"refs/heads/evil"}`), secret) {
		t.Error("signature accepted for a tampered body")
	}
}

func TestWebhookDeployEndpoint(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	const secret = "push-secret"
	withHook := redirectSite("hooked", 0)
	withHook["deploy"] = map[string]any{"webhookSecret": secret, "git": map[string]any{"repo": "https://example.invalid/x.git", "branch": "main"}}
	hooked := e.createSite(admin, withHook)
	plain := e.createSite(admin, redirectSite("no-hook", 0))

	post := func(id string, body []byte, sig string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/hooks/deploy/"+id, bytes.NewReader(body))
		if sig != "" {
			req.Header.Set("X-Hub-Signature-256", "sha256="+sig)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	body := []byte(`{"ref":"refs/heads/feature"}`)

	expect(t, post("unknown-site", body, sign(secret, body)), http.StatusNotFound)
	// A site without a secret does not accept webhooks at all, not even
	// with an empty-key signature.
	expect(t, post(plain.ID, body, sign("", body)), http.StatusNotFound)
	expect(t, post(hooked.ID, body, ""), http.StatusUnauthorized)
	expect(t, post(hooked.ID, body, sign("wrong", body)), http.StatusUnauthorized)
	if !contains(e.auditActions(), "webhook:webhook.rejected") {
		t.Errorf("rejected webhook not audited: %v", e.auditActions())
	}

	// A valid push to another branch is acknowledged but ignored.
	rec := post(hooked.ID, body, sign(secret, body))
	expect(t, rec, http.StatusOK)
	if got := decodeJSON[map[string]string](t, rec); got["status"] != "ignored" {
		t.Errorf("response = %v", got)
	}
	if list, _ := e.c.Store.ListDeployments(context.Background(), hooked.ID, 10); len(list) != 0 {
		t.Errorf("an ignored push started %d deployments", len(list))
	}
}

func TestWebhookWithUndecryptableSecretFailsClosed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	// A secret sealed with another server's master key (as after a restore
	// onto a different machine) is stored as-is and cannot be opened here.
	otherBox, err := secrets.Open(filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	foreign, _ := otherBox.Seal("their-secret")
	body := redirectSite("restored", 0)
	body["deploy"] = map[string]any{"webhookSecret": foreign}
	s := e.createSite(admin, body)

	payload := []byte(`{}`)
	for _, key := range []string{"", "their-secret", foreign} {
		req := httptest.NewRequest(http.MethodPost, "/hooks/deploy/"+s.ID, bytes.NewReader(payload))
		req.Header.Set("X-Hub-Signature-256", "sha256="+sign(key, payload))
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		expect(t, rec, http.StatusServiceUnavailable)
	}
}

// ---- deployments through the API

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, body)
	}
	zw.Close()
	return buf.Bytes()
}

func TestZipDeployValidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	s := e.createSite(admin, map[string]any{"name": "static", "type": "static", "static": map[string]any{"root": "."}})
	e.user("op", model.RoleOperator, false)
	opCk := e.login("op")
	op := session(opCk)
	e.user("view", model.RoleViewer, false)
	viewer := session(e.login("view"))
	path := "/api/sites/" + s.ID + "/deploy/zip"
	archive := zipBytes(t, map[string]string{"index.html": "hi"})

	expect(t, e.upload(path, "file", "site.zip", archive, viewer...), http.StatusForbidden)
	expect(t, e.upload(path, "file", "site.zip", archive, withCookie(opCk)), http.StatusForbidden)
	expect(t, e.upload(path, "file", "site.tar.gz", archive, op...), http.StatusBadRequest)
	expect(t, e.upload(path, "", "", nil, op...), http.StatusBadRequest)
	expect(t, e.upload("/api/sites/missing/deploy/zip", "file", "site.zip", archive, op...), http.StatusNotFound)
	if list, _ := e.c.Store.ListDeployments(context.Background(), s.ID, 10); len(list) != 0 {
		t.Errorf("rejected uploads created %d deployments", len(list))
	}
	if entries, _ := os.ReadDir(e.c.Paths.Tmp); len(entries) != 0 {
		t.Errorf("rejected uploads left %d files in tmp", len(entries))
	}
	// git deploy without a repository is a client error.
	expect(t, e.do(http.MethodPost, "/api/sites/"+s.ID+"/deploy/git", nil, op...), http.StatusBadRequest)
}

// waitDeployment polls the API until a deployment is no longer running and
// the deployer has released it.
func (e *env) waitDeployment(siteID, depID string, auth []opt) model.Deployment {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		rec := e.do(http.MethodGet, "/api/sites/"+siteID+"/deployments", nil, auth...)
		expect(e.t, rec, http.StatusOK)
		for _, d := range decodeJSON[[]model.Deployment](e.t, rec) {
			if d.ID == depID && d.Status != "running" {
				_, _, _, cancel, running := e.c.Deploy.Subscribe(depID)
				cancel()
				if !running {
					time.Sleep(20 * time.Millisecond) // log file closes right after
					return d
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("deployment %s did not finish", depID)
	return model.Deployment{}
}

func TestZipDeployAndRollbackThroughAPI(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	s := e.createSite(admin, map[string]any{"name": "static", "type": "static", "static": map[string]any{"root": "."}})
	other := e.createSite(admin, map[string]any{"name": "other", "type": "static", "static": map[string]any{"root": "."}})
	e.user("op", model.RoleOperator, false)
	op := session(e.login("op"))

	deploy := func(files map[string]string) model.Deployment {
		rec := e.upload("/api/sites/"+s.ID+"/deploy/zip", "file", "Site.ZIP", zipBytes(t, files), op...)
		expect(t, rec, http.StatusAccepted)
		dep := decodeJSON[model.Deployment](t, rec)
		if dep.ID == "" || dep.SiteID != s.ID || dep.Source != "zip" || dep.User != "op" {
			t.Fatalf("accepted deployment = %+v", dep)
		}
		return e.waitDeployment(s.ID, dep.ID, op)
	}

	v1 := deploy(map[string]string{"build/index.html": "v1"})
	if v1.Status != "succeeded" {
		t.Fatalf("v1 = %+v", v1)
	}
	v2 := deploy(map[string]string{"build/index.html": "v2"})
	if v2.Status != "succeeded" {
		t.Fatalf("v2 = %+v", v2)
	}
	site, _ := e.c.Site(s.ID)
	if site.ActiveRelease != v2.ID {
		t.Fatalf("active release = %q, want %q", site.ActiveRelease, v2.ID)
	}
	if b, err := os.ReadFile(filepath.Join(v2.ReleaseDir, "index.html")); err != nil || string(b) != "v2" {
		t.Errorf("v2 release content = %q, %v", b, err)
	}

	rec := e.do(http.MethodGet, "/api/sites/"+s.ID+"/deployments/"+v2.ID+"/log", nil, op...)
	expect(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "extracted 1 files") {
		t.Errorf("deployment log = %s", rec.Body)
	}

	// Roll back to v1; a deployment cannot be activated through another site.
	expect(t, e.do(http.MethodPost, "/api/sites/"+other.ID+"/deployments/"+v1.ID+"/activate", nil, op...), http.StatusNotFound)
	rec = e.do(http.MethodPost, "/api/sites/"+s.ID+"/deployments/"+v1.ID+"/activate", nil, op...)
	expect(t, rec, http.StatusOK)
	site, _ = e.c.Site(s.ID)
	if site.ActiveRelease != v1.ID {
		t.Errorf("active release after rollback = %q, want %q", site.ActiveRelease, v1.ID)
	}
	if !contains(e.auditActions(), "op:site.rollback") {
		t.Errorf("rollback not audited: %v", e.auditActions())
	}

	// A zip-slip archive fails and writes nothing outside the release.
	bad := deploy(map[string]string{"index.html": "x", "../../../../escaped.txt": "pwned"})
	if bad.Status != "failed" || !strings.Contains(bad.Message, "escapes") {
		t.Errorf("zip-slip deployment = %+v", bad)
	}
	for _, p := range []string{filepath.Join(e.root, "escaped.txt"), filepath.Join(e.c.Paths.Sites, "escaped.txt"),
		filepath.Join(e.c.Paths.Sites, s.ID, "escaped.txt")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("zip-slip wrote %s", p)
		}
	}
	site, _ = e.c.Site(s.ID)
	if site.ActiveRelease != v1.ID {
		t.Errorf("a failed deployment changed the active release to %q", site.ActiveRelease)
	}
}

func TestDeploymentLogCannotEscapeLogFolder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	s := e.createSite(admin, redirectSite("logs", 0))
	siteDir := filepath.Join(e.c.Paths.Sites, s.ID)
	os.MkdirAll(filepath.Join(siteDir, "deploy-logs"), 0o750)
	os.WriteFile(filepath.Join(siteDir, "secret.log"), []byte("TOP-SECRET"), 0o640)
	os.WriteFile(filepath.Join(e.c.Paths.Sites, "secret.log"), []byte("TOP-SECRET"), 0o640)

	for _, dep := range []string{"..%2Fsecret", "..%2F..%2Fsecret", "%2E%2E%2Fsecret", "..%5Csecret", "secret"} {
		rec := e.do(http.MethodGet, "/api/sites/"+s.ID+"/deployments/"+dep+"/log", nil, admin...)
		if strings.Contains(rec.Body.String(), "TOP-SECRET") {
			t.Errorf("deployment id %q read a file outside deploy-logs", dep)
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("deployment id %q: status %d, want 404", dep, rec.Code)
		}
	}
}

// ---- settings and backup

func TestSettingsMaskSecretsAndValidate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()

	rec := e.do(http.MethodGet, "/api/settings", nil, admin...)
	expect(t, rec, http.StatusOK)
	s := decodeJSON[map[string]any](t, rec)
	acme := s["acme"].(map[string]any)
	acme["eabHmac"] = "hmac-secret-value"
	s["dnsProviders"] = []map[string]any{{"name": "cf", "provider": "cloudflare", "credentials": map[string]string{"CF_DNS_API_TOKEN": "cf-token-value"}}}
	s["webhooks"] = []map[string]any{{"name": "ops", "url": "https://hooks.example.invalid/x", "enabled": false}}

	rec = e.do(http.MethodPut, "/api/settings", s, admin...)
	expect(t, rec, http.StatusOK)
	for _, secret := range []string{"hmac-secret-value", "cf-token-value"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("settings response leaks %q", secret)
		}
	}
	saved := decodeJSON[model.Settings](t, rec)
	if saved.ACME.EABHMAC != secrets.Mask || saved.DNSProviders[0].Credentials["CF_DNS_API_TOKEN"] != secrets.Mask {
		t.Errorf("secrets not masked: %+v %+v", saved.ACME, saved.DNSProviders)
	}
	if saved.Webhooks[0].ID == "" || saved.DNSProviders[0].ID == "" {
		t.Error("ids not assigned")
	}
	cur := e.c.Settings()
	if cur.ACME.EABHMAC == "hmac-secret-value" || e.c.Box.MustUnseal(cur.ACME.EABHMAC) != "hmac-secret-value" {
		t.Error("EAB HMAC not sealed at rest")
	}

	// Round-tripping the masked document keeps the secrets.
	rec = e.do(http.MethodGet, "/api/settings", nil, admin...)
	expect(t, e.do(http.MethodPut, "/api/settings", rec.Body.String(), admin...), http.StatusOK)
	cur = e.c.Settings()
	if e.c.Box.MustUnseal(cur.ACME.EABHMAC) != "hmac-secret-value" ||
		e.c.Box.MustUnseal(cur.DNSProviders[0].Credentials["CF_DNS_API_TOKEN"]) != "cf-token-value" {
		t.Error("masked round trip lost a secret")
	}

	// Validation.
	for _, tc := range []struct {
		mutate func(m map[string]any)
		field  string
	}{
		{func(m map[string]any) { m["portRangeStart"] = 80 }, "portRangeStart"},
		{func(m map[string]any) { m["portRangeEnd"] = m["portRangeStart"] }, "portRangeStart"},
		{func(m map[string]any) { m["acme"].(map[string]any)["email"] = "not-an-email" }, "acme.email"},
		{func(m map[string]any) {
			m["webhooks"] = []map[string]any{{"name": "x", "url": "file:///etc/passwd", "enabled": true}}
		}, "webhooks[0].url"},
	} {
		m := decodeJSON[map[string]any](t, e.do(http.MethodGet, "/api/settings", nil, admin...))
		tc.mutate(m)
		rec := e.do(http.MethodPut, "/api/settings", m, admin...)
		expect(t, rec, http.StatusUnprocessableEntity)
		if f := decodeJSON[map[string]string](t, rec)["field"]; f != tc.field {
			t.Errorf("field = %q, want %q", f, tc.field)
		}
	}
	if e.c.Settings().PortRangeStart < 1024 {
		t.Error("an invalid settings update was applied")
	}
}

func TestBackupDoesNotExposePlaintextSecrets(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	body := redirectSite("secretive", 0)
	body["deploy"] = map[string]any{"webhookSecret": "backup-hook-secret", "git": map[string]any{"repo": "https://x.invalid/r.git", "token": "backup-git-token"}}
	e.createSite(admin, body)

	rec := e.do(http.MethodGet, "/api/backup", nil, admin...)
	expect(t, rec, http.StatusOK)
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	for _, secret := range []string{"backup-hook-secret", "backup-git-token"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("backup contains plaintext %q", secret)
		}
	}
	if !strings.Contains(rec.Body.String(), `"secretive"`) {
		t.Error("backup lacks the site")
	}
	// A non-backup upload is refused.
	expect(t, e.upload("/api/restore", "file", "b.json", []byte(`{"hello":"world"}`), admin...), http.StatusBadRequest)
	expect(t, e.upload("/api/restore", "file", "b.json", []byte(`not json`), admin...), http.StatusBadRequest)
}

func TestSafeName(t *testing.T) {
	t.Parallel()
	if got := safeName("a\"b\\c/d\r\ne"); strings.ContainsAny(got, "\"\\/\r\n") {
		t.Errorf("safeName = %q", got)
	}
}

func TestTailFile(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "log.txt")
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	os.WriteFile(p, []byte(b.String()), 0o640)
	got := tailFile(p, 3)
	if strings.Join(got, "|") != "line 997|line 998|line 999" {
		t.Errorf("tailFile = %q", got)
	}
	if got := tailFile(filepath.Join(t.TempDir(), "missing"), 5); got == nil || len(got) != 0 {
		t.Errorf("missing file = %#v, want empty slice", got)
	}
}
