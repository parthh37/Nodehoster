package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func wafEvent(id, site, action, ip string, t time.Time, matches ...model.WAFMatch) model.WAFEvent {
	return model.WAFEvent{ID: id, Time: t, SiteID: site, Action: action, ClientIP: ip, Method: "GET", Path: "/x", Score: 5, Threshold: 5, Paranoia: 1, Matches: matches}
}

func TestWAFEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	if err := s.InitWAF(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.InitWAF(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
	base := msTime(time.Now().Add(-time.Hour))
	sqli := model.WAFMatch{RuleID: 942100, Category: model.WAFSQLi, Score: 5}
	xss := model.WAFMatch{RuleID: 941100, Category: model.WAFXSS, Score: 5}
	err := s.AddWAFEvents(ctx, []model.WAFEvent{
		wafEvent("a1", "s1", model.WAFActionBlocked, "203.0.113.1", base, sqli),
		wafEvent("a2", "s1", model.WAFActionDetected, "203.0.113.2", base.Add(time.Minute), xss, sqli),
		wafEvent("a3", "s2", model.WAFActionBlocked, "203.0.113.1", base.Add(2*time.Minute), xss),
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := func(q model.WAFEventQuery) string {
		t.Helper()
		list, err := s.ListWAFEvents(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		out := ""
		for _, e := range list {
			out += e.ID + " "
		}
		return out
	}
	for _, tc := range []struct {
		q    model.WAFEventQuery
		want string
	}{
		{model.WAFEventQuery{}, "a3 a2 a1 "},
		{model.WAFEventQuery{SiteIDs: []string{"s1"}}, "a2 a1 "},
		{model.WAFEventQuery{SiteIDs: []string{}}, ""},
		{model.WAFEventQuery{Action: model.WAFActionBlocked}, "a3 a1 "},
		{model.WAFEventQuery{ClientIP: "203.0.113.1"}, "a3 a1 "},
		{model.WAFEventQuery{RuleID: 942100}, "a2 a1 "},
		{model.WAFEventQuery{RuleID: 94210}, ""},
		{model.WAFEventQuery{Category: model.WAFXSS}, "a3 a2 "},
		{model.WAFEventQuery{RequestID: "a2"}, "a2 "},
		{model.WAFEventQuery{Since: base.Add(time.Minute)}, "a3 a2 "},
		{model.WAFEventQuery{Limit: 2}, "a3 a2 "},
	} {
		if got := ids(tc.q); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.q, got, tc.want)
		}
	}
	// Paging by seq.
	list, _ := s.ListWAFEvents(ctx, model.WAFEventQuery{Limit: 2})
	if got := ids(model.WAFEventQuery{Before: list[1].Seq}); got != "a1 " {
		t.Errorf("next page %q", got)
	}
	if e := list[1]; e.Matches[0].RuleID != 941100 || e.Path != "/x" || !e.Time.Equal(base.Add(time.Minute)) {
		t.Errorf("event %+v", e)
	}

	// Pruning: by age, then by count.
	if err := s.PruneWAFEvents(ctx, base.Add(30*time.Second), 100); err != nil {
		t.Fatal(err)
	}
	if got := ids(model.WAFEventQuery{}); got != "a3 a2 " {
		t.Errorf("after age prune %q", got)
	}
	if err := s.PruneWAFEvents(ctx, base, 1); err != nil {
		t.Fatal(err)
	}
	if got := ids(model.WAFEventQuery{}); got != "a3 " {
		t.Errorf("after count prune %q", got)
	}
	if err := s.DeleteWAFEvents(ctx, "s2"); err != nil {
		t.Fatal(err)
	}
	if got := ids(model.WAFEventQuery{}); got != "" {
		t.Errorf("after delete %q", got)
	}
}

func TestWAFEventsSurviveReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s := openAt(t, path)
	s.InitWAF(ctx)
	var batch []model.WAFEvent
	for i := 0; i < 300; i++ {
		batch = append(batch, wafEvent(fmt.Sprint("e", i), "s", model.WAFActionBlocked, "198.51.100.1", time.Now()))
	}
	if err := s.AddWAFEvents(ctx, batch); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openAt(t, path)
	if err := s.InitWAF(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListWAFEvents(ctx, model.WAFEventQuery{Limit: 1000})
	if err != nil || len(list) != 300 {
		t.Fatalf("%d events, %v", len(list), err)
	}
}
