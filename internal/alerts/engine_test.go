package alerts

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
)

type harness struct {
	t   *testing.T
	st  *store.Store
	e   *Engine
	now time.Time
	cfg model.AlertSettings

	mu      sync.Mutex
	notices []Notice
	ids     int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &harness{t: t, st: st, now: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	h.cfg = model.AlertSettings{
		Enabled:         true,
		RecoveryMinutes: 1,
		SiteRules:       []model.AlertRule{{ID: "cpu", Metric: model.AlertCPU, Threshold: 80, ForMinutes: 1, Severity: model.SeverityWarning}},
		ServerRules:     []model.AlertRule{{ID: "disk", Metric: model.AlertDiskFree, Threshold: 10, ForMinutes: 0, Severity: model.SeverityCritical}},
	}
	h.e = h.engine()
	return h
}

func (h *harness) engine() *Engine {
	return New(Options{
		Store: h.st, LatencyBounds: testBounds,
		NewID: func() string {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.ids++
			return fmt.Sprintf("a%d", h.ids)
		},
		Deliver: func(n []Notice) {
			h.mu.Lock()
			h.notices = append(h.notices, n...)
			h.mu.Unlock()
		},
	})
}

// take returns and forgets the notices so far, as "kind:site:message".
func (h *harness) take() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, n := range h.notices {
		out = append(out, fmt.Sprintf("%s:%s:%s", n.Kind, n.Alert.SiteName, n.Message))
	}
	h.notices = nil
	return out
}

func site(id string, cpu float64) SiteSample {
	s := &model.Site{ID: id, Name: id + ".example.com", Type: model.SiteNode, Node: &model.NodeConfig{Instances: 1}}
	return SiteSample{Site: s, Status: model.SiteStatus{SiteID: id, State: model.StateRunning, Instances: []model.InstanceStatus{ready(0, cpu, 100)}}}
}

