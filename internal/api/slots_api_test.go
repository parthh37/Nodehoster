package api

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
)

// slotServer answers with its version and DB; /slow takes 2 s and
// /missing is a 404.
func slotServer(version string) string {
	return `const http = require('http');
http.createServer((req, res) => {
  if (req.url === '/slow') return setTimeout(() => res.end('slow ` + version + `'), 2000);
  if (req.url === '/missing') { res.statusCode = 404; return res.end('no'); }
  res.end('` + version + ` ' + process.env.DB);
}).listen(process.env.PORT);
`
}

func appZip(t *testing.T, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("server.js")
	io.WriteString(w, slotServer(version))
	zw.Close()
	return buf.Bytes()
}

// slotEnv is an API environment whose sites run the Node.js on PATH.
func slotEnv(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	e := newEnv(t)
	s := e.c.Settings()
	s.DefaultNodeVersion = "" // node on PATH
	s.PortRangeStart, s.PortRangeEnd = 23000, 23999
	if _, err := e.c.UpdateSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return e
}

func slotSiteBody(root string, port int) map[string]any {
	return map[string]any{
		"name": "shop", "type": "node", "autoStart": false,
		"bindings": []map[string]any{
			{"protocol": "http", "ip": "127.0.0.1", "port": port, "host": "www.test"},
			{"protocol": "http", "ip": "127.0.0.1", "port": port, "host": "staging.test", "slot": "staging"},
		},
		"node": map[string]any{"appRoot": root, "script": "server.js", "instances": 2, "shutdownTimeoutSec": 3,
			"env": []map[string]any{{"name": "DB", "value": "prod", "slotSetting": true}}},
		"slots": []map[string]any{{"name": "staging", "instances": 1,
			"env":    []map[string]any{{"name": "DB", "value": "staging"}, {"name": "TOKEN", "value": "s3cret", "secret": true}},
			"warmup": map[string]any{"paths": []string{"/"}, "timeoutSec": 20}}},
	}
}

// through asks the proxy for host.
func through(t *testing.T, port int, host, path string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s%s: %v", host, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func (e *env) deploy(opts []opt, siteID, slot, version string) model.Deployment {
	e.t.Helper()
	path := "/api/sites/" + siteID + "/deploy/zip"
	if slot != "" {
		path += "?slot=" + slot
	}
	rec := e.upload(path, "file", "app.zip", appZip(e.t, version), opts...)
	expect(e.t, rec, http.StatusAccepted)
	d := e.waitDeployment(siteID, decodeJSON[model.Deployment](e.t, rec).ID, opts)
	if d.Status != "succeeded" {
		log, _ := e.c.Deploy.Log(siteID, d.ID)
		e.t.Fatalf("deployment %s: %s\n%s", d.Status, d.Message, log)
	}
	return d
}

