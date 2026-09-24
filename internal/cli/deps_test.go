//go:build !windows

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/nodeversions"
)

func TestDeps(t *testing.T) {
	// Not parallel: no node or git may be found on PATH.
	t.Setenv("PATH", t.TempDir())
	s := newServer(t)

	// The harness's default version is not installed.
	r := s.run("deps", "--json").expect(t, ExitError)
	var list []Dependency
	if err := json.Unmarshal([]byte(r.stdout), &list); err != nil || len(list) != 6 {
		t.Fatalf("deps --json: %v\n%s", err, r.stdout)
	}
	if list[0].Name != "node" || list[0].Installed || list[0].Version != "99.0.0-test" || list[1].Name != "git" || list[1].Installed {
		t.Fatalf("deps --json: %+v", list)
	}
	// Runtimes no site uses are listed but optional.
	for _, d := range list[2:] {
		if !d.Optional {
			t.Fatalf("unused runtime not optional: %+v", d)
		}
	}
	if !strings.Contains(r.stderr, "missing: node, git") || strings.Contains(r.stderr, "bun") {
		t.Fatalf("deps stderr: %s", r.stderr)
	}
	// One a site uses counts.
	s.createSite(&model.Site{Name: "bunapp", Type: model.SiteNode,
		Node: &model.NodeConfig{AppRoot: t.TempDir(), Runtime: model.RuntimeBun, Script: "index.ts"}})
	r = s.run("deps").expect(t, ExitError)
	if !strings.Contains(r.stderr, "missing: node, git, bun") || !strings.Contains(r.stdout, "used by 1 site(s)") {
		t.Fatalf("deps with a bun site:\nstdout %s\nstderr %s", r.stdout, r.stderr)
	}

	// A missing default is installed again, not replaced: sites pin it.
	// The service refuses this one without a download.
	r = s.run("deps", "install", "node").expect(t, ExitError)
	if !strings.Contains(r.stdout, "Installing Node.js 99.0.0-test") || !strings.Contains(r.stderr, "not a version number") {
		t.Fatalf("deps install node, default missing:\nstdout %s\nstderr %s", r.stdout, r.stderr)
	}

	// No default, but a runtime is installed: it becomes the default.
	st := s.c.Settings()
	st.DefaultNodeVersion = ""
	if _, err := s.c.UpdateSettings(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(s.root, "node", "20.1.0", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "node"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	r = s.run("deps", "install", "node").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "Making the installed Node.js 20.1.0 the server default") || !strings.Contains(r.stdout, "server default") {
		t.Fatalf("deps install node, installed but no default:\n%s", r.stdout)
	}
	if v := s.c.Settings().DefaultNodeVersion; v != "20.1.0" {
		t.Fatalf("default version = %q, want 20.1.0", v)
	}
	// Now nothing is left to do.
	r = s.run("deps", "install", "node").expect(t, ExitOK)
	if strings.Contains(r.stdout, "Installing") || strings.Contains(r.stdout, "Making") {
		t.Fatalf("deps install node, nothing missing:\n%s", r.stdout)
	}

	// MinGit is for Windows; elsewhere the error says what to do.
	r = s.run("deps", "install", "git").expect(t, ExitError)
	if !strings.Contains(r.stderr, "package manager") {
		t.Fatalf("deps install git: %s", r.stderr)
	}
	s.run("deps", "install", "python").expect(t, ExitUsage)
	s.run("runtime", "install", "python").expect(t, ExitUsage)
}

func TestPickNodeVersion(t *testing.T) {
	avail := []nodeversions.Available{
		{Version: "25.1.0", LTS: false},
		{Version: "24.11.0", LTS: "Krypton"},
		{Version: "22.21.0", LTS: "Jod"},
	}
	if v := pickNodeVersion(avail); v != "24.11.0" {
		t.Fatalf("pickNodeVersion = %q, want the newest LTS 24.11.0", v)
	}
	if v := pickNodeVersion(avail[:1]); v != "" {
		t.Fatalf("pickNodeVersion without LTS = %q, want none", v)
	}
}
