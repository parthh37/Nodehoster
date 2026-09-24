package procmgr

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
)

// App supervises the instances of one Node.js site: the equivalent of an IIS
// application pool with one worker process per instance.
type App struct {
	m    *Manager
	id   string
	logs *LogSink

	mu         sync.Mutex
	site       *model.Site
	slots      []*slot
	running    bool // desired state
	failed     bool
	failMsg    string
	crashTimes []time.Time
	schedFired map[string]string // "HH:MM" -> date it last fired

	// Automatic recovery from rapid-fail protection.
	recoverTimer *time.Timer
	recoveries   int       // automatic restarts since the site last ran stably
	lastRecovery time.Time // when the last automatic restart happened
	watcher      *fileWatcher

	backends atomic.Pointer[[]*Backend]

	// The deployment slot the app is in (nil = production); a swap moves
	// the app to another slot (see slots.go).
	slot atomic.Pointer[string]
}

type exitInfo struct {
	code int
	at   time.Time
}

type slotCmd struct {
	stop  bool // otherwise: replace (recycle)
	reply chan error
}

// slot keeps one instance of an app alive, restarting it according to the
// site's policy and swapping it for a fresh one on recycle.
type slot struct {
	app   *App
	index int
	cmds  chan slotCmd
	done  chan struct{}

	mu       sync.Mutex
	cur      *Instance
	next     *Instance // replacement being brought up during a recycle
	restarts int
	lastExit *exitInfo
	failures int // consecutive failed starts or short-lived runs
	lostPort int // consecutive runs that ended because the port was taken

	recyclePending atomic.Bool
}

// requestRecycle asks for a replacement unless one is already on its way.
func (s *slot) requestRecycle() {
	if !s.recyclePending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.recyclePending.Store(false)
		s.send(slotCmd{})
	}()
}

func (a *App) config() *model.Site {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.site
}

// workDir is where the application runs from.
func (a *App) workDir(site *model.Site) string {
	return site.ResolveRoot(a.m.opts.SitesDir, site.Node.AppRoot)
}

func (a *App) start() error {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return nil
	}
	site := a.site
	dir := a.workDir(site)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		a.mu.Unlock()
		return fmt.Errorf("application folder %q does not exist", dir)
	}
	a.cancelRecoveryLocked()
	a.running, a.failed, a.failMsg, a.crashTimes = true, false, "", nil
	n := site.Node.Instances
	a.slots = nil
	for i := 0; i < n; i++ {
		a.slots = append(a.slots, a.newSlot(i))
	}
	a.mu.Unlock()
	a.logs.System("starting %d instance(s) from %s", n, dir)
	a.startWatcher()
	return nil
}

func (a *App) newSlot(i int) *slot {
	s := &slot{app: a, index: i, cmds: make(chan slotCmd), done: make(chan struct{})}
	go s.run()
	return s
}

// stop stops every instance, draining in-flight requests first.
func (a *App) stop() {
	a.mu.Lock()
	slots := a.slots
	a.slots = nil
	wasRunning := a.running
	a.running = false
	a.cancelRecoveryLocked()
	a.mu.Unlock()
	a.stopWatcher()
	stopSlots(slots)
	a.publish()
	if wasRunning {
		a.logs.System("stopped")
	}
}

func stopSlots(slots []*slot) {
	var wg sync.WaitGroup
	for _, s := range slots {
		wg.Add(1)
		go func(s *slot) {
			defer wg.Done()
			s.send(slotCmd{stop: true})
		}(s)
	}
	wg.Wait()
}

// recycle replaces every instance one at a time so the site keeps serving.
func (a *App) recycle(reason string) error {
	a.mu.Lock()
	slots := append([]*slot(nil), a.slots...)
	running := a.running
	a.mu.Unlock()
	if !running {
		return errors.New("site is not running")
	}
	a.logs.System("recycling (%s)", reason)
	var errs []error
	for _, s := range slots {
		if err := s.send(slotCmd{}); err != nil {
			errs = append(errs, fmt.Errorf("instance %d: %w", s.index, err))
		}
	}
	if len(errs) == 0 {
		a.m.opts.Bus.Info(events.SiteRecycled, a.id, "%s recycled (%s)", a.config().Name+a.slotSuffix(), reason)
	}
	return errors.Join(errs...)
}

