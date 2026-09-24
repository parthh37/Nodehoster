package model

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Runtimes: what runs the processes of a node or worker site. The
// configuration object keeps its historical name, "node", whatever the
// runtime: NodeConfig's supervision settings (instances, restarts,
// recycling, limits, identity) apply to every runtime alike. A site
// without a runtime (every site saved before runtimes existed) is Node.js.
const (
	RuntimeNode   = "node"   // node <script> or npm run <script>
	RuntimeBun    = "bun"    // bun <script> or bun run <script>
	RuntimeDeno   = "deno"   // deno run <script> or deno task <name>
	RuntimePython = "python" // a script, python -m <module>, or an ASGI/WSGI server
	RuntimeDotnet = "dotnet" // dotnet <app.dll> or a self-contained <app.exe> (Kestrel)
	RuntimeCustom = "custom" // any program
)

// Runtimes lists every runtime, in the order the consoles offer them.
var Runtimes = []string{RuntimeNode, RuntimeBun, RuntimeDeno, RuntimePython, RuntimeDotnet, RuntimeCustom}

// Python servers NodeHoster starts for an ASGI or WSGI application,
// telling each to listen on 127.0.0.1 and the instance's port.
const (
	PythonUvicorn   = "uvicorn"   // ASGI
	PythonHypercorn = "hypercorn" // ASGI
	PythonWaitress  = "waitress"  // WSGI; gunicorn does not run on Windows
)

// PythonConfig says how a python site starts. Exactly one of the node
// config's Script, Module or Server (with App) is set.
type PythonConfig struct {
	Module string `json:"module,omitempty"` // python -m <module>
	Server string `json:"server,omitempty"` // uvicorn | hypercorn | waitress
	App    string `json:"app,omitempty"`    // the application for Server, "main:app"
	// Venv is the virtual environment, relative to the application folder;
	// deployments create it in each release and install requirements into
	// it. Its interpreter is used when it exists, the selected one when it
	// does not. "" = ".venv".
	Venv string `json:"venv,omitempty"`
}

// DefaultVenv is where a python site's virtual environment lives.
const DefaultVenv = ".venv"

// RuntimeDefaults are the server's defaults for sites that pin no version
// (Settings.Runtimes), like DefaultNodeVersion for Node.js.
type RuntimeDefaults struct {
	Bun    string `json:"bun,omitempty"`    // installed version; "" = bun on PATH
	Deno   string `json:"deno,omitempty"`   // installed version; "" = deno on PATH
	Python string `json:"python,omitempty"` // "3.12" or a path to python.exe; "" = the newest found
	Dotnet string `json:"dotnet,omitempty"` // path to dotnet.exe; "" = found automatically
}

// Default returns the default version (or interpreter) for a runtime.
func (d RuntimeDefaults) Default(runtime string) string {
	switch runtime {
	case RuntimeBun:
		return d.Bun
	case RuntimeDeno:
		return d.Deno
	case RuntimePython:
		return d.Python
	case RuntimeDotnet:
		return d.Dotnet
	}
	return ""
}

// ApplyDefaults normalizes the defaults as a site's runtimeVersion is:
// trimmed, and Bun and Deno versions without a leading "v".
func (d *RuntimeDefaults) ApplyDefaults() {
	d.Bun = strings.TrimPrefix(strings.TrimSpace(d.Bun), "v")
	d.Deno = strings.TrimPrefix(strings.TrimSpace(d.Deno), "v")
	d.Python = strings.TrimSpace(d.Python)
	d.Dotnet = strings.TrimSpace(d.Dotnet)
}

// Validate checks the defaults the way a site's runtimeVersion is checked.
func (d RuntimeDefaults) Validate() error {
	for _, c := range []struct{ rt, v string }{{RuntimeBun, d.Bun}, {RuntimeDeno, d.Deno}, {RuntimePython, d.Python}, {RuntimeDotnet, d.Dotnet}} {
		if msg := runtimeVersionProblem(c.rt, c.v); msg != "" {
			return verr("runtimes."+c.rt, "%s", msg)
		}
	}
	return nil
}

