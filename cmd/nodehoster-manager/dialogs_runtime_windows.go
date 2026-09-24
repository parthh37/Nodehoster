package main

import (
	"slices"
	"strings"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// runtimeFields edit what runs a node or worker site's processes in the
// Basic settings dialog: the runtime, its version (or interpreter), the
// entry and, for Python, a module or an ASGI/WSGI server. Fields that do
// not apply to the chosen runtime are hidden.
type runtimeFields struct {
	runtime                  *walk.ComboBox
	entryLabel, npmLabel     *walk.Label
	entry, npm, version      *walk.LineEdit
	versionLabel             *walk.Label
	moduleLabel, serverLabel *walk.Label
	appLabel                 *walk.Label
	module, app              *walk.LineEdit
	server                   *walk.ComboBox
}

var pythonServers = []string{"(none)", model.PythonUvicorn, model.PythonHypercorn, model.PythonWaitress}

// runtimeLayout is what the fields show for a runtime (and, for Python,
// whether a server is chosen).
type runtimeLayout struct {
	entryLabel, entryCue     string
	npmLabel                 string
	pkg, python, app         bool
	versioned                bool
	versionLabel, versionCue string
}

func layoutFor(rt string, server bool) runtimeLayout {
	l := runtimeLayout{pkg: model.PackageScripts(rt), python: rt == model.RuntimePython, versioned: rt != model.RuntimeCustom}
	l.entryLabel, l.entryCue = desktop.EntryHint(rt)
	l.app = l.python && server
	switch rt {
	case model.RuntimeDeno:
		l.npmLabel = "or deno task:"
	case model.RuntimeBun:
		l.npmLabel = "or package.json script:"
	default:
		l.npmLabel = "or npm script:"
	}
	switch rt {
	case model.RuntimeNode:
		l.versionLabel, l.versionCue = "Node.js version:", "server default, e.g. 22.11.0"
	case model.RuntimeBun, model.RuntimeDeno:
		l.versionLabel, l.versionCue = model.RuntimeLabel(rt)+" version:", "server default, e.g. 1.1.30"
	case model.RuntimePython:
		l.versionLabel, l.versionCue = "Python:", `server default, 3.12 or C:\Python312\python.exe`
	case model.RuntimeDotnet:
		l.versionLabel, l.versionCue = "dotnet.exe:", "found automatically"
	default:
		l.versionLabel = "Version:"
	}
	return l
}

// widgets are the grid rows (label, field) for the site's node config.
func (f *runtimeFields) widgets(n *model.NodeConfig) []Widget {
	rt := n.RuntimeName()
	version := n.RuntimeVersion
	if rt == model.RuntimeNode {
		version = n.NodeVersion
	}
	py := n.Python
	if py == nil {
		py = &model.PythonConfig{}
	}
	l := layoutFor(rt, py.Server != "")
	whenCreated(f.update) // Visible below cannot hide while the dialog is built
	return []Widget{
		Label{Text: "Runtime:"}, ComboBox{AssignTo: &f.runtime, Model: desktop.RuntimeOptions(),
			CurrentIndex: max(slices.Index(model.Runtimes, rt), 0), OnCurrentIndexChanged: f.update},
		Label{AssignTo: &f.entryLabel, Text: l.entryLabel}, LineEdit{AssignTo: &f.entry, Text: n.Script, CueBanner: l.entryCue},
		Label{AssignTo: &f.npmLabel, Text: l.npmLabel, Visible: l.pkg}, LineEdit{AssignTo: &f.npm, Text: n.NpmScript, CueBanner: "start", Visible: l.pkg},
		Label{AssignTo: &f.moduleLabel, Text: "or Python module:", Visible: l.python},
		LineEdit{AssignTo: &f.module, Text: py.Module, CueBanner: "myapp.worker", Visible: l.python},
		Label{AssignTo: &f.serverLabel, Text: "or server:", Visible: l.python}, ComboBox{AssignTo: &f.server, Model: pythonServers,
			CurrentIndex: max(slices.Index(pythonServers, py.Server), 0), OnCurrentIndexChanged: f.update, Visible: l.python},
		Label{AssignTo: &f.appLabel, Text: "Application:", Visible: l.app}, LineEdit{AssignTo: &f.app, Text: py.App, CueBanner: "main:app", Visible: l.app},
		Label{AssignTo: &f.versionLabel, Text: l.versionLabel, Visible: l.versioned},
		LineEdit{AssignTo: &f.version, Text: version, CueBanner: l.versionCue, Visible: l.versioned},
	}
}

func (f *runtimeFields) selected() string {
	return model.Runtimes[max(f.runtime.CurrentIndex(), 0)]
}

// update shows the fields of the selected runtime.
func (f *runtimeFields) update() {
	if f.version == nil || f.runtime == nil || f.server == nil { // still being created
		return
	}
	l := layoutFor(f.selected(), f.server.CurrentIndex() > 0)
	f.entryLabel.SetText(l.entryLabel)
	f.entry.SetCueBanner(l.entryCue)
	f.npmLabel.SetText(l.npmLabel)
	for _, w := range []walk.Widget{f.npmLabel, f.npm} {
		setVisible(w, l.pkg)
	}
	for _, w := range []walk.Widget{f.moduleLabel, f.module, f.serverLabel, f.server} {
		setVisible(w, l.python)
	}
	setVisible(f.appLabel, l.app)
	setVisible(f.app, l.app)
	f.versionLabel.SetText(l.versionLabel)
	f.version.SetCueBanner(l.versionCue)
	setVisible(f.versionLabel, l.versioned)
	setVisible(f.version, l.versioned)
}

// apply writes the fields into the site, switching its runtime the way the
// web console does (defaults that followed the old runtime follow the new
// one). It returns a message when something required is missing.
func (f *runtimeFields) apply(s *model.Site) string {
	rt := f.selected()
	s.SetRuntime(rt)
	n := s.Node
	n.Script, n.NpmScript = strings.TrimSpace(f.entry.Text()), ""
	if model.PackageScripts(rt) {
		n.NpmScript = strings.TrimSpace(f.npm.Text())
	}
	version := strings.TrimSpace(f.version.Text())
	switch rt {
	case model.RuntimeNode:
		n.NodeVersion = version
	case model.RuntimeCustom:
	default:
		n.RuntimeVersion = version
	}
	if rt == model.RuntimePython {
		if n.Python == nil {
			n.Python = &model.PythonConfig{}
		}
		n.Python.Module, n.Python.Server, n.Python.App = strings.TrimSpace(f.module.Text()), "", ""
		if i := f.server.CurrentIndex(); i > 0 {
			n.Python.Server, n.Python.App = pythonServers[i], strings.TrimSpace(f.app.Text())
		}
		if n.Script == "" && n.Python.Module == "" && n.Python.Server == "" {
			return "Enter a Python script, a module, or choose a server."
		}
		return ""
	}
	if n.Script == "" && n.NpmScript == "" {
		label, _ := desktop.EntryHint(rt)
		return "Enter the " + strings.ToLower(strings.TrimSuffix(label, ":")) + "."
	}
	return ""
}
