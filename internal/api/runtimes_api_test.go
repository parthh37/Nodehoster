package api

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/runtimes"
)

func runtimeSite(name, root, rt string, node map[string]any) map[string]any {
	n := map[string]any{"appRoot": root, "runtime": rt}
	for k, v := range node {
		n[k] = v
	}
	return map[string]any{"name": name, "type": "node", "autoStart": false, "node": n}
}

func TestRuntimeSites(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	root := t.TempDir()

	// A site created before runtimes existed (no runtime) is Node.js.
	old := e.createSite(admin, map[string]any{"name": "legacy", "type": "node", "autoStart": false,
		"node": map[string]any{"appRoot": root, "script": "server.js"}})
	if old.Node.Runtime != model.RuntimeNode || old.Deploy.InstallCommand != "npm ci --omit=dev" {
		t.Fatalf("legacy site: runtime %q install %q", old.Node.Runtime, old.Deploy.InstallCommand)
	}

	py := e.createSite(admin, runtimeSite("api", root, "python", map[string]any{
		"runtimeVersion": "3.12", "python": map[string]any{"server": "uvicorn", "app": "main:app"}}))
	if py.Node.Python == nil || py.Node.Python.Venv != ".venv" || py.Deploy.InstallCommand != "python -m pip install -r requirements.txt" {
		t.Fatalf("python site: %+v %+v", py.Node.Python, py.Deploy)
	}
	// The runtime's settings round-trip through an update.
	rec := e.do(http.MethodGet, "/api/sites/"+py.ID, nil, admin...)
	expect(t, rec, http.StatusOK)
	got := decodeJSON[siteResp](t, rec)
	if got.Node.RuntimeVersion != "3.12" || got.Node.Python.Server != "uvicorn" || got.Node.Python.App != "main:app" {
		t.Fatalf("stored %+v", got.Node)
	}

	for _, c := range []struct {
		name, field string
		node        map[string]any
	}{
		{"unknown runtime", "node.runtime", map[string]any{"runtime": "ruby", "script": "app.rb"}},
		{"python with an npm script", "node.npmScript", map[string]any{"runtime": "python", "npmScript": "start"}},
		{"python server without an app", "node.python.app", map[string]any{"runtime": "python", "python": map[string]any{"server": "waitress"}}},
		{"deno version", "node.runtimeVersion", map[string]any{"runtime": "deno", "script": "main.ts", "runtimeVersion": "canary"}},
		{"dotnet without an app", "node.script", map[string]any{"runtime": "dotnet"}},
	} {
		body := runtimeSite("bad", root, "", c.node)
		rec := e.do(http.MethodPost, "/api/sites", body, admin...)
		expect(t, rec, http.StatusUnprocessableEntity)
		if f := decodeJSON[map[string]string](t, rec)["field"]; f != c.field {
			t.Errorf("%s: field %q, want %q", c.name, f, c.field)
		}
	}

	// A pinned Bun version cannot be removed while a site uses it.
	e.createSite(admin, runtimeSite("bun-app", root, "bun", map[string]any{"script": "index.ts", "runtimeVersion": "1.1.30"}))
	rec = e.do(http.MethodDelete, "/api/runtimes/bun/versions/1.1.30", nil, admin...)
	expect(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "bun-app") {
		t.Fatalf("conflict does not name the site: %s", rec.Body)
	}
	// Starting a site whose runtime is missing says what to do, in the
	// site's log (instances start in the background).
	s := e.createSite(admin, runtimeSite("deno-app", root, "deno", map[string]any{"script": "main.ts", "runtimeVersion": "2.0.0"}))
	expect(t, e.do(http.MethodPost, "/api/sites/"+s.ID+"/start", nil, admin...), http.StatusOK)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(e.do(http.MethodGet, "/api/sites/"+s.ID+"/logs", nil, admin...).Body.String(), "Deno 2.0.0 is not installed") {
		if time.Now().After(deadline) {
			t.Fatal("the site's log does not say the runtime is missing")
		}
		time.Sleep(50 * time.Millisecond)
	}
	expect(t, e.do(http.MethodPost, "/api/sites/"+s.ID+"/stop", nil, admin...), http.StatusOK)
}

