package procmgr

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

const workerApp = `
console.log('worker up, PORT=' + (process.env.PORT || 'none') + ' instance=' + process.env.NODEHOSTER_INSTANCE);
setInterval(() => {}, 1000);
`

func logText(m *Manager, id string) string {
	var b strings.Builder
	for _, l := range m.Logs(id).Recent(500) {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

// TestWorkerLifecycle: a worker site gets no port and is running once it
// has survived the settle period; recycling replaces it; nothing is
// published to the proxy.
func TestWorkerLifecycle(t *testing.T) {
	m, app := newRecoveryManager(t)
	os.WriteFile(filepath.Join(app, "worker.js"), []byte(workerApp), 0o644)
	site := &model.Site{ID: "w1", Name: "queue", Type: model.SiteWorker,
		Node: &model.NodeConfig{AppRoot: app, Script: "worker.js", Instances: 2, AgentEnabled: true}}
	site.ApplyDefaults()
	m.Apply(site)
	started := time.Now()
	if err := m.Start("w1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool { s, _ := m.Status("w1"); return s.State == model.StateRunning })
	if d := time.Since(started); d < settlePeriod {
		t.Fatalf("running after %s, before the settle period", d)
	}
	st, _ := m.Status("w1")
	for _, in := range st.Instances {
		if in.Port != 0 || in.State != "ready" || in.PID == 0 {
			t.Fatalf("instance: %+v", in)
		}
	}
	if n := len(m.Backends("w1")); n != 0 {
		t.Fatalf("%d backends published for a worker", n)
	}
	waitFor(t, "output", func() bool { return strings.Contains(logText(m, "w1"), "worker up, PORT=none") })
	waitFor(t, "agent metrics", func() bool { s, _ := m.Status("w1"); return s.Instances[0].NodeVersion != "" })

	before := []int{st.Instances[0].PID, st.Instances[1].PID}
	if err := m.Recycle("w1", "test"); err != nil {
		t.Fatal(err)
	}
	after, _ := m.Status("w1")
	for i, in := range after.Instances {
		if in.PID == before[i] || in.State != "ready" {
			t.Fatalf("instance %d after recycle: %+v (was pid %d)", i, in, before[i])
		}
		waitFor(t, "old process gone", func() bool { return !processAlive(before[i]) })
	}
	m.Stop("w1")
}

// TestWorkerRapidFail: a worker that dies within the settle period fails
// to start, and rapid-fail protection stops it like a crashing web app.
func TestWorkerRapidFail(t *testing.T) {
	m, app := newRecoveryManager(t)
	os.WriteFile(filepath.Join(app, "bad.js"), []byte(`setTimeout(() => process.exit(2), 300)`), 0o644)
	site := &model.Site{ID: "w2", Name: "bad", Type: model.SiteWorker,
		Node: &model.NodeConfig{AppRoot: app, Script: "bad.js", MaxRestarts: 1, RestartWindowSec: 60, RapidFailAction: "stop"}}
	site.ApplyDefaults()
	m.Apply(site)
	m.Start("w2")
	waitFor(t, "rapid-fail", func() bool { s, _ := m.Status("w2"); return s.State == model.StateFailed })
	if !strings.Contains(logText(m, "w2"), "exited during startup with code 2") {
		t.Fatalf("log:\n%s", logText(m, "w2"))
	}
}

// syncBuffer collects a task's output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

const taskScript = `
const { spawn } = require('child_process');
const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' });
console.log('task=' + process.env.NODEHOSTER_TASK + ' port=' + (process.env.PORT || 'none') +
  ' site=' + process.env.SITE_VAR + ' extra=' + process.env.EXTRA + ' child=' + child.pid);
setInterval(() => {}, 1000);
`

// TestTaskKillEndsTheTree: a task gets the site's and its own variables
// but no port, and killing it (a timeout) ends the processes it started.
func TestTaskKillEndsTheTree(t *testing.T) {
	m, app := newRecoveryManager(t)
	os.WriteFile(filepath.Join(app, "job.js"), []byte(taskScript), 0o644)
	site := &model.Site{ID: "t1", Name: "jobs", Type: model.SiteNode,
		Node: &model.NodeConfig{AppRoot: app, Script: "server.js", Env: []model.EnvVar{{Name: "SITE_VAR", Value: "s"}, {Name: "EXTRA", Value: "site"}}}}
	site.ApplyDefaults()
	task := model.ScheduledTask{ID: "a", Name: "cleanup", Script: "job.js", Env: []model.EnvVar{{Name: "EXTRA", Value: "task"}}}

	var out syncBuffer
	p, err := m.StartTask(site, task, "run1", &out)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`child=(\d+)`)
	waitFor(t, "task output", func() bool { return re.MatchString(out.String()) })
	if !strings.Contains(out.String(), "task=cleanup port=none site=s extra=task") {
		t.Fatalf("output: %s", out.String())
	}
	child, _ := strconv.Atoi(re.FindStringSubmatch(out.String())[1])
	if !processAlive(child) {
		t.Fatal("the child is not running")
	}
	p.Kill()
	select {
	case <-p.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("the task did not exit after Kill")
	}
	waitFor(t, "child killed with the task", func() bool { return !processAlive(child) })
}

// TestTaskGracefulStop: with the agent, Stop reaches the application's
// SIGTERM handler before anything is killed.
func TestTaskGracefulStop(t *testing.T) {
	m, app := newRecoveryManager(t)
	os.WriteFile(filepath.Join(app, "job.js"), []byte(`
process.on('SIGTERM', () => { console.log('graceful'); process.exit(0); });
console.log('waiting');
setInterval(() => {}, 1000);
`), 0o644)
	site := &model.Site{ID: "t2", Name: "jobs", Type: model.SiteWorker, Node: &model.NodeConfig{AppRoot: app, Script: "x.js", AgentEnabled: true}}
	site.ApplyDefaults()
	var out syncBuffer
	p, err := m.StartTask(site, model.ScheduledTask{ID: "a", Name: "a", Script: "job.js"}, "run2", &out)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task started", func() bool { return strings.Contains(out.String(), "waiting") })
	// The agent connects shortly after start; Stop falls back to SIGTERM
	// (Unix) or a kill (Windows) without it, so wait for it.
	waitFor(t, "agent", func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.agentConn != nil })
	p.Stop(10 * time.Second)
	if code := p.ExitCode(); code != 0 || !strings.Contains(out.String(), "graceful") {
		t.Fatalf("exit %d, output %q", code, out.String())
	}
}
