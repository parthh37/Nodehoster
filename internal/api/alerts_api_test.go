package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/alerts"
	"github.com/parthh37/nodehoster/internal/model"
)

// fireAlerts drives the alert engine directly with made-up readings: the
// sites' CPU far over the limit, and the data drive almost full, until
// their alerts fire.
func fireAlerts(t *testing.T, e *env, siteIDs ...string) time.Time {
	t.Helper()
	cfg := model.AlertSettings{
		Enabled: true, RecoveryMinutes: 1,
		SiteRules:   []model.AlertRule{{ID: "cpu", Metric: model.AlertCPU, Threshold: 80, ForMinutes: 1, Severity: model.SeverityWarning}},
		ServerRules: []model.AlertRule{{ID: "disk", Metric: model.AlertDiskFree, Threshold: 10, Severity: model.SeverityCritical}},
	}
	s := e.c.Settings()
	s.Alerts = cfg
	if _, err := e.c.UpdateSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	var samples []alerts.SiteSample
	for _, id := range siteIDs {
		s, err := e.c.Site(id)
		if err != nil {
			t.Fatal(err)
		}
		// Measured as a Node.js site, whatever it is.
		s.Type, s.Node = model.SiteNode, &model.NodeConfig{Instances: 1}
		samples = append(samples, alerts.SiteSample{Site: s, Status: model.SiteStatus{SiteID: id, State: model.StateRunning,
			Instances: []model.InstanceStatus{{PID: 1, State: "ready", Healthy: true, CPUPercent: 99}}}})
	}
	now := time.Now()
	for i := range 5 {
		now = time.Now().Add(time.Duration(i) * 15 * time.Second)
		e.c.Alerts.Evaluate(context.Background(), now, cfg, samples, alerts.ServerSample{CPUPercent: -1, MemoryPercent: -1, Disks: []alerts.Disk{{Path: `D:\`, FreePercent: 3}}})
	}
	return now
}

func TestAlertRulesValidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()

	rec := e.do(http.MethodGet, "/api/settings", nil, admin...)
	expect(t, rec, http.StatusOK)
	s := decodeJSON[model.Settings](t, rec)
	if s.Alerts.Enabled || len(s.Alerts.SiteRules) == 0 || s.Alerts.RecoveryMinutes != 2 {
		t.Fatalf("default alerts: %+v", s.Alerts)
	}
	s.Alerts.Enabled = true
	s.Alerts.SiteRules = append(s.Alerts.SiteRules, model.AlertRule{Metric: model.AlertDiskFree, Threshold: 5})
	rec = e.do(http.MethodPut, "/api/settings", s, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "alerts.siteRules[6].metric" {
		t.Fatalf("field = %q", f)
	}
	s.Alerts.SiteRules = s.Alerts.SiteRules[:6]
	s.Alerts.EmailTo = []string{"ops@example.com"}
	rec = e.do(http.MethodPut, "/api/settings", s, admin...)
	expect(t, rec, http.StatusOK)
	if got := decodeJSON[model.Settings](t, rec); !got.Alerts.Enabled || got.Alerts.EmailTo[0] != "ops@example.com" {
		t.Fatalf("saved: %+v", got.Alerts)
	}

	// Site rules: overrides and additions, checked against the site's type.
	site := e.createSite(admin, redirectSite("web", 0))
	body := redirectSite("web", 0)
	body["alerts"] = map[string]any{"rules": []map[string]any{{"metric": "eventLoopLag", "threshold": 100}}}
	rec = e.do(http.MethodPut, "/api/sites/"+site.ID, body, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "alerts.rules[0].metric" {
		t.Fatalf("site field = %q (%s)", f, rec.Body)
	}
	body["alerts"] = map[string]any{"rules": []map[string]any{
		{"id": "errors", "metric": "errorRate", "threshold": 20, "forMinutes": 10, "severity": "critical"},
		{"metric": "latency", "threshold": 800},
	}}
	rec = e.do(http.MethodPut, "/api/sites/"+site.ID, body, admin...)
	expect(t, rec, http.StatusOK)
	got := decodeJSON[siteResp](t, rec)
	if r := got.Alerts.Rules; len(r) != 2 || r[1].ID != "site-latency" || r[1].WindowMinutes != 5 || r[1].MinRequests != 20 || r[1].Severity != "warning" {
		t.Fatalf("site rules: %+v", got.Alerts)
	}
}

func TestAlertsAPI(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a, b, _ := threeSites(e, admin)
	fireAlerts(t, e, a.ID, b.ID)

	// Everything, for a server-wide viewer.
	e.user("watcher", model.RoleViewer, false)
	viewer := session(e.login("watcher"))
	rec := e.do(http.MethodGet, "/api/alerts", nil, viewer...)
	expect(t, rec, http.StatusOK)
	l := decodeJSON[model.AlertList](t, rec)
	if !l.Enabled || len(l.Firing) != 3 || l.Firing[0].Metric != model.AlertDiskFree {
		t.Fatalf("viewer's alerts: %+v", l)
	}
	var alertA, server model.Alert
	for _, al := range l.Firing {
		switch al.SiteID {
		case a.ID:
			alertA = al
		case "":
			server = al
		}
	}
	if alertA.SiteName != "site-a" || !strings.HasPrefix(alertA.Message, "CPU 99% for 1 min") {
		t.Fatalf("site-a alert: %+v", alertA)
	}

	// A site-scoped user sees their site's alerts only, no server alerts.
	agency := e.scoped("agency", grant(a.ID, model.RoleOperator))
	scoped := session(e.login("agency"))
	rec = e.do(http.MethodGet, "/api/alerts", nil, scoped...)
	expect(t, rec, http.StatusOK)
	if l := decodeJSON[model.AlertList](t, rec); len(l.Firing) != 1 || l.Firing[0].SiteID != a.ID {
		t.Fatalf("scoped alerts: %+v", l)
	}
	rec = e.do(http.MethodGet, "/api/alerts/history", nil, scoped...)
	expect(t, rec, http.StatusOK)
	if h := decodeJSON[[]model.Alert](t, rec); len(h) != 1 || h[0].SiteID != a.ID {
		t.Fatalf("scoped history: %+v", h)
	}
	expect(t, e.do(http.MethodGet, "/api/alerts?siteId="+b.ID, nil, scoped...), http.StatusNotFound)
	expect(t, e.do(http.MethodGet, "/api/alerts/history?server=1", nil, scoped...), http.StatusForbidden)
	rec = e.do(http.MethodGet, "/api/alerts/history?server=1", nil, viewer...)
	if h := decodeJSON[[]model.Alert](t, rec); len(h) != 1 || h[0].SiteID != "" {
		t.Fatalf("server history: %+v", h)
	}
	rec = e.do(http.MethodGet, "/api/alerts?siteId="+b.ID, nil, viewer...)
	if l := decodeJSON[model.AlertList](t, rec); len(l.Firing) != 1 || l.Firing[0].SiteID != b.ID {
		t.Fatalf("site-b alerts: %+v", l)
	}

	// The rules every site gets, for the site's Alerts tab: to whoever
	// can see the site (the settings are for administrators).
	rec = e.do(http.MethodGet, "/api/sites/"+a.ID+"/alert-rules", nil, scoped...)
	expect(t, rec, http.StatusOK)
	if r := decodeJSON[siteAlertRules](t, rec); !r.Enabled || len(r.Defaults) != 1 || r.Defaults[0].ID != "cpu" || r.RecoveryMinutes != 1 {
		t.Fatalf("site alert rules: %+v", r)
	}
	expect(t, e.do(http.MethodGet, "/api/sites/"+b.ID+"/alert-rules", nil, scoped...), http.StatusNotFound)

	// Silencing: operators of the alert's site (or the server).
	silence := map[string]any{"minutes": 60, "note": "known issue"}
	expect(t, e.do(http.MethodPost, "/api/alerts/"+alertA.ID+"/silence", silence, viewer...), http.StatusForbidden)
	expect(t, e.do(http.MethodPost, "/api/alerts/"+server.ID+"/silence", silence, scoped...), http.StatusNotFound)
	expect(t, e.do(http.MethodPost, "/api/alerts/nope/silence", silence, admin...), http.StatusNotFound)
	rec = e.do(http.MethodPost, "/api/alerts/"+alertA.ID+"/silence", silence, scoped...)
	expect(t, rec, http.StatusOK)
	if s := decodeJSON[model.Alert](t, rec).Silence; s == nil || s.Until == nil || s.By != "agency" || s.Note != "known issue" {
		t.Fatalf("silence: %+v", s)
	}
	expect(t, e.do(http.MethodPost, "/api/alerts/"+alertA.ID+"/silence", map[string]any{"minutes": -1}, scoped...), http.StatusUnprocessableEntity)
	rec = e.do(http.MethodPost, "/api/alerts/"+server.ID+"/silence", map[string]any{"minutes": 0}, admin...)
	expect(t, rec, http.StatusOK)
	if s := decodeJSON[model.Alert](t, rec).Silence; s == nil || s.Until != nil {
		t.Fatalf("acknowledged: %+v", s)
	}
	rec = e.do(http.MethodDelete, "/api/alerts/"+alertA.ID+"/silence", nil, scoped...)
	expect(t, rec, http.StatusOK)
	if decodeJSON[model.Alert](t, rec).Silence != nil {
		t.Fatal("still silenced")
	}
	actions := e.auditActions()
	for _, want := range []string{"agency:alert.silence", "root:alert.silence", "agency:alert.unsilence"} {
		if !contains(actions, want) {
			t.Errorf("audit log lacks %s: %v", want, actions)
		}
	}

	// Prometheus: the firing alerts the caller can see.
	rec = e.do(http.MethodGet, "/metrics", nil, withBearer(e.token(agency)))
	body := rec.Body.String()
	if !strings.Contains(body, `nodehoster_alert_firing{rule="cpu",metric="cpu",severity="warning",site="site-a",silenced="false"} 1`) ||
		strings.Contains(body, `site="site-b",silenced`) || strings.Contains(body, `rule="disk"`) {
		t.Errorf("scoped alert metrics:\n%s", body)
	}
	rec = e.do(http.MethodGet, "/metrics", nil, admin...)
	if body := rec.Body.String(); !strings.Contains(body, `rule="disk",metric="diskFree",severity="critical",site="",silenced="true"} 1`) {
		t.Errorf("admin alert metrics:\n%s", body)
	}

	// Alerts turned off: everything resolves; a resolved alert cannot be
	// silenced.
	st := e.c.Settings()
	st.Alerts.Enabled = false
	if _, err := e.c.UpdateSettings(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	e.c.Alerts.Evaluate(context.Background(), time.Now().Add(2*time.Minute), st.Alerts, nil, alerts.ServerSample{})
	rec = e.do(http.MethodGet, "/api/alerts", nil, viewer...)
	if l := decodeJSON[model.AlertList](t, rec); l.Enabled || len(l.Firing) != 0 {
		t.Fatalf("after turning alerts off: %+v", l)
	}
	expect(t, e.do(http.MethodPost, "/api/alerts/"+alertA.ID+"/silence", silence, scoped...), http.StatusConflict)
	rec = e.do(http.MethodGet, "/api/alerts/history?siteId="+a.ID, nil, viewer...)
	if h := decodeJSON[[]model.Alert](t, rec); len(h) != 1 || h[0].State != model.AlertResolved || h[0].ResolveNote != "alerts turned off" {
		t.Fatalf("history after turning off: %+v", h)
	}
}
