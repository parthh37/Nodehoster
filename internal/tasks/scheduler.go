// Package tasks runs the scheduled tasks of node and worker sites: cron
// schedules, overlap policies, timeouts, run history and per-run logs. The
// processes themselves are started by the process manager, inside the
// site's sandbox (release folder, Node.js version, environment and
// secrets, run-as identity, Job Object limits).
//
// Tasks run whether or not the site's own processes are running: stopping
// a web site for maintenance should not silently stop its nightly backup
// or clean-up. A task is turned off by disabling it. Runs that fell due
// while the service was down (or the machine asleep) are not caught up,
// as with cron: after a restart each task waits for its next time.
package tasks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/cron"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
)

// Runner starts task processes: the process manager, or a fake in tests.
type Runner interface {
	StartTask(site *model.Site, task model.ScheduledTask, runID string, out io.Writer) (Process, error)
}

// Process is a started task.
type Process interface {
	PID() int
	Done() <-chan struct{}
	ExitCode() int            // valid once Done is closed
	Stop(grace time.Duration) // gracefully, then kill the tree
	Kill()                    // the whole tree, now
}

type Options struct {
	Store   *store.Store
	Bus     *events.Bus
	Log     *slog.Logger
	LogsDir string // per-site logs; runs go to <LogsDir>/<site>/tasks/<run>.log
	Runner  Runner
	// Now is the clock (tests inject one). Schedules are evaluated in the
	// location of the times it returns: local time in production.
	Now         func() time.Time
	KeepRuns    int   // history kept per task; default 50
	MaxLogBytes int64 // per run; default 10 MB
}

const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"

	// maxConcurrent caps runs of one task with overlap "allow": a hung
	// every-minute task must not pile up processes until its timeout.
	maxConcurrent = 10
	// maxWait is the longest the loop sleeps, so a changed wall clock
	// (DST, a time sync) is noticed within half a minute.
	maxWait = 30 * time.Second
)

var (
	ErrUnknownTask = fmt.Errorf("task %w", store.ErrNotFound)
	ErrRunning     = errors.New("the task is already running (its overlap policy is skip)")
	ErrQueued      = errors.New("a run of this task is already waiting for the current one")
	ErrTooMany     = fmt.Errorf("the task already has %d runs in progress", maxConcurrent)
	ErrNotRunning  = errors.New("the run is not in progress")
	ErrStopping    = errors.New("the service is stopping")
)

type Scheduler struct {
	opts Options

	mu      sync.Mutex
	sites   map[string]*siteState
	runs    map[string]*run // in progress, by id
	closed  bool
	started bool

	wake     chan struct{}
	stop     chan struct{}
	loopDone chan struct{}
	runsWG   sync.WaitGroup
}

type siteState struct {
	site  *model.Site
	tasks map[string]*taskState
}

type taskState struct {
	task   model.ScheduledTask
	sched  *cron.Schedule // nil: on demand only
	next   time.Time      // zero: not scheduled (disabled, on demand, never)
	active map[string]*run
	queued *pending // overlap "queue": one run waiting for the current one
}

type pending struct{ trigger, user string }

type run struct {
	rec  *model.TaskRun // guarded by Scheduler.mu
	site *model.Site
	task model.ScheduledTask
	log  *runLog

	proc       Process
	cancel     chan struct{}
	cancelOnce sync.Once
	cancelMsg  string
	discard    bool // the site was deleted: record nothing
	done       chan struct{}
}

func (r *run) requestCancel(msg string) {
	r.cancelOnce.Do(func() {
		r.cancelMsg = msg
		close(r.cancel)
	})
}

// New creates the scheduler. Runs the database still shows as running are
// from before a restart of the service (whose Job Objects ended them), so
// they are marked failed.
func New(opts Options) *Scheduler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.KeepRuns <= 0 {
		opts.KeepRuns = 50
	}
	if opts.MaxLogBytes <= 0 {
		opts.MaxLogBytes = 10 << 20
	}
	s := &Scheduler{
		opts: opts, sites: map[string]*siteState{}, runs: map[string]*run{},
		wake: make(chan struct{}, 1), stop: make(chan struct{}), loopDone: make(chan struct{}),
	}
	if n, err := opts.Store.FailInterruptedTaskRuns(context.Background(), opts.Now(), "the NodeHoster service stopped while the task was running"); err != nil {
		opts.Log.Warn("scheduled tasks: mark interrupted runs", "err", err)
	} else if n > 0 {
		opts.Log.Info("scheduled tasks: runs interrupted by a restart marked failed", "count", n)
	}
	return s
}

