// Package cron parses the schedules of scheduled tasks: standard 5-field
// cron expressions (minute hour day-of-month month day-of-week) with
// ranges, steps, lists and month/day names, the @hourly/@daily/@weekly/
// @monthly/@yearly shorthands and "@every <duration>".
//
// Times are wall-clock times in the location of the time passed to Next
// (the server's local time). Around daylight saving time changes each
// wall-clock minute runs at most once: a time skipped when clocks go
// forward does not run at all, and a time repeated when clocks go back
// runs once, the first time it occurs. Many cron implementations run a
// job twice in the repeated hour; for a billing or clean-up script once is
// what people expect.
//
// It is a small parser of our own rather than a dependency: the common
// libraries parse the same syntax but place times around DST changes
// differently (and the web console mirrors this code for its preview).
package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MinEvery is the shortest "@every" interval. Tasks start a Node.js
// process, so anything shorter is almost certainly a mistake.
const MinEvery = time.Minute

// Schedule is a parsed schedule.
type Schedule struct {
	every time.Duration // "@every"; the fields below are unused then

	minute, hour, dom, month, dow uint64 // bit n set = value n allowed
	domStar, dowStar              bool   // field started with "*"
}

type field struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	minuteField = field{name: "minute", min: 0, max: 59}
	hourField   = field{name: "hour", min: 0, max: 23}
	domField    = field{name: "day of month", min: 1, max: 31}
	monthField  = field{name: "month", min: 1, max: 12, names: map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}}
	// 7 is accepted for Sunday, as in most crons, and folded onto 0.
	dowField = field{name: "day of week", min: 0, max: 7, names: map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}}
)

var macros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// Parse parses a schedule. Errors are written for the person typing it.
func Parse(spec string) (*Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("the schedule is empty")
	}
	lower := strings.ToLower(spec)
	if rest, ok := strings.CutPrefix(lower, "@every"); ok {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return nil, errors.New(`"@every" needs a duration, e.g. "@every 15m"`)
		}
		d, err := time.ParseDuration(rest)
		if err != nil {
			return nil, fmt.Errorf("%q is not a duration (use e.g. 90s, 15m, 2h, 1h30m)", rest)
		}
		if d < MinEvery {
			return nil, fmt.Errorf("the shortest interval is %s", MinEvery)
		}
		return &Schedule{every: d.Truncate(time.Second)}, nil
	}
	if strings.HasPrefix(lower, "@") {
		m, ok := macros[lower]
		if !ok {
			return nil, fmt.Errorf("unknown shorthand %q (use @hourly, @daily, @weekly, @monthly, @yearly or @every <duration>)", spec)
		}
		lower = m
	}
	parts := strings.Fields(lower)
	if len(parts) != 5 {
		return nil, fmt.Errorf("a cron expression has 5 fields (minute hour day-of-month month day-of-week), this one has %d", len(parts))
	}
	s := &Schedule{}
	var err error
	if s.minute, _, err = parseField(parts[0], minuteField); err != nil {
		return nil, err
	}
	if s.hour, _, err = parseField(parts[1], hourField); err != nil {
		return nil, err
	}
	if s.dom, s.domStar, err = parseField(parts[2], domField); err != nil {
		return nil, err
	}
	if s.month, _, err = parseField(parts[3], monthField); err != nil {
		return nil, err
	}
	if s.dow, s.dowStar, err = parseField(parts[4], dowField); err != nil {
		return nil, err
	}
	if s.dow&(1<<7) != 0 {
		s.dow = s.dow&^(1<<7) | 1
	}
	return s, nil
}

// parseField parses one comma-separated field into a bit set. star reports
// whether it begins with "*", which matters for the day fields: as in
// Vixie cron, when both day of month and day of week are restricted a day
// matching either runs ("0 0 1 * mon" = the 1st and every Monday).
func parseField(expr string, f field) (bits uint64, star bool, err error) {
	star = strings.HasPrefix(expr, "*") || strings.HasPrefix(expr, "?")
	for _, part := range strings.Split(expr, ",") {
		b, err := parseRange(part, f)
		if err != nil {
			return 0, false, err
		}
		bits |= b
	}
	return bits, star, nil
}

