package procmgr

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
)

func TestRuntimeArgs(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator)+"srv", "app")
	py := &model.PythonConfig{}
	cases := []struct {
		name    string
		rt      string
		rtArgs  []string
		exe     string
		en      entry
		port    int
		agent   bool
		wantExe string
		want    []string
		wantDir string
	}{
		{name: "bun script with agent", rt: model.RuntimeBun, rtArgs: []string{"--smol"}, exe: "bun", agent: true,
			en: entry{script: "index.ts", args: []string{"--x"}}, wantExe: "bun", want: []string{"--smol", "--require", "/agent.js", "index.ts", "--x"}},
		{name: "bun package script", rt: model.RuntimeBun, exe: "bun", en: entry{pkgScript: "start", args: []string{"a"}},
			wantExe: "bun", want: []string{"run", "start", "a"}},
		{name: "deno run", rt: model.RuntimeDeno, rtArgs: []string{"--allow-net", "--allow-env"}, exe: "deno", en: entry{script: "main.ts"},
			wantExe: "deno", want: []string{"run", "--allow-net", "--allow-env", "main.ts"}},
		{name: "deno task ignores permissions", rt: model.RuntimeDeno, rtArgs: []string{"--allow-net"}, exe: "deno", en: entry{pkgScript: "start"},
			wantExe: "deno", want: []string{"task", "start"}},
		{name: "python script", rt: model.RuntimePython, rtArgs: []string{"-X", "dev"}, exe: "python", en: entry{script: "app.py", args: []string{"--v"}, python: py},
			wantExe: "python", want: []string{"-X", "dev", "app.py", "--v"}},
		{name: "python module", rt: model.RuntimePython, exe: "python", en: entry{args: []string{"serve"}, python: &model.PythonConfig{Module: "myapp"}},
			wantExe: "python", want: []string{"-m", "myapp", "serve"}},
		{name: "uvicorn", rt: model.RuntimePython, exe: "python", port: 41000, en: entry{args: []string{"--workers", "1"}, python: &model.PythonConfig{Server: "uvicorn", App: "main:app"}},
			wantExe: "python", want: []string{"-m", "uvicorn", "main:app", "--host", "127.0.0.1", "--port", "41000", "--workers", "1"}},
		{name: "hypercorn", rt: model.RuntimePython, exe: "python", port: 41000, en: entry{python: &model.PythonConfig{Server: "hypercorn", App: "main:app"}},
			wantExe: "python", want: []string{"-m", "hypercorn", "main:app", "--bind", "127.0.0.1:41000"}},
		{name: "waitress takes options first", rt: model.RuntimePython, exe: "python", port: 41000, en: entry{args: []string{"--threads=8"}, python: &model.PythonConfig{Server: "waitress", App: "app:wsgi"}},
			wantExe: "python", want: []string{"-m", "waitress", "--listen=127.0.0.1:41000", "--threads=8", "app:wsgi"}},
		{name: "python task runs its script", rt: model.RuntimePython, exe: "python", en: entry{script: "cleanup.py"},
			wantExe: "python", want: []string{"cleanup.py"}},
		{name: "dotnet dll", rt: model.RuntimeDotnet, rtArgs: []string{"--roll-forward", "Major"}, exe: "/usr/share/dotnet/dotnet", en: entry{script: "publish/Shop.dll", args: []string{"--urls-ignored"}},
			wantExe: "/usr/share/dotnet/dotnet", want: []string{"--roll-forward", "Major", filepath.Join(dir, "publish", "Shop.dll"), "--urls-ignored"}, wantDir: filepath.Join(dir, "publish")},
		{name: "dotnet self-contained exe", rt: model.RuntimeDotnet, en: entry{script: `bin\Shop.exe`},
			wantExe: filepath.Join(dir, "bin", "Shop.exe"), want: nil, wantDir: filepath.Join(dir, "bin")},
		{name: "custom program by path", rt: model.RuntimeCustom, en: entry{script: "bin/server", args: []string{"-p", "1"}},
			wantExe: filepath.Join(dir, "bin", "server"), want: []string{"-p", "1"}},
	}
	for _, c := range cases {
		spec, err := runtimeArgs(c.rt, c.rtArgs, c.exe, dir, c.en, c.port, "/agent.js", c.agent)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		wantDir := c.wantDir
		if wantDir == "" {
			wantDir = dir
		}
		if spec.exe != c.wantExe || !slices.Equal(spec.args, c.want) || spec.dir != wantDir {
			t.Errorf("%s:\n got %s %q in %s\nwant %s %q in %s", c.name, spec.exe, spec.args, spec.dir, c.wantExe, c.want, wantDir)
		}
	}
	// An ASGI server needs a port; a .dll needs the host.
	if _, err := runtimeArgs(model.RuntimePython, nil, "python", dir, entry{python: &model.PythonConfig{Server: "uvicorn", App: "a:b"}}, 0, "", false); err == nil {
		t.Error("a server without a port was accepted")
	}
	if _, err := runtimeArgs(model.RuntimeDotnet, nil, "", dir, entry{script: "Shop.dll"}, 1, "", false); err == nil {
		t.Error("a .dll without the .NET host was accepted")
	}
}

