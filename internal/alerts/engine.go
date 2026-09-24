// Package alerts evaluates resource alert rules (model.AlertRule) against
// the metrics NodeHoster already collects, every few seconds, and decides
// when an alert fires, reminds and resolves (see Step, the state machine).
// It keeps what is in progress in memory, stores alerts that fired, and
// hands notifications to the caller (events, e-mail); it gathers no
// metrics and sends nothing itself.
//
// Across a restart: firing alerts are stored, and come back firing without
// a second notification; they resolve (with a notification) once their
// condition has been clear for the recovery period, as always, or at the
// first evaluation if their rule or site is gone. Pending conditions are
// not stored: the "for" period starts over after a restart.
package alerts

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
)

// ErrNotFound: no alert in progress has that ID.
var ErrNotFound = errors.New("no alert in progress has this ID (it may have resolved)")

const (
	// maxNotices is how many notifications one evaluation delivers; the
	// rest are summarized in one more. When the whole server is in trouble,
	// every site's alerts fire together, and a chat channel should get a
	// dozen messages, not a hundred.
	maxNotices = 10
	// saveEvery bounds how stale a firing alert's stored reading gets.
	saveEvery = 5 * time.Minute
	// keepResolved is how many resolved alerts the history keeps at most,
	// besides the age limit.
	keepResolved = 5000
)

// Notice is a notification to deliver.
type Notice struct {
	Kind    Transition // Fire, Remind or Resolve; None for a summary of notices left out
	Alert   model.Alert
	Message string
}

// Options connect the engine to the rest of the server.
type Options struct {
	Store *store.Store
	Log   *slog.Logger
	// Deliver receives each evaluation's notifications at once, after the
	// engine's lock is released. It is called from Evaluate's goroutine.
	Deliver func([]Notice)
	// LatencyBounds are the buckets of SiteSample.Latency, in ms.
	LatencyBounds []float64
	NewID         func() string // nil = random UUIDs
}

// Engine evaluates the rules. Evaluate is called by one goroutine; the
// other methods may be called concurrently with it.
type Engine struct {
	opts Options

	mu       sync.Mutex
	active   map[string]*tracked           // rule|site → pending or firing
	silences map[string]model.AlertSilence // rule|site → timed silence outliving its alert
	windows  map[string]*window            // site → traffic counters
}

type tracked struct {
	key     string
	state   State
	alert   model.Alert
	peaked  bool // alert.Peak holds a reading
	savedAt time.Time
}

