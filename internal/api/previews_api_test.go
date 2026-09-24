package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
)

const previewHookSecret = "preview-hook-secret"

// previewParent creates a static site with pull request previews. Its
// repository does not exist, so preview deployments fail; the preview
// sites exist all the same.
func (e *env) previewParent(admin []opt, name string) siteResp {
	e.t.Helper()
	return e.createSite(admin, map[string]any{
		"name": name, "type": "static", "static": map[string]any{"root": "."},
		"deploy": map[string]any{
			"webhookSecret": previewHookSecret,
			"git":           map[string]any{"repo": filepath.Join(e.root, "no-such-repo"), "branch": "main"},
			"previews": map[string]any{
				"enabled": true, "hostPattern": "pr-{number}." + name + ".preview.test", "pullRequests": true,
				"protocol": "http", "ip": "127.0.0.1", "port": freePort(e.t),
				"statusToken": "status-token-secret",
				"env":         []map[string]any{{"name": "DATABASE_URL", "value": "postgres://preview-secret", "secret": true}},
			},
		},
	})
}

func prPayload(action string, number int, fork bool) []byte {
	head := `{"id":1,"full_name":"org/app"}`
	if fork {
		head = `{"id":2,"full_name":"someone/app"}`
	}
	return []byte(fmt.Sprintf(`{"action":%q,"number":%d,"pull_request":{"title":"T","user":{"login":"dev"},
	  "head":{"ref":"topic-%d","sha":"0123456789abcdef0123456789abcdef01234567","repo":%s},
	  "base":{"ref":"main","repo":{"id":1,"full_name":"org/app"}}}}`, action, number, number, head))
}

