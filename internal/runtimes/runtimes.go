// Package runtimes provides the runtimes other than Node.js that sites can
// run with. Bun and Deno are installed side by side into the data folder,
// like Node.js versions (nodeversions), from their official GitHub
// releases with the published SHA-256 checked (which catches a corrupt
// download; the checksum comes from the same release, so it is no proof of
// who built it); each site can pin a version. Python and .NET are found
// where they are installed (the py launcher, the registry, PATH, the
// standard folders) and never installed by NodeHoster: they come with
// system-wide installers and updates of their own. NodeHoster runs what it
// finds as SYSTEM, so it only uses (and only ever runs) programs no one but
// administrators can change.
package runtimes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/procmgr"
	"github.com/parthh37/nodehoster/internal/winacl"
)

// Installed is a managed Bun or Deno version (or an install in progress
// or failed), like nodeversions.Installed.
type Installed struct {
	Version   string  `json:"version"`
	Path      string  `json:"path"`
	Status    string  `json:"status"` // installing | installed | error
	Progress  float64 `json:"progress"`
	Error     string  `json:"error,omitempty"`
	IsDefault bool    `json:"isDefault"`
}

// System is a runtime found on PATH.
type System struct {
	Version string `json:"version"`
	Path    string `json:"path"`
}

// Managed is what the server has of Bun or Deno.
type Managed struct {
	System    *System     `json:"system"`
	Installed []Installed `json:"installed"`
}

// Interpreter is a Python installation found on the server.
type Interpreter struct {
	Version   string `json:"version"`
	Path      string `json:"path"`
	Source    string `json:"source"` // py (the py launcher) | registry | path | folder
	IsDefault bool   `json:"isDefault"`
}

// DotnetRuntime is one shared framework `dotnet --list-runtimes` reports.
type DotnetRuntime struct {
	Name    string `json:"name"` // Microsoft.NETCore.App, Microsoft.AspNetCore.App, ...
	Version string `json:"version"`
	Path    string `json:"path"`
}

// Dotnet is the .NET host (dotnet.exe) and the runtimes it can run.
type Dotnet struct {
	Host     string          `json:"host"`
	Runtimes []DotnetRuntime `json:"runtimes"`
}

// Report is everything the Runtimes page shows.
type Report struct {
	Bun      Managed               `json:"bun"`
	Deno     Managed               `json:"deno"`
	Python   []Interpreter         `json:"python"`
	Dotnet   *Dotnet               `json:"dotnet"` // null when .NET is not installed
	Defaults model.RuntimeDefaults `json:"defaults"`
}

// Where to get what NodeHoster does not install.
const (
	PythonDownload = "https://www.python.org/downloads/windows/"
	DotnetDownload = "https://dotnet.microsoft.com/download/dotnet"
)

type Manager struct {
	dir    string // <data>\runtimes: bun\<version>, deno\<version>
	tmp    string
	log    *slog.Logger
	client *http.Client
	api    string // GitHub's API, replaced by tests

	// OnInstalled, optional, is told how each background install ended.
	OnInstalled func(runtime, version string, err error)

	// Seams for tests.
	goos, goarch string
	baseline     func() bool // an x64 CPU without AVX2 needs Bun's baseline build
	lookPath     func(string) (string, error)
	output       func(ctx context.Context, exe string, args ...string) ([]byte, error)
	getenv       func(string) string
	glob         func(string) ([]string, error)
	stat         func(string) error
	// trusted checks that only administrators can change a program found
	// on the server before it is run (winacl.CheckProgram).
	trusted func(exe string, dirs ...string) error
	// registryPythons lists the interpreters registered for all users
	// (HKLM, PEP 514); Windows only.
	registryPythons func() []string

	mu      sync.Mutex
	jobs    map[string]*Installed // runtime/version -> install in progress or failed
	avail   map[string]availCache
	refused map[string]bool // programs not used, already logged

	detectMu sync.Mutex
	detected map[string]*detectEntry // bun, deno, python, dotnet
}

// detectEntry is one kind of detection, run by one caller at a time: a
// slow Python detection does not hold up resolving Bun or .NET.
type detectEntry struct {
	mu   sync.Mutex
	d    detection
	last detection // d, readable under detectMu while a detection runs
}

type availCache struct {
	list []Available
	at   time.Time
}

type detection struct {
	at     time.Time
	system *System
	python []Interpreter
	dotnet *Dotnet
}

// detectTTL is how long a detection is reused: long enough that starting
// many instances runs `python --version` once, short enough that an
// interpreter installed a moment ago shows up.
const detectTTL = time.Minute

