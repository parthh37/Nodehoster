package deploy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/procmgr"
)

// runtimeSite stores and returns a node site of a runtime other than
// Node.js, deployed into releases.
func (h *harness) runtimeSite(t *testing.T, id, rt string, mutate func(*model.Site)) *model.Site {
	t.Helper()
	s := &model.Site{ID: id, Name: "site-" + id, Type: model.SiteNode,
		Node: &model.NodeConfig{AppRoot: ".", Runtime: rt, Script: "main"}}
	s.Deploy.KeepReleases = 5
	if mutate != nil {
		mutate(s)
	}
	s.ApplyDefaults()
	if err := h.st.PutSite(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (h *harness) deployLog(t *testing.T, site *model.Site, entries []zipEntry) (*model.Deployment, string) {
	t.Helper()
	zp := writeZip(t, h.root, site.ID+".zip", entries)
	dep, err := h.d.DeployZip(context.Background(), site, zp, "u")
	if err != nil {
		t.Fatal(err)
	}
	got := h.wait(t, dep)
	log, _ := h.d.Log(site.ID, dep.ID)
	return got, string(log)
}

// echoEnv is a build command that prints a variable, in the shell the
// deployment runs commands with.
func echoEnv(name string) string {
	if runtime.GOOS == "windows" {
		return "echo " + name + "=%" + name + "%"
	}
	return "echo " + name + "=$" + name
}

func TestDeployRuntimeEnvironment(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var asked []string
	h.d.opts.ResolveRuntime = func(rt, version string) (procmgr.RuntimeExe, error) {
		asked = append(asked, rt+"@"+version)
		return procmgr.RuntimeExe{Version: "2.1.4", Exe: filepath.Join(h.root, "bin", rt)}, nil
	}
	site := h.runtimeSite(t, "deno", model.RuntimeDeno, func(s *model.Site) {
		s.Node.RuntimeVersion = "2.1.4"
		s.Deploy.BuildCommand = echoEnv("DENO_DIR")
	})
	if site.Deploy.InstallCommand != "deno install" {
		t.Fatalf("default install command %q", site.Deploy.InstallCommand)
	}
	got, log := h.deployLog(t, site, []zipEntry{{name: "main.ts", body: "Deno.serve(() => new Response('hi'))"}})
	if got.Status != "succeeded" {
		t.Fatalf("status %s (%s)\n%s", got.Status, got.Message, log)
	}
	// No deno.json: nothing to install. The cache is the one processes use.
	if !strings.Contains(log, "no deno.json or deno.jsonc or package.json, skipping install") {
		t.Errorf("install not skipped:\n%s", log)
	}
	if !strings.Contains(log, "DENO_DIR="+model.DenoDir(h.sitesDir, "deno")) {
		t.Errorf("DENO_DIR not set:\n%s", log)
	}
	if len(asked) == 0 || asked[0] != "deno@2.1.4" {
		t.Errorf("resolved %v", asked)
	}

	// A runtime that is not installed fails the deployment, before
	// anything runs, with the resolver's advice.
	h.d.opts.ResolveRuntime = func(rt, version string) (procmgr.RuntimeExe, error) {
		return procmgr.RuntimeExe{}, errors.New("Bun 1.1.30 is not installed; install it on the Runtimes page")
	}
	bun := h.runtimeSite(t, "bun", model.RuntimeBun, nil)
	got, log = h.deployLog(t, bun, []zipEntry{{name: "package.json", body: "{}"}})
	if got.Status != "failed" || !strings.Contains(got.Message, "Runtimes page") {
		t.Fatalf("bun without bun: %s (%s)\n%s", got.Status, got.Message, log)
	}

	// .NET is only needed by a build that runs dotnet: an app deployed
	// ready-built deploys without it, and there is no install step.
	dotnet := h.runtimeSite(t, "dotnet", model.RuntimeDotnet, func(s *model.Site) { s.Node.Script = "Shop.dll" })
	if dotnet.Deploy.InstallCommand != "" {
		t.Fatalf("dotnet install command %q", dotnet.Deploy.InstallCommand)
	}
	got, log = h.deployLog(t, dotnet, []zipEntry{{name: "Shop.dll", body: "MZ"}})
	if got.Status != "succeeded" {
		t.Fatalf("dotnet: %s (%s)\n%s", got.Status, got.Message, log)
	}
}

// TestDeployPythonVenv deploys a Python application: the release gets its
// own virtual environment (replacing one that came in the upload) and the
// install command runs inside it.
func TestDeployPythonVenv(t *testing.T) {
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
	site := h.runtimeSite(t, "py", model.RuntimePython, func(s *model.Site) {
		s.Node.Script = "app.py"
		// The default is pip install -r requirements.txt; this shows which
		// interpreter the commands get without needing the network.
		s.Deploy.InstallCommand = `python -c "import sys; print('install with', sys.prefix)"`
	})
	got, log := h.deployLog(t, site, []zipEntry{
		{name: "requirements.txt", body: ""},
		{name: "app.py", body: "print('hi')"},
		{name: ".venv/pyvenv.cfg", body: "home = C:\\Users\\dev\\Python312\n"}, // made on a developer's computer
	})
	if got.Status != "succeeded" {
		t.Fatalf("status %s (%s)\n%s", got.Status, got.Message, log)
	}
	venv := filepath.Join(got.ReleaseDir, ".venv")
	if !exists(filepath.Join(venv, "pyvenv.cfg")) || strings.Contains(readFile(t, filepath.Join(venv, "pyvenv.cfg")), `C:\Users\dev`) {
		t.Fatalf("the release's virtual environment was not (re)created:\n%s", log)
	}
	if !strings.Contains(log, "replacing the virtual environment that came with the release") {
		t.Errorf("the shipped environment was not replaced:\n%s", log)
	}
	realVenv, _ := filepath.EvalSymlinks(venv)
	if !strings.Contains(log, "install with "+venv) && !strings.Contains(log, "install with "+realVenv) {
		t.Errorf("the install command did not run in the virtual environment:\n%s", log)
	}
}

// TestDeployPythonNothingToInstall: without requirements there is no
// environment to make and nothing to install.
func TestDeployPythonNothingToInstall(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.d.opts.ResolveRuntime = func(rt, version string) (procmgr.RuntimeExe, error) {
		return procmgr.RuntimeExe{Version: "3", Exe: filepath.Join(h.root, "python")}, nil
	}
	site := h.runtimeSite(t, "pyplain", model.RuntimePython, func(s *model.Site) { s.Node.Script = "app.py" })
	if site.Deploy.InstallCommand != "python -m pip install -r requirements.txt" {
		t.Fatalf("default install command %q", site.Deploy.InstallCommand)
	}
	got, log := h.deployLog(t, site, []zipEntry{{name: "app.py", body: "print('hi')"}})
	if got.Status != "succeeded" || !strings.Contains(log, "no requirements.txt or pyproject.toml or setup.py, skipping install") {
		t.Fatalf("status %s (%s)\n%s", got.Status, got.Message, log)
	}
	if exists(filepath.Join(got.ReleaseDir, ".venv")) {
		t.Error("an environment was made with nothing to install")
	}
}

// TestDeployPythonDefaultNeedsRequirements: the default install command
// reads requirements.txt, so a pyproject-only project skips it rather than
// failing on a file it does not have.
func TestDeployPythonDefaultNeedsRequirements(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.d.opts.ResolveRuntime = func(rt, version string) (procmgr.RuntimeExe, error) {
		return procmgr.RuntimeExe{}, errors.New("no python in this test")
	}
	site := h.runtimeSite(t, "pyproject", model.RuntimePython, func(s *model.Site) { s.Node.Script = "app.py" })
	dir := t.TempDir()
	if msg := installSkipMessage(site, dir); msg != "no requirements.txt or pyproject.toml or setup.py, skipping install" {
		t.Fatalf("empty release: %q", msg)
	}
	for name, want := range map[string]string{"pyproject.toml": "no requirements.txt, skipping install", "requirements.txt": ""} {
		d := t.TempDir()
		if err := os.WriteFile(filepath.Join(d, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if got := installSkipMessage(site, d); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	site.Deploy.InstallCommand = "python -m pip install ."
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "pyproject.toml"), nil, 0o644)
	if got := installSkipMessage(site, d); got != "" {
		t.Errorf("custom command skipped: %q", got)
	}
}
