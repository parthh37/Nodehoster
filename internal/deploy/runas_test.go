package deploy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/procmgr"
)

// runAsRecorder stands in for logging on as a site's account: it records
// what would run as which account and runs it as the test's own.
type runAsRecorder struct {
	mu    sync.Mutex
	calls []string // "user password siteDir: args"
	dirs  []string // cmd.Dir
}

func (r *runAsRecorder) run(ctx context.Context, cmd *exec.Cmd, ra model.RunAsConfig, password, siteDir string) error {
	r.mu.Lock()
	r.calls = append(r.calls, ra.Username+" "+password+" "+siteDir+": "+commandLine(cmd))
	r.dirs = append(r.dirs, cmd.Dir)
	r.mu.Unlock()
	return cmd.Run()
}

// TestDeployRunsSiteCommandsAsItsAccount: a site with a run-as account
// runs its install and build commands as that account, with a temporary
// folder of its own; the service does not run them. The caches they use
// (here Bun's, in the site's folder) are that account's to change.
func TestDeployRunsSiteCommandsAsItsAccount(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := &runAsRecorder{}
	h.d.runAsAccount = rec.run
	h.d.opts.ResolveRuntime = func(rt, version string) (procmgr.RuntimeExe, error) {
		return procmgr.RuntimeExe{Version: "1.1.30", Exe: filepath.Join(h.root, "bin", "bun")}, nil
	}
	sealed, err := h.box.Seal("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	site := h.runtimeSite(t, "asuser", model.RuntimeBun, func(s *model.Site) {
		s.Node.RunAs = model.RunAsConfig{Enabled: true, Username: `SRV\app-shop`, Password: sealed}
		s.Deploy.InstallCommand = "echo installing"
		s.Deploy.BuildCommand = echoEnv("TEMP")
	})
	got, log := h.deployLog(t, site, []zipEntry{{name: "package.json", body: "{}"}, {name: "index.ts", body: "1"}})
	if got.Status != "succeeded" {
		t.Fatalf("status %s (%s)\n%s", got.Status, got.Message, log)
	}
	siteDir := filepath.Join(h.sitesDir, "asuser")
	if len(rec.calls) != 2 {
		t.Fatalf("ran as the account: %q\n%s", rec.calls, log)
	}
	for i, c := range rec.calls {
		if !strings.HasPrefix(c, `SRV\app-shop s3cret `+siteDir+": ") {
			t.Errorf("command %d: %q", i, c)
		}
		if rec.dirs[i] != got.ReleaseDir {
			t.Errorf("command %d ran in %s", i, rec.dirs[i])
		}
	}
	if !strings.Contains(rec.calls[0], "echo installing") || !strings.Contains(rec.calls[1], "TEMP") {
		t.Errorf("commands %q", rec.calls)
	}
	tmp := filepath.Join(siteDir, ".tmp")
	if !strings.Contains(log, "TEMP="+tmp) || !exists(tmp) {
		t.Errorf("the account's temporary folder was not used:\n%s", log)
	}
	if !strings.Contains(log, `commands run as SRV\app-shop`) {
		t.Errorf("the log does not say who runs the commands:\n%s", log)
	}

	// Without a run-as account the service runs them, as before.
	plain := h.runtimeSite(t, "asservice", model.RuntimeBun, func(s *model.Site) { s.Deploy.InstallCommand = "echo installing" })
	before := len(rec.calls)
	got, log = h.deployLog(t, plain, []zipEntry{{name: "package.json", body: "{}"}})
	if got.Status != "succeeded" || len(rec.calls) != before || !strings.Contains(log, "installing") {
		t.Fatalf("status %s, %d run-as calls\n%s", got.Status, len(rec.calls)-before, log)
	}
	if strings.Contains(log, "commands run as") {
		t.Errorf("a site without an account says it has one:\n%s", log)
	}
}