func New(dir, tmp string, log *slog.Logger) *Manager {
	return &Manager{
		dir: dir, tmp: tmp, log: log,
		client:   &http.Client{Timeout: 30 * time.Minute},
		api:      "https://api.github.com",
		goos:     runtime.GOOS,
		goarch:   runtime.GOARCH,
		baseline: needsBaseline,
		lookPath: exec.LookPath,
		output:   runOutput,
		getenv:   os.Getenv,
		glob:     filepath.Glob,
		stat: func(p string) error {
			_, err := os.Stat(p)
			return err
		},
		trusted:         winacl.CheckProgram,
		registryPythons: registryPythons,
		jobs:            map[string]*Installed{},
		avail:           map[string]availCache{},
		refused:         map[string]bool{},
		detected:        map[string]*detectEntry{},
	}
}

func runOutput(ctx context.Context, exe string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	hideWindow(cmd)
	return cmd.Output()
}

// Report gathers the state of every runtime, detecting what was not
// detected within the last minute.
func (m *Manager) Report(defaults model.RuntimeDefaults) Report {
	return m.report(defaults, m.System(model.RuntimeBun), m.System(model.RuntimeDeno), m.Python(), m.Dotnet())
}

// CachedReport is Report from what the last detections found, however old,
// without running one: for callers who may not make the server run
// programs (a detection runs every interpreter it finds). What was never
// detected is reported as not found.
func (m *Manager) CachedReport(defaults model.RuntimeDefaults) Report {
	var dotnet *Dotnet
	if d := m.peek(model.RuntimeDotnet).dotnet; d != nil {
		c := *d
		c.Runtimes = append([]DotnetRuntime(nil), d.Runtimes...)
		dotnet = &c
	}
	return m.report(defaults, m.peek(model.RuntimeBun).system, m.peek(model.RuntimeDeno).system,
		append([]Interpreter{}, m.peek(model.RuntimePython).python...), dotnet)
}

func (m *Manager) report(defaults model.RuntimeDefaults, bun, deno *System, python []Interpreter, dotnet *Dotnet) Report {
	r := Report{
		Bun:      Managed{System: bun, Installed: m.List(model.RuntimeBun, defaults.Bun)},
		Deno:     Managed{System: deno, Installed: m.List(model.RuntimeDeno, defaults.Deno)},
		Python:   python,
		Dotnet:   dotnet,
		Defaults: defaults,
	}
	if in, err := pickPython(r.Python, defaults.Python); err == nil {
		for i := range r.Python {
			r.Python[i].IsDefault = r.Python[i].Path == in.Path
		}
	}
	return r
}

// WithoutPaths is the report with every file system path left out (and
// defaults that are paths), for a caller who may know which runtimes the
// server has but not where they are: a user with access to some sites.
func (r Report) WithoutPaths() Report {
	strip := func(m Managed) Managed {
		c := Managed{Installed: make([]Installed, len(m.Installed))}
		if m.System != nil {
			c.System = &System{Version: m.System.Version}
		}
		for i, in := range m.Installed {
			in.Path = ""
			c.Installed[i] = in
		}
		return c
	}
	out := r
	out.Bun, out.Deno = strip(r.Bun), strip(r.Deno)
	out.Python = make([]Interpreter, len(r.Python))
	for i, in := range r.Python {
		in.Path = ""
		out.Python[i] = in
	}
	if r.Dotnet != nil {
		d := &Dotnet{Runtimes: make([]DotnetRuntime, len(r.Dotnet.Runtimes))}
		for i, rt := range r.Dotnet.Runtimes {
			rt.Path = ""
			d.Runtimes[i] = rt
		}
		out.Dotnet = d
	}
	if model.RuntimeVersionIsPath(out.Defaults.Python) {
		out.Defaults.Python = ""
	}
	if model.RuntimeVersionIsPath(out.Defaults.Dotnet) {
		out.Defaults.Dotnet = ""
	}
	return out
}

// Refresh forgets what was detected, so the next look runs the detection
// again (the Runtimes page's Refresh).
func (m *Manager) Refresh() {
	m.detectMu.Lock()
	m.detected = map[string]*detectEntry{}
	m.detectMu.Unlock()
}

// Resolve maps a site's runtime and version (its runtimeVersion, or the
// server default) to the executable that runs it.
func (m *Manager) Resolve(rt, version string) (procmgr.RuntimeExe, error) {
	switch rt {
	case model.RuntimeBun, model.RuntimeDeno:
		return m.resolveManaged(rt, version)
	case model.RuntimePython:
		return m.resolvePython(version)
	case model.RuntimeDotnet:
		return m.resolveDotnet(version)
	}
	return procmgr.RuntimeExe{}, fmt.Errorf("%s has no runtime to resolve", model.RuntimeLabel(rt))
}

