package procmgr

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/winacl"
)

// RuntimeExe is a resolved interpreter or host of a runtime other than
// Node.js: bun, deno, python or dotnet.
type RuntimeExe struct {
	Version string
	Exe     string
}

// entry is what a process runs: a site's instance (everything the site
// configures) or a scheduled task (a script or a package script).
type entry struct {
	script    string
	pkgScript string // npm run / bun run / deno task
	args      []string
	python    *model.PythonConfig // module or server; nil for tasks
}

func instanceEntry(n *model.NodeConfig) entry {
	e := entry{args: n.Args}
	if n.NpmScript != "" {
		e.pkgScript = n.NpmScript
	} else {
		e.script = n.Script
	}
	if n.RuntimeName() == model.RuntimePython {
		e.python = n.Python
	}
	return e
}

func taskEntry(t model.ScheduledTask) entry {
	return entry{script: t.Script, pkgScript: t.NpmScript, args: t.Args}
}

// launched describes how a process was started.
type launched struct {
	runtime string
	version string
	// console: the process is asked to stop the way Ctrl+C asks a console
	// program (Ctrl+Break on Windows), because it has no agent to ask.
	console bool
	note    string // worth a line in the site's log, e.g. a missing virtualenv
}

// label is the runtime and version as the log shows them: "node 22.11.0",
// "python 3.12.1".
func (l launched) label() string {
	return strings.TrimSpace(l.runtime + " " + l.version)
}

