package tasks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
)

// fakeProc is a task process that ends when the test says so.
type fakeProc struct {
	pid  int
	done chan struct{}
	once sync.Once
	code int

	mu      sync.Mutex
	stopped bool
	killed  bool
}

func (p *fakeProc) PID() int              { return p.pid }
func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) ExitCode() int         { <-p.done; return p.code }
func (p *fakeProc) exit(code int) {
	p.once.Do(func() { p.code = code; close(p.done) })
}
func (p *fakeProc) Stop(time.Duration) {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	p.exit(0) // a graceful exit
}
func (p *fakeProc) Kill() {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	p.exit(1)
}
func (p *fakeProc) state() (stopped, killed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped, p.killed
}

type fakeRunner struct {
	mu    sync.Mutex
	procs []*fakeProc
	envs  []string // task names started, in order
	fail  error
}

func (r *fakeRunner) StartTask(site *model.Site, task model.ScheduledTask, runID string, out io.Writer) (Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return nil, r.fail
	}
	fmt.Fprintf(out, "hello from %s\n", task.Name)
	p := &fakeProc{pid: 1000 + len(r.procs), done: make(chan struct{})}
	r.procs = append(r.procs, p)
	r.envs = append(r.envs, task.Name)
	return p, nil
}

func (r *fakeRunner) proc(i int) *fakeProc {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.procs) {
		return nil
	}
	return r.procs[i]
}

func (r *fakeRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.procs)
}

// clock is the injectable time.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

type harness struct {
	t      *testing.T
	s      *Scheduler
	st     *store.Store
	runner *fakeRunner
	clock  *clock
	bus    *events.Bus
	logs   string
}

func at(hhmm string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", "2026-05-04 "+hhmm, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

func newHarness(t *testing.T, keep int) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New(st, log, nil, func(string) string { return "site" })
	h := &harness{t: t, st: st, runner: &fakeRunner{}, clock: &clock{t: at("10:07:00")}, bus: bus, logs: filepath.Join(dir, "logs")}
	h.s = New(Options{Store: st, Bus: bus, Log: log, LogsDir: h.logs, Runner: h.runner, Now: h.clock.now, KeepRuns: keep})
	t.Cleanup(h.s.Shutdown)
	return h
}

func site(tasks ...model.ScheduledTask) *model.Site {
	s := &model.Site{ID: "s1", Name: "app", Type: model.SiteWorker, Node: &model.NodeConfig{AppRoot: "/app", Script: "w.js"}, Tasks: tasks}
	s.ApplyDefaults()
	return s
}

func task(id, schedule, overlap string) model.ScheduledTask {
	return model.ScheduledTask{ID: id, Name: "task-" + id, Schedule: schedule, Script: "job.js", Enabled: true, Overlap: overlap}
}

func (h *harness) runs(taskID string) []*model.TaskRun {
	h.t.Helper()
	list, err := h.st.ListTaskRuns(context.Background(), "s1", taskID, 100)
	if err != nil {
		h.t.Fatal(err)
	}
	return list
}

func (h *harness) statuses(taskID string) string {
	var out []string
	for _, r := range h.runs(taskID) {
		out = append(out, r.Status)
	}
	return strings.Join(out, ",") // newest first
}

func (h *harness) waitStatuses(taskID, want string) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if h.statuses(taskID) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("runs of %s = %q, want %q", taskID, h.statuses(taskID), want)
}

func (h *harness) nextRun(taskID string) time.Time {
	h.t.Helper()
	views, err := h.s.Views(site(task(taskID, "", "")))
	if err != nil {
		h.t.Fatal(err)
	}
	for _, v := range views {
		if v.ID == taskID && v.NextRunAt != nil {
			return *v.NextRunAt
		}
	}
	return time.Time{}
}

