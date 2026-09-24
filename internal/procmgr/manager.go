// Package procmgr runs and supervises the Node.js processes behind node
// sites: port assignment, restart policy, rapid-fail protection, health
// checks, recycling, zero-downtime restarts and log capture.
package procmgr

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
)

//go:embed agent/nodehoster-agent.js
var agentJS []byte

// NodeRuntime is a resolved Node.js installation.
type NodeRuntime struct {
	Version string
	Exe     string // node.exe
	NpmCli  string // .../node_modules/npm/bin/npm-cli.js, "" if absent
}

type Options struct {
	Log      *slog.Logger
	Bus      *events.Bus
	SitesDir string // per-site data (releases, npm cache)
	LogsDir  string // per-site logs
	RunDir   string // agent script and socket
	Settings func() model.Settings
	// ResolveNode maps a version ("" = system) to an installed runtime.
	ResolveNode func(version string) (NodeRuntime, error)
	Unseal      func(string) string
	// ResolveRuntime maps a runtime other than Node.js (bun, deno, python,
	// dotnet) and a site's version ("" = the server default) to the
	// executable that runs it. Optional: without it those sites cannot
	// start (custom commands need none).
	ResolveRuntime func(runtime, version string) (RuntimeExe, error)
	// IsLocationTarget reports whether another site mounts this one as a
	// location, which means it serves HTTP even without bindings.
	IsLocationTarget func(siteID string) bool
	// OnLog, optional, sees every line a site's log sink writes (log
	// shipping). It must not block.
	OnLog func(siteID string, l model.LogLine)
}

type Manager struct {
	opts        Options
	ports       *portAllocator
	agent       *agentServer
	agentScript string
	health      *http.Client

	mu   sync.Mutex
	apps map[string]*App
	logs map[string]*LogSink

	stop chan struct{}
	wg   sync.WaitGroup
}

func New(opts Options) (*Manager, error) {
	if err := os.MkdirAll(opts.RunDir, 0o755); err != nil {
		return nil, err
	}
	script := filepath.Join(opts.RunDir, "nodehoster-agent.js")
	if err := os.WriteFile(script, agentJS, 0o644); err != nil {
		return nil, fmt.Errorf("write agent: %w", err)
	}
	agent, err := newAgentServer(opts.RunDir, opts.Log)
	if err != nil {
		return nil, fmt.Errorf("agent listener: %w", err)
	}
	s := opts.Settings()
	m := &Manager{
		opts:        opts,
		ports:       newPortAllocator(s.PortRangeStart, s.PortRangeEnd),
		agent:       agent,
		agentScript: script,
		health: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		apps: map[string]*App{},
		logs: map[string]*LogSink{},
		stop: make(chan struct{}),
	}
	m.warnEphemeralOverlap(s.PortRangeStart, s.PortRangeEnd)
	m.wg.Add(1)
	go m.monitor()
	return m, nil
}

// Logs returns the log sink for a site, creating it on first use. Sinks
// exist for every site type so deployments can log there too.
func (m *Manager) Logs(siteID string) *LogSink {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.logs[siteID]; ok {
		return l
	}
	s := m.opts.Settings()
	l := NewLogSink(filepath.Join(m.opts.LogsDir, siteID, "app.log"), s.LogMaxSizeMB, s.LogMaxFiles, s.LogRetentionDays)
	if on := m.opts.OnLog; on != nil {
		l.onWrite = func(line model.LogLine) { on(siteID, line) }
	}
	m.logs[siteID] = l
	return l
}

func (m *Manager) SetPortRange(start, end int) {
	m.ports.setRange(start, end)
	m.warnEphemeralOverlap(start, end)
}

// warnEphemeralOverlap logs when instance ports can collide with the local
// ports of outgoing connections. Instances still start (spawn moves one to
// another port when its port is taken), but a range outside the OS's
// ephemeral range avoids the collisions altogether.
func (m *Manager) warnEphemeralOverlap(start, end int) {
	if msg := ephemeralOverlap(start, end); msg != "" {
		m.opts.Log.Warn(msg + "; outgoing connections can take ports meant for instances. Move the port range in Settings below the ephemeral range, or narrow the ephemeral range.")
	}
}

func (m *Manager) app(id string) *App {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.apps[id]
}

