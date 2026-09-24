package alerts

import (
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestFormatValue(t *testing.T) {
	cases := []struct {
		metric string
		v      float64
		want   string
	}{
		{model.AlertCPU, 93.4, "93%"},
		{model.AlertErrorRate, 2.46, "2.5%"},
		{model.AlertErrorRate, 5, "5%"},
		{model.AlertMemory, 1234.4, "1,234 MB"},
		{model.AlertLatencyP95, 850.2, "850 ms"},
		{model.AlertLatencyP95, 2430, "2.4 s"},
		{model.AlertInstancesDown, 2, "2"},
		{model.AlertDiskFree, 6.04, "6%"},
	}
	for _, c := range cases {
		if got := FormatValue(c.metric, c.v); got != c.want {
			t.Errorf("%s %v = %q, want %q", c.metric, c.v, got, c.want)
		}
	}
}

func TestMessages(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	fired := t0.Add(5 * time.Minute)
	a := &model.Alert{Metric: model.AlertCPU, Threshold: 85, ForMinutes: 5, Value: 93, Since: t0}
	if got := firingMessage(a, fired); got != "CPU 93% for 5 min (limit 85%)" {
		t.Errorf("firing = %q", got)
	}
	if got := pendingMessage(a, t0.Add(2*time.Minute)); got != "CPU 93% for 2 min (limit 85%); fires after 5 min" {
		t.Errorf("pending = %q", got)
	}
	if got := reminderMessage(a, t0.Add(3*time.Hour+5*time.Minute)); got != "Still firing: CPU 93% for 3 h 5 min (limit 85%)" {
		t.Errorf("reminder = %q", got)
	}
	a.FiredAt, a.Value, a.Peak = &fired, 40, 97
	if got := resolvedMessage(a, fired.Add(25*time.Minute)); got != "Resolved after 25 min: CPU 40% (limit 85%)" {
		t.Errorf("resolved = %q", got)
	}
	a.ResolveNote = "site deleted"
	if got := resolvedMessage(a, fired.Add(2*time.Hour)); got != "Resolved after 2 h (site deleted): CPU was 97% at worst (limit 85%)" {
		t.Errorf("resolved with a note = %q", got)
	}

	for _, c := range []struct {
		a    model.Alert
		want string
	}{
		{model.Alert{Metric: model.AlertInstancesDown, Value: 2, Detail: "of 4", Threshold: 0, Since: t0}, "2 of 4 instances down for 45 s (limit 0)"},
		{model.Alert{Metric: model.AlertDiskFree, Value: 6, Detail: `D:\`, Threshold: 10, Since: t0}, `Free disk space 6% on D:\ for 45 s (limit 10%)`},
		{model.Alert{Metric: model.AlertMemoryPercent, Value: 95, Detail: "instance 0: 950 of 1000 MB", Threshold: 90, Since: t0}, "Memory 95% of the limit (instance 0: 950 of 1000 MB) for 45 s (limit 90%)"},
		{model.Alert{Metric: model.AlertErrorRate, Value: 12, Detail: "41 of 340 requests in 5 min", Threshold: 5, Since: t0}, "5xx error rate 12% (41 of 340 requests in 5 min) for 45 s (limit 5%)"},
		{model.Alert{Metric: model.AlertServerMemory, Value: 94, Threshold: 90, Since: t0}, "Server memory 94% for 45 s (limit 90%)"},
	} {
		if got := firingMessage(&c.a, t0.Add(45*time.Second)); got != c.want {
			t.Errorf("%s = %q, want %q", c.a.Metric, got, c.want)
		}
	}
}

func TestSummaryMessage(t *testing.T) {
	var rest []Notice
	for i, name := range []string{"a", "b", "a", "", "c", "d", "e", "f"} {
		kind := Fire
		if i%3 == 2 {
			kind = Resolve
		}
		rest = append(rest, Notice{Kind: kind, Alert: model.Alert{SiteName: name}})
	}
	want := "8 more alert notifications (6 fired, 2 resolved), for a, b, server, c, d…; see the Alerts page"
	if got := summaryMessage(rest); got != want {
		t.Fatalf("summary = %q", got)
	}
}