func (h *harness) events(typ string) []model.Event {
	list, _ := h.st.ListEvents(context.Background(), "s1", 100)
	var out []model.Event
	for _, e := range list {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestScheduleTiming(t *testing.T) {
	h := newHarness(t, 50)
	off := task("off", "* * * * *", "")
	off.Enabled = false
	h.s.Apply(site(task("q", "*/15 * * * *", ""), task("demand", "", ""), off, task("every", "@every 1h", "")))

	if n := h.nextRun("q"); !n.Equal(at("10:15:00")) {
		t.Fatalf("next run of */15 at 10:07 = %s", n)
	}
	if n := h.nextRun("every"); !n.Equal(at("11:07:00")) {
		t.Fatalf("next run of @every 1h = %s", n)
	}
	if !h.nextRun("demand").IsZero() || !h.nextRun("off").IsZero() {
		t.Fatal("on-demand and disabled tasks must not be scheduled")
	}

	h.s.tick(at("10:14:59"))
	if h.runner.count() != 0 {
		t.Fatal("a task ran early")
	}
	h.s.tick(at("10:15:00"))
	if h.runner.count() != 1 {
		t.Fatalf("%d runs at 10:15", h.runner.count())
	}
	if n := h.nextRun("q"); !n.Equal(at("10:30:00")) {
		t.Fatalf("next after 10:15 = %s", n)
	}
	h.runner.proc(0).exit(0)
	h.waitStatuses("q", "succeeded")

	// A late tick (the machine slept through 10:30 and 10:45) runs once,
	// not once per missed time.
	h.s.tick(at("10:52:10"))
	if h.runner.count() != 2 {
		t.Fatalf("%d runs after the late tick, want 2", h.runner.count())
	}
	if n := h.nextRun("q"); !n.Equal(at("11:00:00")) {
		t.Fatalf("next after a late tick = %s", n)
	}
	h.runner.proc(1).exit(0)

	// @every keeps its rhythm, and restarts from now after a gap.
	h.s.tick(at("11:07:00"))
	if n := h.nextRun("every"); !n.Equal(at("12:07:00")) {
		t.Fatalf("@every next = %s", n)
	}
	h.runner.proc(2).exit(0)
	h.s.tick(at("15:00:30"))
	if n := h.nextRun("every"); !n.Equal(at("16:00:30")) {
		t.Fatalf("@every next after a gap = %s", n)
	}

	// Disabling unschedules; enabling again counts from now.
	h.clock.set(at("16:10:00"))
	q := task("q", "*/15 * * * *", "")
	q.Enabled = false
	h.s.Apply(site(q))
	if !h.nextRun("q").IsZero() {
		t.Fatal("a disabled task is still scheduled")
	}
	q.Enabled = true
	h.s.Apply(site(q))
	if n := h.nextRun("q"); !n.Equal(at("16:15:00")) {
		t.Fatalf("re-enabled next = %s", n)
	}
}

func TestOverlapSkip(t *testing.T) {
	h := newHarness(t, 50)
	h.s.Apply(site(task("a", "* * * * *", model.OverlapSkip)))
	h.s.tick(at("10:08:00"))
	h.s.tick(at("10:09:00")) // still running
	if h.runner.count() != 1 {
		t.Fatalf("%d processes, want 1", h.runner.count())
	}
	if got := h.statuses("a"); got != "skipped,running" {
		t.Fatalf("runs = %s", got)
	}
	if skipped := h.runs("a")[0]; !strings.Contains(skipped.Error, "still running") || skipped.FinishedAt == nil {
		t.Fatalf("skipped run: %+v", skipped)
	}
	if _, _, err := h.s.Run("s1", "a", "alice"); !errors.Is(err, ErrRunning) {
		t.Fatalf("manual run while running: %v", err)
	}
	h.runner.proc(0).exit(0)
	h.waitStatuses("a", "skipped,succeeded")
	rec, queued, err := h.s.Run("s1", "a", "alice")
	if err != nil || queued || rec.Trigger != TriggerManual || rec.User != "alice" || rec.Status != model.RunRunning {
		t.Fatalf("manual run: %+v %v %v", rec, queued, err)
	}
	h.runner.proc(1).exit(0)
	h.waitStatuses("a", "succeeded,skipped,succeeded")
}

func TestOverlapQueue(t *testing.T) {
	h := newHarness(t, 50)
	h.s.Apply(site(task("a", "* * * * *", model.OverlapQueue)))
	h.s.tick(at("10:08:00"))
	h.s.tick(at("10:09:00")) // queued
	h.s.tick(at("10:10:00")) // one is already waiting: skipped
	if h.runner.count() != 1 {
		t.Fatalf("%d processes, want 1", h.runner.count())
	}
	if got := h.statuses("a"); got != "skipped,running" {
		t.Fatalf("runs = %s", got)
	}
	views, _ := h.s.Views(site(task("a", "* * * * *", model.OverlapQueue)))
	if !views[0].Queued || len(views[0].Running) != 1 {
		t.Fatalf("view: %+v", views[0])
	}
	if _, _, err := h.s.Run("s1", "a", "bob"); !errors.Is(err, ErrQueued) {
		t.Fatalf("manual run with one queued: %v", err)
	}

	// The queued run starts as soon as the current one finishes.
	h.runner.proc(0).exit(0)
	deadline := time.Now().Add(5 * time.Second)
	for h.runner.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.runner.count() != 2 {
		t.Fatal("the queued run did not start")
	}
	h.waitStatuses("a", "running,skipped,succeeded")
	h.runner.proc(1).exit(0)
	h.waitStatuses("a", "succeeded,skipped,succeeded")

	// A manual run is queued behind a running one.
	h.s.tick(at("10:11:00"))
	_, queued, err := h.s.Run("s1", "a", "bob")
	if err != nil || !queued {
		t.Fatalf("manual run while running: queued=%v err=%v", queued, err)
	}
	h.runner.proc(2).exit(0)
	h.waitStatuses("a", "running,succeeded,succeeded,skipped,succeeded")
	if r := h.runs("a")[0]; r.Trigger != TriggerManual || r.User != "bob" {
		t.Fatalf("the queued manual run: %+v", r)
	}
	h.runner.proc(3).exit(0)
}

func TestOverlapAllow(t *testing.T) {
	h := newHarness(t, 50)
	h.s.Apply(site(task("a", "* * * * *", model.OverlapAllow)))
	h.s.tick(at("10:08:00"))
	h.s.tick(at("10:09:00"))
	if _, _, err := h.s.Run("s1", "a", "carol"); err != nil {
		t.Fatal(err)
	}
	if h.runner.count() != 3 || h.statuses("a") != "running,running,running" {
		t.Fatalf("%d processes, runs %s", h.runner.count(), h.statuses("a"))
	}
	views, _ := h.s.Views(site(task("a", "* * * * *", model.OverlapAllow)))
	if len(views[0].Running) != 3 {
		t.Fatalf("running = %v", views[0].Running)
	}
	for i := 0; i < 3; i++ {
		h.runner.proc(i).exit(0)
	}
	h.waitStatuses("a", "succeeded,succeeded,succeeded")
}

func TestTimeoutKillsAndWarns(t *testing.T) {
	h := newHarness(t, 50)
	tk := task("a", "", "")
	tk.TimeoutSec = 1
	h.s.Apply(site(tk))
	rec, _, err := h.s.Run("s1", "a", "dave")
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatuses("a", "timeout")
	if _, killed := h.runner.proc(0).state(); !killed {
		t.Fatal("the process was not killed at the timeout")
	}
	if len(h.events(events.TaskTimeout)) != 1 {
		t.Fatalf("task.timeout events: %v", h.events(events.TaskTimeout))
	}
	r, _ := h.st.GetTaskRun(context.Background(), rec.ID)
	if !strings.Contains(r.Error, "timed out after 1s") {
		t.Fatalf("error = %q", r.Error)
	}
	data, _ := os.ReadFile(r.LogPath)
	if !strings.Contains(string(data), "hello from task-a") || !strings.Contains(string(data), "ending the process tree") {
		t.Fatalf("log:\n%s", data)
	}
}

func TestFailuresEmitEvents(t *testing.T) {
	h := newHarness(t, 50)
	h.s.Apply(site(task("a", "", "")))
	h.s.Run("s1", "a", "erin")
	h.runner.proc(0).exit(3)
	h.waitStatuses("a", "failed")
	r := h.runs("a")[0]
	if r.ExitCode == nil || *r.ExitCode != 3 || r.Error != "exited with code 3" {
		t.Fatalf("failed run: %+v", r)
	}

	h.runner.mu.Lock()
	h.runner.fail = errors.New(`application folder "/app" does not exist`)
	h.runner.mu.Unlock()
	rec, _, err := h.s.Run("s1", "a", "erin")
	if err != nil || rec.Status != model.RunFailed || !strings.Contains(rec.Error, "does not exist") {
		t.Fatalf("start failure: %+v %v", rec, err)
	}
	if n := len(h.events(events.TaskFailed)); n != 2 {
		t.Fatalf("%d task.failed events, want 2", n)
	}
}

func TestCancel(t *testing.T) {
	h := newHarness(t, 50)
	h.s.Apply(site(task("a", "", "")))
	rec, _, _ := h.s.Run("s1", "a", "frank")
	if err := h.s.Cancel("other-site", rec.ID, "frank"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("cancel through another site: %v", err)
	}
	if err := h.s.Cancel("s1", rec.ID, "frank"); err != nil {
		t.Fatal(err)
	}
	h.waitStatuses("a", "cancelled")
	if stopped, _ := h.runner.proc(0).state(); !stopped {
		t.Fatal("the process was not asked to stop")
	}
	if r := h.runs("a")[0]; r.Error != "cancelled by frank" {
		t.Fatalf("error = %q", r.Error)
	}
	if err := h.s.Cancel("s1", rec.ID, "frank"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("cancel a finished run: %v", err)
	}
	if len(h.events(events.TaskFailed)) != 0 {
		t.Fatal("a cancelled run is not a failure")
	}
}

func TestHistoryIsPruned(t *testing.T) {
	h := newHarness(t, 3)
	h.s.Apply(site(task("a", "", "")))
	var logs []string
	for i := 0; i < 5; i++ {
		h.clock.set(at("10:07:00").Add(time.Duration(i) * time.Minute))
		rec, _, err := h.s.Run("s1", "a", "gina")
		if err != nil {
			t.Fatal(err)
		}
		logs = append(logs, rec.LogPath)
		h.runner.proc(i).exit(0)
		h.waitStatuses("a", strings.TrimSuffix(strings.Repeat("succeeded,", min(i+1, 3)), ","))
	}
	for i, p := range logs {
		_, err := os.Stat(p)
		if kept := i >= 2; kept != (err == nil) {
			t.Errorf("log of run %d: kept=%v, stat err=%v", i, kept, err)
		}
	}
}

func TestDeletedTaskTakesItsHistory(t *testing.T) {
	h := newHarness(t, 50)
	h.s.Apply(site(task("a", "", ""), task("b", "", "")))
	rec, _, _ := h.s.Run("s1", "a", "hank")
	h.runner.proc(0).exit(0)
	h.waitStatuses("a", "succeeded")
	h.s.Apply(site(task("b", "", "")))
	if n := len(h.runs("a")); n != 0 {
		t.Fatalf("%d runs of a deleted task remain", n)
	}
	if _, err := os.Stat(rec.LogPath); !os.IsNotExist(err) {
		t.Fatalf("log of a deleted task remains: %v", err)
	}
	if _, _, err := h.s.Run("s1", "a", "hank"); !errors.Is(err, ErrUnknownTask) || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("run of a deleted task: %v", err)
	}
}

func TestRemoveSiteCancelsWithoutRecording(t *testing.T) {
	h := newHarness(t, 50)
	h.s.Apply(site(task("a", "", "")))
	h.s.Run("s1", "a", "ida")
	h.s.Remove("s1") // returns once the run has ended
	if stopped, _ := h.runner.proc(0).state(); !stopped {
		t.Fatal("the run of a deleted site was not stopped")
	}
	// The database row is the one written at the start: nothing is
	// written back for a deleted site (DeleteSite removes its history).
	if got := h.statuses("a"); got != "running" {
		t.Fatalf("runs = %s", got)
	}
	if _, _, err := h.s.Run("s1", "a", "ida"); !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("run after removal: %v", err)
	}
}