// resize adds or removes instances to match the configured count.
func (a *App) resize(n int) {
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return
	}
	var extra []*slot
	for len(a.slots) < n {
		a.slots = append(a.slots, a.newSlot(len(a.slots)))
	}
	if len(a.slots) > n {
		extra = a.slots[n:]
		a.slots = a.slots[:n]
	}
	a.mu.Unlock()
	stopSlots(extra)
	a.publish()
}

// publish rebuilds the list of backends the proxy may route to.
func (a *App) publish() {
	a.mu.Lock()
	slots := a.slots
	worker := a.site.Type == model.SiteWorker
	a.mu.Unlock()
	var list []*Backend
	if worker { // nothing is routed to a worker
		a.backends.Store(&list)
		return
	}
	for _, s := range slots {
		s.mu.Lock()
		for _, inst := range []*Instance{s.cur, s.next} {
			if inst != nil && inst.getState() == "ready" {
				list = append(list, inst.backend)
			}
		}
		s.mu.Unlock()
	}
	a.backends.Store(&list)
}

// recordCrash implements rapid-fail protection: too many crashes within the
// window stops the whole site instead of restarting forever.
func (a *App) recordCrash() (tripped bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := a.site.Node
	now := time.Now()
	window := time.Duration(n.RestartWindowSec) * time.Second
	kept := a.crashTimes[:0]
	for _, t := range a.crashTimes {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	a.crashTimes = append(kept, now)
	if n.MaxRestarts > 0 && len(a.crashTimes) > n.MaxRestarts && !a.failed {
		a.failed = true
		a.failMsg = fmt.Sprintf("rapid-fail protection: %d failures within %s", len(a.crashTimes), window)
		return true
	}
	return false
}

func (a *App) isFailed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.failed
}

func (a *App) status() model.SiteStatus {
	a.mu.Lock()
	slots := a.slots
	running, failed, failMsg := a.running, a.failed, a.failMsg
	a.mu.Unlock()

	st := model.SiteStatus{SiteID: a.id, Instances: []model.InstanceStatus{}}
	ready, healthy := 0, 0
	for _, s := range slots {
		s.mu.Lock()
		inst, restarts, lastExit := s.cur, s.restarts, s.lastExit
		s.mu.Unlock()
		if inst == nil {
			is := model.InstanceStatus{Index: s.index, State: "exited", Restarts: restarts}
			if lastExit != nil {
				code, at := lastExit.code, lastExit.at
				is.LastExitCode, is.LastExitAt = &code, &at
			}
			st.Instances = append(st.Instances, is)
			continue
		}
		is := inst.status(restarts, lastExit)
		if is.State == "ready" {
			ready++
			if is.Healthy {
				healthy++
			}
		}
		st.Instances = append(st.Instances, is)
	}
	switch {
	case failed:
		st.State, st.Message = model.StateFailed, failMsg
	case !running:
		st.State = model.StateStopped
	case len(slots) > 0 && ready == len(slots) && healthy == ready:
		st.State = model.StateRunning
	case ready > 0:
		st.State = model.StateDegraded
	default:
		st.State = model.StateStarting
	}
	return st
}

// ---- slot

func (s *slot) send(c slotCmd) error {
	c.reply = make(chan error, 1)
	select {
	case s.cmds <- c:
		return <-c.reply
	case <-s.done:
		return nil
	}
}

func (s *slot) setCur(inst *Instance) {
	s.mu.Lock()
	s.cur = inst
	s.mu.Unlock()
}