// newRuntimeManager is a process manager whose runtimes other than Node.js
// resolve through resolve (Node.js is not needed).
func newRuntimeManager(t *testing.T, resolve func(rt, version string) (RuntimeExe, error)) (*Manager, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "nh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	app := filepath.Join(dir, "app")
	os.MkdirAll(app, 0o755)
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := New(Options{
		Log: log, Bus: events.New(st, log, testSettings, func(string) string { return "t" }),
		SitesDir: filepath.Join(dir, "sites"), LogsDir: filepath.Join(dir, "logs"), RunDir: filepath.Join(dir, "run"),
		Settings: testSettings, Unseal: func(s string) string { return s }, IsLocationTarget: func(string) bool { return false },
		ResolveNode:    func(string) (NodeRuntime, error) { return NodeRuntime{}, errors.New("no node in this test") },
		ResolveRuntime: resolve,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Shutdown)
	return m, app
}

func envOf(e *env, name string) string { return e.get(name) }

func TestProcessCommandEnvironment(t *testing.T) {
	var asked []string
	m, app := newRuntimeManager(t, func(rt, version string) (RuntimeExe, error) {
		asked = append(asked, rt+"@"+version)
		return RuntimeExe{Version: "9.9.9", Exe: filepath.Join(string(filepath.Separator)+"opt", rt, rt)}, nil
	})
	site := func(n *model.NodeConfig) *model.Site {
		n.AppRoot = app
		s := &model.Site{ID: "s1", Name: "rt", Type: model.SiteNode, Bindings: []model.Binding{{Protocol: "http", Port: 80}}, Node: n}
		s.ApplyDefaults()
		return s
	}

	// .NET: Kestrel is pointed at the instance's port, and a site variable
	// cannot move it elsewhere.
	s := site(&model.NodeConfig{Runtime: model.RuntimeDotnet, Script: "Shop.dll",
		Env: []model.EnvVar{{Name: "ASPNETCORE_URLS", Value: "http://0.0.0.0:80"}, {Name: "ASPNETCORE_ENVIRONMENT", Value: "Staging"}}})
	cmd, e, l, err := m.processCommand(s, app, instanceEntry(s.Node), "tok", 41000)
	if err != nil {
		t.Fatal(err)
	}
	if got := envOf(e, "ASPNETCORE_URLS"); got != "http://127.0.0.1:41000" {
		t.Errorf("ASPNETCORE_URLS = %q", got)
	}
	if envOf(e, "ASPNETCORE_ENVIRONMENT") != "Staging" || envOf(e, "NODEHOSTER_SITE_ID") != "s1" {
		t.Error("site variables were not passed")
	}
	if !l.console || l.runtime != "dotnet" || l.version != "9.9.9" || !strings.HasSuffix(cmd.Args[1], "Shop.dll") {
		t.Errorf("launched %+v, args %q", l, cmd.Args)
	}
	if envOf(e, "NODEHOSTER_AGENT_TOKEN") != "" || envOf(e, "NODE_ENV") != "" {
		t.Error("Node.js variables in a .NET process")
	}
	// A .NET task or worker has no port to be told about.
	if _, e, _, _ = m.processCommand(s, app, taskEntry(model.ScheduledTask{Script: "Tool.dll"}), "tok", 0); envOf(e, "ASPNETCORE_URLS") != "http://0.0.0.0:80" {
		t.Errorf("task ASPNETCORE_URLS = %q, want the site's own", envOf(e, "ASPNETCORE_URLS"))
	}
	// Nor can a secret store move it (a configuration from before
	// validation refused that): store values never replace NodeHoster's,
	// and processCommand leaves variables from stores to setSecretEnv.
	s = site(&model.NodeConfig{Runtime: model.RuntimeDotnet, Script: "Shop.dll", Env: []model.EnvVar{
		{Name: "ASPNETCORE_URLS", From: &model.SecretRef{Store: "vault", Ref: "app#URLS"}},
		{Name: "DB", From: &model.SecretRef{Store: "vault", Ref: "app#DB"}},
	}})
	m.opts.ResolveEnv = func(_ *model.Site, vars []model.EnvVar, _, _ string) (map[string]string, error) {
		out := map[string]string{}
		for _, v := range vars {
			out[v.Name] = "http://0.0.0.0:9999"
		}
		return out, nil
	}
	_, e, _, _ = m.processCommand(s, app, instanceEntry(s.Node), "tok", 41000)
	if _, set := e.vals[fold("DB")]; set {
		t.Error("processCommand set a variable from a secret store")
	}
	if err := m.setSecretEnv(e, s, s.Node.Env, "s1", ""); err != nil {
		t.Fatal(err)
	}
	if envOf(e, "ASPNETCORE_URLS") != "http://127.0.0.1:41000" || envOf(e, "DB") != "http://0.0.0.0:9999" {
		t.Errorf("ASPNETCORE_URLS = %q, DB = %q", envOf(e, "ASPNETCORE_URLS"), envOf(e, "DB"))
	}
	m.opts.ResolveEnv = nil

	// Deno: the module cache is the site's, the one deployments fill.
	s = site(&model.NodeConfig{Runtime: model.RuntimeDeno, RuntimeVersion: "2.1.4", Script: "main.ts"})
	_, e, _, err = m.processCommand(s, app, instanceEntry(s.Node), "tok", 41000)
	if err != nil {
		t.Fatal(err)
	}
	if envOf(e, "DENO_DIR") != model.DenoDir(m.opts.SitesDir, "s1") || envOf(e, "DENO_NO_PROMPT") != "1" {
		t.Errorf("DENO_DIR %q", envOf(e, "DENO_DIR"))
	}
	if !slices.Contains(asked, "deno@2.1.4") {
		t.Errorf("resolved %v", asked)
	}

	// Bun: the agent, with its pipe and token, for an entry script only.
	s = site(&model.NodeConfig{Runtime: model.RuntimeBun, Script: "index.ts", AgentEnabled: true})
	cmd, e, _, _ = m.processCommand(s, app, instanceEntry(s.Node), "tok", 41000)
	if !slices.Contains(cmd.Args, "--require") || envOf(e, "NODEHOSTER_AGENT_TOKEN") != "tok" {
		t.Errorf("bun agent missing: %q", cmd.Args)
	}
	s.Node.Script, s.Node.NpmScript = "", "start"
	cmd, e, _, _ = m.processCommand(s, app, instanceEntry(s.Node), "tok", 41000)
	if slices.Contains(cmd.Args, "--require") || envOf(e, "NODEHOSTER_AGENT_TOKEN") != "" {
		t.Errorf("bun agent in a package script: %q", cmd.Args)
	}

	// Python: unbuffered UTF-8 output; the virtual environment when there
	// is one, with a note in the log when there is not.
	s = site(&model.NodeConfig{Runtime: model.RuntimePython, Script: "app.py"})
	cmd, e, l, _ = m.processCommand(s, app, instanceEntry(s.Node), "tok", 41000)
	if envOf(e, "PYTHONUNBUFFERED") != "1" || envOf(e, "VIRTUAL_ENV") != "" || !strings.Contains(l.note, "no virtual environment") {
		t.Errorf("python without venv: note %q", l.note)
	}
	if cmd.Path != filepath.Join(string(filepath.Separator)+"opt", "python", "python") && !strings.HasSuffix(cmd.Path, filepath.Join("python", "python")) {
		t.Errorf("python exe %q", cmd.Path)
	}
	venvPy := venvPython(filepath.Join(app, ".venv"))
	os.MkdirAll(filepath.Dir(venvPy), 0o755)
	os.WriteFile(venvPy, []byte("#!/bin/sh\n"), 0o755)
	cmd, e, l, _ = m.processCommand(s, app, instanceEntry(s.Node), "tok", 41000)
	if cmd.Path != venvPy || envOf(e, "VIRTUAL_ENV") != filepath.Join(app, ".venv") || l.note != "" || l.version != "9.9.9" {
		t.Errorf("python with venv: %s, VIRTUAL_ENV %q, note %q", cmd.Path, envOf(e, "VIRTUAL_ENV"), l.note)
	}
	if !strings.HasPrefix(envOf(e, "PATH"), filepath.Dir(venvPy)) {
		t.Errorf("venv not first on PATH: %s", envOf(e, "PATH"))
	}

	// A runtime that cannot be resolved does not start.
	m.opts.ResolveRuntime = func(string, string) (RuntimeExe, error) {
		return RuntimeExe{}, errors.New("Bun 1.0.0 is not installed")
	}
	s = site(&model.NodeConfig{Runtime: model.RuntimeBun, Script: "index.ts"})
	if _, _, _, err := m.processCommand(s, app, instanceEntry(s.Node), "tok", 1); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("unresolved runtime: %v", err)
	}
	// ...except a self-contained .NET app, which needs no host.
	s = site(&model.NodeConfig{Runtime: model.RuntimeDotnet, Script: "Shop.exe"})
	if cmd, _, _, err := m.processCommand(s, app, instanceEntry(s.Node), "tok", 1); err != nil || cmd.Path != filepath.Join(app, "Shop.exe") {
		t.Errorf("self-contained .NET app: %v", err)
	}
}