func TestShutdownStopsRuns(t *testing.T) {
	h := newHarness(t, 50)
	h.s.Apply(site(task("a", "", "")))
	h.s.Start()
	h.s.Run("s1", "a", "jo")
	h.s.Shutdown()
	if stopped, _ := h.runner.proc(0).state(); !stopped {
		t.Fatal("shutdown did not stop the run")
	}
	r := h.runs("a")[0]
	if r.Status != model.RunCancelled || !strings.Contains(r.Error, "stopping") {
		t.Fatalf("run after shutdown: %+v", r)
	}
	if _, _, err := h.s.Run("s1", "a", "jo"); !errors.Is(err, ErrStopping) {
		t.Fatalf("run after shutdown: %v", err)
	}
}

func TestInterruptedRunsAreFailedAtStartup(t *testing.T) {
	h := newHarness(t, 50)
	r := &model.TaskRun{ID: "old", SiteID: "s1", TaskID: "a", TaskName: "a", Trigger: TriggerSchedule, Status: model.RunRunning, StartedAt: at("09:00:00")}
	if err := h.st.PutTaskRun(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	New(Options{Store: h.st, Bus: h.bus, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), LogsDir: h.logs, Runner: h.runner, Now: h.clock.now})
	got, _ := h.st.GetTaskRun(context.Background(), "old")
	if got.Status != model.RunFailed || !strings.Contains(got.Error, "service stopped") || got.FinishedAt == nil {
		t.Fatalf("interrupted run: %+v", got)
	}
}

func TestRunLogIsCapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	l, err := openRunLog(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	chunk := []byte(strings.Repeat("x", 300<<10))
	for i := 0; i < 10; i++ {
		if n, err := l.Write(chunk); n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v (must report success so the child is not blocked)", n, err)
		}
	}
	l.printf("finished")
	l.close()
	data, _ := os.ReadFile(path)
	if len(data) > 1<<20+200 || !strings.Contains(string(data), "output truncated") || !strings.HasSuffix(string(data), "finished\n") {
		t.Fatalf("log is %d bytes; tail %q", len(data), data[max(0, len(data)-120):])
	}
}

// TestRunLogSubscribeSplitsOutput: a log viewer gets what was written
// before it subscribed from the file and the rest live, each line once (a
// line written between subscribing and reading the file used to come
// twice).
func TestRunLogSubscribeSplitsOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	l, err := openRunLog(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	l.printf("started")
	ch, offset, cancel := l.subscribe()
	defer cancel()
	l.Write([]byte("output\n")) // after subscribing, before the file is read
	if backlog := string(readPrefix(path, offset)); !strings.HasSuffix(backlog, "started\n") || strings.Contains(backlog, "output") {
		t.Fatalf("backlog = %q, want the line written before subscribing only", backlog)
	}
	select {
	case live := <-ch:
		if live != "output\n" {
			t.Fatalf("live = %q", live)
		}
	default:
		t.Fatal("the output written after subscribing was not sent live")
	}
	select {
	case extra := <-ch:
		t.Fatalf("unexpected live output %q", extra)
	default:
	}
}
