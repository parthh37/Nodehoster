package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/alerts"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
)

func TestAlertServerSample(t *testing.T) {
	c := openTestCore(t)
	t.Cleanup(c.Shutdown)
	disks := c.alertDisks()
	if len(disks) == 0 || disks[0].FreePercent <= 0 || disks[0].FreePercent > 100 {
		t.Fatalf("disks = %+v", disks)
	}
	var m cpuMeter
	if p := m.percent(); p != -1 {
		t.Fatalf("the first reading is a baseline, got %v", p)
	}
	time.Sleep(50 * time.Millisecond)
	if p := m.percent(); p < -1 || p > 100 {
		t.Fatalf("cpu = %v", p)
	}
	s := c.serverSample(&m)
	if s.MemoryPercent <= 0 || s.MemoryPercent > 100 {
		t.Fatalf("server sample = %+v", s)
	}
}

func TestDeliverAlerts(t *testing.T) {
	c := openTestCore(t)
	t.Cleanup(c.Shutdown)
	feed, cancel := c.Bus.Subscribe()
	defer cancel()
	c.deliverAlerts([]alerts.Notice{
		{Kind: alerts.Fire, Alert: model.Alert{SiteID: "s1", SiteName: "api", Severity: model.SeverityCritical}, Message: "CPU 93% for 5 min (limit 85%)"},
		{Kind: alerts.Remind, Alert: model.Alert{Severity: model.SeverityWarning}, Message: "Still firing: Server memory 95% for 2 h (limit 90%)"},
		{Kind: alerts.Resolve, Alert: model.Alert{SiteID: "s1", SiteName: "api", Severity: model.SeverityCritical}, Message: "Resolved after 5 min: CPU 20% (limit 85%)"},
		{Kind: alerts.None, Alert: model.Alert{Severity: model.SeverityWarning, State: model.AlertResolved}, Message: "3 more alert notifications (3 resolved)"},
	})
	var got []string
	for range 4 {
		select {
		case e := <-feed:
			got = append(got, e.Level+" "+e.Type+" "+e.SiteID)
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
		}
	}
	want := []string{"error " + events.AlertFiring + " s1", "warning " + events.AlertFiring + " ", "info " + events.AlertResolved + " s1", "info " + events.AlertResolved + " "}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %q", got)
	}

	// E-mail: one message per evaluation, when recipients are set.
	s := c.Settings()
	s.Alerts.EmailTo = []string{"ops@example.com"}
	if _, err := c.UpdateSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	c.deliverAlerts([]alerts.Notice{{Kind: alerts.Fire, Alert: model.Alert{SiteName: "api", Severity: model.SeverityCritical}, Message: "CPU 93% for 5 min (limit 85%)"}})
	q := c.Mail.Queue("")
	if len(q) != 1 || q[0].Subject != "[NodeHoster] Critical: api: CPU 93% for 5 min (limit 85%)" || q[0].Source != "alert" {
		t.Fatalf("mail queue = %+v", q)
	}
}

func TestAlertMail(t *testing.T) {
	c := &Core{AdminURL: "https://host:8443/"}
	subject, body := c.alertMail([]alerts.Notice{
		{Kind: alerts.Fire, Alert: model.Alert{SiteName: "api", Severity: model.SeverityWarning}, Message: "CPU 93% for 5 min (limit 85%)"},
		{Kind: alerts.Resolve, Alert: model.Alert{SiteName: "shop"}, Message: "Resolved after 5 min: CPU 20% (limit 85%)"},
	})
	if !strings.HasPrefix(subject, "[NodeHoster] 2 alert notifications from ") {
		t.Fatalf("subject = %q", subject)
	}
	if !strings.HasPrefix(body, "Warning: api: CPU 93% for 5 min (limit 85%)\nshop: Resolved after 5 min") || !strings.Contains(body, "Alerts: https://host:8443/alerts\n") {
		t.Fatalf("body = %q", body)
	}
}

// Settings saved before alerts existed get the default rules, turned off;
// invalid rules are refused.
func TestAlertSettings(t *testing.T) {
	c := openTestCore(t)
	t.Cleanup(c.Shutdown)
	s := c.Settings()
	if s.Alerts.Enabled || len(s.Alerts.SiteRules) == 0 {
		t.Fatalf("alerts = %+v", s.Alerts)
	}
	s.Alerts.Enabled = true
	s.Alerts.ServerRules = append(s.Alerts.ServerRules, model.AlertRule{Metric: model.AlertCPU, Threshold: 10})
	_, err := c.UpdateSettings(context.Background(), s)
	if ve, ok := err.(*model.ValidationError); !ok || !strings.HasPrefix(ve.Field, "alerts.serverRules[3]") {
		t.Fatalf("site metric in server rules: %v", err)
	}
}