func New(opts Options) *Engine {
	if opts.NewID == nil {
		opts.NewID = uuid.NewString
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	return &Engine{
		opts: opts, active: map[string]*tracked{},
		silences: map[string]model.AlertSilence{}, windows: map[string]*window{},
	}
}

func key(ruleID, siteID string) string { return strings.ToLower(ruleID) + "|" + siteID }

// Load restores the alerts that were firing when the service stopped, and
// the timed silences of recent ones.
func (e *Engine) Load(ctx context.Context, now time.Time) error {
	firing, err := e.opts.Store.FiringAlerts(ctx)
	if err != nil {
		return err
	}
	recent, err := e.opts.Store.ListAlerts(ctx, store.AlertFilter{Since: now.Add(-7 * 24 * time.Hour), Limit: 1000})
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range firing {
		k := key(a.RuleID, model.SlotKey(a.SiteID, a.Slot))
		if a.FiredAt == nil {
			continue
		}
		st := State{Phase: Firing, Since: a.Since, FiredAt: *a.FiredAt, LastNotified: *a.FiredAt}
		if a.LastNotifiedAt != nil {
			st.LastNotified = *a.LastNotifiedAt
		}
		// Oldest first: a duplicate (which should not exist) is replaced
		// by the newer alert, and will not be updated again.
		e.active[k] = &tracked{key: k, state: st, alert: a, peaked: true, savedAt: now}
	}
	for i := len(recent) - 1; i >= 0; i-- { // oldest first: the newest silence wins
		a := recent[i]
		if a.State == model.AlertResolved && a.Silence != nil && a.Silence.Until != nil && a.Silence.Active(now) {
			e.silences[key(a.RuleID, model.SlotKey(a.SiteID, a.Slot))] = *a.Silence
		}
	}
	return nil
}

// Evaluate runs every rule once against the samples and delivers what
// changed.
func (e *Engine) Evaluate(ctx context.Context, now time.Time, cfg model.AlertSettings, sites []SiteSample, server ServerSample) {
	notices := e.evaluate(ctx, now, cfg, sites, server)
	if len(notices) > 0 && e.opts.Deliver != nil {
		e.opts.Deliver(notices)
	}
}

func (e *Engine) evaluate(ctx context.Context, now time.Time, cfg model.AlertSettings, sites []SiteSample, server ServerSample) []Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	recovery := time.Duration(cfg.RecoveryMinutes) * time.Minute
	var out []Notice
	seen := map[string]bool{}
	live := map[string]bool{} // subjects: sites and slots (model.SlotKey)
	for _, in := range sites {
		id := model.SlotKey(in.Site.ID, in.Site.Slot)
		live[id] = true
		live[in.Site.ID] = true
		w := e.windows[id]
		if w == nil {
			w = &window{}
			e.windows[id] = w
		}
		w.add(sampleCounters(now, in))
		if !cfg.Enabled {
			continue
		}
		for _, r := range model.EffectiveAlertRules(cfg.SiteRules, in.Site) {
			k := key(r.ID, id)
			seen[k] = true
			if in.Hold {
				continue // not watched meanwhile, as across a gap
			}
			obs, rd := measureSite(r, in, w, e.opts.LatencyBounds)
			e.step(ctx, k, r, in.Site, obs, rd, now, recovery, &out)
		}
	}
	for id := range e.windows {
		if !live[id] {
			delete(e.windows, id)
		}
	}
	if cfg.Enabled {
		for _, r := range cfg.ServerRules {
			if r.Disabled {
				continue
			}
			k := key(r.ID, "")
			seen[k] = true
			obs, rd := measureServer(r, server)
			e.step(ctx, k, r, nil, obs, rd, now, recovery, &out)
		}
	}
	// Alerts nothing evaluates any more end now.
	for k, t := range e.active {
		if seen[k] {
			continue
		}
		note := "rule removed or turned off"
		switch {
		case !cfg.Enabled:
			note = "alerts turned off"
		case t.alert.SiteID != "" && !live[t.alert.SiteID]:
			note = "site deleted"
		case t.alert.Slot != "" && !live[model.SlotKey(t.alert.SiteID, t.alert.Slot)]:
			note = "slot removed"
		}
		e.finish(ctx, t, now, note, &out)
	}
	for k, s := range e.silences {
		if !s.Active(now) {
			delete(e.silences, k)
		}
	}
	return limitNotices(out)
}