// Start runs the schedule loop until Shutdown.
func (s *Scheduler) Start() {
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	go s.loop()
}

func (s *Scheduler) loop() {
	defer close(s.loopDone)
	for {
		now := s.opts.Now()
		s.tick(now)
		t := time.NewTimer(s.untilNext(s.opts.Now()))
		select {
		case <-s.stop:
			t.Stop()
			return
		case <-s.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

func (s *Scheduler) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// untilNext is how long the loop may sleep.
func (s *Scheduler) untilNext(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := maxWait
	for _, ss := range s.sites {
		for _, st := range ss.tasks {
			if !st.next.IsZero() {
				d = min(d, max(st.next.Sub(now), 0))
			}
		}
	}
	return d
}

// Apply registers a site's task definitions (after a create or an
// update). A new or changed schedule counts from now; runs in progress
// continue with the definition they started with.
func (s *Scheduler) Apply(site *model.Site) {
	now := s.opts.Now()
	s.mu.Lock()
	ss := s.sites[site.ID]
	old := map[string]*taskState{}
	if ss != nil {
		old = ss.tasks
	}
	tasks := map[string]*taskState{}
	if site.RunsNode() {
		for _, t := range site.Tasks {
			st := old[t.ID]
			if st == nil {
				st = &taskState{active: map[string]*run{}}
			}
			if st.sched == nil || st.task.Schedule != t.Schedule || st.task.Enabled != t.Enabled || st.next.IsZero() {
				st.sched, st.next = nil, time.Time{}
				if sched, err := cron.Parse(t.Schedule); err == nil {
					st.sched = sched
					if t.Enabled {
						st.next = sched.Next(now)
					}
				}
			}
			if !t.Enabled {
				st.next = time.Time{}
			}
			st.task = t
			tasks[t.ID] = st
		}
	}
	var removed []string
	for id, st := range old {
		if _, ok := tasks[id]; !ok {
			removed = append(removed, id)
			st.queued = nil
		}
	}
	if len(tasks) == 0 {
		delete(s.sites, site.ID)
	} else {
		s.sites[site.ID] = &siteState{site: site, tasks: tasks}
	}
	s.mu.Unlock()
	// A deleted task takes its history with it; runs still in progress
	// clean up after themselves when they finish.
	for _, id := range removed {
		s.deleteHistory(site.ID, id)
	}
	s.poke()
}

// Remove forgets a deleted site: its runs in progress are stopped (and
// waited for, so the site's files can be deleted next) and not recorded.
func (s *Scheduler) Remove(siteID string) {
	s.mu.Lock()
	delete(s.sites, siteID)
	var runs []*run
	for _, r := range s.runs {
		if r.site.ID == siteID {
			r.discard = true
			runs = append(runs, r)
		}
	}
	s.mu.Unlock()
	for _, r := range runs {
		r.requestCancel("the site was deleted")
	}
	for _, r := range runs {
		<-r.done
	}
}

// Releases lists the releases (deployment ids) that runs in progress of a
// site use: a run keeps the release it started in while later deployments
// switch the site to others, so pruning must not delete it.
func (s *Scheduler) Releases(siteID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.runs {
		if r.site.ID == siteID && r.site.ActiveRelease != "" && !slices.Contains(out, r.site.ActiveRelease) {
			out = append(out, r.site.ActiveRelease)
		}
	}
	return out
}

// Shutdown stops the loop and every run in progress, gracefully (the
// agent's shutdown, or SIGTERM) within each site's shutdown timeout.
func (s *Scheduler) Shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	started := s.started
	runs := make([]*run, 0, len(s.runs))
	for _, r := range s.runs {
		runs = append(runs, r)
	}
	s.mu.Unlock()
	close(s.stop)
	if started {
		<-s.loopDone
	}
	for _, r := range runs {
		r.requestCancel("the NodeHoster service is stopping")
	}
	s.runsWG.Wait()
}

// tick starts every task that is due at now and schedules its next run.
func (s *Scheduler) tick(now time.Time) {
	type due struct{ site, task string }
	var list []due
	s.mu.Lock()
	for siteID, ss := range s.sites {
		for taskID, st := range ss.tasks {
			if st.next.IsZero() || now.Before(st.next) {
				continue
			}
			list = append(list, due{siteID, taskID})
			if every := st.sched.Every(); every > 0 {
				// Keep the rhythm of an interval, unless the loop fell
				// behind (a sleeping machine): then count from now.
				n := st.next.Add(every)
				if !n.After(now) {
					n = now.Add(every)
				}
				st.next = n
			} else {
				st.next = st.sched.Next(now)
			}
		}
	}
	s.mu.Unlock()
	for _, d := range list {
		if _, _, err := s.trigger(d.site, d.task, TriggerSchedule, ""); err != nil && !errors.Is(err, ErrStopping) {
			s.opts.Log.Warn("scheduled task", "site", d.site, "task", d.task, "err", err)
		}
	}
}

// Run starts a task now on an operator's request. queued is true when the
// run waits for the current one (overlap "queue").
func (s *Scheduler) Run(siteID, taskID, user string) (rec *model.TaskRun, queued bool, err error) {
	return s.trigger(siteID, taskID, TriggerManual, user)
}

func (s *Scheduler) trigger(siteID, taskID, trigger, user string) (*model.TaskRun, bool, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, false, ErrStopping
	}
	ss := s.sites[siteID]
	var st *taskState
	if ss != nil {
		st = ss.tasks[taskID]
	}
	if st == nil {
		s.mu.Unlock()
		return nil, false, ErrUnknownTask
	}
	manual := trigger == TriggerManual
	if n := len(st.active); n > 0 {
		skip := ""
		switch st.task.Overlap {
		case model.OverlapQueue:
			if st.queued == nil {
				st.queued = &pending{trigger: trigger, user: user}
				s.mu.Unlock()
				return nil, true, nil
			}
			if manual {
				s.mu.Unlock()
				return nil, false, ErrQueued
			}
			skip = "the previous run was still running and another was already waiting"
		case model.OverlapAllow:
			if n >= maxConcurrent {
				if manual {
					s.mu.Unlock()
					return nil, false, ErrTooMany
				}
				skip = fmt.Sprintf("%d runs were still in progress", n)
			}
		default:
			if manual {
				s.mu.Unlock()
				return nil, false, ErrRunning
			}
			skip = "the previous run was still running"
		}
		if skip != "" {
			now := s.opts.Now()
			rec := &model.TaskRun{
				ID: uuid.NewString(), SiteID: siteID, TaskID: taskID, TaskName: st.task.Name, Trigger: trigger, User: user,
				Status: model.RunSkipped, StartedAt: now, FinishedAt: &now, Error: skip,
			}
			s.mu.Unlock()
			s.record(rec)
			s.prune(siteID, taskID)
			return rec, false, nil
		}
	}
	id := uuid.NewString()
	r := &run{
		rec: &model.TaskRun{
			ID: id, SiteID: siteID, TaskID: taskID, TaskName: st.task.Name, Trigger: trigger, User: user,
			Status: model.RunRunning, StartedAt: s.opts.Now(),
			LogPath: filepath.Join(s.opts.LogsDir, siteID, "tasks", id+".log"),
		},
		site: ss.site, task: st.task, cancel: make(chan struct{}), done: make(chan struct{}),
	}
	st.active[id] = r
	s.runs[id] = r
	s.runsWG.Add(1)
	s.mu.Unlock()

	s.start(r)
	s.mu.Lock()
	snap := *r.rec
	s.mu.Unlock()
	return &snap, false, nil
}

// start opens the run's log and starts its process; failures to start
// finish the run as failed.
func (s *Scheduler) start(r *run) {
	s.mu.Lock()
	rec := *r.rec
	s.mu.Unlock()
	s.record(&rec)
	l, err := openRunLog(rec.LogPath, s.opts.MaxLogBytes)
	if err != nil {
		s.finish(r, model.RunFailed, nil, fmt.Sprintf("cannot write the run's log: %v", err))
		return
	}
	s.mu.Lock()
	r.log = l
	s.mu.Unlock()
	how := "node " + r.task.Script
	if r.task.NpmScript != "" {
		how = "npm run " + r.task.NpmScript
	}
	who := "on schedule " + r.task.Schedule
	if rec.Trigger == TriggerManual {
		who = "started by " + rec.User
	}
	l.printf("task %q (%s), %s", r.task.Name, how, who)
	proc, err := s.opts.Runner.StartTask(r.site, r.task, rec.ID, l)
	if err != nil {
		l.printf("could not start: %v", err)
		s.finish(r, model.RunFailed, nil, err.Error())
		return
	}
	l.printf("started: pid %d", proc.PID())
	r.proc = proc
	go s.watch(r)
}

// watch waits for the run to end: on its own, at the timeout (the tree is
// killed) or when cancelled (asked to stop gracefully first).
func (s *Scheduler) watch(r *run) {
	timeout := time.Duration(r.task.TimeoutSec) * time.Second
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	status, msg := "", ""
	select {
	case <-r.proc.Done():
	case <-timer.C:
		status, msg = model.RunTimeout, fmt.Sprintf("timed out after %s", timeout)
		r.log.printf("%s; ending the process tree", msg)
		r.proc.Kill()
	case <-r.cancel:
		status, msg = model.RunCancelled, r.cancelMsg
		grace := time.Duration(r.site.Node.ShutdownTimeoutSec) * time.Second
		r.log.printf("%s; stopping the task (up to %s)", msg, grace)
		r.proc.Stop(grace)
	}
	var code *int
	select {
	case <-r.proc.Done():
		c := r.proc.ExitCode()
		code = &c
	case <-time.After(30 * time.Second):
		r.log.printf("the process did not exit")
	}
	if status == "" {
		if code != nil && *code == 0 {
			status = model.RunSucceeded
		} else {
			status, msg = model.RunFailed, "exited with code "+exitText(code)
		}
	}
	s.finish(r, status, code, msg)
}

func exitText(code *int) string {
	if code == nil {
		return "unknown"
	}
	return fmt.Sprint(*code)
}

func (s *Scheduler) finish(r *run, status string, code *int, msg string) {
	now := s.opts.Now()
	s.mu.Lock()
	r.rec.Status, r.rec.FinishedAt, r.rec.ExitCode, r.rec.Error = status, &now, code, msg
	rec := *r.rec
	discard := r.discard
	var next *pending
	taskGone := true
	if ss := s.sites[rec.SiteID]; ss != nil {
		if st := ss.tasks[rec.TaskID]; st != nil {
			taskGone = false
			delete(st.active, rec.ID)
			if st.queued != nil && !s.closed && len(st.active) == 0 {
				next, st.queued = st.queued, nil
			}
		}
	}
	s.mu.Unlock()

	if r.log != nil {
		took := now.Sub(rec.StartedAt).Round(time.Millisecond)
		switch {
		case code != nil:
			r.log.printf("%s: exit code %d after %s", status, *code, took)
		default:
			r.log.printf("%s after %s", status, took)
		}
	}
	if !discard {
		s.record(&rec)
	}
	if r.log != nil {
		r.log.close() // after the record: a log viewer reads it when the log ends
	}
	// Only now is the run no longer in progress for Subscribe: a viewer
	// told so reads the complete log and the final record.
	s.mu.Lock()
	delete(s.runs, rec.ID)
	s.mu.Unlock()
	if !discard {
		name := r.site.Name
		switch status {
		case model.RunFailed:
			s.opts.Bus.Error(events.TaskFailed, rec.SiteID, "%s: task %q failed: %s", name, rec.TaskName, msg)
		case model.RunTimeout:
			s.opts.Bus.Warn(events.TaskTimeout, rec.SiteID, "%s: task %q %s and was stopped", name, rec.TaskName, msg)
		}
		if taskGone {
			s.deleteHistory(rec.SiteID, rec.TaskID)
		} else {
			s.prune(rec.SiteID, rec.TaskID)
		}
	}
	close(r.done)
	if next != nil {
		if _, _, err := s.trigger(rec.SiteID, rec.TaskID, next.trigger, next.user); err != nil && !errors.Is(err, ErrStopping) {
			s.opts.Log.Warn("queued task run", "site", rec.SiteID, "task", rec.TaskID, "err", err)
		}
	}
	s.runsWG.Done()
}

func (s *Scheduler) record(rec *model.TaskRun) {
	if err := s.opts.Store.PutTaskRun(context.Background(), rec); err != nil {
		s.opts.Log.Warn("record task run", "err", err)
	}
}

func (s *Scheduler) prune(siteID, taskID string) {
	logs, err := s.opts.Store.PruneTaskRuns(context.Background(), siteID, taskID, s.opts.KeepRuns)
	s.removeLogs(logs, err)
}

func (s *Scheduler) deleteHistory(siteID, taskID string) {
	logs, err := s.opts.Store.DeleteTaskRuns(context.Background(), siteID, taskID)
	s.removeLogs(logs, err)
}

func (s *Scheduler) removeLogs(logs []string, err error) {
	if err != nil {
		s.opts.Log.Warn("prune task runs", "err", err)
		return
	}
	for _, p := range logs {
		os.Remove(p)
	}
}

// Cancel stops a run in progress of the given site.
func (s *Scheduler) Cancel(siteID, runID, by string) error {
	s.mu.Lock()
	r := s.runs[runID]
	s.mu.Unlock()
	if r == nil || r.site.ID != siteID {
		return ErrNotRunning
	}
	r.requestCancel("cancelled by " + by)
	return nil
}

// Views returns a site's task definitions (pass a masked site) with their
// next run, runs in progress and last result.
func (s *Scheduler) Views(site *model.Site) ([]model.TaskView, error) {
	last, err := s.opts.Store.LastTaskRuns(context.Background(), site.ID)
	if err != nil {
		return nil, err
	}
	out := make([]model.TaskView, 0, len(site.Tasks))
	s.mu.Lock()
	defer s.mu.Unlock()
	var ss *siteState
	if site.RunsNode() {
		ss = s.sites[site.ID]
	}
	for _, t := range site.Tasks {
		v := model.TaskView{ScheduledTask: t, Running: []string{}, LastRun: last[t.ID]}
		if ss != nil {
			if st := ss.tasks[t.ID]; st != nil {
				if !st.next.IsZero() {
					n := st.next
					v.NextRunAt = &n
				}
				runs := make([]*run, 0, len(st.active))
				for _, r := range st.active {
					runs = append(runs, r)
				}
				slices.SortFunc(runs, func(a, b *run) int { return a.rec.StartedAt.Compare(b.rec.StartedAt) })
				for _, r := range runs {
					v.Running = append(v.Running, r.rec.ID)
				}
				v.Queued = st.queued != nil
				if len(runs) > 0 {
					// The run in progress is more current than the last
					// finished one.
					cur := *runs[len(runs)-1].rec
					v.LastRun = &cur
				}
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// Subscribe follows the output of a run in progress: backlog is what it
// wrote so far and lines what it writes next, each chunk in exactly one of
// them (the backlog is read up to where the subscription starts, so a line
// written in between is neither sent twice nor lost). ok is false when it
// is not in progress: its log file is then complete and its record final.
func (s *Scheduler) Subscribe(runID string) (backlog []byte, lines <-chan string, done <-chan struct{}, cancel func(), ok bool) {
	s.mu.Lock()
	var l *runLog
	if r := s.runs[runID]; r != nil {
		l = r.log
	}
	s.mu.Unlock()
	if l == nil {
		return nil, nil, nil, func() {}, false
	}
	ch, offset, unsub := l.subscribe()
	return readPrefix(l.path, offset), ch, l.done, unsub, true
}