func (e *env) hook(siteID, event string, body []byte) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/deploy/"+siteID, bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", "sha256="+sign(previewHookSecret, body))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// previews waits until a site's previews are all settled (not deploying
// or deleting) and returns them.
func (e *env) previews(opts []opt, siteID string, want int) []model.PreviewView {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var list []model.PreviewView
	for time.Now().Before(deadline) {
		rec := e.do(http.MethodGet, "/api/sites/"+siteID+"/previews", nil, opts...)
		expect(e.t, rec, http.StatusOK)
		list = decodeJSON[[]model.PreviewView](e.t, rec)
		settled := len(list) == want
		for _, p := range list {
			settled = settled && p.State != model.PreviewDeploying && p.State != model.PreviewDeleting
		}
		if settled {
			return list
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("previews of %s = %+v, want %d settled", siteID, list, want)
	return nil
}

func TestPreviewWebhook(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	parent := e.previewParent(admin, "shop")

	rec := e.hook(parent.ID, "pull_request", prPayload("opened", 42, false))
	expect(t, rec, http.StatusAccepted)
	if got := decodeJSON[map[string]string](t, rec); got["action"] != "deploy" || got["preview"] != "pr:42" {
		t.Errorf("response = %v", got)
	}
	list := e.previews(admin, parent.ID, 1)
	p := list[0]
	if p.Preview.Number != 42 || p.Preview.Branch != "topic-42" || p.Preview.Host != "pr-42.shop.preview.test" || p.State != model.PreviewFailed {
		t.Errorf("preview = %+v", p)
	}
	if !contains(e.auditActions(), "webhook:preview.deploy") {
		t.Errorf("audit = %v", e.auditActions())
	}
	// A pull request event never deploys the site itself (it used to: the
	// payload has no top-level ref).
	if deps, _ := e.c.Store.ListDeployments(context.Background(), parent.ID, 10); len(deps) != 0 {
		t.Errorf("the parent was deployed %d times", len(deps))
	}

	// Ignored: labels, forks, a pull request of a site without previews.
	for _, tc := range []struct {
		body   []byte
		reason string
	}{
		{prPayload("labeled", 42, false), "nothing to deploy"},
		{prPayload("opened", 43, true), "fork"},
	} {
		rec := e.hook(parent.ID, "pull_request", tc.body)
		expect(t, rec, http.StatusOK)
		if got := decodeJSON[map[string]string](t, rec); got["status"] != "ignored" || !strings.Contains(got["reason"], tc.reason) {
			t.Errorf("response = %v, want ignored: %s", got, tc.reason)
		}
	}
	plain := redirectSite("plain", 0)
	plain["deploy"] = map[string]any{"webhookSecret": previewHookSecret, "git": map[string]any{"repo": "https://example.invalid/x.git", "branch": "main"}}
	ps := e.createSite(admin, plain)
	rec = e.hook(ps.ID, "pull_request", prPayload("opened", 1, false))
	expect(t, rec, http.StatusOK)
	if got := decodeJSON[map[string]string](t, rec); got["status"] != "ignored" || !strings.Contains(got["reason"], "not enabled") {
		t.Errorf("response = %v", got)
	}
	if deps, _ := e.c.Store.ListDeployments(context.Background(), ps.ID, 10); len(deps) != 0 {
		t.Error("a pull request deployed a site without previews")
	}
	// A bad signature is refused before anything else.
	req := httptest.NewRequest(http.MethodPost, "/hooks/deploy/"+parent.ID, bytes.NewReader(prPayload("opened", 50, false)))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", "sha256="+sign("wrong", prPayload("opened", 50, false)))
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	expect(t, rec, http.StatusUnauthorized)

	// Closing the pull request deletes the preview.
	expect(t, e.hook(parent.ID, "pull_request", prPayload("closed", 42, false)), http.StatusAccepted)
	e.previews(admin, parent.ID, 0)
	if _, err := e.c.Site(p.ID); err == nil {
		t.Error("the preview site still exists")
	}
}

// TestPreviewWebhookPushes: pushes to previewed branches make previews;
// the others go to the site's push handling as before.
func TestPreviewWebhookPushes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	parent := e.previewParent(admin, "shop")
	in := parent.Site
	in.Deploy.Previews.Branches = []string{"feature/*"}
	expect(t, e.do(http.MethodPut, "/api/sites/"+parent.ID, in, admin...), http.StatusOK)
	push := func(ref, after string) *httptest.ResponseRecorder {
		return e.hook(parent.ID, "push", []byte(fmt.Sprintf(`{"ref":%q,"after":%q}`, ref, after)))
	}
	const head = "1111111111111111111111111111111111111111"

	rec := push("refs/heads/feature/x", head)
	expect(t, rec, http.StatusAccepted)
	if got := decodeJSON[map[string]string](t, rec); got["preview"] != "branch:feature/x" || got["action"] != "deploy" {
		t.Errorf("response = %v", got)
	}
	if p := e.previews(admin, parent.ID, 1); p[0].Preview.Host != "feature-x.shop.preview.test" {
		t.Errorf("preview = %+v", p[0])
	}
	// Not previewed: ignored by the push handling, as before.
	for _, ref := range []string{"refs/heads/develop", "refs/heads/feature/über"} {
		rec = push(ref, head)
		expect(t, rec, http.StatusOK)
		if got := decodeJSON[map[string]string](t, rec); got["status"] != "ignored" || !strings.Contains(got["reason"], "push to") {
			t.Errorf("%s: response = %v", ref, got)
		}
	}
	// The production branch deploys the site itself.
	rec = push("refs/heads/main", head)
	expect(t, rec, http.StatusAccepted)
	if deps, _ := e.c.Store.ListDeployments(context.Background(), parent.ID, 10); len(deps) != 1 || deps[0].Source != "webhook" {
		t.Errorf("parent deployments = %+v", deps)
	}
	// Deleting the branch deletes its preview.
	expect(t, push("refs/heads/feature/x", "0000000000000000000000000000000000000000"), http.StatusAccepted)
	e.previews(admin, parent.ID, 0)
}

func TestPreviewEndpointsAndPermissions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	parent := e.previewParent(admin, "shop")
	other := e.createSite(admin, redirectSite("other", 0))
	expect(t, e.hook(parent.ID, "pull_request", prPayload("opened", 7, false)), http.StatusAccepted)
	p := e.previews(admin, parent.ID, 1)[0]

	opUser := e.scoped("op", grant(parent.ID, model.RoleOperator))
	e.scoped("viewer", grant(parent.ID, model.RoleViewer))
	e.scoped("stranger", grant(other.ID, model.RoleOperator))
	op, viewer, stranger := session(e.login("op")), session(e.login("viewer")), session(e.login("stranger"))

	// The operator of the parent sees the preview as a site too.
	rec := e.do(http.MethodGet, "/api/sites", nil, op...)
	expect(t, rec, http.StatusOK)
	if got := siteNames(t, rec); !slices.Equal(got, []string{"shop", "shop pr-7"}) {
		t.Errorf("sites of the parent's operator = %v", got)
	}
	for _, tc := range []struct {
		who          []opt
		method, path string
		want         int
	}{
		{op, "GET", "/api/sites/" + parent.ID + "/previews", 200},
		{op, "GET", "/api/sites/" + p.ID, 200},
		{op, "GET", "/api/sites/" + p.ID + "/deployments", 200},
		{viewer, "GET", "/api/sites/" + parent.ID + "/previews", 200},
		{viewer, "GET", "/api/sites/" + p.ID, 200},
		{viewer, "POST", "/api/sites/" + parent.ID + "/previews/" + p.ID + "/redeploy", 403},
		{viewer, "DELETE", "/api/sites/" + parent.ID + "/previews/" + p.ID, 403},
		{viewer, "POST", "/api/sites/" + p.ID + "/start", 403},
		{stranger, "GET", "/api/sites/" + parent.ID + "/previews", 404},
		{stranger, "GET", "/api/sites/" + p.ID, 404},
		{stranger, "DELETE", "/api/sites/" + parent.ID + "/previews/" + p.ID, 404},
		// A preview is reached under its own parent only.
		{admin, "POST", "/api/sites/" + other.ID + "/previews/" + p.ID + "/redeploy", 404},
		// Editing or deleting a preview as a site stays with administrators.
		{op, "DELETE", "/api/sites/" + p.ID, 403},
		// Nothing deployed: it would serve a folder that is not its own.
		{op, "POST", "/api/sites/" + p.ID + "/start", 400},
		{op, "POST", "/api/sites/" + parent.ID + "/previews/" + p.ID + "/redeploy", 202},
	} {
		rec := e.do(tc.method, tc.path, nil, tc.who...)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body)
		}
	}
	e.previews(op, parent.ID, 1)

	// A token restricted to the parent reaches its previews.
	raw, _, err := e.c.Auth.CreateRestrictedToken(context.Background(), opUser.ID, "ci", 0, model.RoleOperator, []string{parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, e.do(http.MethodGet, "/api/sites/"+p.ID, nil, withBearer(raw)), http.StatusOK)

	// Manual branch previews: the production branch is refused.
	expect(t, e.do(http.MethodPost, "/api/sites/"+parent.ID+"/previews", map[string]string{"branch": "main"}, op...), http.StatusUnprocessableEntity)
	expect(t, e.do(http.MethodPost, "/api/sites/"+parent.ID+"/previews", map[string]string{"branch": "--x"}, op...), http.StatusUnprocessableEntity)
	expect(t, e.do(http.MethodPost, "/api/sites/"+parent.ID+"/previews", map[string]string{"branch": "develop"}, viewer...), http.StatusForbidden)
	rec = e.do(http.MethodPost, "/api/sites/"+parent.ID+"/previews", map[string]string{"branch": "develop"}, op...)
	expect(t, rec, http.StatusAccepted)
	list := e.previews(op, parent.ID, 2)
	if list[0].Preview.Branch != "develop" && list[1].Preview.Branch != "develop" {
		t.Errorf("previews = %+v", list)
	}

	// The operator deletes the preview of the pull request.
	expect(t, e.do(http.MethodDelete, "/api/sites/"+parent.ID+"/previews/"+p.ID, nil, op...), http.StatusAccepted)
	e.previews(op, parent.ID, 1)
	actions := e.auditActions()
	for _, a := range []string{"op:preview.redeploy", "op:preview.create", "op:preview.delete"} {
		if !contains(actions, a) {
			t.Errorf("%s not audited: %v", a, actions)
		}
	}
}