func parseRange(part string, f field) (uint64, error) {
	if part == "" {
		return 0, fmt.Errorf("%s: empty value in a list", f.name)
	}
	rng, stepStr, hasStep := strings.Cut(part, "/")
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepStr)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("%s: %q is not a valid step", f.name, stepStr)
		}
		step = n
	}
	var lo, hi int
	switch {
	case rng == "*" || rng == "?":
		lo, hi = f.min, f.max
		if f.name == dowField.name {
			hi = 6 // "*" is Sunday to Saturday; 7 would be Sunday again
		}
	case strings.Contains(rng, "-"):
		a, b, _ := strings.Cut(rng, "-")
		var err error
		if lo, err = value(a, f); err != nil {
			return 0, err
		}
		if hi, err = value(b, f); err != nil {
			return 0, err
		}
		if lo > hi {
			return 0, fmt.Errorf("%s: range %q runs backwards", f.name, rng)
		}
	default:
		v, err := value(rng, f)
		if err != nil {
			return 0, err
		}
		lo, hi = v, v
		if hasStep { // "5/15" = from 5 to the end, every 15
			hi = f.max
		}
	}
	var bits uint64
	for v := lo; v <= hi; v += step {
		bits |= 1 << uint(v)
	}
	return bits, nil
}

func value(s string, f field) (int, error) {
	if v, ok := f.names[s]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		if f.names != nil {
			return 0, fmt.Errorf("%s: %q is not a number or a name", f.name, s)
		}
		return 0, fmt.Errorf("%s: %q is not a number", f.name, s)
	}
	if v < f.min || v > f.max {
		return 0, fmt.Errorf("%s: %d is out of range (%d-%d)", f.name, v, f.min, f.max)
	}
	return v, nil
}

// Every returns the interval of an "@every" schedule, 0 otherwise.
func (s *Schedule) Every() time.Duration { return s.every }

// searchLimit bounds the search for the next time: an expression such as
// "0 0 30 2 *" (February 30th) never matches.
const searchLimit = 5 * 366 * 24 * time.Hour

// Next returns the first time after t that the schedule fires, in t's
// location, or the zero time if it never fires. An "@every" schedule
// fires every interval after t.
func (s *Schedule) Next(t time.Time) time.Time {
	if s.every > 0 {
		return t.Add(s.every).Truncate(time.Second)
	}
	loc := t.Location()
	// The search runs on the wall clock, in UTC where every day has 24
	// hours; each match is then placed in the real location.
	wall := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC).Add(time.Minute)
	end := wall.Add(searchLimit)
	for wall.Before(end) {
		if s.month&(1<<uint(wall.Month())) == 0 {
			wall = time.Date(wall.Year(), wall.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !s.dayMatches(wall) {
			wall = time.Date(wall.Year(), wall.Month(), wall.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if s.hour&(1<<uint(wall.Hour())) == 0 {
			wall = wall.Truncate(time.Hour).Add(time.Hour)
			continue
		}
		if s.minute&(1<<uint(wall.Minute())) == 0 {
			wall = wall.Add(time.Minute)
			continue
		}
		if at, ok := place(wall, loc, t); ok {
			return at
		}
		wall = wall.Add(time.Minute)
	}
	return time.Time{}
}

func (s *Schedule) dayMatches(wall time.Time) bool {
	dom := s.dom&(1<<uint(wall.Day())) != 0
	dow := s.dow&(1<<uint(wall.Weekday())) != 0
	if s.domStar || s.dowStar {
		return dom && dow
	}
	return dom || dow
}

// place turns a wall-clock time into an instant in loc after t. A wall
// time that does not exist (skipped when clocks go forward) is not
// placed; one that exists twice (clocks go back) is the earlier instant
// that is still after t.
func place(wall time.Time, loc *time.Location, after time.Time) (time.Time, bool) {
	at := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, loc)
	same := func(x time.Time) bool {
		return x.Year() == wall.Year() && x.Month() == wall.Month() && x.Day() == wall.Day() &&
			x.Hour() == wall.Hour() && x.Minute() == wall.Minute()
	}
	if !same(at) {
		return time.Time{}, false
	}
	// Go may resolve an ambiguous time to either occurrence; look for the
	// other one (DST shifts are 30 minutes or an hour).
	best := at
	for _, d := range []time.Duration{-time.Hour, -30 * time.Minute, 30 * time.Minute, time.Hour} {
		if o := at.Add(d); same(o) && o.After(after) && (!best.After(after) || o.Before(best)) {
			best = o
		}
	}
	if !best.After(after) {
		return time.Time{}, false
	}
	return best, true
}
