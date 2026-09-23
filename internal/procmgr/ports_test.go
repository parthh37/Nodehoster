package procmgr

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

// racedApp reports its port and waits for the test to take that port before
// listening, the way an outgoing connection can take a port between the
// allocator's check and the application's listen. Later runs listen at once.
const racedApp = `
const fs = require('fs'), path = require('path'), http = require('http');
const listen = () => http.createServer((req, res) => res.end('up')).listen(process.env.PORT, '127.0.0.1');
const portFile = path.join(__dirname, 'port'), claimed = path.join(__dirname, 'claimed');
if (fs.existsSync(portFile)) {
  listen();
} else {
  fs.writeFileSync(portFile + '.tmp', process.env.PORT);
  fs.renameSync(portFile + '.tmp', portFile);
  const t = setInterval(() => { if (fs.existsSync(claimed)) { clearInterval(t); listen(); } }, 20);
}
`

// TestStartSurvivesPortTaken: an instance whose port is taken before it can
// listen starts on another port, and the lost port does not count as a crash.
func TestStartSurvivesPortTaken(t *testing.T) {
	m, app := newRecoveryManager(t)
	os.WriteFile(filepath.Join(app, "server.js"), []byte(racedApp), 0o644)
	m.Apply(crashLoopSite(app, "stop"))
	m.Start("s1")

	var port string
	waitFor(t, "the first instance's port", func() bool {
		b, err := os.ReadFile(filepath.Join(app, "port"))
		port = string(b)
		return err == nil
	})
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	os.WriteFile(filepath.Join(app, "claimed"), nil, 0o644)

	waitFor(t, "running", func() bool { s, _ := m.Status("s1"); return s.State == model.StateRunning })
	inst := mustStatus(t, m).Instances[0]
	if strconv.Itoa(inst.Port) == port {
		t.Fatalf("instance still on the taken port %s", port)
	}
	if inst.Restarts != 0 || inst.LastExitCode != nil {
		t.Fatalf("the lost port counted as a failure: restarts %d, last exit recorded %v", inst.Restarts, inst.LastExitCode != nil)
	}
	if n := countLogs(m, "retrying on another port"); n != 1 {
		t.Fatalf("%d retries logged, want 1", n)
	}
}

// TestPortRetriesAreBounded: an app that keeps reporting its port in use
// gets a limited number of fresh ports per start, then fails like any other
// crash; a genuine crash is not retried at all.
func TestPortRetriesAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		script   string
		attempts int // processes per counted failure
	}{
		{"port in use", `console.error('Error: listen EADDRINUSE: address already in use :::' + process.env.PORT); process.exit(1);`, 1 + portRetries},
		{"crash", `console.error('Error: connect ECONNREFUSED 127.0.0.1:5432'); process.exit(1);`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, app := newRecoveryManager(t)
			os.WriteFile(filepath.Join(app, "server.js"), []byte(tc.script), 0o644)
			m.Apply(crashLoopSite(app, "stop")) // fails after 2 counted failures
			m.Start("s1")
			waitFor(t, "rapid-fail", func() bool { s, _ := m.Status("s1"); return s.State == model.StateFailed })

			failures, started := countLogs(m, "failed to start"), countLogs(m, "started: pid")
			if failures != 2 {
				t.Fatalf("%d counted failures, want 2", failures)
			}
			if started != failures*tc.attempts {
				t.Fatalf("%d processes for %d failures, want %d each", started, failures, tc.attempts)
			}
		})
	}
}

func TestMentionsAddrInUse(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"Error: listen EADDRINUSE: address already in use :::41000", true},
		{"Error: listen EADDRINUSE: address already in use 127.0.0.1:41000", true},
		{"Error: listen EADDRINUSE :::41000", true}, // Node before 10
		{"Error: listen EADDRINUSE: address already in use :::410001", false},
		{"Error: listen EADDRINUSE: address already in use :::9090", false},
		{"Error: listen EADDRINUSE: address already in use :::4100 and :::41000", true},
		{"server listening on :41000", false},
	} {
		if got := mentionsAddrInUse(tc.line, "41000"); got != tc.want {
			t.Errorf("mentionsAddrInUse(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestEphemeralOverlap(t *testing.T) {
	lo, hi, ok := ephemeralRange()
	if !ok {
		if runtime.GOOS == "linux" {
			t.Fatal("could not read the ephemeral port range")
		}
		if ephemeralOverlap(1024, 65535) != "" {
			t.Fatal("overlap reported without a known ephemeral range")
		}
		return
	}
	if ephemeralOverlap(hi-10, hi+10) == "" || ephemeralOverlap(lo-10, lo) == "" {
		t.Fatalf("overlap with %d-%d not reported", lo, hi)
	}
	if lo > 1100 && ephemeralOverlap(1024, lo-1) != "" {
		t.Fatalf("range below %d-%d reported as overlapping", lo, hi)
	}
}

func mustStatus(t *testing.T, m *Manager) model.SiteStatus {
	t.Helper()
	s, ok := m.Status("s1")
	if !ok {
		t.Fatal("site s1 is not registered")
	}
	return s
}

func countLogs(m *Manager, substr string) int {
	n := 0
	for _, l := range m.Logs("s1").Recent(1000) {
		if strings.Contains(l.Text, substr) {
			n++
		}
	}
	return n
}