func (s *slot) run() {
	defer close(s.done)
	a := s.app
	var inst *Instance
	for {
		if inst == nil {
			if a.isFailed() {
				if s.idle() {
					return
				}
				continue
			}
			var err error
			inst, err = a.spawn(s.index)
			if err != nil {
				a.logs.System("instance %d failed to start: %v", s.index, err)
				s.mu.Lock()
				s.failures++
				s.lastExit = &exitInfo{code: -1, at: time.Now()}
				s.mu.Unlock()
				if a.recordCrash() {
					a.tripRapidFail()
					if s.idle() {
						return
					}
					continue
				}
				if s.wait(restartDelay(s.failures, 0)) {
					return
				}
				continue
			}
			s.setCur(inst)
			a.publish()
		}

		select {
		case <-inst.exited:
			uptime := time.Since(inst.startedAt)
			a.m.ports.release(inst.port)
			a.m.agent.unregister(inst.token)
			inst.os.release()
			if s.portLost(inst, uptime) {
				a.logs.System("instance %d: port %d was taken by another process before the application could listen on it; retrying on another port", s.index, inst.port)
				s.setCur(nil)
				a.publish()
				inst = nil
				continue
			}
			s.mu.Lock()
			s.cur = nil
			s.lastExit = &exitInfo{code: inst.exitCode, at: time.Now()}
			if uptime > 60*time.Second {
				s.failures = 0
			}
			s.failures++
			s.mu.Unlock()
			a.publish()
			inst0 := inst
			inst = nil

			site := a.config()
			policy, code := site.Node.RestartPolicy, inst0.exitCode
			a.logs.System("instance %d exited with code %d after %s", s.index, code, uptime.Round(time.Second))
			a.m.opts.Bus.Warn(events.SiteCrashed, a.id, "%s instance %d exited unexpectedly (code %d)", site.Name+a.slotSuffix(), s.index, code)
			if policy == "never" || (policy == "on-failure" && code == 0) {
				if s.idle() {
					return
				}
				continue
			}
			if a.recordCrash() {
				a.tripRapidFail()
				if s.idle() {
					return
				}
				continue
			}
			s.mu.Lock()
			s.restarts++
			fails := s.failures
			s.mu.Unlock()
			if s.wait(restartDelay(fails, uptime)) {
				return
			}

		case c := <-s.cmds:
			if c.stop {
				a.retire(inst)
				s.setCur(nil)
				c.reply <- nil
				return
			}
			// replace returns whichever instance is current afterwards: the
			// new one, the old one if the replacement failed, or nil if a
			// fixed-port site lost both (the loop then starts a fresh one).
			var err error
			inst, err = s.replace(inst)
			if inst == nil {
				s.setCur(nil)
				a.publish()
			}
			c.reply <- err
		}
	}
}

// portLost reports whether an instance that just exited only failed because
// another process held its port, in which case the slot starts a new one at
// once without counting a crash. That happens when something listening on
// the port answered the readiness check before the application gave up on
// it. Only the application's own report counts, only for a run no longer
// than the startup timeout, and at most portRetries times in a row.
func (s *slot) portLost(inst *Instance, uptime time.Duration) bool {
	site := s.app.config()
	lost := site.Node.PortMode != "fixed" && inst.addrInUse.Load() &&
		uptime <= time.Duration(site.Node.StartupTimeoutSec)*time.Second
	s.mu.Lock()
	defer s.mu.Unlock()
	if !lost || s.lostPort >= portRetries {
		s.lostPort = 0
		return false
	}
	s.lostPort++
	return true
}

// replace brings up a new instance, moves traffic to it, then retires the
// old one. With a fixed port both cannot run at once, so the old one is
// stopped first and the site is briefly unavailable.
//
// A worker is replaced the same way: the new process runs (past its settle
// period) before the old one is asked to shut down, so a queue consumer
// never stops consuming, and a new build that fails to start leaves the
// old one working. The price is a few seconds with both running, which
// consumers of a job queue (BullMQ, RabbitMQ, SQS) are built for. A
// singleton that cannot tolerate that should be restarted instead, which
// stops before it starts.
func (s *slot) replace(old *Instance) (*Instance, error) {
	a := s.app
	site := a.config()
	if site.Node.PortMode == "fixed" {
		a.retire(old)
		s.setCur(nil)
		a.publish()
		inst, err := a.spawn(s.index)
		if err != nil {
			return nil, err
		}
		s.setCur(inst)
		a.publish()
		return inst, nil
	}
	next, err := a.spawn(s.index)
	if err != nil {
		a.logs.System("instance %d: replacement failed to start, keeping the running instance: %v", s.index, err)
		return old, err
	}
	s.mu.Lock()
	s.next = next
	s.mu.Unlock()
	a.publish() // old + new both receive traffic
	s.mu.Lock()
	s.cur, s.next = next, nil
	s.mu.Unlock()
	a.publish() // only new
	a.retire(old)
	return next, nil
}