// Apply registers or updates a node site's configuration. A running site
// picks the change up: instance count changes are applied directly, and any
// other change to how the process runs triggers a rolling recycle.
func (m *Manager) Apply(site *model.Site) {
	if !site.RunsNode() {
		m.Remove(site.ID)
		return
	}
	m.mu.Lock()
	a, ok := m.apps[site.ID]
	if !ok {
		a = &App{m: m, id: site.ID, site: site, schedFired: map[string]string{}}
		m.apps[site.ID] = a
		m.mu.Unlock()
		a.logs = m.Logs(site.ID)
		return
	}
	m.mu.Unlock()

	a.mu.Lock()
	old := a.site
	a.site = site
	running := a.running
	a.mu.Unlock()
	if !running {
		return
	}
	oldCount, newCount := old.Node.Instances, site.Node.Instances
	if processChanged(old, site) {
		a.stopWatcher()
		a.startWatcher()
		if newCount < oldCount {
			a.resize(newCount)
		}
		go func() {
			a.recycle("configuration changed")
			if newCount > oldCount {
				a.resize(newCount)
			}
		}()
	} else if newCount != oldCount {
		a.resize(newCount)
	}
}

// processChanged reports whether anything that affects the running process
// differs, ignoring the instance count and pure monitoring settings.
func processChanged(a, b *model.Site) bool {
	if a.ActiveRelease != b.ActiveRelease {
		return true
	}
	x, y := *a.Node, *b.Node
	x.Instances, y.Instances = 0, 0
	x.HealthCheck, y.HealthCheck = model.HealthCheck{}, model.HealthCheck{}
	x.Recycle, y.Recycle = model.RecycleConfig{}, model.RecycleConfig{}
	x.RestartPolicy, y.RestartPolicy = "", ""
	x.MaxRestarts, y.MaxRestarts = 0, 0
	x.RestartWindowSec, y.RestartWindowSec = 0, 0
	x.RapidFailAction, y.RapidFailAction = "", ""
	x.RecoverAfterSec, y.RecoverAfterSec = 0, 0
	j1, _ := json.Marshal(x)
	j2, _ := json.Marshal(y)
	return string(j1) != string(j2)
}

// Remove stops a site and forgets it.
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	a := m.apps[id]
	delete(m.apps, id)
	m.mu.Unlock()
	if a != nil {
		a.stop()
	}
}

// ForgetLogs closes a deleted site's log sink.
func (m *Manager) ForgetLogs(id string) {
	m.mu.Lock()
	l := m.logs[id]
	delete(m.logs, id)
	m.mu.Unlock()
	if l != nil {
		l.Close()
	}
}

var ErrNotNode = errors.New("not a Node.js site")

func (m *Manager) Start(id string) error {
	a := m.app(id)
	if a == nil {
		return ErrNotNode
	}
	if err := a.start(); err != nil {
		return err
	}
	m.opts.Bus.Info(events.SiteStarted, id, "%s started", a.config().Name)
	return nil
}

func (m *Manager) Stop(id string) error {
	a := m.app(id)
	if a == nil {
		return ErrNotNode
	}
	a.stop()
	m.opts.Bus.Info(events.SiteStopped, id, "%s stopped", a.config().Name)
	return nil
}

// Restart is a hard restart: every instance stops, then the site starts.
func (m *Manager) Restart(id string) error {
	a := m.app(id)
	if a == nil {
		return ErrNotNode
	}
	a.stop()
	return a.start()
}

// Recycle is a zero-downtime rolling restart. A stopped or failed site is
// started instead.
func (m *Manager) Recycle(id, reason string) error {
	a := m.app(id)
	if a == nil {
		return ErrNotNode
	}
	a.mu.Lock()
	running := a.running
	a.mu.Unlock()
	if !running {
		return a.start()
	}
	return a.recycle(reason)
}

func (m *Manager) Running(id string) bool {
	a := m.app(id)
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

func (m *Manager) Status(id string) (model.SiteStatus, bool) {
	a := m.app(id)
	if a == nil {
		return model.SiteStatus{}, false
	}
	return a.status(), true
}

// Backends returns the ready instances of a site for the proxy. It is called
// on every request, so it only takes the registry lock briefly.
func (m *Manager) Backends(id string) []*Backend {
	a := m.app(id)
	if a == nil {
		return nil
	}
	if p := a.backends.Load(); p != nil {
		return *p
	}
	return nil
}

// Usage sums CPU and memory over all instances of a site.
func (m *Manager) Usage(id string) (cpu float64, mem uint64) {
	st, ok := m.Status(id)
	if !ok {
		return 0, 0
	}
	for _, i := range st.Instances {
		cpu += i.CPUPercent
		mem += i.MemoryBytes
	}
	return
}

// Shutdown stops every site in parallel; used when the service stops.
func (m *Manager) Shutdown() {
	close(m.stop)
	m.wg.Wait()
	m.mu.Lock()
	apps := make([]*App, 0, len(m.apps))
	for _, a := range m.apps {
		apps = append(apps, a)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, a := range apps {
		wg.Add(1)
		go func(a *App) {
			defer wg.Done()
			a.stop()
		}(a)
	}
	wg.Wait()
	m.agent.close()
	m.mu.Lock()
	for _, l := range m.logs {
		l.Close()
	}
	m.mu.Unlock()
}

// ---- monitoring

func (m *Manager) monitor() {
	defer m.wg.Done()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-t.C:
			m.mu.Lock()
			apps := make([]*App, 0, len(m.apps))
			for _, a := range m.apps {
				apps = append(apps, a)
			}
			m.mu.Unlock()
			for _, a := range apps {
				m.check(a, now)
			}
		}
	}
}