// RuntimeName is the site's runtime, "node" when none is set.
func (n *NodeConfig) RuntimeName() string {
	if n == nil || n.Runtime == "" {
		return RuntimeNode
	}
	return n.Runtime
}

// Agent reports whether the NodeHoster agent is preloaded into a process
// started with the given package script ("" for an entry script): Node.js
// always when enabled, Bun for entry scripts (Bun does not pass a preload
// on to the processes a package script starts). Other runtimes have none.
func (n *NodeConfig) Agent(pkgScript string) bool {
	if !n.AgentEnabled {
		return false
	}
	switch n.RuntimeName() {
	case RuntimeNode:
		return true
	case RuntimeBun:
		return pkgScript == ""
	}
	return false
}

// PackageScripts reports whether a runtime runs package.json scripts (or
// Deno tasks): what npmScript names.
func PackageScripts(runtime string) bool {
	return runtime == RuntimeNode || runtime == RuntimeBun || runtime == RuntimeDeno
}

// RuntimeLabel names a runtime for display.
func RuntimeLabel(runtime string) string {
	switch runtime {
	case "", RuntimeNode:
		return "Node.js"
	case RuntimeBun:
		return "Bun"
	case RuntimeDeno:
		return "Deno"
	case RuntimePython:
		return "Python"
	case RuntimeDotnet:
		return ".NET"
	case RuntimeCustom:
		return "Custom command"
	}
	return runtime
}

// DefaultInstallCommand is the install command a deployment runs for a
// runtime when the site sets none. Python's runs inside the release's
// virtual environment, which the deployment creates first.
func DefaultInstallCommand(runtime string) string {
	switch runtime {
	case "", RuntimeNode:
		return "npm ci --omit=dev"
	case RuntimeBun:
		return "bun install --production"
	case RuntimeDeno:
		return "deno install"
	case RuntimePython:
		return "python -m pip install -r requirements.txt"
	}
	return "" // .NET publishes in the build command, if at all; custom has no convention
}

// DefaultWatchIgnore are the folders file watching skips for a runtime.
func DefaultWatchIgnore(runtime string) []string {
	switch runtime {
	case RuntimePython:
		return []string{".venv", "__pycache__", ".git", "logs"}
	case RuntimeDotnet, RuntimeCustom:
		return []string{".git", "logs"}
	}
	return []string{"node_modules", ".git", "logs"}
}

// DenoDir is a Deno site's module cache (DENO_DIR), shared by its
// deployments and its processes so modules fetched at install are found
// at run time.
func DenoDir(sitesDir, siteID string) string {
	return filepath.Join(sitesDir, siteID, ".deno-cache")
}

// applyRuntimeDefaults normalizes the runtime settings of a node or worker
// site. It runs before the generic defaults (install command, watch
// ignore) that depend on the runtime.
func (n *NodeConfig) applyRuntimeDefaults() {
	n.Runtime = strings.ToLower(strings.TrimSpace(n.Runtime))
	if n.Runtime == "" {
		n.Runtime = RuntimeNode
	}
	n.RuntimeVersion = strings.TrimSpace(n.RuntimeVersion)
	switch n.Runtime {
	case RuntimeNode, RuntimeCustom:
		n.RuntimeVersion = "" // node pins nodeVersion; custom names its program
	case RuntimeBun, RuntimeDeno:
		n.RuntimeVersion = strings.TrimPrefix(n.RuntimeVersion, "v")
	}
	if n.Runtime == RuntimePython {
		if n.Python == nil {
			n.Python = &PythonConfig{}
		}
		p := n.Python
		p.Module, p.App = strings.TrimSpace(p.Module), strings.TrimSpace(p.App)
		p.Server = strings.ToLower(strings.TrimSpace(p.Server))
		p.Venv = strings.TrimSpace(p.Venv)
		if p.Venv == "" {
			p.Venv = DefaultVenv
		}
	} else {
		n.Python = nil
	}
}