func TestRuntimeEndpoints(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()

	rec := e.do(http.MethodGet, "/api/runtimes", nil, admin...)
	expect(t, rec, http.StatusOK)
	rep := decodeJSON[runtimes.Report](t, rec)
	if rep.Bun.Installed == nil || rep.Deno.Installed == nil {
		t.Fatalf("report %s", rec.Body)
	}
	expect(t, e.do(http.MethodPost, "/api/runtimes/refresh", nil, admin...), http.StatusOK)

	// Only Bun and Deno are installed by NodeHoster.
	for _, rt := range []string{"python", "dotnet", "node"} {
		expect(t, e.do(http.MethodPost, "/api/runtimes/"+rt+"/versions", map[string]string{"version": "3.12.0"}, admin...), http.StatusNotFound)
		expect(t, e.do(http.MethodGet, "/api/runtimes/"+rt+"/available", nil, admin...), http.StatusNotFound)
	}
	rec = e.do(http.MethodPost, "/api/runtimes/bun/versions", map[string]string{"version": "latest"}, admin...)
	expect(t, rec, http.StatusBadRequest)
	expect(t, e.do(http.MethodDelete, "/api/runtimes/deno/versions/2.1.0", nil, admin...), http.StatusBadRequest) // not installed

	// Server defaults are settings, checked like a site's version.
	rec = e.do(http.MethodGet, "/api/settings", nil, admin...)
	expect(t, rec, http.StatusOK)
	settings := decodeJSON[map[string]any](t, rec)
	settings["runtimes"] = map[string]any{"python": "python3"}
	rec = e.do(http.MethodPut, "/api/settings", settings, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "runtimes.python" {
		t.Fatalf("field %q", f)
	}
	settings["runtimes"] = map[string]any{"python": "3.12", "bun": "1.1.30"}
	expect(t, e.do(http.MethodPut, "/api/settings", settings, admin...), http.StatusOK)
	if d := e.c.Settings().Runtimes; d.Python != "3.12" || d.Bun != "1.1.30" {
		t.Fatalf("defaults %+v", d)
	}
	// The default counts as a use.
	rec = e.do(http.MethodDelete, "/api/runtimes/bun/versions/1.1.30", nil, admin...)
	expect(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "server default") {
		t.Fatalf("body %s", rec.Body)
	}

	// Operators and viewers read the catalog; only administrators install.
	e.user("ops", model.RoleOperator, false)
	ops := session(e.login("ops"))
	expect(t, e.do(http.MethodGet, "/api/runtimes", nil, ops...), http.StatusOK)
	expect(t, e.do(http.MethodPost, "/api/runtimes/bun/versions", map[string]string{"version": "1.1.30"}, ops...), http.StatusForbidden)
	expect(t, e.do(http.MethodDelete, "/api/runtimes/bun/versions/1.1.30", nil, ops...), http.StatusForbidden)
	expect(t, e.do(http.MethodPost, "/api/runtimes/refresh", nil, ops...), http.StatusForbidden)

	// A site-scoped user sees the catalog (the site settings need it) but
	// nothing server-wide.
	a := e.createSite(admin, redirectSite("site-a", 0))
	e.scoped("agency", grant(a.ID, model.RoleOperator))
	agency := session(e.login("agency"))
	expect(t, e.do(http.MethodGet, "/api/runtimes", nil, agency...), http.StatusOK)
	expect(t, e.do(http.MethodGet, "/api/runtimes/bun/available", nil, agency...), http.StatusForbidden)
	expect(t, e.do(http.MethodPost, "/api/runtimes/refresh", nil, agency...), http.StatusForbidden)

	// Removing an installed version is audited.
	dir := filepath.Join(e.root, "runtimes", "deno", "2.1.0")
	exe := "deno"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, exe), []byte("x"), 0o755)
	rec = e.do(http.MethodGet, "/api/runtimes", nil, admin...)
	if rep := decodeJSON[runtimes.Report](t, rec); len(rep.Deno.Installed) != 1 || rep.Deno.Installed[0].Version != "2.1.0" {
		t.Fatalf("installed deno: %s", rec.Body)
	}
	expect(t, e.do(http.MethodDelete, "/api/runtimes/deno/versions/2.1.0", nil, admin...), http.StatusNoContent)
	if !contains(e.auditActions(), "root:runtime.remove") {
		t.Fatalf("audit %v", e.auditActions())
	}
}
