package alerts

import "time"

// The state machine of one rule on one subject (a site, or the server).
// It is a pure function of the previous state, what one evaluation found
// and the time, so that when an alert fires and resolves can be tested
// (and changed) without metrics, timers or I/O.
//
//	           breach                   held for `for`
//	Inactive ─────────▶ Pending ───────────────────────▶ Firing ──┐ breach: reminder
//	    ▲                 │ clear                          │  ▲ ◀──┘ every `repeat`
//	    └─────────────────┘                                │  │ breach
//	    ▲                                clear (or no data)▼  │
//	    └──────────────────────────────────────────── recovering
//	                         clear for `recovery`

// Observation is what one evaluation found.
type Observation int

const (
	// Clear: measured and within the limit, or nothing runs that could be
	// past it (a stopped site).
	Clear Observation = iota
	// Breach: measured and past the limit.
	Breach
	// NoData: nothing to measure right now, such as a site starting up. A
	// pending condition neither advances nor resets; a firing one counts
	// it as clear, so an alert never outlives what it was about.
	NoData
)

// Phase is where a rule stands.
type Phase int

const (
	Inactive Phase = iota
	Pending        // the condition holds, not yet for long enough
	Firing
)

// Timing is a rule's durations.
type Timing struct {
	For      time.Duration // how long the condition must hold before firing
	Recovery time.Duration // how long it must stay clear before a firing alert resolves
	Repeat   time.Duration // reminders while firing; 0 = none
}

// MaxGap is the longest time between two evaluations that still counts
// as watching continuously. Evaluations run every 15 s; a longer gap means
// the service was stopped or stalled, and nobody can vouch for what the
// metric did meanwhile.
const MaxGap = time.Minute

// State is a rule's state on one subject.
type State struct {
	Phase        Phase
	Since        time.Time // Pending, Firing: when the condition started holding
	FiredAt      time.Time // Firing
	ClearSince   time.Time // Firing: first seen clear again; zero while it holds
	LastSeen     time.Time // the previous evaluation
	LastNotified time.Time // Firing: when it fired or the last reminder was due
}

// Transition is what a Step asks the caller to announce.
type Transition int

const (
	None    Transition = iota
	Fire               // the condition has held for `for`
	Remind             // still firing, `repeat` after the last notification
	Resolve            // clear for `recovery`
)

func (t Transition) String() string {
	return [...]string{"none", "fire", "remind", "resolve"}[t]
}

// sustained is the product rule for firing: a condition that started
// holding at since, and holds now, has held long enough. "Held" means
// every evaluation since then found it past the limit, except those with
// nothing to measure (a site restarting), with no gap longer than MaxGap
// between evaluations (see Step). Change this to make alerts fire on,
// say, 4 of the last 5 minutes instead.
func sustained(since, now time.Time, t Timing) bool {
	return now.Sub(since) >= t.For
}

// recovered is the product rule for resolving: a firing alert whose
// condition has been clear (or unmeasurable) since clearSince has been so
// long enough. The recovery period is the hysteresis that keeps a value
// hovering at the limit from firing and resolving over and over.
func recovered(clearSince, now time.Time, t Timing) bool {
	return now.Sub(clearSince) >= t.Recovery
}

// Step advances a rule's state by one evaluation.
func Step(s State, obs Observation, now time.Time, t Timing) (State, Transition) {
	gap := !s.LastSeen.IsZero() && now.Sub(s.LastSeen) > MaxGap
	s.LastSeen = now

	if s.Phase == Inactive {
		if obs != Breach {
			return s, None
		}
		s.Phase, s.Since, gap = Pending, now, false
	}

	if s.Phase == Pending {
		if gap {
			s.Since = now // unwatched time does not count towards `for`
		}
		switch obs {
		case Clear:
			return State{LastSeen: now}, None
		case NoData:
			return s, None
		}
		if !sustained(s.Since, now, t) {
			return s, None
		}
		s.Phase, s.FiredAt, s.LastNotified, s.ClearSince = Firing, now, now, time.Time{}
		return s, Fire
	}

	// Firing.
	if obs == Breach {
		s.ClearSince = time.Time{}
		if t.Repeat > 0 && now.Sub(s.LastNotified) >= t.Repeat {
			s.LastNotified = now
			return s, Remind
		}
		return s, None
	}
	if s.ClearSince.IsZero() || gap {
		s.ClearSince = now
	}
	if recovered(s.ClearSince, now, t) {
		return State{LastSeen: now}, Resolve
	}
	return s, None
}
