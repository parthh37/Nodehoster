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

// TestAlertSamples: previews are not watched; each deployment slot is,
// as the configuration it runs, held while a swap prepares it.
func TestAlertSamples(t *testing.T) {
	c := testCore(t)
	ctx := context.Background()
	shop, err := c.CreateSite(ctx, slotTestSite())
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.CreateSite(ctx, &model.Site{Name: "shop pr-1", Type: model.SiteRedirect, Redirect: &model.RedirectConfig{TargetURL: "https://example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	c.sitesMu.Lock()
	marked := clone(p)
	marked.PreviewOf, marked.Preview = shop.ID, &model.PreviewInfo{Key: "pr:1"}
	_, err = c.updateSite(ctx, p.ID, marked)
	c.sitesMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	subjects := func() map[string]alerts.SiteSample {
		out := map[string]alerts.SiteSample{}
		for _, s := range c.alertSamples() {
			out[model.SlotKey(s.Site.ID, s.Site.Slot)] = s
		}
		return out
	}
	got := subjects()
	if _, ok := got[p.ID]; ok {
		t.Error("a preview is watched")
	}
	prod, ok1 := got[shop.ID]
	slot, ok2 := got[model.SlotKey(shop.ID, "staging")]
	if !ok1 || !ok2 || len(got) != 2 {
		t.Fatalf("subjects = %v", got)
	}
	if prod.Site.Slot != "" || slot.Site.Slot != "staging" || slot.Site.ID != shop.ID || slot.Status.State != model.StateStopped || slot.Hold {
		t.Errorf("slot sample = %+v / %+v", slot.Site, slot.Status)
	}
	c.slots.mu.Lock()
	if c.slots.active == nil {
		c.slots.active = map[string]*model.SwapProgress{}
	}
	c.slots.active[shop.ID] = &model.SwapProgress{Slot: "staging"}
	c.slots.mu.Unlock()
	defer func() {
		c.slots.mu.Lock()
		delete(c.slots.active, shop.ID)
		c.slots.mu.Unlock()
	}()
	if got := subjects(); !got[model.SlotKey(shop.ID, "staging")].Hold || got[shop.ID].Hold {
		t.Error("the slot a swap prepares is not held")
	}

	// A slot's events say which slot.
	feed, cancel := c.Bus.Subscribe()
	defer cancel()
	c.deliverAlerts([]alerts.Notice{{Kind: alerts.Fire, Alert: model.Alert{SiteID: shop.ID, Slot: "staging", SiteName: "shop (staging)", Severity: model.SeverityCritical}, Message: "Instances down 1 of 1"}})
	select {
	case e := <-feed:
		if e.SiteID != shop.ID || e.Message != "slot staging: Instances down 1 of 1" {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
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
