package desktop

import (
	"testing"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

func TestOverallAlerts(t *testing.T) {
	sum := &localapi.Summary{
		Sites: []localapi.SiteSummary{{ID: "a", Name: "api", State: model.StateRunning}},
		Alerts: []localapi.AlertSummary{
			{ID: "1", Site: "api", Severity: model.SeverityWarning, Message: "CPU 93% for 5 min (limit 85%)"},
			{ID: "2", Site: "api", Severity: model.SeverityCritical, Message: "5xx error rate 12% for 5 min (limit 5%)", Silenced: true},
		},
	}
	// Warnings and silenced alerts leave the icon green.
	if h := Overall("running", sum); h.Level != LevelOK || h.Summary != "Running · 1 of 1 sites running" {
		t.Fatalf("health = %+v", h)
	}
	sum.Alerts = append(sum.Alerts, localapi.AlertSummary{ID: "3", Severity: model.SeverityCritical, Message: `Free disk space 4% on D:\ for 5 min (limit 10%)`})
	h := Overall("running", sum)
	if h.Level != LevelWarning || h.Summary != "Running · 1 of 1 sites running · 1 critical alert" {
		t.Fatalf("health = %+v", h)
	}
	crit := CriticalAlerts(sum)
	if len(crit) != 1 || AlertText(crit[0]) != `Server: Free disk space 4% on D:\ for 5 min (limit 10%)` {
		t.Fatalf("critical = %+v", crit)
	}
	sum.Alerts[1].Silenced = false
	if h := Overall("running", sum); h.Summary != "Running · 1 of 1 sites running · 2 critical alerts" {
		t.Fatalf("health = %+v", h)
	}
	// A stopped service shows no alerts, whatever it last reported.
	if h := Overall("stopped", sum); h.Level != LevelDown {
		t.Fatalf("stopped = %+v", h)
	}
}