var (
	semverRe     = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	pyVersionRe  = regexp.MustCompile(`^\d+(\.\d+){0,2}$`)
	pyModuleRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	pyAppRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*:[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	pyServers    = []string{PythonUvicorn, PythonHypercorn, PythonWaitress}
	knownRuntime = map[string]bool{}
)

func init() {
	for _, r := range Runtimes {
		knownRuntime[r] = true
	}
}

// runtimeVersionProblem says what is wrong with a runtime version (or
// interpreter) setting, "" when nothing is.
func runtimeVersionProblem(runtime, v string) string {
	if v == "" {
		return ""
	}
	switch runtime {
	case RuntimeBun, RuntimeDeno:
		if !semverRe.MatchString(strings.TrimPrefix(v, "v")) {
			return fmt.Sprintf("%q is not a %s version number (1.2.3)", v, RuntimeLabel(runtime))
		}
	case RuntimePython:
		if !pyVersionRe.MatchString(v) && !isAbsPath(v) {
			return fmt.Sprintf("%q is neither a Python version (3.12) nor the full path of python.exe", v)
		}
	case RuntimeDotnet:
		if !isAbsPath(v) {
			return fmt.Sprintf("%q is not the full path of dotnet.exe", v)
		}
	}
	return ""
}

// DenoWebPermissions are the permissions a Deno web application usually
// needs (the network, its variables, its files): Deno grants nothing
// unless told to, so the consoles start a Deno site with them.
var DenoWebPermissions = []string{"--allow-net", "--allow-env", "--allow-read"}

// SetRuntime switches a node or worker site to another runtime, as the
// consoles do when one is picked. Settings still at the old runtime's
// defaults (the install command, the folders file watching ignores) take
// the new runtime's; ones an administrator changed are kept. The version
// pin, the runtime's own arguments, the Python settings and package
// scripts (the site's and its tasks') belong to the old runtime and are
// dropped; a task left without a script is then refused until it has one.
// A site switched to Deno gets DenoWebPermissions.
func (s *Site) SetRuntime(rt string) {
	n := s.Node
	if n == nil {
		return
	}
	old := n.RuntimeName()
	if old == rt {
		return
	}
	if s.Deploy.InstallCommand == DefaultInstallCommand(old) {
		s.Deploy.InstallCommand = DefaultInstallCommand(rt)
	}
	if slices.Equal(n.WatchIgnore, DefaultWatchIgnore(old)) {
		n.WatchIgnore = DefaultWatchIgnore(rt)
	}
	n.Runtime, n.RuntimeVersion = rt, ""
	if rt != RuntimePython {
		n.Python = nil
	} else if n.Python == nil {
		n.Python = &PythonConfig{Venv: DefaultVenv}
	}
	n.NodeArgs = nil
	if rt == RuntimeDeno {
		n.NodeArgs = slices.Clone(DenoWebPermissions)
	}
	if !PackageScripts(rt) {
		n.NpmScript = ""
		for i := range s.Tasks {
			s.Tasks[i].NpmScript = ""
		}
	}
}

// StartText is the command that starts the site's processes, for display:
// "node server.js", "npm run start", "python -m uvicorn main:app",
// "dotnet Shop.dll". Arguments are left out.
func (n *NodeConfig) StartText() string {
	rt := n.RuntimeName()
	if n.NpmScript != "" {
		switch rt {
		case RuntimeBun:
			return "bun run " + n.NpmScript
		case RuntimeDeno:
			return "deno task " + n.NpmScript
		}
		return "npm run " + n.NpmScript
	}
	switch rt {
	case RuntimeDeno:
		return "deno run " + n.Script
	case RuntimePython:
		if p := n.Python; p != nil && n.Script == "" {
			switch {
			case p.Module != "":
				return "python -m " + p.Module
			case p.Server != "":
				return "python -m " + p.Server + " " + p.App
			}
		}
		return "python " + n.Script
	case RuntimeDotnet:
		if strings.HasSuffix(strings.ToLower(n.Script), ".dll") {
			return "dotnet " + n.Script
		}
		return n.Script
	case RuntimeCustom:
		return n.Script
	}
	return rt + " " + n.Script
}

// RuntimeVersionIsPath reports whether a runtimeVersion names an
// interpreter or host by its full path rather than by a version.
func RuntimeVersionIsPath(v string) bool { return isAbsPath(v) }

// validateRuntime checks what starts a node or worker site's processes and
// the tasks it runs.
func (s *Site) validateRuntime() error {
	n := s.Node
	rt := n.RuntimeName()
	if !knownRuntime[rt] {
		return verr("node.runtime", "unknown runtime %q: use %s", n.Runtime, strings.Join(Runtimes, ", "))
	}
	if msg := runtimeVersionProblem(rt, n.RuntimeVersion); msg != "" {
		return verr("node.runtimeVersion", "%s", msg)
	}
	script := strings.TrimSpace(n.Script)
	if n.NpmScript != "" && !PackageScripts(rt) {
		return verr("node.npmScript", "package scripts run with Node.js, Bun or Deno; set an entry script for %s", RuntimeLabel(rt))
	}
	switch rt {
	case RuntimeNode:
		if script == "" && n.NpmScript == "" {
			return verr("node.script", "set an entry script or an npm script")
		}
	case RuntimeBun:
		if script == "" && n.NpmScript == "" {
			return verr("node.script", "set an entry script or a package.json script")
		}
	case RuntimeDeno:
		if script == "" && n.NpmScript == "" {
			return verr("node.script", "set an entry script or a deno task")
		}
	case RuntimePython:
		if err := s.validatePython(script); err != nil {
			return err
		}
	case RuntimeDotnet:
		if script == "" {
			return verr("node.script", "set the application: its .dll (run by dotnet) or a self-contained .exe")
		}
	case RuntimeCustom:
		if script == "" {
			return verr("node.script", "set the program to run")
		}
	}
	for i, t := range s.Tasks {
		if t.NpmScript != "" && !PackageScripts(rt) {
			return verr(fmt.Sprintf("tasks[%d].npmScript", i), "package scripts run with Node.js, Bun or Deno; set a script for %s", RuntimeLabel(rt))
		}
	}
	return nil
}

func (s *Site) validatePython(script string) error {
	p := s.Node.Python
	if p == nil {
		p = &PythonConfig{}
	}
	set := 0
	for _, v := range []string{script, p.Module, p.Server} {
		if v != "" {
			set++
		}
	}
	switch {
	case set == 0:
		return verr("node.script", "set a script, a module or an ASGI/WSGI server")
	case set > 1:
		return verr("node.script", "set only one of a script, a module or a server")
	}
	if p.Module != "" && !pyModuleRe.MatchString(p.Module) {
		return verr("node.python.module", "%q is not a Python module name", p.Module)
	}
	if p.Server != "" {
		if !slices.Contains(pyServers, p.Server) {
			return verr("node.python.server", "must be %s", strings.Join(pyServers, ", "))
		}
		if !pyAppRe.MatchString(p.App) {
			return verr("node.python.app", "set the application as module:attribute, e.g. main:app")
		}
		if s.Type == SiteWorker {
			return verr("node.python.server", "a background worker does not serve HTTP; run a script or a module")
		}
	}
	// Not the application folder itself: a deployment would make (and on
	// a redeploy, --clear) the environment right in it.
	v := path.Clean(strings.ReplaceAll(p.Venv, `\`, "/"))
	if isAbsPath(p.Venv) || v == "." || v == ".." || strings.HasPrefix(v, "../") {
		return verr("node.python.venv", "must be a folder inside the application, e.g. .venv")
	}
	return nil
}