// retire drains and stops an instance that is no longer published.
func (a *App) retire(inst *Instance) {
	if inst == nil {
		return
	}
	site := a.config()
	timeout := time.Duration(site.Node.ShutdownTimeoutSec) * time.Second
	inst.setState("stopping")
	a.publish()
	deadline := time.Now().Add(timeout)
	for inst.backend.Active.Load() > 0 && time.Now().Before(deadline) && !inst.isExited() {
		time.Sleep(100 * time.Millisecond)
	}
	remaining := time.Until(deadline)
	if remaining < 2*time.Second {
		remaining = 2 * time.Second
	}
	inst.stop(remaining)
	a.m.ports.release(inst.port)
	a.m.agent.unregister(inst.token)
	inst.os.release()
}

// idle parks a slot that will not restart until it is told to stop or to
// recycle (which is how a failed site is started again). It returns true
// when the slot should exit.
func (s *slot) idle() bool {
	c := <-s.cmds
	c.reply <- nil
	return c.stop
}

// wait sleeps before a restart while still answering commands. It returns
// true when the slot was told to stop.
func (s *slot) wait(d time.Duration) bool {
	if d > 0 {
		s.app.logs.System("instance %d restarting in %s", s.index, d.Round(100*time.Millisecond))
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return false
	case c := <-s.cmds:
		c.reply <- nil
		return c.stop
	}
}

// restartDelay decides how long to wait before restarting an instance that
// exited or failed to start. failures counts consecutive failures of this
// instance (reset once a run lasts over a minute); lastUptime is how long the
// previous run lasted (0 when it never started).
//
// TODO(user): this is the policy knob for crash loops. The default below is
// exponential backoff from 1s, capped at 30s.
func restartDelay(failures int, lastUptime time.Duration) time.Duration {
	if failures <= 1 {
		return time.Second
	}
	d := time.Second << min(failures-1, 5)
	return min(d, 30*time.Second)
}

func (a *App) tripRapidFail() {
	a.mu.Lock()
	name := a.site.Name + a.slotSuffix()
	// Detach the instances now, under the lock, so a Start or Restart issued
	// while they are still stopping gets a fresh set of slots instead of
	// having its new processes wiped from view (and orphaned) afterwards.
	slots := a.slots
	a.slots = nil
	a.running = false
	next := "the site is stopped until it is started again"
	if d, ok := a.scheduleRecoveryLocked(); ok {
		next = fmt.Sprintf("restarting automatically in %s", d)
	}
	a.failMsg += "; " + next
	msg := a.failMsg
	a.mu.Unlock()
	a.logs.System("%s", msg)
	a.m.opts.Bus.Error(events.SiteFailed, a.id, "%s stopped: %s", name, msg)
	a.stopWatcher()
	a.publish()
	// Asynchronous because the caller is one of these slots: it parks in
	// idle() and receives its stop command like the rest.
	go stopSlots(slots)
}

// scheduleRecoveryLocked arms the timer that starts a site again after
// rapid-fail protection stopped it, unless the site is configured to wait for
// an operator. a.mu must be held.
func (a *App) scheduleRecoveryLocked() (time.Duration, bool) {
	a.cancelRecoveryLocked()
	n := a.site.Node
	if n.RapidFailAction == "stop" {
		return 0, false
	}
	// A site that ran for an hour since its last automatic restart was
	// stable; its next failure starts from the shortest pause again.
	if time.Since(a.lastRecovery) > time.Hour {
		a.recoveries = 0
	}
	d := recoveryDelay(time.Duration(n.RecoverAfterSec)*time.Second, a.recoveries)
	a.recoverTimer = time.AfterFunc(d, a.autoRecover)
	return d, true
}

func (a *App) cancelRecoveryLocked() {
	if a.recoverTimer != nil {
		a.recoverTimer.Stop()
		a.recoverTimer = nil
	}
}

