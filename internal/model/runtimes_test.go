package model

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// TestRuntimeOldDocument: a site saved before runtimes existed decodes
// without one and stays a Node.js site with Node.js' defaults.
func TestRuntimeOldDocument(t *testing.T) {
	var s Site
	old := `{"id":"a","name":"app","type":"node","bindings":[],"node":{"appRoot":"C:\\apps\\app","script":"server.js","instances":1},"deploy":{}}`
	if err := json.Unmarshal([]byte(old), &s); err != nil {
		t.Fatal(err)
	}
	if s.Node.RuntimeName() != RuntimeNode {
		t.Fatalf("runtime %q", s.Node.RuntimeName())
	}
	s.ApplyDefaults()
	if s.Node.Runtime != RuntimeNode || s.Node.Python != nil || s.Deploy.InstallCommand != "npm ci --omit=dev" {
		t.Fatalf("defaults: runtime %q python %v install %q", s.Node.Runtime, s.Node.Python, s.Deploy.InstallCommand)
	}
	if !slices.Equal(s.Node.WatchIgnore, []string{"node_modules", ".git", "logs"}) {
		t.Fatalf("watch ignore %v", s.Node.WatchIgnore)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if !s.Node.Agent("") && s.Node.AgentEnabled {
		t.Fatal("agent refused for node")
	}
}

func runtimeSite(rt string, edit func(n *NodeConfig)) *Site {
	s := &Site{ID: "a", Name: "app", Type: SiteNode,
		Bindings: []Binding{{Protocol: "http", Host: "example.com"}},
		Node:     &NodeConfig{AppRoot: `C:\apps\app`, Runtime: rt}}
	if edit != nil {
		edit(s.Node)
	}
	s.ApplyDefaults()
	return s
}

func TestRuntimeDefaults(t *testing.T) {
	for rt, want := range map[string]string{
		RuntimeBun: "bun install --production", RuntimeDeno: "deno install",
		RuntimePython: "python -m pip install -r requirements.txt", RuntimeDotnet: "", RuntimeCustom: "",
	} {
		s := runtimeSite(rt, func(n *NodeConfig) { n.Script = "x" })
		if s.Deploy.InstallCommand != want {
			t.Errorf("%s: install %q, want %q", rt, s.Deploy.InstallCommand, want)
		}
	}
	s := runtimeSite(" Python ", func(n *NodeConfig) { n.Script = "app.py" })
	if s.Node.Runtime != RuntimePython || s.Node.Python == nil || s.Node.Python.Venv != DefaultVenv {
		t.Fatalf("python defaults: %q %+v", s.Node.Runtime, s.Node.Python)
	}
	if !slices.Contains(s.Node.WatchIgnore, ".venv") {
		t.Fatalf("python watch ignore %v", s.Node.WatchIgnore)
	}
	// Switching away drops what only the old runtime used.
	s.Node.Runtime, s.Node.RuntimeVersion = RuntimeNode, "3.12"
	s.ApplyDefaults()
	if s.Node.Python != nil || s.Node.RuntimeVersion != "" {
		t.Fatalf("stale python settings kept: %+v %q", s.Node.Python, s.Node.RuntimeVersion)
	}
	b := runtimeSite(RuntimeBun, func(n *NodeConfig) { n.Script = "index.ts"; n.RuntimeVersion = "v1.2.3" })
	if b.Node.RuntimeVersion != "1.2.3" {
		t.Fatalf("bun version %q", b.Node.RuntimeVersion)
	}
}

func TestRuntimeValidation(t *testing.T) {
	ok := map[string]func(*NodeConfig){
		"bun script":    func(n *NodeConfig) { n.Runtime = RuntimeBun; n.Script = "index.ts"; n.RuntimeVersion = "1.1.30" },
		"bun package":   func(n *NodeConfig) { n.Runtime = RuntimeBun; n.NpmScript = "start" },
		"deno task":     func(n *NodeConfig) { n.Runtime = RuntimeDeno; n.NpmScript = "start" },
		"python script": func(n *NodeConfig) { n.Runtime = RuntimePython; n.Script = "app.py"; n.RuntimeVersion = "3.12" },
		"python module": func(n *NodeConfig) { n.Runtime = RuntimePython; n.Python = &PythonConfig{Module: "myapp.server"} },
		"python uvicorn": func(n *NodeConfig) {
			n.Runtime = RuntimePython
			n.Python = &PythonConfig{Server: "uvicorn", App: "main:app"}
		},
		"python path": func(n *NodeConfig) {
			n.Runtime = RuntimePython
			n.Script = "app.py"
			n.RuntimeVersion = `C:\Python312\python.exe`
		},
		"waitress": func(n *NodeConfig) {
			n.Runtime = RuntimePython
			n.Python = &PythonConfig{Server: "Waitress", App: "app.wsgi:application"}
		},
		"dotnet dll": func(n *NodeConfig) { n.Runtime = RuntimeDotnet; n.Script = `publish\Shop.dll` },
		"dotnet exe": func(n *NodeConfig) {
			n.Runtime = RuntimeDotnet
			n.Script = "Shop.exe"
			n.RuntimeVersion = `C:\Program Files\dotnet\dotnet.exe`
		},
		"custom": func(n *NodeConfig) { n.Runtime = RuntimeCustom; n.Script = `C:\php\php-cgi.exe` },
	}
	for name, edit := range ok {
		if err := runtimeSite("", edit).Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string]struct {
		field string
		edit  func(*NodeConfig)
	}{
		"unknown":        {"node.runtime", func(n *NodeConfig) { n.Runtime = "ruby"; n.Script = "x" }},
		"bun version":    {"node.runtimeVersion", func(n *NodeConfig) { n.Runtime = RuntimeBun; n.Script = "x"; n.RuntimeVersion = "latest" }},
		"python version": {"node.runtimeVersion", func(n *NodeConfig) { n.Runtime = RuntimePython; n.Script = "x"; n.RuntimeVersion = "python3" }},
		"dotnet host":    {"node.runtimeVersion", func(n *NodeConfig) { n.Runtime = RuntimeDotnet; n.Script = "x.dll"; n.RuntimeVersion = "8.0" }},
		"python nothing": {"node.script", func(n *NodeConfig) { n.Runtime = RuntimePython }},
		"python two": {"node.script", func(n *NodeConfig) {
			n.Runtime = RuntimePython
			n.Script = "a.py"
			n.Python = &PythonConfig{Module: "a"}
		}},
		"python npm": {"node.npmScript", func(n *NodeConfig) { n.Runtime = RuntimePython; n.NpmScript = "start" }},
		"python server": {"node.python.server", func(n *NodeConfig) {
			n.Runtime = RuntimePython
			n.Python = &PythonConfig{Server: "gunicorn", App: "a:b"}
		}},
		"python app": {"node.python.app", func(n *NodeConfig) {
			n.Runtime = RuntimePython
			n.Python = &PythonConfig{Server: "uvicorn", App: "main"}
		}},
		"python module name": {"node.python.module", func(n *NodeConfig) { n.Runtime = RuntimePython; n.Python = &PythonConfig{Module: "my-app"} }},
		"python venv is the app": {"node.python.venv", func(n *NodeConfig) {
			n.Runtime = RuntimePython
			n.Script = "a.py"
			n.Python = &PythonConfig{Venv: "./"}
		}},
		"python venv": {"node.python.venv", func(n *NodeConfig) {
			n.Runtime = RuntimePython
			n.Script = "a.py"
			n.Python = &PythonConfig{Venv: `..\shared`}
		}},
		"dotnet empty": {"node.script", func(n *NodeConfig) { n.Runtime = RuntimeDotnet }},
		"custom empty": {"node.script", func(n *NodeConfig) { n.Runtime = RuntimeCustom }},
	}
	for name, c := range bad {
		err := runtimeSite("", c.edit).Validate()
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != c.field {
			t.Errorf("%s: got %v, want an error on %s", name, err, c.field)
		}
	}
}

func TestRuntimeWorkerAndTasks(t *testing.T) {
	w := &Site{ID: "w", Name: "worker", Type: SiteWorker,
		Node: &NodeConfig{AppRoot: `C:\apps\w`, Runtime: RuntimePython, Python: &PythonConfig{Server: "uvicorn", App: "main:app"}}}
	w.ApplyDefaults()
	var ve *ValidationError
	if err := w.Validate(); !errors.As(err, &ve) || ve.Field != "node.python.server" {
		t.Fatalf("worker with a server: %v", err)
	}
	w.Node.Python = &PythonConfig{Module: "worker"}
	w.Tasks = []ScheduledTask{{Name: "cleanup", Script: "cleanup.py"}}
	w.ApplyDefaults()
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	w.Tasks = []ScheduledTask{{Name: "cleanup", NpmScript: "cleanup"}}
	w.ApplyDefaults()
	if err := w.Validate(); !errors.As(err, &ve) || ve.Field != "tasks[0].npmScript" {
		t.Fatalf("python task with an npm script: %v", err)
	}
}

func TestRuntimeAgent(t *testing.T) {
	n := &NodeConfig{AgentEnabled: true}
	cases := []struct {
		rt, pkg string
		want    bool
	}{
		{"", "", true}, {RuntimeNode, "start", true}, {RuntimeBun, "", true}, {RuntimeBun, "start", false},
		{RuntimeDeno, "", false}, {RuntimePython, "", false}, {RuntimeDotnet, "", false}, {RuntimeCustom, "", false},
	}
	for _, c := range cases {
		n.Runtime = c.rt
		if got := n.Agent(c.pkg); got != c.want {
			t.Errorf("%q %q: agent %v", c.rt, c.pkg, got)
		}
	}
	n.AgentEnabled = false
	n.Runtime = RuntimeNode
	if n.Agent("") {
		t.Error("agent while disabled")
	}
}

func TestRuntimeDefaultsSettings(t *testing.T) {
	d := RuntimeDefaults{Bun: "1.1.30", Deno: "2.1.4", Python: "3.12", Dotnet: `C:\Program Files\dotnet\dotnet.exe`}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if d.Default(RuntimePython) != "3.12" || d.Default(RuntimeCustom) != "" {
		t.Fatal("Default")
	}
	d.Deno = "canary"
	var ve *ValidationError
	if err := d.Validate(); !errors.As(err, &ve) || ve.Field != "runtimes.deno" {
		t.Fatalf("got %v", err)
	}
}

func TestSetRuntime(t *testing.T) {
	s := runtimeSite(RuntimeNode, func(n *NodeConfig) { n.NpmScript = "start"; n.NodeVersion = "22.11.0" })
	s.SetRuntime(RuntimePython)
	if s.Node.Runtime != RuntimePython || s.Node.NpmScript != "" || s.Node.Python == nil ||
		s.Deploy.InstallCommand != "python -m pip install -r requirements.txt" || !slices.Contains(s.Node.WatchIgnore, ".venv") {
		t.Fatalf("to python: %+v %+v", s.Node, s.Deploy)
	}
	// A changed install command is the administrator's and stays.
	s.Deploy.InstallCommand = "pip install -r requirements/prod.txt"
	s.Node.RuntimeVersion = "3.12"
	s.SetRuntime(RuntimeDotnet)
	if s.Deploy.InstallCommand != "pip install -r requirements/prod.txt" || s.Node.RuntimeVersion != "" || s.Node.Python != nil {
		t.Fatalf("to dotnet: %+v %+v", s.Node, s.Deploy)
	}
	d := runtimeSite(RuntimeDotnet, func(n *NodeConfig) { n.Script = "Shop.dll" })
	d.SetRuntime(RuntimeBun)
	if d.Deploy.InstallCommand != "bun install --production" {
		t.Fatalf("dotnet to bun: %q", d.Deploy.InstallCommand)
	}
	// The old runtime's arguments would make the new one refuse to start;
	// Deno starts with what a web app needs.
	d.Node.NodeArgs = []string{"--smol"}
	d.SetRuntime(RuntimeDeno)
	if !slices.Equal(d.Node.NodeArgs, DenoWebPermissions) {
		t.Fatalf("deno arguments %v", d.Node.NodeArgs)
	}
	d.SetRuntime(RuntimePython)
	if len(d.Node.NodeArgs) != 0 {
		t.Fatalf("python arguments %v", d.Node.NodeArgs)
	}
	// Package-script tasks cannot run under Python: they lose the script
	// and must get one before the site saves.
	n := runtimeSite(RuntimeNode, func(n *NodeConfig) { n.Script = "server.js" })
	n.Tasks = []ScheduledTask{{Name: "report", NpmScript: "report"}}
	n.SetRuntime(RuntimePython)
	n.ApplyDefaults()
	var ve *ValidationError
	if n.Tasks[0].NpmScript != "" || !errors.As(n.Validate(), &ve) || ve.Field != "tasks[0].script" {
		t.Fatalf("tasks after the switch: %+v %v", n.Tasks, n.Validate())
	}
}

func TestRuntimeDefaultsNormalized(t *testing.T) {
	d := RuntimeDefaults{Bun: " v1.2.3 ", Deno: "v2.0.0", Python: " 3.12 "}
	d.ApplyDefaults()
	if d.Bun != "1.2.3" || d.Deno != "2.0.0" || d.Python != "3.12" {
		t.Fatalf("%+v", d)
	}
}

func TestStartText(t *testing.T) {
	for want, n := range map[string]*NodeConfig{
		"node server.js":             {Script: "server.js"},
		"npm run start":              {NpmScript: "start"},
		"bun run dev":                {Runtime: RuntimeBun, NpmScript: "dev"},
		"bun index.ts":               {Runtime: RuntimeBun, Script: "index.ts"},
		"deno task start":            {Runtime: RuntimeDeno, NpmScript: "start"},
		"deno run main.ts":           {Runtime: RuntimeDeno, Script: "main.ts"},
		"python app.py":              {Runtime: RuntimePython, Script: "app.py", Python: &PythonConfig{}},
		"python -m worker":           {Runtime: RuntimePython, Python: &PythonConfig{Module: "worker"}},
		"python -m uvicorn main:app": {Runtime: RuntimePython, Python: &PythonConfig{Server: "uvicorn", App: "main:app"}},
		"dotnet publish/Shop.dll":    {Runtime: RuntimeDotnet, Script: "publish/Shop.dll"},
		"Shop.exe":                   {Runtime: RuntimeDotnet, Script: "Shop.exe"},
		`C:\php\php-cgi.exe`:         {Runtime: RuntimeCustom, Script: `C:\php\php-cgi.exe`},
	} {
		if got := n.StartText(); got != want {
			t.Errorf("StartText = %q, want %q", got, want)
		}
	}
}