// python3 returns an interpreter for the tests that run Python, skipping
// them without one.
func python3(t *testing.T) (RuntimeExe, string) {
	t.Helper()
	exe, err := exec.LookPath("python3")
	if err != nil {
		if exe, err = exec.LookPath("python"); err != nil {
			t.Skip("python is not installed")
		}
	}
	out, err := exec.Command(exe, "--version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "Python 3") {
		t.Skip("python 3 is not installed")
	}
	return RuntimeExe{Version: strings.TrimSpace(strings.TrimPrefix(string(out), "Python ")), Exe: exe}, exe
}

const pythonApp = `
import http.server, os, signal, sys

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = ("instance=" + os.environ.get("NODEHOSTER_INSTANCE", "") + " venv=" + os.environ.get("VIRTUAL_ENV", "none")).encode()
        self.send_response(200)
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *args):
        pass

def stop(*args):
    print("graceful stop", flush=True)
    sys.exit(0)

signal.signal(signal.SIGTERM, stop)
if hasattr(signal, "SIGBREAK"):  # Windows: NodeHoster sends Ctrl+Break
    signal.signal(signal.SIGBREAK, stop)
server = http.server.HTTPServer(("127.0.0.1", int(os.environ["PORT"])), Handler)
print("python ready", flush=True)
server.serve_forever()
`