// autoRecover starts a failed site again. It does nothing when the site was
// removed, started or stopped by an operator, or the server is shutting down
// since the timer was armed.
func (a *App) autoRecover() {
	select {
	case <-a.m.stop:
		return
	default:
	}
	if a.m.app(a.key()) != a {
		return
	}
	a.mu.Lock()
	if a.recoverTimer == nil || !a.failed || a.running {
		a.mu.Unlock()
		return
	}
	a.recoverTimer = nil
	a.recoveries++
	a.lastRecovery = time.Now()
	attempt, name := a.recoveries, a.site.Name+a.slotSuffix()
	a.mu.Unlock()

	a.logs.System("automatic restart after rapid-fail protection (attempt %d)", attempt)
	if err := a.start(); err != nil {
		a.mu.Lock()
		a.failMsg = fmt.Sprintf("automatic restart failed: %v", err)
		if d, ok := a.scheduleRecoveryLocked(); ok {
			a.failMsg += fmt.Sprintf("; retrying in %s", d)
		}
		msg := a.failMsg
		a.mu.Unlock()
		a.logs.System("%s", msg)
		a.m.opts.Bus.Error(events.SiteFailed, a.id, "%s: %s", name, msg)
		return
	}
	a.m.opts.Bus.Info(events.SiteStarted, a.id, "%s restarted automatically after rapid-fail protection (attempt %d)", name, attempt)
}

// recoveryDelay is how long a site stopped by rapid-fail protection waits
// before it is started again: base for the first trip, doubling for every
// further trip without a stable hour in between, capped at an hour so a
// site whose dependency (database, disk, network) comes back is never down
// for long.
func recoveryDelay(base time.Duration, previous int) time.Duration {
	d := base << min(previous, 10)
	return min(d, time.Hour)
}

// ---- spawning

func newToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// nodeCommand builds the command line and environment that run a script
// (or an npm script) of a site with the site's Node.js version and
// variables, optionally with the agent preloaded. The caller adds its own
// variables to the returned env and sets cmd.Env from it.
func (m *Manager) nodeCommand(site *model.Site, rt NodeRuntime, dir, script, npmScript string, args []string, token string) (*exec.Cmd, *env, error) {
	n := site.Node
	var argv []string
	argv = append(argv, n.NodeArgs...)
	nodeOptions := ""
	if npmScript != "" {
		if rt.NpmCli == "" {
			return nil, nil, fmt.Errorf("npm was not found next to %s", rt.Exe)
		}
		if n.AgentEnabled {
			nodeOptions = fmt.Sprintf(`--require "%s"`, m.agentScript)
		}
		argv = append(argv, rt.NpmCli, "run", npmScript)
		if len(args) > 0 {
			argv = append(append(argv, "--"), args...)
		}
	} else {
		if n.AgentEnabled {
			argv = append(argv, "--require", m.agentScript)
		}
		argv = append(argv, script)
		argv = append(argv, args...)
	}

	cmd := exec.Command(rt.Exe, argv...)
	cmd.Dir = dir
	e := newEnv(os.Environ())
	e.prependPath(filepath.Dir(rt.Exe))
	e.set("NODE_ENV", "production")
	e.set("npm_config_update_notifier", "false")
	e.set("npm_config_cache", filepath.Join(m.opts.SitesDir, site.ID, ".npm-cache"))
	for _, v := range n.Env {
		if v.From != nil {
			continue // set by the caller: see setSecretEnv
		}
		val := v.Value
		if v.Secret {
			val = m.opts.Unseal(val)
		}
		e.set(v.Name, val)
	}
	e.set("NODEHOSTER_SITE", site.Name)
	e.set("NODEHOSTER_SITE_ID", site.ID)
	if n.AgentEnabled {
		e.set("NODEHOSTER_AGENT_PIPE", m.agent.path)
		e.set("NODEHOSTER_AGENT_TOKEN", token)
		if nodeOptions != "" {
			if cur := e.get("NODE_OPTIONS"); cur != "" {
				nodeOptions = cur + " " + nodeOptions
			}
			e.set("NODE_OPTIONS", nodeOptions)
		}
	}
	return cmd, e, nil
}