// step feeds one observation to one rule on one subject (site nil: the
// server) and acts on the transition.
func (e *Engine) step(ctx context.Context, k string, r model.AlertRule, site *model.Site, obs Observation, rd Reading, now time.Time, recovery time.Duration, out *[]Notice) {
	t := e.active[k]
	if t != nil && t.alert.Metric != r.Metric {
		// The rule now watches something else: what was pending or firing
		// is over, and the new metric starts from scratch.
		e.finish(ctx, t, now, "rule changed", out)
		t = nil
	}
	if t == nil {
		if obs != Breach {
			return // nothing starts but a breach
		}
		t = &tracked{key: k, alert: model.Alert{ID: e.opts.NewID(), State: model.AlertPending}}
		if site != nil {
			t.alert.SiteID, t.alert.Slot = site.ID, site.Slot
		}
		if s, ok := e.silences[k]; ok && s.Active(now) {
			t.alert.Silence = &s
		}
		e.active[k] = t
	}
	var tr Transition
	t.state, tr = Step(t.state, obs, now, Timing{
		For:      time.Duration(r.ForMinutes) * time.Minute,
		Recovery: recovery,
		Repeat:   time.Duration(r.RepeatHours) * time.Hour,
	})

	a := &t.alert
	// The rule as it is now: its limit may have been edited meanwhile.
	a.RuleID, a.Metric, a.Severity, a.Threshold, a.ForMinutes = r.ID, r.Metric, r.Severity, r.Threshold, r.ForMinutes
	if site != nil {
		a.SiteName = site.Name
		if site.Slot != "" {
			a.SiteName = fmt.Sprintf("%s (%s)", site.Name, site.Slot) // "shop (staging)"
		}
	}
	if obs != NoData {
		a.Value, a.Detail = round(rd.Value), rd.Detail
	}
	if obs == Breach && (!t.peaked || worse(a.Metric, a.Value, a.Peak)) {
		a.Peak, t.peaked = a.Value, true
	}
	if t.state.Phase != Inactive {
		a.Since = t.state.Since
	}
	if a.Silence != nil && !a.Silence.Active(now) {
		a.Silence = nil
	}
	silenced := a.Silence != nil

	switch tr {
	case Fire:
		fired := now
		a.State, a.FiredAt = model.AlertFiring, &fired
		a.Message = firingMessage(a, now)
		if !silenced {
			e.notified(a, now)
			*out = append(*out, Notice{Kind: Fire, Alert: *a, Message: a.Message})
		}
		e.save(ctx, t, now)
	case Remind:
		a.Message = firingMessage(a, now)
		if a.Notified && !silenced {
			a.LastNotifiedAt = &now
			*out = append(*out, Notice{Kind: Remind, Alert: *a, Message: reminderMessage(a, now)})
			e.save(ctx, t, now)
		}
	case Resolve:
		e.finish(ctx, t, now, "", out)
	default:
		switch t.state.Phase {
		case Inactive:
			delete(e.active, k) // a pending condition that cleared: never announced, never stored
		case Pending:
			a.Message = pendingMessage(a, now)
		case Firing:
			a.Message = firingMessage(a, now)
			if !a.Notified && !silenced {
				// Fired while silenced, and the silence has ended (or was
				// lifted) with the condition still there.
				e.notified(a, now)
				t.state.LastNotified = now
				*out = append(*out, Notice{Kind: Fire, Alert: *a, Message: a.Message})
				e.save(ctx, t, now)
			} else if now.Sub(t.savedAt) >= saveEvery {
				e.save(ctx, t, now)
			}
		}
	}
}

func (e *Engine) notified(a *model.Alert, now time.Time) {
	a.Notified = true
	a.LastNotifiedAt = &now
}

// finish resolves an alert: it recovered (note ""), or nothing evaluates
// it any more (note says why). A pending one simply goes.
func (e *Engine) finish(ctx context.Context, t *tracked, now time.Time, note string, out *[]Notice) {
	delete(e.active, t.key)
	a := &t.alert
	if a.State != model.AlertFiring {
		return
	}
	resolved := now
	a.State, a.ResolvedAt, a.ResolveNote = model.AlertResolved, &resolved, note
	a.Message = resolvedMessage(a, now)
	if s := a.Silence; s != nil && s.Until != nil && s.Active(now) {
		e.silences[t.key] = *s // a flapping condition stays quiet until the silence ends
	}
	e.save(ctx, t, now)
	if a.Notified {
		*out = append(*out, Notice{Kind: Resolve, Alert: *a, Message: a.Message})
	}
}

func (e *Engine) save(ctx context.Context, t *tracked, now time.Time) {
	if t.alert.State == model.AlertPending {
		return
	}
	t.savedAt = now
	a := t.alert
	if err := e.opts.Store.PutAlert(ctx, &a); err != nil {
		e.opts.Log.Error("save alert", "alert", a.ID, "err", err)
	}
}

// noticeRank orders notifications for delivery when there are too many:
// critical alerts firing first, resolutions last.
func noticeRank(n Notice) int {
	switch {
	case n.Kind == Fire && n.Alert.Severity == model.SeverityCritical:
		return 0
	case n.Kind == Fire:
		return 1
	case n.Kind == Remind:
		return 2
	}
	return 3
}

