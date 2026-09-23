package cron

import (
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // DST tests need zones on machines without a zoneinfo database (Windows)
)

func mustParse(t *testing.T, spec string) *Schedule {
	t.Helper()
	s, err := Parse(spec)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spec, err)
	}
	return s
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct{ spec, want string }{
		{"", "empty"},
		{"* * * *", "5 fields"},
		{"* * * * * *", "5 fields"},
		{"60 * * * *", "minute: 60 is out of range"},
		{"* 24 * * *", "hour: 24 is out of range"},
		{"* * 0 * *", "day of month: 0 is out of range"},
		{"* * 32 * *", "day of month"},
		{"* * * 13 *", "month: 13"},
		{"* * * * 8", "day of week: 8"},
		{"*/0 * * * *", "not a valid step"},
		{"*/x * * * *", "not a valid step"},
		{"5-1 * * * *", "runs backwards"},
		{"1,,2 * * * *", "empty value"},
		{"* * * foo *", "not a number or a name"},
		{"a * * * *", "not a number"},
		{"@often", "unknown shorthand"},
		{"@every", "needs a duration"},
		{"@every soon", "not a duration"},
		{"@every 30s", "shortest interval is 1m"},
	} {
		_, err := Parse(tc.spec)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Parse(%q) = %v, want an error containing %q", tc.spec, err, tc.want)
		}
	}
}

func TestNext(t *testing.T) {
	utc := time.UTC
	from := time.Date(2026, 3, 10, 14, 7, 30, 0, utc) // a Tuesday
	for _, tc := range []struct {
		spec string
		want []string // the next few times, "2006-01-02 15:04"
	}{
		{"* * * * *", []string{"2026-03-10 14:08", "2026-03-10 14:09"}},
		{"*/15 * * * *", []string{"2026-03-10 14:15", "2026-03-10 14:30", "2026-03-10 14:45", "2026-03-10 15:00"}},
		{"5/20 * * * *", []string{"2026-03-10 14:25", "2026-03-10 14:45", "2026-03-10 15:05"}},
		{"0 9-17/4 * * *", []string{"2026-03-10 17:00", "2026-03-11 09:00", "2026-03-11 13:00"}},
		{"30 2 * * *", []string{"2026-03-11 02:30", "2026-03-12 02:30"}},
		{"0 0,12 * * *", []string{"2026-03-11 00:00", "2026-03-11 12:00"}},
		{"0 8 * * mon-fri", []string{"2026-03-11 08:00", "2026-03-12 08:00", "2026-03-13 08:00", "2026-03-16 08:00"}},
		{"0 8 * * SAT,sun", []string{"2026-03-14 08:00", "2026-03-15 08:00", "2026-03-21 08:00"}},
		{"0 8 * * 7", []string{"2026-03-15 08:00"}}, // 7 = Sunday
		{"0 0 1 jan-mar *", []string{"2027-01-01 00:00", "2027-02-01 00:00"}},
		{"0 0 31 * *", []string{"2026-03-31 00:00", "2026-05-31 00:00", "2026-07-31 00:00"}},
		{"0 0 29 2 *", []string{"2028-02-29 00:00"}},
		// Both day fields restricted: either matches (Vixie cron).
		{"0 0 13 * fri", []string{"2026-03-13 00:00", "2026-03-20 00:00", "2026-03-27 00:00", "2026-04-03 00:00", "2026-04-10 00:00", "2026-04-13 00:00"}},
		// Day of week restricted, day of month "*": both must match.
		{"0 0 * * fri", []string{"2026-03-13 00:00", "2026-03-20 00:00"}},
		{"0 0 */10 * *", []string{"2026-03-11 00:00", "2026-03-21 00:00", "2026-03-31 00:00", "2026-04-01 00:00"}},
		{"@hourly", []string{"2026-03-10 15:00", "2026-03-10 16:00"}},
		{"@daily", []string{"2026-03-11 00:00"}},
		{"@weekly", []string{"2026-03-15 00:00", "2026-03-22 00:00"}},
		{"@monthly", []string{"2026-04-01 00:00", "2026-05-01 00:00"}},
		{"@YEARLY", []string{"2027-01-01 00:00"}},
	} {
		s := mustParse(t, tc.spec)
		at := from
		for i, want := range tc.want {
			at = s.Next(at)
			if got := at.Format("2006-01-02 15:04"); got != want {
				t.Errorf("%q: run %d = %s, want %s", tc.spec, i+1, got, want)
				break
			}
		}
	}
}