// resolveNode finds the site's Node.js runtime.
func (m *Manager) resolveNode(site *model.Site) (NodeRuntime, error) {
	version := site.Node.NodeVersion
	if version == "" {
		version = m.opts.Settings().DefaultNodeVersion
	}
	return m.opts.ResolveNode(version)
}

// portRetries is how many times spawn moves an instance to another port when
// its port was taken before the application could listen on it.
const portRetries = 2

// spawn starts one process and waits until it accepts connections (a
// worker: until it has stayed up for the settle period). A start that
// failed only because the port was lost is retried on a fresh port, so the
// race between the allocator's check and the application's listen does not
// reach restart backoff or rapid-fail protection.
func (a *App) spawn(index int) (*Instance, error) {
	for attempt := 0; ; attempt++ {
		inst, err := a.spawnOnce(index)
		var lost *portLostError
		if attempt == portRetries || !errors.As(err, &lost) {
			return inst, err
		}
		a.logs.System("instance %d: port %d was taken by another process before the application could listen on it; retrying on another port", index, lost.port)
	}
}

func (a *App) spawnOnce(index int) (*Instance, error) {
	site := a.config()
	n := site.Node
	worker := site.Type == model.SiteWorker
	dir := a.workDir(site)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("application folder %q does not exist", dir)
	}

	// A worker gets no port: nothing is routed to it, and an app that
	// happens to read PORT should not find one.
	var err error
	port := n.FixedPort
	if worker {
		port = 0
	} else if n.PortMode != "fixed" {
		if port, err = a.m.ports.allocate(); err != nil {
			return nil, err
		}
	}
	releasePort := func() {
		if port != 0 {
			a.m.ports.release(port)
		}
	}

	token := newToken()
	cmd, env, rt, err := a.m.processCommand(site, dir, instanceEntry(n), token, port)
	if err != nil {
		releasePort()
		return nil, err
	}
	if err := a.m.setSecretEnv(env, site, n.Env, a.key(), ""); err != nil {
		releasePort()
		return nil, err
	}
	if rt.note != "" {
		a.logs.System("instance %d: %s", index, rt.note)
	}
	if !worker {
		env.set("PORT", strconv.Itoa(port))
	}
	env.set("NODEHOSTER_INSTANCE", strconv.Itoa(index))
	env.set("NODE_APP_INSTANCE", strconv.Itoa(index)) // pm2 convention
	cmd.Env = env.list()

	// A worker has no port to lose: its output is not watched for
	// "address in use", which would be about some other port of its own.
	portStr, addrInUse := strconv.Itoa(port), new(atomic.Bool)
	watch := addrInUse
	if worker {
		watch = nil
	}
	outW := &lineWriter{sink: a.logs, stream: "stdout", instance: index, port: portStr, addrInUse: watch}
	errW := &lineWriter{sink: a.logs, stream: "stderr", instance: index, port: portStr, addrInUse: watch}
	cmd.Stdout, cmd.Stderr = outW, errW
	cmd.WaitDelay = 5 * time.Second // do not hang on grandchildren holding the pipes

	cleanup, err := prepare(cmd, n.RunAs, a.m.opts.Unseal(n.RunAs.Password), filepath.Join(a.m.opts.SitesDir, a.id))
	defer cleanup()
	if err != nil {
		releasePort()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		releasePort()
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	osp, err := afterStart(cmd.Process.Pid, n.Limits)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		releasePort()
		return nil, err
	}

	inst := &Instance{
		app: a, index: index, port: port, token: token, addrInUse: addrInUse,
		cmd: cmd, os: osp, pid: cmd.Process.Pid, startedAt: time.Now(),
		backend: &Backend{Addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Slot: index},
		exited:  make(chan struct{}),
		state:   "starting", healthy: true,
		runtime: rt.runtime, runtimeVersion: rt.version, console: rt.console,
	}
	if worker {
		inst.backend.Addr = ""
	}
	a.m.agent.register(token, inst)
	go func() {
		err := cmd.Wait()
		outW.flush()
		errW.flush()
		code := 0
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		} else if err != nil {
			code = -1
		}
		inst.mu.Lock()
		inst.exitCode = code
		inst.state = "exited"
		inst.mu.Unlock()
		close(inst.exited)
	}()
	if worker {
		a.logs.System("instance %d started: pid %d, %s", index, inst.pid, rt.label())
	} else {
		a.logs.System("instance %d started: pid %d, port %d, %s", index, inst.pid, port, rt.label())
	}

	if err := a.waitReady(inst, site); err != nil {
		// Decide before retire releases the port: once it is back in the
		// pool another instance may bind it and look like the culprit. A
		// node site without bindings never binds its port, so only its own
		// output counts; a worker site has no port at all.
		lost := !worker && n.PortMode != "fixed" && inst.isExited() &&
			(addrInUse.Load() || (!a.isWorker(site) && !portFree(port)))
		a.retire(inst)
		if lost {
			return nil, &portLostError{port: port, err: err}
		}
		return nil, err
	}
	inst.setState("ready")
	if worker {
		a.logs.System("instance %d running", index)
	} else {
		a.logs.System("instance %d ready on port %d", index, port)
	}
	return inst, nil
}