// TestPythonSiteLifecycle runs a Python web application under the process
// manager: PORT, readiness, the runtime in the status, a zero-downtime
// recycle and a graceful stop.
func TestPythonSiteLifecycle(t *testing.T) {
	py, _ := python3(t)
	m, app := newRuntimeManager(t, func(rt, _ string) (RuntimeExe, error) {
		if rt != model.RuntimePython {
			return RuntimeExe{}, errors.New("unexpected runtime " + rt)
		}
		return py, nil
	})
	os.WriteFile(filepath.Join(app, "server.py"), []byte(pythonApp), 0o644)
	site := &model.Site{ID: "py", Name: "py", Type: model.SiteNode, Bindings: []model.Binding{{Protocol: "http", Port: 80}},
		Node: &model.NodeConfig{AppRoot: app, Runtime: model.RuntimePython, Script: "server.py", Instances: 2, ShutdownTimeoutSec: 10}}
	site.ApplyDefaults()
	if err := site.Validate(); err != nil {
		t.Fatal(err)
	}
	m.Apply(site)
	if err := m.Start("py"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "2 ready instances", func() bool { return len(m.Backends("py")) == 2 })
	for _, b := range m.Backends("py") {
		if body := get(t, "http://"+b.Addr+"/"); !strings.Contains(body, "venv=none") {
			t.Fatalf("response %q", body)
		}
	}
	st, _ := m.Status("py")
	for _, in := range st.Instances {
		if in.Runtime != model.RuntimePython || in.RuntimeVersion != py.Version || in.Agent || in.HeapUsedBytes != 0 {
			t.Fatalf("instance status %+v", in)
		}
	}
	if !strings.Contains(logText(m, "py"), "python "+py.Version) {
		t.Fatalf("the start line does not name the runtime:\n%s", logText(m, "py"))
	}
	if err := m.Recycle("py", "test"); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop("py"); err != nil {
		t.Fatal(err)
	}
	// Stopped by asking (SIGTERM here, Ctrl+Break on Windows), not killed.
	waitFor(t, "graceful stops", func() bool { return strings.Count(logText(m, "py"), "graceful stop") >= 4 })
	if strings.Contains(logText(m, "py"), "terminating") {
		t.Fatalf("an instance had to be killed:\n%s", logText(m, "py"))
	}
}