func TestNeverMatches(t *testing.T) {
	s := mustParse(t, "0 0 30 2 *")
	if n := s.Next(time.Now()); !n.IsZero() {
		t.Fatalf("February 30th fired at %s", n)
	}
}

func TestEvery(t *testing.T) {
	s := mustParse(t, "@every 1h30m")
	if s.Every() != 90*time.Minute {
		t.Fatalf("Every = %s", s.Every())
	}
	from := time.Date(2026, 1, 1, 10, 0, 0, 500, time.UTC)
	if got := s.Next(from); !got.Equal(time.Date(2026, 1, 1, 11, 30, 0, 0, time.UTC)) {
		t.Fatalf("Next = %s", got)
	}
}

// TestDST: in New York clocks go from 02:00 to 03:00 on 8 March 2026 and
// from 02:00 back to 01:00 on 1 November 2026.
func TestDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	fmtT := func(x time.Time) string { return x.Format("2006-01-02 15:04 MST") }

	t.Run("skipped hour does not run", func(t *testing.T) {
		s := mustParse(t, "30 2 * * *")
		got := s.Next(time.Date(2026, 3, 7, 12, 0, 0, 0, ny))
		if fmtT(got) != "2026-03-09 02:30 EDT" {
			t.Fatalf("02:30 does not exist on 8 March; want the 9th, got %s", fmtT(got))
		}
		// Every quarter hour: 01:45 EST is followed by 03:00 EDT.
		q := mustParse(t, "*/15 * * * *")
		if got := q.Next(time.Date(2026, 3, 8, 1, 45, 0, 0, ny)); fmtT(got) != "2026-03-08 03:00 EDT" {
			t.Fatalf("after 01:45: %s", fmtT(got))
		}
	})

	t.Run("repeated hour runs once", func(t *testing.T) {
		s := mustParse(t, "30 1 * * *")
		first := s.Next(time.Date(2026, 10, 31, 12, 0, 0, 0, ny))
		if fmtT(first) != "2026-11-01 01:30 EDT" {
			t.Fatalf("first = %s, want the first 01:30 (EDT)", fmtT(first))
		}
		second := s.Next(first)
		if fmtT(second) != "2026-11-02 01:30 EST" {
			t.Fatalf("after the first 01:30 = %s, want the next day (not 01:30 EST the same night)", fmtT(second))
		}

		// Every 20 minutes through the night: each wall time once.
		q := mustParse(t, "*/20 * * * *")
		at := time.Date(2026, 11, 1, 0, 30, 0, 0, ny)
		var runs []string
		for i := 0; i < 6; i++ {
			at = q.Next(at)
			runs = append(runs, fmtT(at))
		}
		want := []string{
			"2026-11-01 00:40 EDT", "2026-11-01 01:00 EDT", "2026-11-01 01:20 EDT", "2026-11-01 01:40 EDT",
			"2026-11-01 02:00 EST", "2026-11-01 02:20 EST",
		}
		if strings.Join(runs, ", ") != strings.Join(want, ", ") {
			t.Fatalf("runs:\n%s\nwant:\n%s", strings.Join(runs, "\n"), strings.Join(want, "\n"))
		}
	})

	t.Run("starting inside the repeated hour", func(t *testing.T) {
		// The service (re)started at 01:10 EST, the second 01:10: the next
		// quarter hour is 01:15 EST, not the long-gone 01:15 EDT.
		secondPass := time.Date(2026, 11, 1, 6, 10, 0, 0, time.UTC).In(ny)
		if fmtT(secondPass) != "2026-11-01 01:10 EST" {
			t.Fatalf("setup: %s", fmtT(secondPass))
		}
		got := mustParse(t, "*/15 * * * *").Next(secondPass)
		if fmtT(got) != "2026-11-01 01:15 EST" || !got.After(secondPass) {
			t.Fatalf("got %s", fmtT(got))
		}
	})

	t.Run("@every ignores the wall clock", func(t *testing.T) {
		s := mustParse(t, "@every 1h")
		from := time.Date(2026, 3, 8, 1, 30, 0, 0, ny)
		if got := s.Next(from); got.Sub(from) != time.Hour || fmtT(got) != "2026-03-08 03:30 EDT" {
			t.Fatalf("got %s", fmtT(got))
		}
	})
}
