//go:build !windows

package localserver

import (
	"context"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/alerts"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

func TestStatusAlerts(t *testing.T) {
	c, root := newServer(t)
	ctx := context.Background()
	cfg := model.AlertSettings{Enabled: true, RecoveryMinutes: 1, ServerRules: []model.AlertRule{
		{ID: "disk", Metric: model.AlertDiskFree, Threshold: 10, Severity: model.SeverityCritical},
	}}
	c.Alerts.Evaluate(ctx, time.Now(), cfg, nil, alerts.ServerSample{Disks: []alerts.Disk{{Path: `D:\`, FreePercent: 2}}})

	sum, err := localapi.Connect(localapi.Status, root).Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Alerts) != 1 || sum.Alerts[0].Severity != model.SeverityCritical || sum.Alerts[0].Site != "" ||
		sum.Alerts[0].Message != `Free disk space 2% on D:\ (limit 10%)` {
		t.Fatalf("alerts = %+v", sum.Alerts)
	}
	if n, ok := notice(c, model.Event{Type: events.AlertFiring, Level: "error", Message: "x"}); !ok || n.Level != "error" {
		t.Fatalf("alert.firing is not a notice: %+v", n)
	}
}
