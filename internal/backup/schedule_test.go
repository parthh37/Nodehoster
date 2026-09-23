package backup

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	t.Parallel()
	utc := time.UTC
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, utc) }
	// 2026-03-02 is a Monday.
	for _, tc := range []struct {
		name     string
		hhmm     string
		weekdays []int
		after    time.Time
		want     time.Time
	}{
		{"later today", "02:30", nil, at(2026, 3, 2, 1, 0), at(2026, 3, 2, 2, 30)},
		{"exactly now is not next", "02:30", nil, at(2026, 3, 2, 2, 30), at(2026, 3, 3, 2, 30)},
		{"tomorrow", "02:30", nil, at(2026, 3, 2, 3, 0), at(2026, 3, 3, 2, 30)},
		{"weekday later this week", "23:00", []int{5}, at(2026, 3, 2, 9, 0), at(2026, 3, 6, 23, 0)},
		{"weekday next week", "01:00", []int{1}, at(2026, 3, 2, 9, 0), at(2026, 3, 9, 1, 0)},
		{"sunday", "00:00", []int{0, 6}, at(2026, 3, 2, 9, 0), at(2026, 3, 7, 0, 0)},
		{"month end", "12:00", nil, at(2026, 2, 28, 13, 0), at(2026, 3, 1, 12, 0)},
	} {
		got, ok := Next(tc.hhmm, tc.weekdays, tc.after)
		if !ok || !got.Equal(tc.want) {
			t.Errorf("%s: Next = %v %v, want %v", tc.name, got, ok, tc.want)
		}
	}
	if _, ok := Next("25:00", nil, time.Now()); ok {
		t.Error("an invalid time was scheduled")
	}
	if _, ok := Next("2:30", nil, time.Now()); ok {
		t.Error("an invalid time was scheduled")
	}

	// Spring forward in New York (2026-03-08, 02:00 → 03:00): 02:30 does
	// not exist that day and runs at 03:30.
	if ny, err := time.LoadLocation("America/New_York"); err == nil {
		got, _ := Next("02:30", nil, time.Date(2026, 3, 8, 0, 0, 0, 0, ny))
		if got.Hour() != 3 || got.Minute() != 30 || got.Day() != 8 {
			t.Errorf("DST gap: Next = %v", got)
		}
	}
}

// fakeClock drives a Scheduler: each advance moves the time and ticks.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func TestSchedulerRunsOncePerSlot(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: time.Date(2026, 3, 2, 2, 0, 0, 0, time.UTC)}
	tick := make(chan time.Time)
	runs := make(chan time.Time, 10)
	var mu sync.Mutex
	enabled, hhmm, days := true, "02:30", []int(nil)
	s := &Scheduler{
		Now:  clock.Now,
		Tick: tick,
		Schedule: func() (bool, string, []int) {
			mu.Lock()
			defer mu.Unlock()
			return enabled, hhmm, days
		},
		Run: func(context.Context) { runs <- clock.Now() },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Loop(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	step := func(h, m int) {
		clock.set(time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute))
		tick <- time.Time{}
	}
	expectRuns := func(n int) {
		t.Helper()
		tick <- time.Time{} // the loop has finished the previous step when it takes this one
		if len(runs) != n {
			t.Fatalf("runs = %d, want %d", len(runs), n)
		}
	}
	step(2, 10)
	step(2, 29)
	expectRuns(0)
	step(2, 30) // the scheduled minute
	expectRuns(1)
	step(2, 31)
	step(3, 0)
	expectRuns(1) // not again the same day
	step(26, 45)  // next day, the service was busy past 02:30: runs once
	expectRuns(2)
	step(26, 46)
	expectRuns(2)

	// Disabled: nothing, even across a slot.
	mu.Lock()
	enabled = false
	mu.Unlock()
	step(50, 40)
	expectRuns(2)

	// Re-enabled with another time: applies at once.
	mu.Lock()
	enabled, hhmm = true, "03:00"
	mu.Unlock()
	step(50, 50)
	step(51, 0)
	expectRuns(3)
}