// processCommand builds the command line and environment of a site's
// process in the site's runtime: an instance (port > 0 when it serves
// HTTP) or a task (port 0). The caller adds its own variables (PORT,
// NODEHOSTER_INSTANCE, ...) to the returned env and sets cmd.Env from it.
func (m *Manager) processCommand(site *model.Site, dir string, en entry, token string, port int) (*exec.Cmd, *env, launched, error) {
	n := site.Node
	rt := n.RuntimeName()
	if rt == model.RuntimeNode {
		nrt, err := m.resolveNode(site)
		if err != nil {
			return nil, nil, launched{}, err
		}
		cmd, e, err := m.nodeCommand(site, nrt, dir, en.script, en.pkgScript, en.args, token)
		return cmd, e, launched{runtime: rt, version: nrt.Version}, err
	}

	var exe RuntimeExe
	var resolveErr error
	if rt != model.RuntimeCustom {
		if m.opts.ResolveRuntime == nil {
			resolveErr = fmt.Errorf("%s is not available on this server", model.RuntimeLabel(rt))
		} else {
			exe, resolveErr = m.opts.ResolveRuntime(rt, n.RuntimeVersion)
		}
	}
	l := launched{runtime: rt, version: exe.Version, console: true}

	venvDir := ""
	if rt == model.RuntimePython {
		venv := model.DefaultVenv
		if n.Python != nil && n.Python.Venv != "" {
			venv = n.Python.Venv
		}
		venvDir = filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(venv, `\`, "/")))
		if py := venvPython(venvDir); fileExists(py) {
			// The environment's interpreter runs even when the one it was
			// made from cannot be found: it only lacks a version to show.
			exe.Exe, resolveErr = py, nil
		} else {
			venvDir = ""
			if resolveErr == nil {
				l.note = fmt.Sprintf("no virtual environment in %s; running with %s", venv, exe.Exe)
			}
		}
	}
	// A self-contained .NET app (an .exe) runs without the host.
	dll := rt == model.RuntimeDotnet && strings.HasSuffix(strings.ToLower(en.script), ".dll")
	if resolveErr != nil && (rt != model.RuntimeDotnet || dll) {
		return nil, nil, launched{}, resolveErr
	}

	agent := n.Agent(en.pkgScript)
	spec, err := runtimeArgs(rt, n.NodeArgs, exe.Exe, dir, en, port, m.agentScript, agent)
	if err != nil {
		return nil, nil, launched{}, err
	}
	cmd := exec.Command(spec.exe, spec.args...)
	cmd.Dir = spec.dir

	e := newEnv(os.Environ())
	if exe.Exe != "" {
		e.prependPath(filepath.Dir(exe.Exe))
	}
	switch rt {
	case model.RuntimeBun:
		e.set("NODE_ENV", "production")
	case model.RuntimeDeno:
		e.set("NODE_ENV", "production")
		e.set("DENO_DIR", model.DenoDir(m.opts.SitesDir, site.ID))
		e.set("DENO_NO_UPDATE_CHECK", "1")
		e.set("DENO_NO_PROMPT", "1")
	case model.RuntimePython:
		e.set("PYTHONUNBUFFERED", "1") // log lines as they are printed, not in 8 KB blocks
		e.set("PYTHONUTF8", "1")       // not the ANSI code page for the log pipes
		if venvDir != "" {
			e.set("VIRTUAL_ENV", venvDir) // its Scripts folder is on PATH: the interpreter's
		}
	case model.RuntimeDotnet:
		e.set("DOTNET_CLI_TELEMETRY_OPTOUT", "1")
		e.set("DOTNET_NOLOGO", "1")
		if exe.Exe != "" {
			// A framework-dependent app.exe finds the runtimes through it
			// when .NET is not where it looks by default.
			e.set("DOTNET_ROOT", filepath.Dir(exe.Exe))
		}
	}
	for _, v := range n.Env {
		if v.From != nil {
			continue // set by the caller: see setSecretEnv
		}
		val := v.Value
		if v.Secret {
			val = m.opts.Unseal(val)
		}
		e.set(v.Name, val)
	}
	e.set("NODEHOSTER_SITE", site.Name)
	e.set("NODEHOSTER_SITE_ID", site.ID)
	if rt == model.RuntimeDotnet && port > 0 {
		// Kestrel listens where ASPNETCORE_URLS says unless the app sets
		// its URLs in code; ASPNETCORE_ENVIRONMENT passes through as set.
		e.set("ASPNETCORE_URLS", "http://127.0.0.1:"+strconv.Itoa(port))
	}
	if agent {
		e.set("NODEHOSTER_AGENT_PIPE", m.agent.path)
		e.set("NODEHOSTER_AGENT_TOKEN", token)
	}
	return cmd, e, l, nil
}

// runSpec is a program, its arguments and the folder it runs in.
type runSpec struct {
	exe  string
	args []string
	dir  string
}

// runtimeArgs builds the command line of a runtime other than Node.js.
// exe is the resolved interpreter or host ("" for custom commands and
// self-contained .NET apps); runtimeArgs are the site's arguments for it.
func runtimeArgs(rt string, runtimeArgs []string, exe, dir string, en entry, port int, agentScript string, agent bool) (runSpec, error) {
	spec := runSpec{exe: exe, dir: dir}
	args := func(a ...string) { spec.args = append(spec.args, a...) }
	switch rt {
	case model.RuntimeBun:
		args(runtimeArgs...)
		if en.pkgScript != "" {
			args("run", en.pkgScript)
		} else {
			if agent {
				args("--require", agentScript)
			}
			args(en.script)
		}
		args(en.args...)
	case model.RuntimeDeno:
		if en.pkgScript != "" {
			// A task runs a command line of its own: permission flags
			// belong in deno.json's task, not here.
			args("task", en.pkgScript)
		} else {
			args("run")
			args(runtimeArgs...)
			args(en.script)
		}
		args(en.args...)
	case model.RuntimePython:
		args(runtimeArgs...)
		p := en.python
		switch {
		case en.script != "":
			args(en.script)
			args(en.args...)
		case p != nil && p.Module != "":
			args("-m", p.Module)
			args(en.args...)
		case p != nil && p.Server != "":
			if port <= 0 {
				return spec, errors.New("an ASGI/WSGI server needs a port; a background worker runs a script or a module")
			}
			listen := "127.0.0.1:" + strconv.Itoa(port)
			switch p.Server {
			case model.PythonUvicorn:
				args("-m", "uvicorn", p.App, "--host", "127.0.0.1", "--port", strconv.Itoa(port))
				args(en.args...)
			case model.PythonHypercorn:
				args("-m", "hypercorn", p.App, "--bind", listen)
				args(en.args...)
			case model.PythonWaitress:
				// waitress-serve takes its options before the application.
				args("-m", "waitress", "--listen="+listen)
				args(en.args...)
				args(p.App)
			default:
				return spec, fmt.Errorf("unknown Python server %q", p.Server)
			}
		default:
			return spec, errors.New("set a script, a module or an ASGI/WSGI server")
		}
	case model.RuntimeDotnet:
		app := programPath(dir, en.script)
		// An ASP.NET Core app finds appsettings.json and wwwroot in its
		// content root, the working folder: the app's own folder, as
		// under IIS.
		spec.dir = filepath.Dir(app)
		if strings.HasSuffix(strings.ToLower(app), ".dll") {
			if exe == "" {
				return spec, errors.New(".NET was not found on this server")
			}
			args(runtimeArgs...)
			args(app)
		} else {
			spec.exe = app
		}
		args(en.args...)
	case model.RuntimeCustom:
		prog, err := findProgram(dir, en.script)
		if err != nil {
			return spec, err
		}
		spec.exe = prog
		args(en.args...)
	default:
		return spec, fmt.Errorf("unknown runtime %q", rt)
	}
	return spec, nil
}

// programPath makes a relative program or file absolute in the
// application folder (exec runs a relative path from NodeHoster's own
// working folder otherwise).
func programPath(dir, p string) string {
	p = filepath.FromSlash(strings.ReplaceAll(p, `\`, "/"))
	if filepath.IsAbs(p) || model.RuntimeVersionIsPath(p) {
		return p
	}
	return filepath.Join(dir, p)
}

// findProgram locates a custom command's program: a path (absolute, or
// relative to the application folder), a program in the application
// folder, or one on PATH.
func findProgram(dir, name string) (string, error) {
	if name == "" {
		return "", errors.New("no program is set")
	}
	if strings.ContainsAny(name, `/\`) || filepath.IsAbs(name) {
		return programPath(dir, name), nil
	}
	candidates := []string{filepath.Join(dir, name)}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		for _, ext := range []string{".exe", ".cmd", ".bat"} {
			candidates = append(candidates, filepath.Join(dir, name+ext))
		}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("the program %q was not found in the application folder or on PATH", name)
	}
	// Found on PATH, not chosen by path: only one that users other than
	// administrators cannot replace.
	if err := winacl.CheckProgram(p); err != nil {
		return "", fmt.Errorf("the program %q found on PATH is not used: %w; give its full path", name, err)
	}
	return p, nil
}

// venvPython is a virtual environment's interpreter.
func venvPython(venvDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venvDir, "Scripts", "python.exe")
	}
	return filepath.Join(venvDir, "bin", "python")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// listenHint says how an application of the site's runtime is expected to
// find its port, for the error when it never listens.
func listenHint(n *model.NodeConfig) string {
	switch n.RuntimeName() {
	case model.RuntimeNode:
		return "the application must listen on process.env.PORT"
	case model.RuntimeBun:
		return "the application must listen on process.env.PORT (Bun.serve does by default)"
	case model.RuntimeDeno:
		return `the application must listen on Deno.env.get("PORT"), which needs --allow-net and --allow-env in its arguments`
	case model.RuntimePython:
		if n.Python != nil && n.Python.Server != "" {
			return n.Python.Server + " was told to listen on it; see the log for why it did not"
		}
		return `the application must listen on os.environ["PORT"]`
	case model.RuntimeDotnet:
		return "Kestrel listens on ASPNETCORE_URLS unless the application sets its own URLs (UseUrls, Kestrel endpoints in appsettings.json)"
	}
	return "the program must listen on the PORT environment variable"
}