func TestPreviewSettingsMasked(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	parent := e.previewParent(admin, "shop")
	rec := e.do(http.MethodGet, "/api/sites/"+parent.ID, nil, admin...)
	expect(t, rec, http.StatusOK)
	body := rec.Body.String()
	for _, leak := range []string{"status-token-secret", "postgres://preview-secret", "enc:v1:"} {
		if strings.Contains(body, leak) {
			t.Errorf("the site's JSON contains %q", leak)
		}
	}
	got := decodeJSON[siteResp](t, rec)
	if got.Deploy.Previews.StatusToken != secrets.Mask || got.Deploy.Previews.Env[0].Value != secrets.Mask {
		t.Errorf("previews = %+v", got.Deploy.Previews)
	}
	// Saved back as it came, the secrets are kept.
	expect(t, e.do(http.MethodPut, "/api/sites/"+parent.ID, got.Site, admin...), http.StatusOK)
	stored, _ := e.c.Site(parent.ID)
	if tok, err := e.c.Box.Unseal(stored.Deploy.Previews.StatusToken); err != nil || tok != "status-token-secret" {
		t.Errorf("status token after a masked save = %q, %v", tok, err)
	}
	// Previews need the webhook secret and the production branch.
	bad := got.Site
	bad.Deploy.Git.Branch = ""
	rec = e.do(http.MethodPut, "/api/sites/"+parent.ID, bad, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "deploy.git.branch" {
		t.Errorf("field = %q", f)
	}
}

// TestWebhookSlotLeavesPreviews: the webhook URL of a slot deploys the
// slot on pushes to the production branch only; previews are created by
// the site's own webhook URL, once.
func TestWebhookSlotLeavesPreviews(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	parent := e.previewParent(admin, "shop")
	in := parent.Site
	in.Deploy.Previews.Branches = []string{"feature/*"}
	expect(t, e.do(http.MethodPut, "/api/sites/"+parent.ID, in, admin...), http.StatusOK)
	slotHook := func(event string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/hooks/deploy/"+parent.ID+"?slot=staging", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-Hub-Signature-256", "sha256="+sign(previewHookSecret, body))
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	rec := slotHook("pull_request", prPayload("opened", 4, false))
	expect(t, rec, http.StatusOK)
	if got := decodeJSON[map[string]string](t, rec); got["status"] != "ignored" || !strings.Contains(got["reason"], "slot") {
		t.Errorf("pull request on the slot's URL = %v", got)
	}
	rec = slotHook("push", []byte(`{"ref":"refs/heads/feature/x","after":"1111111111111111111111111111111111111111"}`))
	expect(t, rec, http.StatusOK)
	if got := decodeJSON[map[string]string](t, rec); got["status"] != "ignored" {
		t.Errorf("branch push on the slot's URL = %v", got)
	}
	time.Sleep(100 * time.Millisecond)
	if list := e.previews(admin, parent.ID, 0); len(list) != 0 {
		t.Errorf("the slot's webhook made previews: %+v", list)
	}
	if deps, _ := e.c.Store.ListDeployments(context.Background(), parent.ID, 10); len(deps) != 0 {
		t.Errorf("deployments = %+v", deps)
	}
}
