package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/deps"
	"github.com/parthh37/nodehoster/internal/nodeversions"
)

func init() {
	register(
		&Command{Name: "deps", MaxArgs: 0,
			Summary: "Check what sites need besides NodeHoster: Node.js, Git and the runtimes sites use (exit code 1 if one is missing)",
			Setup:   func(*flag.FlagSet) Runner { return depsStatus }},
		&Command{Name: "deps install", Args: "[node] [git] [bun] [deno]", MaxArgs: 4,
			Summary: "Install what is missing (default: node and git); setup runs this",
			Setup:   func(*flag.FlagSet) Runner { return depsInstall }},
	)
}

// Dependency is one line of `nodehoster deps`.
type Dependency struct {
	Name      string `json:"name"` // node | git | bun | deno | python | dotnet
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
	Note      string `json:"note,omitempty"`

	// Optional: a runtime no site uses (Bun, Deno, Python, .NET); it does
	// not count as missing.
	Optional bool `json:"optional,omitempty"`
}

// nodeState is what the service knows about Node.js runtimes.
type nodeState struct {
	Default   string // the server's default version; "" = node on PATH
	System    *nodeversions.System
	Installed []nodeversions.Installed // newest first
}

func (e *Env) nodeState() (nodeState, error) {
	var st nodeState
	var v struct {
		System    *nodeversions.System     `json:"system"`
		Installed []nodeversions.Installed `json:"installed"`
	}
	if _, err := e.get("/api/node/versions", &v); err != nil {
		return st, err
	}
	var s struct {
		DefaultNodeVersion string `json:"defaultNodeVersion"`
	}
	if _, err := e.get("/api/settings", &s); err != nil {
		return st, err
	}
	st.Default, st.System, st.Installed = strings.TrimPrefix(s.DefaultNodeVersion, "v"), v.System, v.Installed
	return st, nil
}

// ready returns the installed runtime with version v, if any.
func (st nodeState) ready(v string) *nodeversions.Installed {
	for i, in := range st.Installed {
		if in.Version == v && in.Status == "installed" {
			return &st.Installed[i]
		}
	}
	return nil
}

func (st nodeState) dependency() Dependency {
	d := Dependency{Name: "node"}
	switch {
	case st.Default != "":
		if in := st.ready(st.Default); in != nil {
			d.Installed, d.Version, d.Path, d.Note = true, in.Version, in.Path, "server default"
		} else {
			d.Version, d.Note = st.Default, "the server default is not installed"
		}
	case st.System != nil:
		d.Installed, d.Version, d.Path, d.Note = true, st.System.Version, st.System.Path, "on the system PATH"
	default:
		d.Note = "no Node.js to run sites with"
	}
	return d
}

func (e *Env) gitDependency() Dependency {
	d := Dependency{Name: "git"}
	p, err := deps.FindGit(e.DataDir)
	if err != nil {
		d.Note = "needed to deploy from git repositories"
		return d
	}
	d.Installed, d.Path = true, p
	ctx, cancel := context.WithTimeout(e.Ctx, 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, p, "--version").Output(); err == nil {
		d.Version = strings.TrimPrefix(strings.TrimSpace(string(out)), "git version ")
	}
	return d
}

func (e *Env) printDependencies(list []Dependency) error {
	if e.JSON {
		return e.printJSON(list)
	}
	names := map[string]string{"node": "Node.js", "git": "Git", "bun": "Bun", "deno": "Deno", "python": "Python", "dotnet": ".NET"}
	var rows [][]string
	for _, d := range list {
		status, version := "missing", "—"
		if d.Installed {
			status = "installed"
		} else if d.Optional {
			status = "not installed"
		}
		if d.Version != "" {
			version = d.Version
		}
		where := d.Path
		if d.Note != "" {
			where = strings.TrimSpace(where + " (" + d.Note + ")")
		}
		rows = append(rows, []string{names[d.Name], status, version, where})
	}
	e.table([]string{"DEPENDENCY", "STATUS", "VERSION", "WHERE"}, rows)
	return nil
}

