package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestAlerts(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	base := msTime(time.Now().Add(-48 * time.Hour))
	put := func(id, site, state string, fired time.Time) *model.Alert {
		f := fired
		a := &model.Alert{ID: id, RuleID: "cpu", SiteID: site, Metric: model.AlertCPU, Severity: model.SeverityWarning, State: state, Since: fired.Add(-time.Minute), FiredAt: &f, Message: "CPU " + id}
		if state == model.AlertResolved {
			r := fired.Add(10 * time.Minute)
			a.ResolvedAt = &r
		}
		if err := s.PutAlert(ctx, a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	put("a1", "s1", model.AlertResolved, base)
	put("a2", "s2", model.AlertResolved, base.Add(time.Hour))
	put("a3", "", model.AlertFiring, base.Add(2*time.Hour))
	a4 := put("a4", "s1", model.AlertFiring, base.Add(3*time.Hour))

	firing, err := s.FiringAlerts(ctx)
	if err != nil || len(firing) != 2 || firing[0].ID != "a3" || firing[1].ID != "a4" {
		t.Fatalf("firing = %+v, %v", firing, err)
	}

	// Resolving updates the row in place.
	r := base.Add(4 * time.Hour)
	a4.State, a4.ResolvedAt, a4.Message = model.AlertResolved, &r, "resolved"
	if err := s.PutAlert(ctx, a4); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAlert(ctx, "a4")
	if err != nil || got.State != model.AlertResolved || got.Message != "resolved" || !got.ResolvedAt.Equal(r) {
		t.Fatalf("a4 = %+v, %v", got, err)
	}
	if _, err := s.GetAlert(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}

	ids := func(f AlertFilter) string {
		list, err := s.ListAlerts(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		out := ""
		for _, a := range list {
			out += a.ID + " "
		}
		return out
	}
	for _, c := range []struct {
		f    AlertFilter
		want string
	}{
		{AlertFilter{}, "a4 a3 a2 a1 "},
		{AlertFilter{Limit: 2}, "a4 a3 "},
		{AlertFilter{SiteIDs: []string{"s1"}}, "a4 a1 "},
		{AlertFilter{SiteIDs: []string{"s1"}, Server: true}, "a4 a3 a1 "},
		{AlertFilter{SiteIDs: []string{}}, ""},
		{AlertFilter{SiteIDs: []string{}, Server: true}, "a3 "},
		{AlertFilter{Since: base.Add(90 * time.Minute)}, "a4 a3 "},
	} {
		if got := ids(c.f); got != c.want {
			t.Errorf("%+v: %q, want %q", c.f, got, c.want)
		}
	}

	// Pruning keeps firing alerts, whatever their age.
	if err := s.PruneAlerts(ctx, base.Add(5*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if got := ids(AlertFilter{}); got != "a3 " {
		t.Fatalf("after pruning by age: %q", got)
	}
	for i := 0; i < 5; i++ {
		put(fmt.Sprintf("b%d", i), "s1", model.AlertResolved, base.Add(time.Duration(10+i)*time.Hour))
	}
	if err := s.PruneAlerts(ctx, base, 2); err != nil {
		t.Fatal(err)
	}
	if got := ids(AlertFilter{}); got != "b4 b3 a3 " {
		t.Fatalf("after pruning by count: %q", got)
	}
}
