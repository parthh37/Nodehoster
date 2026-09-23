package procmgr

import (
	"io"
	"log/slog"
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

// flakyApp crashes on start until a file named "ok" appears next to it, like
// an app whose database is down.
const flakyApp = `
const fs = require('fs'), path = require('path'), http = require('http');
if (!fs.existsSync(path.join(__dirname, 'ok'))) process.exit(1);
http.createServer((req, res) => res.end('up')).listen(process.env.PORT);
`

func newRecoveryManager(t *testing.T) (m *Manager, app string) {
	t.Helper()
	nodeExe, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	// Not t.TempDir(): the agent's Unix socket lives under it, and long test
	// names push the path past the 104-byte limit on macOS.
	dir, err := os.MkdirTemp("", "nh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	app = filepath.Join(dir, "app")
	os.MkdirAll(app, 0o755)
	os.WriteFile(filepath.Join(app, "server.js"), []byte(flakyApp), 0o644)
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	settings := testSettings
	m, err = New(Options{
		Log: log, Bus: events.New(st, log, settings, func(string) string { return "t" }),
		SitesDir: filepath.Join(dir, "sites"), LogsDir: filepath.Join(dir, "logs"), RunDir: filepath.Join(dir, "run"),
		Settings: settings, Unseal: func(s string) string { return s }, IsLocationTarget: func(string) bool { return false },
		ResolveNode: func(string) (NodeRuntime, error) { return NodeRuntime{Version: "test", Exe: nodeExe}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Shutdown)
	return m, app
}

func crashLoopSite(app, action string) *model.Site {
	site := &model.Site{ID: "s1", Name: "t", Type: model.SiteNode, Bindings: []model.Binding{{Protocol: "http", Port: 80}},
		Node: &model.NodeConfig{AppRoot: app, Script: "server.js", MaxRestarts: 1, RestartWindowSec: 60,
			RapidFailAction: action, RecoverAfterSec: 1}}
	site.ApplyDefaults()
	return site
}

// TestRapidFailRecovers: a site stopped by rapid-fail protection comes back
// on its own once whatever made it crash is fixed.
func TestRapidFailRecovers(t *testing.T) {
	m, app := newRecoveryManager(t)
	m.Apply(crashLoopSite(app, "recover"))
	m.Start("s1")
	waitFor(t, "rapid-fail", func() bool { s, _ := m.Status("s1"); return s.State == model.StateFailed })
	if s, _ := m.Status("s1"); !strings.Contains(s.Message, "restarting automatically") {
		t.Fatalf("failed status should announce the automatic restart, got %q", s.Message)
	}

	os.WriteFile(filepath.Join(app, "ok"), nil, 0o644)
	waitFor(t, "automatic recovery", func() bool { s, _ := m.Status("s1"); return s.State == model.StateRunning })
	if n := len(m.Backends("s1")); n != 1 {
		t.Fatalf("%d backends after recovery, want 1", n)
	}
}

// TestRapidFailStopWaitsForOperator: with rapidFailAction "stop" a failed
// site stays down, and a manual stop cancels a pending automatic restart.
func TestRapidFailStopWaitsForOperator(t *testing.T) {
	for _, tc := range []struct {
		action     string
		manualStop bool
	}{{"stop", false}, {"recover", true}} {
		t.Run(tc.action, func(t *testing.T) {
			m, app := newRecoveryManager(t)
			m.Apply(crashLoopSite(app, tc.action))
			m.Start("s1")
			waitFor(t, "rapid-fail", func() bool { s, _ := m.Status("s1"); return s.State == model.StateFailed })
			if tc.manualStop {
				m.Stop("s1")
			}
			os.WriteFile(filepath.Join(app, "ok"), nil, 0o644)
			time.Sleep(3 * time.Second) // several recovery delays
			if m.Running("s1") {
				t.Fatal("site was restarted automatically")
			}
		})
	}
}

func TestRecoveryDelay(t *testing.T) {
	base := 5 * time.Minute
	for _, tc := range []struct {
		previous int
		want     time.Duration
	}{{0, 5 * time.Minute}, {1, 10 * time.Minute}, {2, 20 * time.Minute}, {3, 40 * time.Minute}, {4, time.Hour}, {1000, time.Hour}} {
		if got := recoveryDelay(base, tc.previous); got != tc.want {
			t.Errorf("recoveryDelay(%s, %d) = %s, want %s", base, tc.previous, got, tc.want)
		}
	}
}