func missing(list []Dependency) error {
	var names []string
	for _, d := range list {
		if !d.Installed && !d.Optional {
			names = append(names, d.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return fmt.Errorf("missing: %s; install with: nodehoster deps install", strings.Join(names, ", "))
}

func depsStatus(e *Env, _ []string) error {
	st, err := e.nodeState()
	if err != nil {
		return err
	}
	list := []Dependency{st.dependency(), e.gitDependency()}
	rts, err := e.runtimeDependencies()
	if err != nil {
		return err
	}
	list = append(list, rts...)
	if err := e.printDependencies(list); err != nil {
		return err
	}
	return missing(list)
}

func depsInstall(e *Env, args []string) error {
	want := args
	if len(want) == 0 {
		want = []string{"node", "git"}
	}
	for _, w := range want {
		if w != "node" && w != "git" && w != "bun" && w != "deno" {
			return usagef("%q is not a dependency NodeHoster installs: use node, git, bun or deno (Python and .NET come with their own installers)", w)
		}
	}
	logf := func(format string, a ...any) { e.printf(format+"\n", a...) }

	// Each is attempted even when the other fails: Git does not need the
	// service, and a site that is not deployed from git runs without it.
	var list []Dependency
	var errs []error
	if slices.Contains(want, "node") {
		d, err := e.ensureNode(logf)
		list, errs = append(list, d), append(errs, err)
	}
	if slices.Contains(want, "git") {
		d := e.gitDependency()
		if !d.Installed {
			ctx, cancel := context.WithTimeout(e.Ctx, 15*time.Minute)
			_, err := deps.InstallGit(ctx, e.DataDir, logf)
			cancel()
			if err != nil {
				errs = append(errs, fmt.Errorf("git: %w", err))
			}
			d = e.gitDependency()
		}
		list = append(list, d)
	}
	// Bun and Deno only when asked for: most servers need neither.
	for _, rt := range []string{"bun", "deno"} {
		if !slices.Contains(want, rt) {
			continue
		}
		rep, _, err := e.runtimeReport()
		if err != nil {
			list, errs = append(list, Dependency{Name: rt, Note: "the service did not answer"}), append(errs, fmt.Errorf("%s: %w", rt, err))
			continue
		}
		d := runtimeDependency(rep, rt, nil)
		if !d.Installed {
			d, err = e.ensureRuntime(rt, "", false, logf)
			errs = append(errs, err)
		}
		d.Optional = false
		list = append(list, d)
	}
	if err := e.printDependencies(list); err != nil {
		return err
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return missing(list)
}

// ensureNode makes sure sites have a Node.js to run with: the server's
// default version, else the node on PATH, else a version installed through
// the service and made the default.
func (e *Env) ensureNode(logf func(string, ...any)) (Dependency, error) {
	st, err := e.nodeState()
	if err != nil {
		return Dependency{Name: "node", Note: "the service did not answer"}, fmt.Errorf("node: %w", err)
	}
	d := st.dependency()
	if d.Installed {
		return d, nil
	}
	version := st.Default
	switch {
	case version != "":
		// The default was removed from disk: put it back, sites pin it.
	case len(st.Installed) > 0 && st.Installed[0].Status == "installed":
		// Installed but never made the default: no download needed.
		version = st.Installed[0].Version
		logf("Making the installed Node.js %s the server default...", version)
		return e.finishNode(version)
	default:
		ctx, cancel := context.WithTimeout(e.Ctx, 30*time.Second)
		defer cancel()
		var avail []nodeversions.Available
		if err := e.Client.Get(ctx, "/api/node/available", &avail); err != nil {
			return d, fmt.Errorf("node: %w", err)
		}
		if version = pickNodeVersion(avail); version == "" {
			return d, errors.New("node: nodejs.org lists no LTS release for this computer")
		}
	}
	logf("Installing Node.js %s...", version)
	if err := e.Client.Post(e.Ctx, "/api/node/versions", map[string]string{"version": version}, nil); err != nil {
		return d, fmt.Errorf("node: %w", err)
	}
	if err := e.waitNode(version, logf); err != nil {
		return d, fmt.Errorf("node: %w", err)
	}
	return e.finishNode(version)
}

// pickNodeVersion chooses the Node.js version setup installs on a server
// that has none. avail is nodejs.org's list for this computer, newest
// first; each entry's LTS is false or the line's codename ("Jod").
func pickNodeVersion(avail []nodeversions.Available) string {
	// The newest LTS release: the line nodejs.org recommends for production.
	for _, a := range avail {
		if codename, ok := a.LTS.(string); ok && codename != "" {
			return a.Version
		}
	}
	return ""
}

// waitNode follows an install the service runs in the background.
func (e *Env) waitNode(version string, logf func(string, ...any)) error {
	deadline := time.Now().Add(15 * time.Minute)
	last := -1
	for time.Now().Before(deadline) {
		st, err := e.nodeState()
		if err != nil {
			return err
		}
		i := slices.IndexFunc(st.Installed, func(in nodeversions.Installed) bool { return in.Version == version })
		if i < 0 {
			return fmt.Errorf("the service lost track of the Node.js %s install", version)
		}
		switch in := st.Installed[i]; in.Status {
		case "installed":
			return nil
		case "error":
			return errors.New(in.Error)
		default:
			if p := int(in.Progress) / 25 * 25; p > last && p > 0 {
				logf("  %d%%", p)
				last = p
			}
		}
		select {
		case <-e.Ctx.Done():
			return e.Ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("Node.js %s was not installed within 15 minutes", version)
}

// finishNode makes version the server's default Node.js. The settings go
// back as they came, with only that field changed, as the consoles do.
func (e *Env) finishNode(version string) (Dependency, error) {
	var s map[string]json.RawMessage
	if _, err := e.get("/api/settings", &s); err != nil {
		return Dependency{Name: "node"}, fmt.Errorf("node: %w", err)
	}
	s["defaultNodeVersion"], _ = json.Marshal(version)
	if err := e.Client.Put(e.Ctx, "/api/settings", s, nil); err != nil {
		return Dependency{Name: "node"}, fmt.Errorf("node: set the default version: %w", err)
	}
	st, err := e.nodeState()
	if err != nil {
		return Dependency{Name: "node"}, fmt.Errorf("node: %w", err)
	}
	return st.dependency(), nil
}