// check samples resource usage and applies health checks and recycling
// rules for one app.
func (m *Manager) check(a *App, now time.Time) {
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return
	}
	site := a.site
	slots := append([]*slot(nil), a.slots...)
	a.mu.Unlock()
	n := site.Node

	// Scheduled recycling at fixed times of day.
	hhmm, today := now.Format("15:04"), now.Format("2006-01-02")
	for _, t := range n.Recycle.ScheduleTimes {
		a.mu.Lock()
		due := t == hhmm && a.schedFired[t] != today
		if due {
			a.schedFired[t] = today
		}
		a.mu.Unlock()
		if due {
			go a.recycle("scheduled at " + t)
			return
		}
	}

	for _, s := range slots {
		s.mu.Lock()
		inst := s.cur
		s.mu.Unlock()
		if inst == nil || inst.getState() != "ready" {
			continue
		}
		inst.sample()

		reason := ""
		inst.mu.Lock()
		mem := inst.mem
		inst.mu.Unlock()
		switch {
		case n.Recycle.MemoryLimitMB > 0 && mem > uint64(n.Recycle.MemoryLimitMB)<<20:
			reason = fmt.Sprintf("memory %d MB over limit %d MB", mem>>20, n.Recycle.MemoryLimitMB)
		case n.Recycle.PeriodicMinutes > 0 && now.Sub(inst.startedAt) > time.Duration(n.Recycle.PeriodicMinutes)*time.Minute:
			reason = fmt.Sprintf("periodic recycle after %d minutes", n.Recycle.PeriodicMinutes)
		case n.Recycle.MaxRequests > 0 && inst.backend.Requests.Load() >= n.Recycle.MaxRequests:
			reason = fmt.Sprintf("served %d requests", inst.backend.Requests.Load())
		}
		if reason == "" && n.HealthCheck.Enabled && len(site.Bindings) > 0 {
			reason = m.healthCheck(a, inst, n.HealthCheck, now)
		}
		if reason != "" {
			a.logs.System("instance %d: %s; recycling it", inst.index, reason)
			s.requestRecycle()
		}
	}
}

// healthCheck pings an instance; after enough consecutive failures it asks
// for a replacement, like IIS worker process pinging.
func (m *Manager) healthCheck(a *App, inst *Instance, hc model.HealthCheck, now time.Time) string {
	inst.mu.Lock()
	due := now.Sub(inst.lastHealth) >= time.Duration(hc.IntervalSec)*time.Second
	if due {
		inst.lastHealth = now
	}
	inst.mu.Unlock()
	if !due {
		return ""
	}
	m.health.Timeout = time.Duration(hc.TimeoutSec) * time.Second
	resp, err := m.health.Get("http://" + inst.backend.Addr + hc.Path)
	ok := err == nil && resp.StatusCode < 500
	if resp != nil {
		resp.Body.Close()
	}
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if ok {
		if !inst.healthy {
			a.logs.System("instance %d is healthy again", inst.index)
		}
		inst.healthy, inst.healthFails = true, 0
		return ""
	}
	inst.healthFails++
	detail := "server error"
	if err != nil {
		detail = err.Error()
	} else {
		detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	a.logs.System("instance %d health check failed (%d/%d): %s", inst.index, inst.healthFails, hc.UnhealthyThreshold, detail)
	if inst.healthFails >= hc.UnhealthyThreshold {
		inst.healthy = false
		m.opts.Bus.Warn(events.SiteUnhealthy, a.id, "%s instance %d failed %d health checks", a.site.Name, inst.index, inst.healthFails)
		return "unhealthy"
	}
	return ""
}
