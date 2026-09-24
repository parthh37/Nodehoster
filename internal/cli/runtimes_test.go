//go:build !windows

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/runtimes"
)

func TestRuntimeCommands(t *testing.T) {
	s := newServer(t)
	// A Deno version as the service's installer leaves it.
	exe := "deno"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	dir := filepath.Join(s.root, "runtimes", "deno", "2.1.4")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, exe), []byte("x"), 0o755)

	r := s.run("runtime", "list").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "RUNTIME") || !strings.Contains(r.stdout, "2.1.4") || !strings.Contains(r.stdout, "Python") || !strings.Contains(r.stdout, ".NET") {
		t.Fatalf("runtime list:\n%s", r.stdout)
	}
	r = s.run("runtime", "list", "--json").expect(t, ExitOK)
	var rep runtimes.Report
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil || len(rep.Deno.Installed) != 1 {
		t.Fatalf("runtime list --json: %v\n%s", err, r.stdout)
	}

	// Sites show their runtime.
	s.createSite(&model.Site{Name: "api", Type: model.SiteNode,
		Node: &model.NodeConfig{AppRoot: t.TempDir(), Runtime: model.RuntimeDeno, RuntimeVersion: "2.1.4", Script: "main.ts"}})
	r = s.run("site", "list").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "RUNTIME") || !strings.Contains(r.stdout, "Deno 2.1.4") {
		t.Fatalf("site list:\n%s", r.stdout)
	}
	r = s.run("site", "show", "api").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "Runtime:") || !strings.Contains(r.stdout, "deno run main.ts") {
		t.Fatalf("site show:\n%s", r.stdout)
	}

	// A version in use stays; the service says by whom.
	r = s.run("runtime", "remove", "deno", "2.1.4").expect(t, ExitError)
	if !strings.Contains(r.stderr, "api") {
		t.Fatalf("remove in use: %s", r.stderr)
	}
	s.run("runtime", "remove", "python", "3.12").expect(t, ExitUsage)
	s.run("runtime", "install").expect(t, ExitUsage)
}
