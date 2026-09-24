//go:build !windows

package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/alerts"
	"github.com/parthh37/nodehoster/internal/model"
)

func TestAlertCommands(t *testing.T) {
	s := newServer(t)
	s.run("alert", "list").expect(t, ExitOK)
	if r := s.run("alert", "list"); !strings.Contains(r.stdout, "Alerts are off") {
		t.Fatalf("alerts off: %q", r.stdout)
	}

	// A server alert fires (made-up readings: the drive is almost full).
	cfg := model.AlertSettings{Enabled: true, RecoveryMinutes: 1, ServerRules: []model.AlertRule{
		{ID: "disk", Metric: model.AlertDiskFree, Threshold: 10, Severity: model.SeverityCritical},
	}}
	st := s.c.Settings()
	st.Alerts = cfg
	if _, err := s.c.UpdateSettings(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	full := alerts.ServerSample{Disks: []alerts.Disk{{Path: "/data", FreePercent: 2}}}
	s.c.Alerts.Evaluate(context.Background(), time.Now(), cfg, nil, full)

	r := s.run("alert", "list").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "critical") || !strings.Contains(r.stdout, "(server)") || !strings.Contains(r.stdout, "Free disk space 2% on /data") {
		t.Fatalf("list:\n%s", r.stdout)
	}
	var l model.AlertList
	if err := json.Unmarshal([]byte(s.run("--json", "alert", "list").expect(t, ExitOK).stdout), &l); err != nil || len(l.Firing) != 1 {
		t.Fatalf("json list: %+v %v", l, err)
	}
	id := l.Firing[0].ID

	s.run("alert", "silence", "nope").expect(t, ExitError)
	s.run("alert", "silence", id, "--minutes", "-5").expect(t, ExitUsage)
	r = s.run("alert", "silence", id[:8], "--minutes", "30", "--note", "cleaning up").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "Silenced until") {
		t.Fatalf("silence: %q", r.stdout)
	}
	if a, _ := s.c.Alerts.Get(id); a.Silence == nil || a.Silence.Note != "cleaning up" || a.Silence.Until == nil {
		t.Fatalf("silenced: %+v", a.Silence)
	}
	r = s.run("alert", "ack", id).expect(t, ExitOK)
	if !strings.Contains(r.stdout, "Acknowledged") {
		t.Fatalf("ack: %q", r.stdout)
	}
	s.run("alert", "unsilence", id).expect(t, ExitOK)
	if a, _ := s.c.Alerts.Get(id); a.Silence != nil {
		t.Fatalf("still silenced: %+v", a.Silence)
	}

	r = s.run("alert", "history", "-n", "5").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "firing") || !strings.Contains(r.stdout, "FIRED") {
		t.Fatalf("history:\n%s", r.stdout)
	}
	s.run("alert", "history", "-n", "0").expect(t, ExitUsage)
	s.run("alert", "history", "--site", "missing").expect(t, ExitError)
}
