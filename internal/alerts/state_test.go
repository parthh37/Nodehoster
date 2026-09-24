package alerts

import (
	"strings"
	"testing"
	"time"
)

// run feeds a script of observations, one every 15 s unless a step says
// otherwise, and returns the transitions as a string such as
// "....F...R" (. none, F fire, M remind, R resolve).
type obsAt struct {
	obs Observation
	gap time.Duration // since the previous step; 0 = 15 s
}

func script(s string) []obsAt {
	var out []obsAt
	for _, c := range s {
		switch c {
		case 'B':
			out = append(out, obsAt{obs: Breach})
		case 'c':
			out = append(out, obsAt{obs: Clear})
		case '?':
			out = append(out, obsAt{obs: NoData})
		case '~': // the next step comes after a 2-minute gap
			out = append(out, obsAt{obs: -1})
		}
	}
	return out
}

func runScript(t *testing.T, steps []obsAt, tm Timing) (string, State) {
	t.Helper()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	var s State
	var b strings.Builder
	gap := 15 * time.Second
	for _, st := range steps {
		if st.obs == -1 {
			gap = 2 * time.Minute
			continue
		}
		now = now.Add(gap)
		gap = 15 * time.Second
		var tr Transition
		s, tr = Step(s, st.obs, now, tm)
		b.WriteByte(".FMR"[tr])
	}
	return b.String(), s
}

func TestStep(t *testing.T) {
	// for 1 min = 4 evaluations after the first breach; recovery 30 s.
	tm := Timing{For: time.Minute, Recovery: 30 * time.Second}
	cases := []struct {
		name, script, want string
		phase              Phase
	}{
		{"fires once held for `for`", "BBBBB", "....F", Firing},
		{"a clear evaluation starts over", "BBBcBBBBB", "........F", Firing},
		{"stays firing while it holds", "BBBBBBBBBB", "....F.....", Firing},
		{"resolves after the recovery period", "BBBBBccc", "....F..R", Inactive},
		{"hysteresis: a blip does not resolve", "BBBBBcBcBcB", "....F......", Firing},
		{"no data holds a pending condition", "BB??", "....", Pending},
		{"…and fires on the next breach once due", "BB????BB", "......F.", Firing},
		{"no data never fires by itself", "BBB?????", "........", Pending},
		{"no data counts as clear when firing", "BBBBB???", "....F..R", Inactive},
		{"a gap restarts a pending condition", "BBB~BBBBB", ".......F", Firing},
		{"a gap during no data restarts too", "BBB~?BBB", ".......", Pending},
		{"a gap does not resolve by itself", "BBBBB~B", "....F.", Firing},
		{"a gap restarts the recovery period", "BBBBBc~ccc", "....F...R", Inactive},
		{"clear from the start does nothing", "ccc??c", "......", Inactive},
	}
	for _, c := range cases {
		got, s := runScript(t, script(c.script), tm)
		if got != c.want || s.Phase != c.phase {
			t.Errorf("%s: %s → %s (phase %d), want %s (phase %d)", c.name, c.script, got, s.Phase, c.want, c.phase)
		}
	}
}

func TestStepImmediate(t *testing.T) {
	// for 0: the first breach fires; recovery 0: the first clear resolves.
	got, _ := runScript(t, script("cBBcB"), Timing{})
	if got != ".F.RF" {
		t.Fatalf("got %s", got)
	}
}

func TestStepReminders(t *testing.T) {
	tm := Timing{For: 0, Recovery: time.Minute, Repeat: time.Minute}
	got, _ := runScript(t, script("BBBBBBBBBcBBBBB"), tm)
	// Fires, then reminds every 4 evaluations (1 min); a clear evaluation
	// does not reset the reminder schedule.
	if got != "F...M...M...M.." {
		t.Fatalf("got %s", got)
	}
}

func TestStepTimestamps(t *testing.T) {
	tm := Timing{For: time.Minute, Recovery: time.Minute}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var s State
	s, _ = Step(s, Breach, t0, tm)
	if s.Phase != Pending || !s.Since.Equal(t0) {
		t.Fatalf("pending = %+v", s)
	}
	var tr Transition
	s, tr = Step(s, Breach, t0.Add(time.Minute), tm)
	if tr != Fire || !s.FiredAt.Equal(t0.Add(time.Minute)) || !s.Since.Equal(t0) || !s.LastNotified.Equal(s.FiredAt) {
		t.Fatalf("fired = %+v, %v", s, tr)
	}
	s, _ = Step(s, Clear, t0.Add(90*time.Second), tm)
	if !s.ClearSince.Equal(t0.Add(90 * time.Second)) {
		t.Fatalf("recovering = %+v", s)
	}
	s, tr = Step(s, Clear, t0.Add(150*time.Second), tm)
	if tr != Resolve || s != (State{LastSeen: t0.Add(150 * time.Second)}) {
		t.Fatalf("resolved = %+v, %v", s, tr)
	}
}