// TestDeployRunAsFailureFailsTheDeployment: an account that cannot log on
// fails the step instead of running it as the service.
func TestDeployRunAsFailureFailsTheDeployment(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.d.runAsAccount = func(context.Context, *exec.Cmd, model.RunAsConfig, string, string) error {
		return os.ErrPermission
	}
	h.d.opts.ResolveRuntime = func(rt, version string) (procmgr.RuntimeExe, error) {
		return procmgr.RuntimeExe{Version: "1.1.30", Exe: filepath.Join(h.root, "bin", "bun")}, nil
	}
	marker := filepath.Join(h.root, "ran")
	site := h.runtimeSite(t, "nologon", model.RuntimeBun, func(s *model.Site) {
		s.Node.RunAs = model.RunAsConfig{Enabled: true, Username: "app"}
		s.Deploy.InstallCommand = "echo x > " + marker
	})
	got, log := h.deployLog(t, site, []zipEntry{{name: "package.json", body: "{}"}})
	if got.Status != "failed" || exists(marker) {
		t.Fatalf("status %s, ran: %v\n%s", got.Status, exists(marker), log)
	}
}

func TestIsolatedPythonCommands(t *testing.T) {
	if args := venvArgs("/r/.venv"); !slices.Equal(args, []string{"-I", "-m", "venv", "/r/.venv"}) {
		t.Errorf("venv args %q", args)
	}
	py := &model.Site{Type: model.SiteNode, Node: &model.NodeConfig{Runtime: model.RuntimePython}}
	def := model.DefaultInstallCommand(model.RuntimePython)
	cmd := isolatedInstall(context.Background(), py, def, "/r/.venv/bin/python")
	if cmd == nil {
		t.Fatal("the default install command is not run isolated")
	}
	if want := []string{"/r/.venv/bin/python", "-I", "-X", "utf8", "-m", "pip", "install", "-r", "requirements.txt"}; !slices.Equal(cmd.Args, want) {
		t.Errorf("install %q", cmd.Args)
	}
	// A command of the site's own runs through the shell as it is set; no
	// environment, nothing to run it with; other runtimes' installs too.
	bun := &model.Site{Type: model.SiteNode, Node: &model.NodeConfig{Runtime: model.RuntimeBun}}
	for name, c := range map[string]*exec.Cmd{
		"own command":    isolatedInstall(context.Background(), py, "python -m pip install .", "/r/.venv/bin/python"),
		"no environment": isolatedInstall(context.Background(), py, def, ""),
		"bun":            isolatedInstall(context.Background(), bun, def, "/r/.venv/bin/python"),
	} {
		if c != nil {
			t.Errorf("%s: %q", name, c.Args)
		}
	}
}

// TestDeployPythonIgnoresModulesInTheRelease: a venv.py or pip.py in the
// release is not what `-m venv` and the default `-m pip` run.
func TestDeployPythonIgnoresModulesInTheRelease(t *testing.T) {
	t.Parallel()
	exe, err := exec.LookPath("python3")
	if err != nil {
		if exe, err = exec.LookPath("python"); err != nil {
			t.Skip("python is not installed")
		}
	}
	if out, err := exec.Command(exe, "-c", "import venv, ensurepip").CombinedOutput(); err != nil {
		t.Skipf("python cannot create virtual environments: %s", out)
	}
	h := newHarness(t)
	h.d.opts.ResolveRuntime = func(rt, version string) (procmgr.RuntimeExe, error) {
		return procmgr.RuntimeExe{Version: "3", Exe: exe}, nil
	}
	site := h.runtimeSite(t, "pyhijack", model.RuntimePython, func(s *model.Site) { s.Node.Script = "app.py" })
	hijack := "import sys\nprint('HIJACKED')\nsys.exit(3)\n"
	entries := []zipEntry{
		{name: "requirements.txt", body: ""},
		{name: "app.py", body: "print('hi')"},
		{name: "venv.py", body: hijack},
		{name: "pip.py", body: hijack},
		{name: "ensurepip.py", body: hijack},
	}
	if runtime.GOOS == "windows" {
		entries = append(entries, zipEntry{name: "python.bat", body: "@echo HIJACKED\r\n@exit /b 3\r\n"})
	}
	got, log := h.deployLog(t, site, entries)
	if got.Status != "succeeded" || strings.Contains(log, "HIJACKED") {
		t.Fatalf("status %s (%s)\n%s", got.Status, got.Message, log)
	}
	if !strings.Contains(log, "-I -X utf8 -m pip install -r requirements.txt") {
		t.Errorf("the install did not run isolated:\n%s", log)
	}
}
