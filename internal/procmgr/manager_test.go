package procmgr

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
)

const testApp = `
const http = require('http');
http.createServer((req, res) => res.end('pid=' + process.pid + ' instance=' + process.env.NODEHOSTER_INSTANCE))
  .listen(process.env.PORT, () => console.log('ready'));
`

// TestLifecycle runs a real Node.js app through start, load balancing, a
// rolling recycle and stop. On Windows this exercises the job objects,
// suspended start and the named-pipe agent.
func TestLifecycle(t *testing.T) {
	nodeExe, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	app := filepath.Join(dir, "app")
	os.MkdirAll(app, 0o755)
	os.WriteFile(filepath.Join(app, "server.js"), []byte(testApp), 0o644)

	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	settings := testSettings
	bus := events.New(st, log, settings, func(string) string { return "test" })
	m, err := New(Options{
		Log: log, Bus: bus, SitesDir: filepath.Join(dir, "sites"), LogsDir: filepath.Join(dir, "logs"),
		RunDir: filepath.Join(dir, "run"), Settings: settings,
		ResolveNode: func(string) (NodeRuntime, error) { return NodeRuntime{Version: "test", Exe: nodeExe}, nil },
		Unseal:      func(s string) string { return s }, IsLocationTarget: func(string) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()

	site := &model.Site{ID: "s1", Name: "test", Type: model.SiteNode,
		Bindings: []model.Binding{{Protocol: "http", Port: 80}},
		Node:     &model.NodeConfig{AppRoot: app, Script: "server.js", Instances: 2, AgentEnabled: true}}
	site.ApplyDefaults()
	m.Apply(site)
	if err := m.Start("s1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "2 ready instances", func() bool { return len(m.Backends("s1")) == 2 })

	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		for _, b := range m.Backends("s1") {
			seen[get(t, "http://"+b.Addr+"/")] = true
		}
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 distinct instances, got %v", seen)
	}

	waitFor(t, "agent connection", func() bool {
		st, _ := m.Status("s1")
		return st.Instances[0].NodeVersion != "" && st.Instances[1].NodeVersion != ""
	})

	before, _ := m.Status("s1")
	if err := m.Recycle("s1", "test"); err != nil {
		t.Fatal(err)
	}
	after, _ := m.Status("s1")
	for i := range after.Instances {
		if after.Instances[i].PID == before.Instances[i].PID {
			t.Fatalf("instance %d was not replaced by the recycle", i)
		}
		if after.Instances[i].State != "ready" {
			t.Fatalf("instance %d state %s after recycle", i, after.Instances[i].State)
		}
	}
	if after.State != model.StateRunning {
		t.Fatalf("state %s after recycle", after.State)
	}

	pids := []int{after.Instances[0].PID, after.Instances[1].PID}
	if err := m.Stop("s1"); err != nil {
		t.Fatal(err)
	}
	if n := len(m.Backends("s1")); n != 0 {
		t.Fatalf("%d backends left after stop", n)
	}
	for _, pid := range pids {
		waitFor(t, "process exit", func() bool { return !processAlive(pid) })
	}
	logs := m.Logs("s1").Recent(100)
	var text []string
	for _, l := range logs {
		text = append(text, l.Text)
	}
	if !strings.Contains(strings.Join(text, "\n"), "ready") {
		t.Fatalf("application output was not captured:\n%s", strings.Join(text, "\n"))
	}
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return signalZero(p) == nil
}

func TestRestartDelay(t *testing.T) {
	if d := restartDelay(1, 0); d != time.Second {
		t.Fatalf("first restart: %s", d)
	}
	if d := restartDelay(100, 0); d > 30*time.Second {
		t.Fatalf("delay not capped: %s", d)
	}
	prev := time.Duration(0)
	for i := 1; i < 10; i++ {
		d := restartDelay(i, 0)
		if d < prev {
			t.Fatalf("delay decreased at %d: %s < %s", i, d, prev)
		}
		prev = d
	}
}

func TestEnvCaseFolding(t *testing.T) {
	e := newEnv([]string{"Path=/a", "NODEHOSTER_AGENT_TOKEN=leak", "FOO=1"})
	e.prependPath("/node")
	e.set("FOO", "2")
	list := strings.Join(e.list(), ";")
	if strings.Contains(list, "leak") {
		t.Fatal("inherited NODEHOSTER_ variables must be dropped")
	}
	if !strings.Contains(list, "FOO=2") {
		t.Fatalf("override lost: %s", list)
	}
	if !strings.Contains(list, "/node") {
		t.Fatalf("PATH not prepended: %s", list)
	}
}

// TestRestartAfterRapidFail: a site stopped by rapid-fail protection must be
// startable again, and the new instances must be tracked (not orphaned).
func TestRestartAfterRapidFail(t *testing.T) {
	nodeExe, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	app := filepath.Join(dir, "app")
	os.MkdirAll(app, 0o755)
	os.WriteFile(filepath.Join(app, "bad.js"), []byte("process.exit(1)"), 0o644)
	os.WriteFile(filepath.Join(app, "server.js"), []byte(testApp), 0o644)

	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	settings := testSettings
	m, err := New(Options{
		Log: log, Bus: events.New(st, log, settings, func(string) string { return "t" }),
		SitesDir: filepath.Join(dir, "sites"), LogsDir: filepath.Join(dir, "logs"), RunDir: filepath.Join(dir, "run"),
		Settings: settings, Unseal: func(s string) string { return s }, IsLocationTarget: func(string) bool { return false },
		ResolveNode: func(string) (NodeRuntime, error) { return NodeRuntime{Version: "test", Exe: nodeExe}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()

	site := &model.Site{ID: "s1", Name: "t", Type: model.SiteNode, Bindings: []model.Binding{{Protocol: "http", Port: 80}},
		Node: &model.NodeConfig{AppRoot: app, Script: "bad.js", MaxRestarts: 1, RestartWindowSec: 60}}
	site.ApplyDefaults()
	m.Apply(site)
	m.Start("s1")
	waitFor(t, "rapid-fail", func() bool { s, _ := m.Status("s1"); return s.State == model.StateFailed })

	fixed := *site
	node := *site.Node
	node.Script = "server.js"
	fixed.Node = &node
	m.Apply(&fixed)
	if err := m.Restart("s1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running after restart", func() bool { s, _ := m.Status("s1"); return s.State == model.StateRunning })
	time.Sleep(time.Second) // the rapid-fail cleanup must not wipe the new instances
	if s, _ := m.Status("s1"); s.State != model.StateRunning || len(m.Backends("s1")) != 1 {
		t.Fatalf("state %s, %d backends after restart", s.State, len(m.Backends("s1")))
	}
	m.Stop("s1")
}

// testSettings are the defaults with instance ports below every OS's
// ephemeral range (Linux starts at 32768). The default range, 41000-48999,
// is inside Linux's: while go test runs packages in parallel, another
// package's sockets can take a port between the allocator's check and the
// test app's listen, and the app exits with EADDRINUSE.
func testSettings() model.Settings {
	s := model.DefaultSettings()
	s.PortRangeStart, s.PortRangeEnd = 21000, 21999
	return s
}