// limitNotices keeps maxNotices notifications and summarizes the rest.
func limitNotices(out []Notice) []Notice {
	if len(out) <= maxNotices {
		return out
	}
	slices.SortStableFunc(out, func(a, b Notice) int { return cmp.Compare(noticeRank(a), noticeRank(b)) })
	rest := out[maxNotices:]
	kept := slices.Clone(out[:maxNotices])
	// The summary is as severe as the most important notice it stands for.
	head := rest[0].Alert
	return append(kept, Notice{Kind: None, Message: summaryMessage(rest), Alert: model.Alert{Severity: head.Severity, State: head.State}})
}

// ---- queries and silences

// List returns the alerts in progress: critical first, then the longest
// standing. Enabled is left to the caller (it is a setting).
func (e *Engine) List() model.AlertList {
	e.mu.Lock()
	l := model.AlertList{Firing: []model.Alert{}, Pending: []model.Alert{}}
	for _, t := range e.active {
		if t.state.Phase == Firing {
			l.Firing = append(l.Firing, t.alert)
		} else if t.state.Phase == Pending {
			l.Pending = append(l.Pending, t.alert)
		}
	}
	e.mu.Unlock()
	order := func(a, b model.Alert) int {
		if a.Severity != b.Severity {
			if a.Severity == model.SeverityCritical {
				return -1
			}
			return 1
		}
		if c := a.Since.Compare(b.Since); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	}
	slices.SortFunc(l.Firing, order)
	slices.SortFunc(l.Pending, order)
	return l
}

// Get returns an alert in progress.
func (e *Engine) Get(id string) (model.Alert, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t := e.find(id); t != nil {
		return t.alert, true
	}
	return model.Alert{}, false
}

func (e *Engine) find(id string) *tracked {
	for _, t := range e.active {
		if t.alert.ID == id {
			return t
		}
	}
	return nil
}

// History returns stored alerts, most recent first, with the ones in
// progress as they are now (the store has them as of their last save).
func (e *Engine) History(ctx context.Context, f store.AlertFilter) ([]model.Alert, error) {
	list, err := e.opts.Store.ListAlerts(ctx, f)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range list {
		if t := e.find(list[i].ID); t != nil {
			list[i] = t.alert
		}
	}
	return list, nil
}

// Silence keeps an alert in progress quiet for minutes (0: until it
// resolves, which acknowledges it).
func (e *Engine) Silence(ctx context.Context, id string, minutes int, by, note string, now time.Time) (model.Alert, error) {
	if minutes < 0 || minutes > 30*24*60 {
		return model.Alert{}, &model.ValidationError{Field: "minutes", Message: "must be between 0 (until the alert resolves) and 43200 (30 days)"}
	}
	if len(note) > 500 {
		return model.Alert{}, &model.ValidationError{Field: "note", Message: "must be at most 500 characters"}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.find(id)
	if t == nil {
		return model.Alert{}, ErrNotFound
	}
	s := model.AlertSilence{By: by, At: now, Note: strings.TrimSpace(note)}
	if minutes > 0 {
		until := now.Add(time.Duration(minutes) * time.Minute)
		s.Until = &until
	}
	t.alert.Silence = &s
	e.save(ctx, t, now)
	return t.alert, nil
}

// Unsilence lifts an alert's silence. If it is firing and was never
// notified, the next evaluation notifies it.
func (e *Engine) Unsilence(ctx context.Context, id string, now time.Time) (model.Alert, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.find(id)
	if t == nil {
		return model.Alert{}, ErrNotFound
	}
	t.alert.Silence = nil
	delete(e.silences, t.key)
	e.save(ctx, t, now)
	return t.alert, nil
}

// Prune deletes resolved alerts older than retention (and the oldest
// beyond a few thousand).
func (e *Engine) Prune(ctx context.Context, now time.Time, retention time.Duration) error {
	return e.opts.Store.PruneAlerts(ctx, now.Add(-retention), keepResolved)
}