// waitSwap waits until no swap runs and returns how the last one ended.
func (e *env) waitSwap(opts []opt, siteID string) model.SwapResult {
	e.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		v := decodeJSON[model.SlotsView](e.t, e.do(http.MethodGet, "/api/sites/"+siteID+"/slots", nil, opts...))
		if v.Swap == nil && v.LastSwap != nil {
			return *v.LastSwap
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatal("the swap did not finish")
	return model.SwapResult{}
}

// TestSlotsEndToEnd deploys to production and to a staging slot, tests
// staging on its own host name, swaps it in through the API and back,
// all through the real proxy and Node.js processes.
func TestSlotsEndToEnd(t *testing.T) {
	// Before the environment: removed after Shutdown stopped the processes
	// running from it (Windows cannot delete files in use).
	root := t.TempDir()
	e := slotEnv(t)
	admin := e.adminSession()
	e.user("ops", model.RoleOperator, false)
	ops := session(e.login("ops"))
	e.user("watcher", model.RoleViewer, false)
	viewer := session(e.login("watcher"))
	os.WriteFile(filepath.Join(root, "server.js"), []byte(slotServer("v0")), 0o644)
	port := freePort(t)
	body := slotSiteBody(root, port)
	// A secret both production and the slot set, to different values.
	node := body["node"].(map[string]any)
	node["env"] = append(node["env"].([]map[string]any), map[string]any{"name": "API_KEY", "value": "prod-key", "secret": true})
	slot := body["slots"].([]map[string]any)[0]
	slot["env"] = append(slot["env"].([]map[string]any), map[string]any{"name": "API_KEY", "value": "staging-key", "secret": true})
	site := e.createSite(admin, body)
	id := site.ID

	// The slot's secret is sealed at rest and masked in the API.
	if got := site.Slots[0].Env[1].Value; got != secrets.Mask {
		t.Fatalf("slot secret in the API = %q", got)
	}
	stored, _ := e.c.Site(id)
	if v := stored.Slots[0].Env[1].Value; !secrets.IsSealed(v) || e.c.Box.MustUnseal(v) != "s3cret" {
		t.Fatalf("slot secret stored as %q", v)
	}

	expect(t, e.do(http.MethodPost, "/api/sites/"+id+"/start", nil, admin...), http.StatusOK)
	v1 := e.deploy(ops, id, "", "v1")
	v2 := e.deploy(ops, id, "staging", "v2")
	if v2.Slot != "staging" || v1.Slot != "" {
		t.Fatalf("deployment slots: %q %q", v1.Slot, v2.Slot)
	}
	waitUntil(t, "production on v1", func() bool { return through(t, port, "www.test", "/") == "v1 prod" })
	// Deploying to the slot started it, on its own host name and settings.
	waitUntil(t, "staging on v2", func() bool { return through(t, port, "staging.test", "/") == "v2 staging" })

	view := decodeJSON[model.SlotsView](t, e.do(http.MethodGet, "/api/sites/"+id+"/slots", nil, viewer...))
	if len(view.Slots) != 2 || view.Slots[0].Name != "production" || view.Slots[0].Release != v1.ID ||
		view.Slots[1].Release != v2.ID || view.Slots[1].Status.State != model.StateRunning || len(view.Slots[1].Bindings) != 1 {
		t.Fatalf("slots = %+v", view)
	}
	deps := decodeJSON[[]model.Deployment](t, e.do(http.MethodGet, "/api/sites/"+id+"/deployments?slot=staging", nil, viewer...))
	if len(deps) != 1 || deps[0].ID != v2.ID {
		t.Fatalf("staging history = %+v", deps)
	}
	deps = decodeJSON[[]model.Deployment](t, e.do(http.MethodGet, "/api/sites/"+id+"/deployments?slot=production", nil, viewer...))
	if len(deps) != 1 || deps[0].ID != v1.ID {
		t.Fatalf("production history = %+v", deps)
	}

	pv := decodeJSON[model.SwapPreview](t, e.do(http.MethodGet, "/api/sites/"+id+"/slots/staging/swap", nil, viewer...))
	if len(pv.Blockers) != 0 || pv.SlotRelease != v2.ID || pv.ProductionRelease != v1.ID ||
		!strings.Contains(strings.Join(pv.Changes, "\n"), "DB") || strings.Contains(strings.Join(pv.Changes, "\n"), "s3cret") {
		t.Fatalf("preview = %+v", pv)
	}
	// Which secret values differ is only told to an administrator.
	if ch := strings.Join(pv.Changes, "\n"); strings.Contains(ch, "API_KEY") || !strings.Contains(ch, "1 secret variable(s)") {
		t.Fatalf("viewer's preview names the secret: %q", ch)
	}
	pv = decodeJSON[model.SwapPreview](t, e.do(http.MethodGet, "/api/sites/"+id+"/slots/staging/swap", nil, admin...))
	if ch := strings.Join(pv.Changes, "\n"); !strings.Contains(ch, "API_KEY") || strings.Contains(ch, "key") {
		t.Fatalf("administrator's preview: %q", ch)
	}

	// Only operators swap.
	expect(t, e.do(http.MethodPost, "/api/sites/"+id+"/slots/staging/swap", nil, viewer...), http.StatusForbidden)
	expect(t, e.do(http.MethodPost, "/api/sites/"+id+"/slots/qa/swap", nil, ops...), http.StatusNotFound)
	rec := e.do(http.MethodPost, "/api/sites/"+id+"/slots/staging/swap", nil, ops...)
	expect(t, rec, http.StatusAccepted)
	if p := decodeJSON[model.SwapProgress](t, rec); p.Slot != "staging" || p.User != "ops" {
		t.Fatalf("progress = %+v", p)
	}
	res := e.waitSwap(viewer, id)
	if !res.Succeeded || res.ProductionRelease != v2.ID || res.SlotRelease != v1.ID {
		t.Fatalf("swap = %+v", res)
	}
	// Production now answers from v2 with production's settings, at once.
	for range 10 {
		if got := through(t, port, "www.test", "/"); got != "v2 prod" {
			t.Fatalf("production after the swap = %q", got)
		}
	}
	waitUntil(t, "v1 in staging with staging settings", func() bool { return through(t, port, "staging.test", "/") == "v1 staging" })

	// The swap is stored: a restart comes back with it.
	cur, _ := e.c.Store.GetSite(context.Background(), id)
	if cur.ActiveRelease != v2.ID || cur.FindSlot("staging").ActiveRelease != v1.ID {
		t.Fatalf("stored releases %s / %s", cur.ActiveRelease, cur.FindSlot("staging").ActiveRelease)
	}
	evs, _ := e.c.Store.ListEvents(context.Background(), id, 100)
	if !hasEvent(evs, events.SlotSwapped) {
		t.Fatalf("no %s event", events.SlotSwapped)
	}
	if !contains(e.auditActions(), "ops:site.slot.swap") {
		t.Fatalf("audit = %v", e.auditActions())
	}
	// Logs can be read per slot.
	lines := decodeJSON[[]model.LogLine](t, e.do(http.MethodGet, "/api/sites/"+id+"/logs?slot=staging", nil, viewer...))
	if len(lines) == 0 {
		t.Fatal("no staging log lines")
	}
	for _, l := range lines {
		if l.Slot != "staging" {
			t.Fatalf("line of slot %q in the staging log", l.Slot)
		}
	}

	// Swapping again is the rollback.
	expect(t, e.do(http.MethodPost, "/api/sites/"+id+"/slots/staging/swap", nil, ops...), http.StatusAccepted)
	if res := e.waitSwap(viewer, id); !res.Succeeded || res.ProductionRelease != v1.ID {
		t.Fatalf("swap back = %+v", res)
	}
	if got := through(t, port, "www.test", "/"); got != "v1 prod" {
		t.Fatalf("production after swapping back = %q", got)
	}
}

// TestSwapWarmupFailure: a slot that never answers its warm-up path is
// not swapped; production keeps running untouched, the slot goes back to
// its own settings, and slot.swap_failed is reported.
func TestSwapWarmupFailure(t *testing.T) {
	// Before the environment: removed after Shutdown stopped the processes
	// running from it (Windows cannot delete files in use).
	root := t.TempDir()
	e := slotEnv(t)
	admin := e.adminSession()
	os.WriteFile(filepath.Join(root, "server.js"), []byte(slotServer("v0")), 0o644)
	port := freePort(t)
	body := slotSiteBody(root, port)
	body["slots"].([]map[string]any)[0]["warmup"] = map[string]any{"paths": []string{"/missing"}, "statuses": "200-299", "timeoutSec": 5}
	site := e.createSite(admin, body)
	id := site.ID
	expect(t, e.do(http.MethodPost, "/api/sites/"+id+"/start", nil, admin...), http.StatusOK)
	v2 := e.deploy(admin, id, "staging", "v2")
	waitUntil(t, "staging", func() bool { return through(t, port, "staging.test", "/") == "v2 staging" })

	expect(t, e.do(http.MethodPost, "/api/sites/"+id+"/slots/staging/swap", nil, admin...), http.StatusAccepted)
	res := e.waitSwap(admin, id)
	if res.Succeeded || !strings.Contains(res.Message, "warm-up failed") || !strings.Contains(res.Message, "HTTP 404") {
		t.Fatalf("result = %+v", res)
	}
	if got := through(t, port, "www.test", "/"); got != "v0 prod" {
		t.Fatalf("production = %q", got)
	}
	waitUntil(t, "staging back on its settings", func() bool { return through(t, port, "staging.test", "/") == "v2 staging" })
	cur, _ := e.c.Site(id)
	if cur.ActiveRelease != "" || cur.FindSlot("staging").ActiveRelease != v2.ID {
		t.Fatalf("releases changed: %q / %q", cur.ActiveRelease, cur.FindSlot("staging").ActiveRelease)
	}
	evs, _ := e.c.Store.ListEvents(context.Background(), id, 100)
	if !hasEvent(evs, events.SlotSwapFailed) {
		t.Fatalf("no %s event", events.SlotSwapFailed)
	}
}

// TestSwapExclusive: while a swap warms up, the site's configuration,
// deployments, rollbacks and start/stop/recycle are refused with 409, and
// a second swap too.
func TestSwapExclusive(t *testing.T) {
	// Before the environment: removed after Shutdown stopped the processes
	// running from it (Windows cannot delete files in use).
	root := t.TempDir()
	e := slotEnv(t)
	admin := e.adminSession()
	os.WriteFile(filepath.Join(root, "server.js"), []byte(slotServer("v0")), 0o644)
	port := freePort(t)
	body := slotSiteBody(root, port)
	body["slots"].([]map[string]any)[0]["warmup"] = map[string]any{"paths": []string{"/slow"}, "timeoutSec": 30}
	site := e.createSite(admin, body)
	id := site.ID
	expect(t, e.do(http.MethodPost, "/api/sites/"+id+"/start", nil, admin...), http.StatusOK)
	v2 := e.deploy(admin, id, "staging", "v2")
	waitUntil(t, "staging", func() bool { return through(t, port, "staging.test", "/") == "v2 staging" })

	expect(t, e.do(http.MethodPost, "/api/sites/"+id+"/slots/staging/swap", nil, admin...), http.StatusAccepted)
	waitUntil(t, "warming", func() bool {
		v := decodeJSON[model.SlotsView](t, e.do(http.MethodGet, "/api/sites/"+id+"/slots", nil, admin...))
		return v.Swap != nil && v.Swap.Phase == model.SwapWarming
	})
	cur := decodeJSON[siteResp](t, e.do(http.MethodGet, "/api/sites/"+id, nil, admin...))
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/slots/staging/swap"},
		{http.MethodPost, "/recycle"},
		{http.MethodPost, "/stop"},
		{http.MethodPost, "/slots/staging/stop"},
		{http.MethodPost, "/deployments/" + v2.ID + "/activate"},
		{http.MethodDelete, ""},
	} {
		rec := e.do(c.method, "/api/sites/"+id+c.path, nil, admin...)
		if rec.Code != http.StatusConflict {
			t.Errorf("%s %s during a swap: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	rec := e.do(http.MethodPut, "/api/sites/"+id, cur.Site, admin...)
	expect(t, rec, http.StatusConflict)
	rec = e.upload("/api/sites/"+id+"/deploy/zip", "file", "app.zip", appZip(t, "v3"), admin...)
	expect(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "swap") {
		t.Fatalf("deploy refusal: %s", rec.Body)
	}
	if res := e.waitSwap(admin, id); !res.Succeeded {
		t.Fatalf("swap = %+v", res)
	}
	expect(t, e.do(http.MethodPost, "/api/sites/"+id+"/recycle", nil, admin...), http.StatusOK)
}

func TestSlotValidationAPI(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	root := t.TempDir()
	port := freePort(t)

	body := slotSiteBody(root, port)
	body["node"].(map[string]any)["portMode"] = "fixed"
	body["node"].(map[string]any)["fixedPort"] = freePort(t)
	body["node"].(map[string]any)["instances"] = 1
	rec := e.do(http.MethodPost, "/api/sites", body, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "node.portMode" {
		t.Fatalf("field = %q", f)
	}

	site := e.createSite(admin, slotSiteBody(root, port))
	// Nothing deployed to the slot: no swap, no deployment to a slot that
	// does not exist.
	rec = e.do(http.MethodPost, "/api/sites/"+site.ID+"/slots/staging/swap", nil, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	rec = e.upload("/api/sites/"+site.ID+"/deploy/zip?slot=qa", "file", "app.zip", []byte("x"), admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	expect(t, e.do(http.MethodGet, "/api/sites/"+site.ID+"/logs?slot=qa", nil, admin...), http.StatusNotFound)

	// A masked slot secret keeps its value; releases are NodeHoster's.
	stored, _ := e.c.Site(site.ID)
	sealed := stored.Slots[0].Env[1].Value
	upd := decodeJSON[siteResp](t, e.do(http.MethodGet, "/api/sites/"+site.ID, nil, admin...)).Site
	upd.Slots[0].ActiveRelease = "forged"
	upd.Slots[0].AutoSwap = true
	rec = e.do(http.MethodPut, "/api/sites/"+site.ID, upd, admin...)
	expect(t, rec, http.StatusOK)
	stored, _ = e.c.Site(site.ID)
	if stored.Slots[0].Env[1].Value != sealed || stored.Slots[0].ActiveRelease != "" || !stored.Slots[0].AutoSwap {
		t.Fatalf("after update: %+v", stored.Slots[0])
	}
	// A binding for a slot that does not exist.
	upd.Bindings[1].Slot = "qa"
	rec = e.do(http.MethodPut, "/api/sites/"+site.ID, upd, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "bindings[1].slot" {
		t.Fatalf("field = %q", f)
	}
}

// TestSlotSecretsInBackup: slot secrets travel through a configuration
// backup like the site's own.
func TestSlotSecretsInBackup(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	site := e.createSite(admin, slotSiteBody(t.TempDir(), freePort(t)))
	rec := e.do(http.MethodGet, "/api/backup", nil, admin...)
	expect(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatal("backup contains the slot secret in clear")
	}
	res, err := e.c.RestoreFile(context.Background(), writeTemp(t, rec.Body.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings: %v", res.Warnings)
	}
	stored, _ := e.c.Site(site.ID)
	if e.c.Box.MustUnseal(stored.Slots[0].Env[1].Value) != "s3cret" {
		t.Fatal("slot secret lost in restore")
	}
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "backup.json")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func hasEvent(list []model.Event, typ string) bool {
	for _, ev := range list {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
