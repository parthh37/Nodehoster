package waf

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"golang.org/x/time/rate"
)

// The recorder keeps what the firewall did: per-site counters for the
// metrics, and events (blocked and detected requests) for the console,
// saved in batches by a background writer. Recording never blocks a
// request: under attack, events beyond what the writer keeps up with (or
// beyond eventsPerSecond) are dropped and counted, the counters stay exact.

const (
	queueSize       = 2048
	batchSize       = 256
	flushEvery      = time.Second
	eventsPerSecond = 200 // saved; bursts of twice that
	// notablePerMinute limits security.waf events (and webhook calls): the
	// rest of a minute's blocks are summarized in the next one.
	notablePerMinute = 10
)

// Counters are a site's firewall counters. Their fields are updated
// atomically on the request path.
type Counters struct {
	Inspected atomic.Int64 // requests inspected
	Blocked   atomic.Int64
	Detected  atomic.Int64 // detect mode: would have been blocked
	byCat     [16]atomic.Int64
}

// CounterSnapshot is a copy of a site's counters.
type CounterSnapshot struct {
	Inspected int64            `json:"inspected"`
	Blocked   int64            `json:"blocked"`
	Detected  int64            `json:"detected"`
	Matches   map[string]int64 `json:"matches"` // rule matches by category
}

// Matched counts a request's matches by category.
func (c *Counters) Matched(res Result) {
	if c == nil {
		return
	}
	for _, m := range res.Matches {
		if i, ok := catIndex[m.Category]; ok {
			c.byCat[i].Add(1)
		}
	}
}

func (c *Counters) snapshot() CounterSnapshot {
	s := CounterSnapshot{Inspected: c.Inspected.Load(), Blocked: c.Blocked.Load(), Detected: c.Detected.Load(), Matches: map[string]int64{}}
	for i, cat := range model.WAFCategories {
		s.Matches[cat] = c.byCat[i].Load()
	}
	return s
}

// RecorderOptions connect the recorder to the rest of the server.
type RecorderOptions struct {
	Log *slog.Logger
	// Save stores a batch of events.
	Save func(ctx context.Context, events []model.WAFEvent) error
	// OnBlock reports a blocked request, at most notablePerMinute times a
	// minute (the security.waf event).
	OnBlock func(ev model.WAFEvent, message string)
	Now     func() time.Time
}

// Recorder is safe for concurrent use; a nil *Recorder records nothing.
type Recorder struct {
	opts    RecorderOptions
	queue   chan model.WAFEvent
	limiter *rate.Limiter
	dropped atomic.Int64

	mu       sync.RWMutex
	counters map[string]*Counters

	nMu      sync.Mutex
	nWindow  time.Time
	nSent    int
	nDropped int
}

// NewRecorder creates a recorder; Run saves its events.
func NewRecorder(opts RecorderOptions) *Recorder {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Recorder{
		opts: opts, queue: make(chan model.WAFEvent, queueSize),
		limiter:  rate.NewLimiter(eventsPerSecond, 2*eventsPerSecond),
		counters: map[string]*Counters{},
	}
}

// Counters returns a site's counters, created on first use. The proxy
// keeps the pointer with the site's compiled configuration.
func (r *Recorder) Counters(siteID string) *Counters {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	c := r.counters[siteID]
	r.mu.RUnlock()
	if c != nil {
		return c
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c = r.counters[siteID]; c == nil {
		c = &Counters{}
		r.counters[siteID] = c
	}
	return c
}

// Snapshot returns every site's counters.
func (r *Recorder) Snapshot() map[string]CounterSnapshot {
	out := map[string]CounterSnapshot{}
	if r == nil {
		return out
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for id, c := range r.counters {
		out[id] = c.snapshot()
	}
	return out
}

// Forget drops a deleted site's counters.
func (r *Recorder) Forget(siteID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.counters, siteID)
	r.mu.Unlock()
}

// Dropped is how many events were not saved (too many, too fast).
func (r *Recorder) Dropped() int64 {
	if r == nil {
		return 0
	}
	return r.dropped.Load()
}

// Record queues an event to be saved and, for a block, raises the
// notable event. It never blocks.
func (r *Recorder) Record(ev model.WAFEvent) {
	if r == nil {
		return
	}
	if ev.Action == model.WAFActionBlocked {
		r.notable(ev)
	}
	if !r.limiter.Allow() {
		r.dropped.Add(1)
		return
	}
	select {
	case r.queue <- ev:
	default:
		r.dropped.Add(1)
	}
}

// notable raises security.waf at most notablePerMinute times a minute;
// the first one after that mentions how many blocks went unreported.
func (r *Recorder) notable(ev model.WAFEvent) {
	if r.opts.OnBlock == nil {
		return
	}
	msg := Describe(ev)
	now := r.opts.Now()
	r.nMu.Lock()
	if now.Sub(r.nWindow) >= time.Minute {
		if r.nDropped > 0 {
			msg += fmt.Sprintf("; %d more requests were blocked in the last minute", r.nDropped)
		}
		r.nWindow, r.nSent, r.nDropped = now, 0, 0
	}
	if r.nSent >= notablePerMinute {
		r.nDropped++
		r.nMu.Unlock()
		return
	}
	r.nSent++
	r.nMu.Unlock()
	r.opts.OnBlock(ev, msg)
}

// Describe is a one-line account of an event, for the event log and
// webhooks: "Blocked GET /login from 203.0.113.7: SQL injection ... in
// argument user (rule 942100, score 5) [request 9f2c...]".
func Describe(ev model.WAFEvent) string {
	verb := "Blocked"
	if ev.Action == model.WAFActionDetected {
		verb = "Detected (not blocked)"
	}
	what := "an attack"
	if len(ev.Matches) > 0 {
		m := ev.Matches[0]
		what = fmt.Sprintf("%s (rule %d)", m.Message, m.RuleID)
		what += " in " + m.Where()
		if len(ev.Matches) > 1 {
			what += fmt.Sprintf(" and %d more", len(ev.Matches)-1)
		}
	}
	return fmt.Sprintf("%s %s %s from %s: %s, score %d [request %s]", verb, ev.Method, ev.Path, ev.ClientIP, what, ev.Score, ev.ID)
}

// Run saves queued events until ctx ends, then saves what is left.
func (r *Recorder) Run(ctx context.Context) {
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	batch := make([]model.WAFEvent, 0, batchSize)
	flush := func() {
		if len(batch) == 0 || r.opts.Save == nil {
			batch = batch[:0]
			return
		}
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := r.opts.Save(sctx, batch); err != nil {
			r.opts.Log.Warn("save firewall events", "err", err, "events", len(batch))
		}
		cancel()
		batch = batch[:0]
	}
	for {
		select {
		case ev := <-r.queue:
			batch = append(batch, ev)
			if len(batch) >= batchSize {
				flush()
			}
		case <-t.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case ev := <-r.queue:
					batch = append(batch, ev)
					if len(batch) >= batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}