// TestPythonVenvAndWorker: a worker running a module with the site's
// virtual environment, and a task in the same environment.
func TestPythonVenvAndWorker(t *testing.T) {
	py, exe := python3(t)
	m, app := newRuntimeManager(t, func(string, string) (RuntimeExe, error) { return py, nil })
	if out, err := exec.Command(exe, "-m", "venv", "--without-pip", filepath.Join(app, "env")).CombinedOutput(); err != nil {
		t.Skipf("python cannot create a virtual environment: %v %s", err, out)
	}
	os.MkdirAll(filepath.Join(app, "jobs"), 0o755)
	os.WriteFile(filepath.Join(app, "jobs", "__init__.py"), nil, 0o644)
	os.WriteFile(filepath.Join(app, "jobs", "consumer.py"), []byte(`
import os, sys, time
print("consumer up, PORT=" + os.environ.get("PORT", "none") + " prefix=" + sys.prefix, flush=True)
while True:
    time.sleep(1)
`), 0o644)
	os.WriteFile(filepath.Join(app, "once.py"), []byte(`
import os, sys
print("task in " + os.environ.get("VIRTUAL_ENV", "none"))
sys.exit(3)
`), 0o644)
	site := &model.Site{ID: "w", Name: "w", Type: model.SiteWorker,
		Node: &model.NodeConfig{AppRoot: app, Runtime: model.RuntimePython, Python: &model.PythonConfig{Module: "jobs.consumer", Venv: "env"}}}
	site.ApplyDefaults()
	if err := site.Validate(); err != nil {
		t.Fatal(err)
	}
	m.Apply(site)
	if err := m.Start("w"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool { s, _ := m.Status("w"); return s.State == model.StateRunning })
	waitFor(t, "output", func() bool {
		real, _ := filepath.EvalSymlinks(filepath.Join(app, "env"))
		text := logText(m, "w")
		return strings.Contains(text, "consumer up, PORT=none prefix="+real) || strings.Contains(text, "consumer up, PORT=none prefix="+filepath.Join(app, "env"))
	})

	var out bytes.Buffer
	p, err := m.StartTask(site, model.ScheduledTask{Name: "once", Script: "once.py"}, "run1", &out)
	if err != nil {
		t.Fatal(err)
	}
	if code := p.ExitCode(); code != 3 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.Contains(out.String(), "task in "+filepath.Join(app, "env")) {
		t.Fatalf("task output %q", out.String())
	}
}