func (m *Manager) resolveManaged(rt, version string) (procmgr.RuntimeExe, error) {
	label := model.RuntimeLabel(rt)
	version = strings.TrimPrefix(version, "v")
	if version == "" {
		sys := m.System(rt)
		if sys == nil {
			return procmgr.RuntimeExe{}, fmt.Errorf("no %s version is selected and %s was not found on PATH; install one on the Runtimes page", label, rt)
		}
		return procmgr.RuntimeExe{Version: sys.Version, Exe: sys.Path}, nil
	}
	exe := m.exePath(rt, version)
	if _, err := os.Stat(exe); err != nil {
		return procmgr.RuntimeExe{}, fmt.Errorf("%s %s is not installed; install it on the Runtimes page", label, version)
	}
	return procmgr.RuntimeExe{Version: version, Exe: exe}, nil
}

// resolvePython finds a site's interpreter: the newest detected of a
// version, or the one an administrator set by path. That one is refused,
// like those detection leaves out, when other accounts than administrators
// can change it: the site's deployments run it as SYSTEM.
func (m *Manager) resolvePython(version string) (procmgr.RuntimeExe, error) {
	if model.RuntimeVersionIsPath(version) {
		if m.stat(version) != nil {
			return procmgr.RuntimeExe{}, fmt.Errorf("the Python interpreter %s does not exist", version)
		}
		if err := m.usable(model.RuntimePython, version); err != nil {
			return procmgr.RuntimeExe{}, err
		}
		return procmgr.RuntimeExe{Version: m.pythonVersion(version), Exe: version}, nil
	}
	in, err := pickPython(m.Python(), version)
	if err != nil {
		return procmgr.RuntimeExe{}, err
	}
	return procmgr.RuntimeExe{Version: in.Version, Exe: in.Path}, nil
}

// resolveDotnet finds the .NET host: the one detected, or the one an
// administrator set by path, refused like Python's when other accounts
// than administrators can change it.
func (m *Manager) resolveDotnet(host string) (procmgr.RuntimeExe, error) {
	if host != "" {
		if m.stat(host) != nil {
			return procmgr.RuntimeExe{}, fmt.Errorf("the .NET host %s does not exist", host)
		}
		if err := m.usable(model.RuntimeDotnet, host); err != nil {
			return procmgr.RuntimeExe{}, err
		}
		return procmgr.RuntimeExe{Exe: host, Version: newestNetCore(m.dotnetRuntimes(host))}, nil
	}
	d := m.Dotnet()
	if d == nil {
		return procmgr.RuntimeExe{}, fmt.Errorf(".NET was not found on this server; install the ASP.NET Core Runtime (Hosting Bundle) from %s", DotnetDownload)
	}
	return procmgr.RuntimeExe{Exe: d.Host, Version: newestNetCore(d.Runtimes)}, nil
}

// newestNetCore is the newest Microsoft.NETCore.App, shown as the host's
// version (what an app actually runs on depends on its runtimeconfig).
func newestNetCore(list []DotnetRuntime) string {
	best := ""
	for _, r := range list {
		if r.Name == "Microsoft.NETCore.App" && (best == "" || compareVersions(r.Version, best) > 0) {
			best = r.Version
		}
	}
	return best
}

// System reports the bun or deno found on PATH, if any, when only
// administrators can change it (see usable): one a user installed in their
// profile and added to the machine's PATH is not run as SYSTEM.
func (m *Manager) System(rt string) *System {
	return m.cached(rt, func() detection {
		d := detection{}
		p, err := m.lookPath(rt)
		if err != nil {
			return d
		}
		if !model.RuntimeVersionIsPath(p) {
			p, _ = filepath.Abs(p)
		}
		if m.usable(rt, p) != nil {
			return d
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := m.output(ctx, p, "--version")
		if err != nil {
			return d
		}
		v := parseToolVersion(string(out))
		if v == "" {
			return d
		}
		d.system = &System{Version: v, Path: p}
		return d
	}).system
}

func (m *Manager) cached(key string, detect func() detection) detection {
	m.detectMu.Lock()
	e := m.detected[key]
	if e == nil {
		e = &detectEntry{}
		m.detected[key] = e
	}
	m.detectMu.Unlock()
	// Callers of the same kind wait for one detection instead of each
	// running their own.
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.d.at.IsZero() && time.Since(e.d.at) < detectTTL {
		return e.d
	}
	d := detect()
	d.at = time.Now()
	e.d = d
	m.detectMu.Lock()
	e.last = d
	m.detectMu.Unlock()
	return d
}

// peek is the last detection of a kind, however old, without running one;
// zero when there was none since the last Refresh.
func (m *Manager) peek(key string) detection {
	m.detectMu.Lock()
	defer m.detectMu.Unlock()
	if e := m.detected[key]; e != nil {
		return e.last
	}
	return detection{}
}

var errNotManaged = errors.New("only Bun and Deno are installed by NodeHoster")