// settlePeriod is how long a process that does not listen (a worker, or a
// node site without bindings) must stay up to count as started. A script
// that crashes on a bad configuration or a missing module is gone well
// within it, so its start fails and rapid-fail protection counts it; one
// that dies later is an ordinary crash, counted the same way.
const settlePeriod = 2 * time.Second

// waitReady waits until the instance accepts TCP connections on its port.
// A worker site, or a node site without bindings that no other site mounts,
// is ready once it has stayed up for the settle period.
func (a *App) waitReady(inst *Instance, site *model.Site) error {
	timeout := time.Duration(site.Node.StartupTimeoutSec) * time.Second
	worker := a.isWorker(site)
	if worker && timeout < 2*settlePeriod {
		timeout = 2 * settlePeriod
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-inst.exited:
			return fmt.Errorf("process exited during startup with code %d (see the log)", inst.exitCode)
		case <-time.After(150 * time.Millisecond):
		}
		if worker {
			if time.Since(inst.startedAt) > settlePeriod {
				return nil
			}
			continue
		}
		c, err := net.DialTimeout("tcp", inst.backend.Addr, time.Second)
		if err == nil {
			c.Close()
			return nil
		}
	}
	return fmt.Errorf("did not start listening on port %d within %s; %s", inst.port, timeout, listenHint(site.Node))
}

// isWorker reports whether a site runs in the background without serving
// HTTP, so its instances need not listen on their port: a worker site, or a
// node site without bindings that no other site mounts.
func (a *App) isWorker(site *model.Site) bool {
	return site.Type == model.SiteWorker || (len(site.Bindings) == 0 && !a.m.opts.IsLocationTarget(site.ID))
}

// ---- environment

// env is an ordered environment with Windows' case-insensitive names.
type env struct {
	keys []string
	vals map[string]string
	orig map[string]string // folded -> original spelling
}

func fold(k string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(k)
	}
	return k
}

func newEnv(base []string) *env {
	e := &env{vals: map[string]string{}, orig: map[string]string{}}
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 {
			if strings.HasPrefix(strings.ToUpper(kv[:i]), "NODEHOSTER_") {
				continue
			}
			e.set(kv[:i], kv[i+1:])
		}
	}
	return e
}

func (e *env) set(k, v string) {
	f := fold(k)
	if _, ok := e.vals[f]; !ok {
		e.keys = append(e.keys, f)
		e.orig[f] = k
	}
	e.vals[f] = v
}

func (e *env) get(k string) string { return e.vals[fold(k)] }

func (e *env) prependPath(dir string) {
	cur := e.get("PATH")
	if cur == "" {
		e.set("PATH", dir)
		return
	}
	e.set("PATH", dir+string(os.PathListSeparator)+cur)
}

func (e *env) list() []string {
	keys := append([]string(nil), e.keys...)
	sort.Strings(keys) // Windows expects a sorted environment block
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, e.orig[k]+"="+e.vals[k])
	}
	return out
}
