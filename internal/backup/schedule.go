package backup

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"
)

// Object is a file at a destination.
type Object struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// From the name: which server made it, and when.
	Host    string    `json:"host"`
	Created time.Time `json:"created"`
}

// Archives keeps the objects named like archives, newest first, with
// their host and time filled in.
func Archives(objs []Object) []Object {
	out := []Object{}
	for _, o := range objs {
		if h, t, ok := ParseName(o.Name); ok {
			o.Host, o.Created = h, t
			out = append(out, o)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].Name > out[j].Name
	})
	return out
}

// Expired returns the archives of host that retention deletes: those that
// are neither among the keepLast newest nor younger than keepDays. Other
// servers' archives and files not named like an archive are never
// returned, and neither is the newest archive. With both rules off,
// nothing expires.
func Expired(objs []Object, host string, keepLast, keepDays int, now time.Time) []Object {
	if keepLast <= 0 && keepDays <= 0 {
		return nil
	}
	host = SafeHost(host)
	var mine []Object
	for _, o := range Archives(objs) {
		if strings.EqualFold(o.Host, host) {
			mine = append(mine, o)
		}
	}
	cutoff := now.AddDate(0, 0, -keepDays)
	var out []Object
	for i, o := range mine {
		keep := i == 0 || (keepLast > 0 && i < keepLast) || (keepDays > 0 && o.Created.After(cutoff))
		if !keep {
			out = append(out, o)
		}
	}
	return out
}

// Next is the first scheduled time strictly after t, in t's location:
// hhmm ("02:30") on the given weekdays (0 = Sunday; empty = every day).
// On the day clocks spring forward, a time that does not exist runs an
// hour later.
func Next(hhmm string, weekdays []int, t time.Time) (time.Time, bool) {
	var h, m int
	if len(hhmm) != 5 || hhmm[2] != ':' {
		return time.Time{}, false
	}
	h = int(hhmm[0]-'0')*10 + int(hhmm[1]-'0')
	m = int(hhmm[3]-'0')*10 + int(hhmm[4]-'0')
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, false
	}
	for i := 0; i <= 8; i++ {
		c := time.Date(t.Year(), t.Month(), t.Day()+i, h, m, 0, 0, t.Location())
		if c.Hour() != h || c.Minute() != m {
			// In the gap: Go picks the time before the jump.
			c = c.Add(time.Hour)
		}
		if !c.After(t) {
			continue
		}
		if len(weekdays) == 0 || slices.Contains(weekdays, int(c.Weekday())) {
			return c, true
		}
	}
	return time.Time{}, false
}

// Scheduler calls Run at each scheduled time. It checks on every Tick
// whether a scheduled time passed since the previous check, so a changed
// schedule applies at once and a slow run never causes a second one to
// pile up. A time missed while the service was stopped is not caught up.
type Scheduler struct {
	Now      func() time.Time
	Tick     <-chan time.Time
	Schedule func() (enabled bool, hhmm string, weekdays []int)
	Run      func(ctx context.Context)
}

func (s *Scheduler) Loop(ctx context.Context) {
	last := s.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.Tick:
		}
		now := s.Now()
		if on, hhmm, days := s.Schedule(); on {
			if next, ok := Next(hhmm, days, last); ok && !next.After(now) {
				s.Run(ctx)
				now = s.Now()
			}
		}
		last = now
	}
}
