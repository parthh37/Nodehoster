package api

import (
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestImportAuthorization(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a := e.createSite(admin, redirectSite("site-a", 0))
	e.user("op", model.RoleOperator, false)
	e.scoped("agency", grant(a.ID, model.RoleOperator))
	body := map[string]any{"source": "pm2", "text": `{"apps":[{"name":"x","script":"x.js","cwd":"/srv/x"}]}`}
	for _, who := range []string{"op", "agency"} {
		opts := session(e.login(who))
		expect(t, e.do(http.MethodPost, "/api/import/preview", body, opts...), http.StatusForbidden)
		expect(t, e.do(http.MethodPost, "/api/import/apply", map[string]any{"items": []any{}}, opts...), http.StatusForbidden)
	}
	expect(t, e.do(http.MethodPost, "/api/import/preview", body, admin...), http.StatusOK)
}

func TestImportPreviewAndApply(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	e.createSite(admin, redirectSite("taken", 0))

	data, err := os.ReadFile("../importer/testdata/pm2/ecosystem.config.js")
	if err != nil {
		t.Fatal(err)
	}
	rec := e.upload("/api/import/preview", "file", "ecosystem.config.js", data, admin...)
	// The source is a form field (or query parameter); without it the
	// preview is refused.
	expect(t, rec, http.StatusUnprocessableEntity)
	rec = e.upload("/api/import/preview?source=pm2", "file", "ecosystem.config.js", data, admin...)
	expect(t, rec, http.StatusOK)
	if pv := decodeJSON[model.ImportPreview](t, rec); len(pv.Items) != 4 {
		t.Fatalf("multipart preview: %+v", pv)
	}

	rec = e.do(http.MethodPost, "/api/import/preview", map[string]any{"source": "pm2", "filename": "ecosystem.config.js", "text": string(data)}, admin...)
	expect(t, rec, http.StatusOK)
	pv := decodeJSON[model.ImportPreview](t, rec)
	if len(pv.Items) != 4 {
		t.Fatalf("preview: %+v", pv)
	}
	items := map[string]model.ImportItem{}
	for _, it := range pv.Items {
		items[it.Key] = it
	}
	pick := func(key string) model.ImportApplyItem {
		it := items[key]
		o := it.Options[it.Choice]
		return model.ImportApplyItem{Key: key, Kind: o.Kind, Site: o.Site, Task: o.Task, TaskSite: o.TaskSite}
	}

	// Code instead of a literal is refused, never run.
	rec = e.do(http.MethodPost, "/api/import/preview", map[string]any{"source": "pm2", "filename": "ecosystem.config.js", "text": "module.exports = require('./apps')"}, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if !strings.Contains(rec.Body.String(), "pm2 jlist") {
		t.Fatalf("refusal: %s", rec.Body)
	}

	// The task is listed before the site it goes to, and a name is taken:
	// the task still finds its site, the duplicate fails alone.
	dup := pick("pm2-queue-worker")
	dup.Key = "dup"
	dupSite := *dup.Site
	dupSite.Name = "Taken"
	dup.Site = &dupSite
	req := model.ImportApplyRequest{Source: "pm2", Items: []model.ImportApplyItem{pick("pm2-cleanup"), pick("pm2-api"), pick("pm2-queue-worker"), dup}}
	rec = e.do(http.MethodPost, "/api/import/apply", req, admin...)
	expect(t, rec, http.StatusOK)
	res := decodeJSON[model.ImportApplyResult](t, rec)
	if len(res.Created) != 3 || len(res.Failed) != 1 || res.Failed[0].Key != "dup" || !strings.Contains(res.Failed[0].Error, "already exists") {
		t.Fatalf("result: %+v", res)
	}

	var api, worker *model.Site
	for _, s := range e.c.Sites() {
		switch s.Name {
		case "api":
			api = s
		case "queue-worker":
			worker = s
		}
	}
	if api == nil || worker == nil || worker.Type != model.SiteWorker {
		t.Fatalf("sites: %+v", e.c.Sites())
	}
	if len(api.Tasks) != 1 || api.Tasks[0].Name != "cleanup" || api.Tasks[0].Schedule != "*/30 * * * *" || api.Tasks[0].ID == "" {
		t.Fatalf("api tasks: %+v", api.Tasks)
	}
	// Created stopped even though the drafts start automatically later.
	if e.c.IsRunning(api) || !api.AutoStart {
		t.Fatalf("api running=%v autoStart=%v", e.c.IsRunning(api), api.AutoStart)
	}
	for _, v := range api.Node.Env {
		if v.Name == "STRIPE_SECRET_KEY" && (!v.Secret || v.Value == "sk_live_Abc" || e.c.Box.MustUnseal(v.Value) != "sk_live_Abc") {
			t.Fatalf("secret not sealed: %+v", v)
		}
	}
	if acts := e.auditActions(); !contains(acts, "root:site.import") || !contains(acts, "root:site.update") {
		t.Fatalf("audit: %v", acts)
	}

	// A preview now reports the names as taken.
	rec = e.do(http.MethodPost, "/api/import/preview", map[string]any{"source": "pm2", "filename": "ecosystem.config.js", "text": string(data)}, admin...)
	pv = decodeJSON[model.ImportPreview](t, rec)
	for _, it := range pv.Items {
		if it.Key == "pm2-api" && (it.Selected || len(it.Conflicts) == 0) {
			t.Fatalf("existing name not reported: %+v", it)
		}
	}
}

func TestImportApplyMountsInOrder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	child := &model.Site{Name: "shop api", Type: model.SiteNode, Node: &model.NodeConfig{AppRoot: `C:\sites\api`, Script: "index.js"}}
	parent := &model.Site{Name: "shop", Type: model.SiteStatic, Static: &model.StaticConfig{Root: `C:\sites\shop`},
		Routing: model.RoutingConfig{Locations: []model.Location{{Path: "/api", Kind: "site", SiteID: "import:child"}}}}
	orphan := &model.Site{Name: "orphan", Type: model.SiteStatic, Static: &model.StaticConfig{Root: `C:\x`},
		Routing: model.RoutingConfig{Locations: []model.Location{{Path: "/api", Kind: "site", SiteID: "import:missing"}}}}
	req := model.ImportApplyRequest{Start: true, Items: []model.ImportApplyItem{
		{Key: "parent", Kind: "site", Site: parent},
		{Key: "orphan", Kind: "site", Site: orphan},
		{Key: "child", Kind: "site", Site: child},
		{Key: "task", Kind: "task", Task: &model.ScheduledTask{Name: "t", Script: "t.js"}, TaskSite: "import:parent"},
	}}
	rec := e.do(http.MethodPost, "/api/import/apply", req, admin...)
	expect(t, rec, http.StatusOK)
	res := decodeJSON[model.ImportApplyResult](t, rec)
	if len(res.Created) != 2 || res.Created[0].Key != "child" || res.Created[1].Key != "parent" {
		t.Fatalf("created: %+v", res.Created)
	}
	failed := map[string]string{}
	for _, f := range res.Failed {
		failed[f.Key] = f.Error
	}
	if !strings.Contains(failed["orphan"], "mounts an application that was not imported") || !strings.Contains(failed["task"], "not a Node.js or background worker") {
		t.Fatalf("failed: %+v", res.Failed)
	}
	p, err := e.c.Site(res.Created[1].SiteID)
	if err != nil || p.Routing.Locations[0].SiteID != res.Created[0].SiteID {
		t.Fatalf("location not resolved: %+v", p.Routing.Locations)
	}
	// Start was asked: the static site runs; the node site could not start
	// (no Node.js in tests) and says so without failing the import.
	if !e.c.IsRunning(p) {
		t.Error("parent not started")
	}
	if res.Created[0].Warning == "" {
		t.Errorf("no warning for the node site that could not start: %+v", res.Created[0])
	}
}

func TestImportLocalIIS(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("reads the real IIS configuration on Windows")
	}
	e := newEnv(t)
	admin := e.adminSession()
	rec := e.do(http.MethodPost, "/api/import/preview", map[string]any{"source": "local-iis"}, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if !strings.Contains(rec.Body.String(), "only possible on Windows") {
		t.Fatalf("body: %s", rec.Body)
	}
}
