//go:build !windows

package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

const cliSlotApp = `require('http').createServer((q, r) => r.end(require('path').basename(__dirname))).listen(process.env.PORT);`

func slotSite(root string, port int) *model.Site {
	return &model.Site{Name: "shop", Type: model.SiteNode,
		Bindings: []model.Binding{{Protocol: "http", IP: "127.0.0.1", Port: port, Host: "www.test"}, {Protocol: "http", IP: "127.0.0.1", Port: port, Host: "staging.test", Slot: "staging"}},
		Node:     &model.NodeConfig{AppRoot: root, Script: "server.js", ShutdownTimeoutSec: 2},
		Slots:    []model.DeploymentSlot{{Name: "staging", Warmup: model.WarmupConfig{TimeoutSec: 20}}},
	}
}

func TestSlotCommands(t *testing.T) {
	s := newServer(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "server.js"), []byte(cliSlotApp), 0o644)
	s.createSite(slotSite(root, freePort(t)))

	r := s.run("slot", "list", "shop").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "production") || !strings.Contains(r.stdout, "staging") || !strings.Contains(r.stdout, "staging.test") {
		t.Fatalf("slot list:\n%s", r.stdout)
	}
	// Nothing deployed to the slot yet.
	r = s.run("slot", "swap", "shop", "--yes").expect(t, ExitError)
	if !strings.Contains(r.stderr, "deploy to it first") {
		t.Fatalf("stderr: %s", r.stderr)
	}
	r = s.run("slot", "swap", "shop", "production", "--yes").expect(t, ExitUsage)
	r = s.run("deploy", "shop", "--zip", "x.zip", "--slot", "qa").expect(t, ExitError)
	if !strings.Contains(r.stderr, `no deployment slot "qa"`) {
		t.Fatalf("stderr: %s", r.stderr)
	}
	r = s.run("releases", "shop", "--slot", "staging").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "no deployments") {
		t.Fatalf("releases: %s", r.stdout)
	}
	r = s.run("slot", "start", "shop", "staging").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "Started shop [staging]") {
		t.Fatalf("slot start: %s", r.stdout)
	}
	s.run("slot", "stop", "shop", "staging").expect(t, ExitOK)
	s.run("slot", "stop", "shop", "qa").expect(t, ExitError)
}

// TestSlotDeployAndSwap deploys to a slot and swaps it in from the command
// line, with the Node.js on PATH.
func TestSlotDeployAndSwap(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	s := newServer(t)
	st := s.c.Settings()
	st.DefaultNodeVersion = ""
	st.PortRangeStart, st.PortRangeEnd = 24000, 24999
	if _, err := s.c.UpdateSettings(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "server.js"), []byte(cliSlotApp), 0o644)
	site := s.createSite(slotSite(root, freePort(t)))
	s.run("site", "start", "shop").expect(t, ExitOK)

	zip := writeZip(t, t.TempDir(), "v2.zip", map[string]string{"server.js": cliSlotApp})
	r := s.run("deploy", "shop", "--zip", zip, "--slot", "staging").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "of shop [staging] succeeded") {
		t.Fatalf("deploy:\n%s", r.stdout)
	}
	r = s.run("releases", "shop", "--slot", "staging").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "staging") {
		t.Fatalf("releases:\n%s", r.stdout)
	}
	// A swap asks first, and scripts must say --yes.
	r = s.run("slot", "swap", "shop").expect(t, ExitUsage)
	if !strings.Contains(r.stderr, "--yes") {
		t.Fatalf("stderr: %s", r.stderr)
	}
	r = s.runWith(strings.NewReader("n\n"), true, "slot", "swap", "shop").expect(t, ExitError)
	if !strings.Contains(r.stdout, "Swap now?") || !strings.Contains(r.stderr, "cancelled") {
		t.Fatalf("interactive: %s / %s", r.stdout, r.stderr)
	}
	r = s.run("slot", "swap", "shop", "--yes", "--json").expect(t, ExitOK)
	var res model.SwapResult
	if err := json.Unmarshal([]byte(r.stdout), &res); err != nil || !res.Succeeded || res.ProductionRelease == "" {
		t.Fatalf("swap: %v %+v\n%s\n%s", err, res, r.stdout, r.stderr)
	}
	cur, _ := s.c.Site(site.ID)
	if cur.ActiveRelease != res.ProductionRelease || cur.FindSlot("staging").ActiveRelease != "" {
		t.Fatalf("releases after swap: %q / %q", cur.ActiveRelease, cur.FindSlot("staging").ActiveRelease)
	}
}