// TestCustomCommand runs a program the site names, here the Python
// interpreter with a script as its argument.
func TestCustomCommand(t *testing.T) {
	_, exe := python3(t)
	m, app := newRuntimeManager(t, nil)
	os.WriteFile(filepath.Join(app, "server.py"), []byte(pythonApp), 0o644)
	site := &model.Site{ID: "c", Name: "c", Type: model.SiteNode, Bindings: []model.Binding{{Protocol: "http", Port: 80}},
		Node: &model.NodeConfig{AppRoot: app, Runtime: model.RuntimeCustom, Script: exe, Args: []string{"server.py"}}}
	site.ApplyDefaults()
	m.Apply(site)
	if err := m.Start("c"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ready", func() bool { return len(m.Backends("c")) == 1 })
	st, _ := m.Status("c")
	if st.Instances[0].Runtime != model.RuntimeCustom {
		t.Fatalf("runtime %q", st.Instances[0].Runtime)
	}
	m.Stop("c")
}

// TestBunAgent: under Bun the agent reports (heap, and it is what stops
// the process), without claiming a Node.js version.
func TestBunAgent(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed")
	}
	m, app := newRuntimeManager(t, func(string, string) (RuntimeExe, error) { return RuntimeExe{Version: "test", Exe: bun}, nil })
	os.WriteFile(filepath.Join(app, "server.js"), []byte(testApp), 0o644)
	site := &model.Site{ID: "b", Name: "b", Type: model.SiteNode, Bindings: []model.Binding{{Protocol: "http", Port: 80}},
		Node: &model.NodeConfig{AppRoot: app, Runtime: model.RuntimeBun, Script: "server.js", AgentEnabled: true}}
	site.ApplyDefaults()
	m.Apply(site)
	if err := m.Start("b"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ready", func() bool { return len(m.Backends("b")) == 1 })
	waitFor(t, "agent", func() bool { s, _ := m.Status("b"); return s.Instances[0].Agent && s.Instances[0].HeapUsedBytes > 0 })
	st, _ := m.Status("b")
	if st.Instances[0].NodeVersion != "" || st.Instances[0].Runtime != model.RuntimeBun {
		t.Fatalf("status %+v", st.Instances[0])
	}
	pid := st.Instances[0].PID
	started := time.Now()
	m.Stop("b")
	waitFor(t, "exit", func() bool { return !processAlive(pid) })
	if d := time.Since(started); d > 10*time.Second {
		t.Fatalf("stopping took %s: the agent did not stop it", d)
	}
}

// TestRuntimeTaskRejectsUnresolved: a task of a site whose runtime is not
// installed fails to start with the resolver's message.
func TestRuntimeTaskRejectsUnresolved(t *testing.T) {
	m, app := newRuntimeManager(t, func(string, string) (RuntimeExe, error) {
		return RuntimeExe{}, errors.New("Deno 2.0.0 is not installed; install it on the Runtimes page")
	})
	site := &model.Site{ID: "d", Name: "d", Type: model.SiteWorker, Node: &model.NodeConfig{AppRoot: app, Runtime: model.RuntimeDeno, Script: "main.ts"}}
	site.ApplyDefaults()
	_, err := m.StartTask(site, model.ScheduledTask{Name: "x", Script: "x.ts"}, "r", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "Runtimes page") {
		t.Fatalf("got %v", err)
	}
}
