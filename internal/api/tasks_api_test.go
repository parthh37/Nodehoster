package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
)

func workerSite(name, root string, tasks ...map[string]any) map[string]any {
	s := map[string]any{
		"name": name, "type": "worker", "autoStart": false,
		"node": map[string]any{"appRoot": root, "script": "worker.js"},
	}
	if len(tasks) > 0 {
		s["tasks"] = tasks
	}
	return s
}

// waitRun polls a site's runs until the newest is no longer running.
func (e *env) waitRun(opts []opt, siteID string) model.TaskRun {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rec := e.do(http.MethodGet, "/api/sites/"+siteID+"/runs", nil, opts...)
		expect(e.t, rec, http.StatusOK)
		runs := decodeJSON[[]model.TaskRun](e.t, rec)
		if len(runs) > 0 && runs[0].Status != model.RunRunning {
			return runs[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("the run did not finish")
	return model.TaskRun{}
}

func TestWorkerSites(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	root := t.TempDir()

	bad := workerSite("queue", root)
	bad["bindings"] = []map[string]any{{"protocol": "http", "port": freePort(t)}}
	rec := e.do(http.MethodPost, "/api/sites", bad, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "bindings" {
		t.Fatalf("field = %q", f)
	}

	w := e.createSite(admin, workerSite("queue", root))
	if w.Type != model.SiteWorker || len(w.Bindings) != 0 || w.Status.State != model.StateStopped {
		t.Fatalf("worker: %+v", w)
	}

	// A worker serves no HTTP, so no other site may mount it.
	web := redirectSite("web", 0)
	web["routing"] = map[string]any{"locations": []map[string]any{{"path": "/jobs", "kind": "site", "siteId": w.ID}}}
	rec = e.do(http.MethodPost, "/api/sites", web, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "routing.locations[0].siteId" {
		t.Fatalf("field = %q (%s)", f, rec.Body)
	}

	// The type is fixed once created.
	upd := workerSite("queue", root)
	upd["type"] = "node"
	expect(t, e.do(http.MethodPut, "/api/sites/"+w.ID, upd, admin...), http.StatusUnprocessableEntity)
}

func TestScheduledTasksAPI(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	root := t.TempDir()

	dup := workerSite("jobs", root,
		map[string]any{"name": "nightly", "schedule": "0 3 * * *", "script": "a.js", "enabled": true},
		map[string]any{"name": "Nightly", "schedule": "@daily", "script": "b.js", "enabled": true})
	rec := e.do(http.MethodPost, "/api/sites", dup, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "tasks[1].name" {
		t.Fatalf("field = %q", f)
	}
	badCron := workerSite("jobs", root, map[string]any{"name": "x", "schedule": "61 * * * *", "script": "a.js"})
	rec = e.do(http.MethodPost, "/api/sites", badCron, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if b := rec.Body.String(); !strings.Contains(b, "tasks[0].schedule") || !strings.Contains(b, "out of range") {
		t.Fatalf("bad cron: %s", b)
	}

	s := e.createSite(admin, workerSite("jobs", root,
		map[string]any{"name": "nightly", "schedule": "0 3 * * *", "script": "nightly.js", "enabled": true,
			"env": []map[string]any{{"name": "API_KEY", "value": "hunter2", "secret": true}, {"name": "MODE", "value": "full"}}},
		map[string]any{"name": "migrate", "npmScript": "migrate", "enabled": true}))
	if len(s.Tasks) != 2 || s.Tasks[0].ID == "" || s.Tasks[0].Overlap != model.OverlapSkip || s.Tasks[0].TimeoutSec != model.DefaultTaskTimeoutSec {
		t.Fatalf("tasks: %+v", s.Tasks)
	}
	if v := s.Tasks[0].Env[0].Value; v != secrets.Mask {
		t.Fatalf("task secret returned as %q", v)
	}
	stored, _ := e.c.Site(s.ID)
	sealed := stored.Tasks[0].Env[0].Value
	if sealed == "hunter2" || e.c.Box.MustUnseal(sealed) != "hunter2" {
		t.Fatalf("task secret not sealed: %q", sealed)
	}

	// Saving the site back with the mask keeps the secret.
	upd := s.Site
	upd.Tasks[1].Schedule = "@every 2h"
	rec = e.do(http.MethodPut, "/api/sites/"+s.ID, upd, admin...)
	expect(t, rec, http.StatusOK)
	stored, _ = e.c.Site(s.ID)
	if e.c.Box.MustUnseal(stored.Tasks[0].Env[0].Value) != "hunter2" || stored.Tasks[0].ID != s.Tasks[0].ID {
		t.Fatalf("masked secret lost or task id changed: %+v", stored.Tasks[0])
	}

	rec = e.do(http.MethodGet, "/api/sites/"+s.ID+"/tasks", nil, admin...)
	expect(t, rec, http.StatusOK)
	views := decodeJSON[[]model.TaskView](t, rec)
	if len(views) != 2 || views[0].NextRunAt == nil || views[1].NextRunAt == nil || views[0].LastRun != nil {
		t.Fatalf("views: %+v", views)
	}
	if n := *views[0].NextRunAt; n.Hour() != 3 || n.Minute() != 0 || !n.After(time.Now()) {
		t.Fatalf("next run of 0 3 * * * = %s", n)
	}
	if views[0].Env[0].Value != secrets.Mask {
		t.Fatal("task views leak secrets")
	}

	// Run now: the test server has no Node.js, so the run fails to start,
	// which is recorded, logged and notified like any failure.
	rec = e.do(http.MethodPost, "/api/sites/"+s.ID+"/tasks/nightly/run", nil, admin...)
	expect(t, rec, http.StatusAccepted)
	started := decodeJSON[runResponse](t, rec)
	if started.Run == nil || started.Queued || started.Run.Trigger != "manual" || started.Run.User != "root" {
		t.Fatalf("run: %+v", started)
	}
	run := e.waitRun(admin, s.ID)
	if run.ID != started.Run.ID || run.Status != model.RunFailed || run.Error == "" {
		t.Fatalf("finished run: %+v", run)
	}
	rec = e.do(http.MethodGet, "/api/sites/"+s.ID+"/runs/"+run.ID+"/log", nil, admin...)
	expect(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "could not start") {
		t.Fatalf("log: %s", rec.Body)
	}
	rec = e.do(http.MethodGet, "/api/sites/"+s.ID+"/runs/"+run.ID+"/log/stream", nil, admin...)
	if !strings.Contains(rec.Body.String(), "event: done") || !strings.Contains(rec.Body.String(), "could not start") {
		t.Fatalf("stream of a finished run: %s", rec.Body)
	}
	expect(t, e.do(http.MethodPost, "/api/sites/"+s.ID+"/runs/"+run.ID+"/cancel", nil, admin...), http.StatusConflict)
	expect(t, e.do(http.MethodPost, "/api/sites/"+s.ID+"/tasks/nope/run", nil, admin...), http.StatusNotFound)
	rec = e.do(http.MethodGet, "/api/sites/"+s.ID+"/runs?task=migrate", nil, admin...)
	if runs := decodeJSON[[]model.TaskRun](t, rec); len(runs) != 0 {
		t.Fatalf("runs of migrate: %+v", runs)
	}

	evs, _ := e.c.Store.ListEvents(context.Background(), s.ID, 20)
	found := false
	for _, ev := range evs {
		found = found || (ev.Type == events.TaskFailed && strings.Contains(ev.Message, "nightly"))
	}
	if !found {
		t.Fatalf("no task.failed event: %+v", evs)
	}
	if !contains(e.auditActions(), "root:task.run") {
		t.Fatalf("run not audited: %v", e.auditActions())
	}
	rec = e.do(http.MethodGet, "/api/sites/"+s.ID+"/tasks", nil, admin...)
	if v := decodeJSON[[]model.TaskView](t, rec); v[0].LastRun == nil || v[0].LastRun.ID != run.ID {
		t.Fatalf("last run: %+v", v[0].LastRun)
	}

	// Removing the task removes its history.
	upd.Tasks = upd.Tasks[1:]
	expect(t, e.do(http.MethodPut, "/api/sites/"+s.ID, upd, admin...), http.StatusOK)
	expect(t, e.do(http.MethodGet, "/api/sites/"+s.ID+"/runs/"+run.ID+"/log", nil, admin...), http.StatusNotFound)

	// Only node and worker sites have tasks.
	red := redirectSite("red", 0)
	red["tasks"] = []map[string]any{{"name": "x", "script": "x.js"}}
	expect(t, e.do(http.MethodPost, "/api/sites", red, admin...), http.StatusUnprocessableEntity)
}

func TestScheduledTasksAuthorization(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	task := map[string]any{"name": "report", "script": "report.js", "enabled": true}
	a := e.createSite(admin, workerSite("site-a", t.TempDir(), task))
	b := e.createSite(admin, workerSite("site-b", t.TempDir(), task))
	c := e.createSite(admin, workerSite("site-c", t.TempDir(), task))

	// A run of b, to be looked up through other sites' paths.
	expect(t, e.do(http.MethodPost, "/api/sites/"+b.ID+"/tasks/report/run", nil, admin...), http.StatusAccepted)
	runB := e.waitRun(admin, b.ID)

	e.scoped("ops", grant(a.ID, model.RoleOperator), grant(b.ID, model.RoleViewer))
	ops := session(e.login("ops"))
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/sites/" + b.ID + "/tasks", 200},
		{"GET", "/api/sites/" + b.ID + "/runs", 200},
		{"GET", "/api/sites/" + b.ID + "/runs/" + runB.ID + "/log", 200},
		{"POST", "/api/sites/" + b.ID + "/tasks/report/run", 403},
		{"POST", "/api/sites/" + b.ID + "/runs/" + runB.ID + "/cancel", 403},
		{"POST", "/api/sites/" + a.ID + "/tasks/report/run", 202},
		// b's run is not a's, whatever the caller may do on a.
		{"GET", "/api/sites/" + a.ID + "/runs/" + runB.ID + "/log", 404},
		{"GET", "/api/sites/" + a.ID + "/runs/" + runB.ID + "/log/stream", 404},
		{"POST", "/api/sites/" + a.ID + "/runs/" + runB.ID + "/cancel", 404},
		// c is invisible.
		{"GET", "/api/sites/" + c.ID + "/tasks", 404},
		{"GET", "/api/sites/" + c.ID + "/runs", 404},
		{"POST", "/api/sites/" + c.ID + "/tasks/report/run", 404},
		// Task definitions are site configuration: administrators only.
		{"PUT", "/api/sites/" + a.ID, 403},
	} {
		var body any
		if tc.method == "PUT" {
			body = a.Site
		}
		rec := e.do(tc.method, tc.path, body, ops...)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d (%s)", tc.method, tc.path, rec.Code, tc.want, strings.TrimSpace(rec.Body.String()))
		}
	}

	// A server-wide viewer reads but does not run.
	e.user("viewer", model.RoleViewer, false)
	viewer := session(e.login("viewer"))
	expect(t, e.do(http.MethodGet, "/api/sites/"+a.ID+"/tasks", nil, viewer...), http.StatusOK)
	expect(t, e.do(http.MethodPost, "/api/sites/"+a.ID+"/tasks/report/run", nil, viewer...), http.StatusForbidden)
}