var healthyDisk = ServerSample{CPUPercent: 10, MemoryPercent: 10, Disks: []Disk{{`C:\`, 50}}}

// tick evaluates once, 15 s after the previous one.
func (h *harness) tick(server ServerSample, sites ...SiteSample) {
	h.now = h.now.Add(15 * time.Second)
	h.e.Evaluate(context.Background(), h.now, h.cfg, sites, server)
}

func (h *harness) ticks(n int, server ServerSample, sites ...SiteSample) {
	for range n {
		h.tick(server, sites...)
	}
}

func expectNotices(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("notices:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestEngineFireAndResolve(t *testing.T) {
	h := newHarness(t)
	h.tick(healthyDisk, site("api", 95))
	l := h.e.List()
	if len(l.Pending) != 1 || len(l.Firing) != 0 || l.Pending[0].State != model.AlertPending {
		t.Fatalf("pending: %+v", l)
	}
	if !strings.HasSuffix(l.Pending[0].Message, "fires after 1 min") {
		t.Fatalf("pending message: %q", l.Pending[0].Message)
	}
	h.ticks(3, healthyDisk, site("api", 95))
	expectNotices(t, h.take())
	h.tick(healthyDisk, site("api", 97)) // 1 min after the first breach
	expectNotices(t, h.take(), "fire:api.example.com:CPU 97% for 1 min (limit 80%)")
	l = h.e.List()
	if len(l.Firing) != 1 || l.Firing[0].SiteID != "api" || l.Firing[0].Peak != 97 || l.Firing[0].FiredAt == nil {
		t.Fatalf("firing: %+v", l)
	}
	stored, err := h.st.GetAlert(context.Background(), l.Firing[0].ID)
	if err != nil || stored.State != model.AlertFiring || !stored.Notified {
		t.Fatalf("stored: %+v %v", stored, err)
	}

	// The recovery period: a blip under the limit does not resolve.
	h.tick(healthyDisk, site("api", 50))
	h.tick(healthyDisk, site("api", 90))
	h.ticks(5, healthyDisk, site("api", 30))
	expectNotices(t, h.take(), "resolve:api.example.com:Resolved after 2 min: CPU 30% (limit 80%)")
	if l := h.e.List(); len(l.Firing)+len(l.Pending) != 0 {
		t.Fatalf("still active: %+v", l)
	}
	hist, err := h.e.History(context.Background(), store.AlertFilter{})
	if err != nil || len(hist) != 1 || hist[0].State != model.AlertResolved || hist[0].ResolvedAt == nil || hist[0].Peak != 97 {
		t.Fatalf("history: %+v %v", hist, err)
	}
}

func TestEnginePendingNeverStored(t *testing.T) {
	h := newHarness(t)
	h.ticks(2, healthyDisk, site("api", 95))
	h.tick(healthyDisk, site("api", 10))
	if l := h.e.List(); len(l.Pending) != 0 {
		t.Fatalf("pending after clearing: %+v", l)
	}
	hist, _ := h.e.History(context.Background(), store.AlertFilter{})
	if len(hist) != 0 {
		t.Fatalf("a pending alert was stored: %+v", hist)
	}
	expectNotices(t, h.take())
}

func TestEngineStoppedSite(t *testing.T) {
	h := newHarness(t)
	h.ticks(5, healthyDisk, site("api", 95))
	expectNotices(t, h.take(), "fire:api.example.com:CPU 95% for 1 min (limit 80%)")
	// Stopping the site resolves its alert after the recovery period;
	// starting it again does not fire before the condition held again.
	stopped := site("api", 0)
	stopped.Status.State = model.StateStopped
	h.ticks(5, healthyDisk, stopped)
	if got := h.take(); len(got) != 1 || !strings.HasPrefix(got[0], "resolve:") {
		t.Fatalf("stopped: %v", got)
	}
	starting := site("api", 100)
	starting.Status.State = model.StateStarting
	h.ticks(10, healthyDisk, starting)
	expectNotices(t, h.take())
	if l := h.e.List(); len(l.Pending) != 0 {
		t.Fatalf("a starting site is pending: %+v", l)
	}
}

func TestEngineServerRules(t *testing.T) {
	h := newHarness(t)
	low := ServerSample{CPUPercent: 10, MemoryPercent: 10, Disks: []Disk{{`C:\`, 50}, {`D:\`, 4}}}
	h.tick(low) // forMinutes 0: the first breach fires
	expectNotices(t, h.take(), `fire::Free disk space 4% on D:\ (limit 10%)`)
	l := h.e.List()
	if len(l.Firing) != 1 || l.Firing[0].SiteID != "" || l.Firing[0].Severity != model.SeverityCritical {
		t.Fatalf("server alert: %+v", l)
	}
}

func TestEngineSilence(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.tick(healthyDisk, site("api", 95))
	id := h.e.List().Pending[0].ID
	// Silenced for an hour while pending: it fires quietly.
	a, err := h.e.Silence(ctx, id, 60, "alice", "deploying", h.now)
	if err != nil || a.Silence == nil || a.Silence.Until == nil || a.Silence.By != "alice" || a.Silence.Note != "deploying" {
		t.Fatalf("silence: %+v %v", a, err)
	}
	h.ticks(5, healthyDisk, site("api", 95))
	expectNotices(t, h.take())
	if l := h.e.List(); len(l.Firing) != 1 || l.Firing[0].Notified {
		t.Fatalf("silenced firing: %+v", l)
	}
	// Never notified, so its resolution is not either.
	h.ticks(5, healthyDisk, site("api", 10))
	expectNotices(t, h.take())
	// The silence outlives the alert: the next one is quiet too.
	h.ticks(5, healthyDisk, site("api", 95))
	expectNotices(t, h.take())
	l := h.e.List()
	if len(l.Firing) != 1 || l.Firing[0].Silence == nil {
		t.Fatalf("second alert: %+v", l)
	}
	// Lifting the silence notifies the alert still firing.
	if _, err := h.e.Unsilence(ctx, l.Firing[0].ID, h.now); err != nil {
		t.Fatal(err)
	}
	h.tick(healthyDisk, site("api", 95))
	if got := h.take(); len(got) != 1 || !strings.HasPrefix(got[0], "fire:api.example.com:CPU 95% for 1 min") {
		t.Fatalf("after unsilencing: %v", got)
	}

	if _, err := h.e.Silence(ctx, "nope", 0, "alice", "", h.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown alert: %v", err)
	}
	var ve *model.ValidationError
	if _, err := h.e.Silence(ctx, l.Firing[0].ID, -5, "alice", "", h.now); !errors.As(err, &ve) {
		t.Fatalf("negative minutes: %v", err)
	}
}

func TestEngineAcknowledge(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.cfg.SiteRules[0].RepeatHours = 1
	h.ticks(5, healthyDisk, site("api", 95))
	expectNotices(t, h.take(), "fire:api.example.com:CPU 95% for 1 min (limit 80%)")
	id := h.e.List().Firing[0].ID
	if _, err := h.e.Silence(ctx, id, 0, "bob", "on it", h.now); err != nil {
		t.Fatal(err)
	}
	// Acknowledged: no reminders, but it was notified, so its resolution is.
	h.ticks(4*70, healthyDisk, site("api", 95))
	expectNotices(t, h.take())
	h.ticks(5, healthyDisk, site("api", 10))
	if got := h.take(); len(got) != 1 || !strings.HasPrefix(got[0], "resolve:") {
		t.Fatalf("resolution: %v", got)
	}
	// Acknowledging lasts until the alert resolves: the next one notifies.
	h.ticks(5, healthyDisk, site("api", 95))
	if got := h.take(); len(got) != 1 || !strings.HasPrefix(got[0], "fire:") {
		t.Fatalf("next alert: %v", got)
	}
}

func TestEngineReminders(t *testing.T) {
	h := newHarness(t)
	h.cfg.SiteRules[0].RepeatHours = 1
	h.ticks(5, healthyDisk, site("api", 95))
	h.take()
	h.ticks(4*60, healthyDisk, site("api", 95))
	expectNotices(t, h.take(), "remind:api.example.com:Still firing: CPU 95% for 1 h 1 min (limit 80%)")
}

func TestEngineRestart(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.ticks(5, healthyDisk, site("api", 95))
	h.take()
	firing := h.e.List().Firing[0]

	// The service restarts ten minutes later with the site still hot: the
	// alert is still firing, and not notified again.
	h.now = h.now.Add(10 * time.Minute)
	h.e = h.engine()
	if err := h.e.Load(ctx, h.now); err != nil {
		t.Fatal(err)
	}
	if l := h.e.List(); len(l.Firing) != 1 || l.Firing[0].ID != firing.ID {
		t.Fatalf("restored: %+v", l)
	}
	h.ticks(8, healthyDisk, site("api", 95))
	expectNotices(t, h.take())
	// It resolves as usual, and says so.
	h.ticks(5, healthyDisk, site("api", 5))
	if got := h.take(); len(got) != 1 || !strings.HasPrefix(got[0], "resolve:api.example.com:Resolved after 13 min") {
		t.Fatalf("resolved: %v", got)
	}

	// A restart after the rule was removed resolves the alert at once.
	h.ticks(5, healthyDisk, site("api", 95))
	h.take()
	h.e = h.engine()
	if err := h.e.Load(ctx, h.now); err != nil {
		t.Fatal(err)
	}
	h.cfg.SiteRules = nil
	h.tick(healthyDisk, site("api", 95))
	if got := h.take(); len(got) != 1 || !strings.Contains(got[0], "(rule removed or turned off)") {
		t.Fatalf("rule removed: %v", got)
	}
}

func TestEngineRestoresSilences(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.ticks(5, healthyDisk, site("api", 95))
	h.take()
	if _, err := h.e.Silence(ctx, h.e.List().Firing[0].ID, 120, "alice", "", h.now); err != nil {
		t.Fatal(err)
	}
	h.ticks(5, healthyDisk, site("api", 5)) // resolved (notified: it fired before the silence)
	h.take()
	h.e = h.engine()
	if err := h.e.Load(ctx, h.now); err != nil {
		t.Fatal(err)
	}
	h.ticks(5, healthyDisk, site("api", 95))
	expectNotices(t, h.take())
	if l := h.e.List(); len(l.Firing) != 1 || l.Firing[0].Silence == nil {
		t.Fatalf("silence not restored: %+v", l)
	}
}

func TestEngineEnds(t *testing.T) {
	h := newHarness(t)
	h.ticks(5, healthyDisk, site("a", 95), site("b", 95))
	h.take()
	// A deleted site's alert resolves with a note.
	h.tick(healthyDisk, site("b", 95))
	if got := h.take(); len(got) != 1 || !strings.Contains(got[0], "(site deleted)") {
		t.Fatalf("site deleted: %v", got)
	}
	// Opting the site out ends its alerts.
	b := site("b", 95)
	b.Site.Alerts.Disabled = true
	h.tick(healthyDisk, b)
	if got := h.take(); len(got) != 1 || !strings.Contains(got[0], "(rule removed or turned off)") {
		t.Fatalf("opted out: %v", got)
	}
	// Turning alerts off ends every alert.
	h.ticks(5, healthyDisk, site("c", 95))
	h.take()
	h.cfg.Enabled = false
	h.tick(healthyDisk, site("c", 95))
	if got := h.take(); len(got) != 1 || !strings.Contains(got[0], "(alerts turned off)") {
		t.Fatalf("alerts off: %v", got)
	}
	if l := h.e.List(); len(l.Firing) != 0 {
		t.Fatalf("still firing: %+v", l)
	}
}

func TestEngineSiteOverride(t *testing.T) {
	h := newHarness(t)
	s := site("api", 85)
	// The site raises the server-wide limit: 85% is fine here.
	s.Site.Alerts.Rules = []model.AlertRule{{ID: "cpu", Metric: model.AlertCPU, Threshold: 90, ForMinutes: 1, Severity: model.SeverityCritical}}
	h.ticks(10, healthyDisk, s)
	expectNotices(t, h.take())
	s.Status.Instances[0].CPUPercent = 95
	h.ticks(5, healthyDisk, s)
	got := h.take()
	if len(got) != 1 || !strings.Contains(got[0], "(limit 90%)") {
		t.Fatalf("override: %v", got)
	}
	if a := h.e.List().Firing[0]; a.Severity != model.SeverityCritical || a.Threshold != 90 {
		t.Fatalf("override alert: %+v", a)
	}
}

func TestEngineRuleChanged(t *testing.T) {
	h := newHarness(t)
	h.ticks(5, healthyDisk, site("api", 95))
	h.take()
	// The rule of the same ID now watches memory: the CPU alert ends.
	h.cfg.SiteRules[0] = model.AlertRule{ID: "cpu", Metric: model.AlertMemory, Threshold: 50, ForMinutes: 1, Severity: model.SeverityWarning}
	h.tick(healthyDisk, site("api", 95))
	got := h.take()
	if len(got) != 1 || !strings.Contains(got[0], "(rule changed): CPU was 95% at worst") {
		t.Fatalf("rule changed: %v", got)
	}
	l := h.e.List()
	if len(l.Firing) != 0 || len(l.Pending) != 1 || l.Pending[0].Metric != model.AlertMemory {
		t.Fatalf("after the change: %+v", l)
	}
}

func TestEngineNoticeLimit(t *testing.T) {
	h := newHarness(t)
	var sites []SiteSample
	for i := range 15 {
		sites = append(sites, site(fmt.Sprintf("s%02d", i), 99))
	}
	low := ServerSample{CPUPercent: -1, MemoryPercent: -1, Disks: []Disk{{`C:\`, 1}}}
	h.ticks(4, healthyDisk, sites...)
	h.tick(low, sites...)
	got := h.take()
	if len(got) != maxNotices+1 {
		t.Fatalf("%d notices: %v", len(got), got)
	}
	// The critical server alert goes first; the rest are summarized.
	if !strings.HasPrefix(got[0], "fire::Free disk space") || !strings.HasPrefix(got[maxNotices], "none::6 more alert notifications (6 fired), for ") {
		t.Fatalf("notices: %v", got)
	}
}

// Evaluations, listings and silences at once, for the race detector.
func TestEngineConcurrent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			l := h.e.List()
			for _, a := range append(l.Firing, l.Pending...) {
				h.e.Silence(ctx, a.ID, 5, "x", "", time.Now())
				h.e.Get(a.ID)
				h.e.Unsilence(ctx, a.ID, time.Now())
			}
			h.e.History(ctx, store.AlertFilter{Limit: 5})
		}
	})
	for i := range 200 {
		cpu := 95.0
		if i%40 > 30 {
			cpu = 10
		}
		h.e.Evaluate(ctx, h.now.Add(time.Duration(i)*15*time.Second), h.cfg, []SiteSample{site("a", cpu), site("b", 100-cpu)}, healthyDisk)
	}
	close(stop)
	wg.Wait()
}
