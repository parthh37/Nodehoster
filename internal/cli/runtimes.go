package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/runtimes"
)

func init() {
	register(
		&Command{Name: "runtime list", MaxArgs: 0,
			Summary: "List the runtimes besides Node.js: Bun and Deno versions, Python interpreters, .NET",
			Setup:   func(*flag.FlagSet) Runner { return runtimeList }},
		&Command{Name: "runtime install", Args: "<bun|deno> [version]", MinArgs: 1, MaxArgs: 2,
			Summary: "Install a Bun or Deno version (default: the newest) and wait for it",
			Setup: func(fs *flag.FlagSet) Runner {
				makeDefault := fs.Bool("default", false, "make it the server's default for sites that pin no version")
				return func(e *Env, args []string) error { return runtimeInstall(e, args, *makeDefault) }
			}},
		&Command{Name: "runtime remove", Args: "<bun|deno> <version>", MinArgs: 2, MaxArgs: 2,
			Summary: "Remove a Bun or Deno version no site uses",
			Setup:   func(*flag.FlagSet) Runner { return runtimeRemove }},
	)
}

func (e *Env) runtimeReport() (runtimes.Report, json.RawMessage, error) {
	var rep runtimes.Report
	raw, err := e.get("/api/runtimes", &rep)
	return rep, raw, err
}

func runtimeList(e *Env, _ []string) error {
	rep, raw, err := e.runtimeReport()
	if err != nil || e.JSON {
		if err == nil {
			err = e.printJSON(raw)
		}
		return err
	}
	var rows [][]string
	managed := func(label string, m runtimes.Managed) {
		for _, in := range m.Installed {
			status := in.Status
			if in.Status == "installing" {
				status = fmt.Sprintf("installing %d%%", int(in.Progress))
			} else if in.Error != "" {
				status = "error: " + in.Error
			}
			rows = append(rows, []string{label, in.Version, status, yesNo(in.IsDefault), in.Path})
		}
		if m.System != nil {
			def := !slices.ContainsFunc(m.Installed, func(in runtimes.Installed) bool { return in.IsDefault })
			rows = append(rows, []string{label, m.System.Version, "on PATH", yesNo(def), m.System.Path})
		}
		if m.System == nil && len(m.Installed) == 0 {
			rows = append(rows, []string{label, "-", "not installed", "-", "nodehoster runtime install " + strings.ToLower(label)})
		}
	}
	managed("Bun", rep.Bun)
	managed("Deno", rep.Deno)
	for _, in := range rep.Python {
		rows = append(rows, []string{"Python", in.Version, "found (" + in.Source + ")", yesNo(in.IsDefault), in.Path})
	}
	if len(rep.Python) == 0 {
		rows = append(rows, []string{"Python", "-", "not found", "-", runtimes.PythonDownload})
	}
	if rep.Dotnet != nil {
		for _, r := range rep.Dotnet.Runtimes {
			rows = append(rows, []string{".NET", r.Version, r.Name, "-", rep.Dotnet.Host})
		}
		if len(rep.Dotnet.Runtimes) == 0 {
			rows = append(rows, []string{".NET", "-", "host without runtimes", "-", rep.Dotnet.Host})
		}
	} else {
		rows = append(rows, []string{".NET", "-", "not found", "-", runtimes.DotnetDownload})
	}
	e.table([]string{"RUNTIME", "VERSION", "STATUS", "DEFAULT", "PATH"}, rows)
	return nil
}

func managedArg(rt string) error {
	if rt != model.RuntimeBun && rt != model.RuntimeDeno {
		return usagef("%q is not a runtime NodeHoster installs: use bun or deno (Python and .NET are installed with their own installers)", rt)
	}
	return nil
}

func runtimeInstall(e *Env, args []string, makeDefault bool) error {
	rt := strings.ToLower(args[0])
	if err := managedArg(rt); err != nil {
		return err
	}
	version := ""
	if len(args) > 1 {
		version = strings.TrimPrefix(args[1], "v")
	}
	// Progress goes to stderr with --json, which keeps stdout the JSON.
	logf := func(format string, a ...any) { e.printf(format+"\n", a...) }
	if e.JSON {
		logf = func(format string, a ...any) { fmt.Fprintf(e.Stderr, format+"\n", a...) }
	}
	d, err := e.ensureRuntime(rt, version, makeDefault, logf)
	if err != nil {
		return err
	}
	if e.JSON {
		return e.printJSON(d)
	}
	return nil
}

func runtimeRemove(e *Env, args []string) error {
	rt := strings.ToLower(args[0])
	if err := managedArg(rt); err != nil {
		return err
	}
	v := strings.TrimPrefix(args[1], "v")
	if err := e.Client.Delete(e.Ctx, "/api/runtimes/"+rt+"/versions/"+v); err != nil {
		return err
	}
	if !e.JSON {
		e.printf("Removed %s %s.\n", model.RuntimeLabel(rt), v)
	}
	return nil
}

// ensureRuntime installs a Bun or Deno version through the service (the
// newest published when version is ""), unless it is installed already,
// waits for it, and makes it the server default when asked to or when
// sites would otherwise have none.
func (e *Env) ensureRuntime(rt, version string, makeDefault bool, logf func(string, ...any)) (Dependency, error) {
	label := model.RuntimeLabel(rt)
	if version == "" {
		var avail []runtimes.Available
		if _, err := e.get("/api/runtimes/"+rt+"/available", &avail); err != nil {
			return Dependency{Name: rt}, fmt.Errorf("%s: %w", rt, err)
		}
		if len(avail) == 0 {
			return Dependency{Name: rt}, fmt.Errorf("%s: no release is published for this computer", rt)
		}
		version = avail[0].Version
	}
	rep, _, err := e.runtimeReport()
	if err != nil {
		return Dependency{Name: rt}, err
	}
	if in := managedOf(rep, rt).ready(version); in == nil {
		logf("Installing %s %s...", label, version)
		if err := e.Client.Post(e.Ctx, "/api/runtimes/"+rt+"/versions", map[string]string{"version": version}, nil); err != nil {
			return Dependency{Name: rt}, fmt.Errorf("%s: %w", rt, err)
		}
		if err := e.waitRuntime(rt, version, logf); err != nil {
			return Dependency{Name: rt}, fmt.Errorf("%s: %w", rt, err)
		}
		logf("Installed %s %s.", label, version)
	}
	if rep.Defaults.Default(rt) == "" && managedOf(rep, rt).System == nil {
		makeDefault = true // sites that pin no version would find none
	}
	if makeDefault {
		if err := e.setRuntimeDefault(rt, version); err != nil {
			return Dependency{Name: rt}, err
		}
		logf("%s %s is the server default.", label, version)
	}
	if rep, _, err = e.runtimeReport(); err != nil {
		return Dependency{Name: rt}, err
	}
	return runtimeDependency(rep, rt, nil), nil
}

func managedOf(rep runtimes.Report, rt string) managedState {
	if rt == model.RuntimeBun {
		return managedState(rep.Bun)
	}
	return managedState(rep.Deno)
}

type managedState runtimes.Managed

func (m managedState) ready(v string) *runtimes.Installed {
	for i, in := range m.Installed {
		if in.Version == v && in.Status == "installed" {
			return &m.Installed[i]
		}
	}
	return nil
}

// waitRuntime follows an install the service runs in the background.
func (e *Env) waitRuntime(rt, version string, logf func(string, ...any)) error {
	deadline := time.Now().Add(15 * time.Minute)
	last := -1
	for time.Now().Before(deadline) {
		rep, _, err := e.runtimeReport()
		if err != nil {
			return err
		}
		m := managedOf(rep, rt)
		i := slices.IndexFunc(m.Installed, func(in runtimes.Installed) bool { return in.Version == version })
		if i < 0 {
			return fmt.Errorf("the service lost track of the install of %s", version)
		}
		switch in := m.Installed[i]; in.Status {
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
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("%s was not installed within 15 minutes", version)
}

// setRuntimeDefault changes one field of the settings, which go back as
// they came, as the consoles do.
func (e *Env) setRuntimeDefault(rt, version string) error {
	var s map[string]json.RawMessage
	if _, err := e.get("/api/settings", &s); err != nil {
		return err
	}
	var d map[string]any
	if raw, ok := s["runtimes"]; ok {
		json.Unmarshal(raw, &d)
	}
	if d == nil {
		d = map[string]any{}
	}
	d[rt] = version
	s["runtimes"], _ = json.Marshal(d)
	if err := e.Client.Put(e.Ctx, "/api/settings", s, nil); err != nil {
		return fmt.Errorf("make %s %s the default: %w", rt, version, err)
	}
	return nil
}

// runtimeDependency is the `nodehoster deps` line of a runtime besides
// Node.js. They are optional: only one a site uses counts as missing.
func runtimeDependency(rep runtimes.Report, rt string, sites []localapi.Site) Dependency {
	d := Dependency{Name: rt, Optional: true}
	used := 0
	for _, s := range sites {
		if s.Site != nil && s.RunsNode() && s.Node.RuntimeName() == rt {
			used++
		}
	}
	switch rt {
	case model.RuntimeBun, model.RuntimeDeno:
		m := managedOf(rep, rt)
		if def := rep.Defaults.Default(rt); def != "" {
			if in := m.ready(def); in != nil {
				d.Installed, d.Version, d.Path, d.Note = true, in.Version, in.Path, "server default"
			} else {
				d.Version, d.Note = def, "the server default is not installed"
			}
		} else if m.System != nil {
			d.Installed, d.Version, d.Path, d.Note = true, m.System.Version, m.System.Path, "on the system PATH"
		} else if len(m.Installed) > 0 && m.Installed[0].Status == "installed" {
			d.Installed, d.Version, d.Path = true, m.Installed[0].Version, m.Installed[0].Path
		}
	case model.RuntimePython:
		for _, in := range rep.Python {
			if in.IsDefault || !d.Installed {
				d.Installed, d.Version, d.Path = true, in.Version, in.Path
			}
		}
		if !d.Installed {
			d.Note = "install it from " + runtimes.PythonDownload
		}
	case model.RuntimeDotnet:
		if rep.Dotnet != nil {
			d.Installed, d.Path = true, rep.Dotnet.Host
			var vs []string
			for _, r := range rep.Dotnet.Runtimes {
				if r.Name == "Microsoft.AspNetCore.App" || r.Name == "Microsoft.NETCore.App" {
					vs = append(vs, strings.TrimPrefix(r.Name, "Microsoft.")+" "+r.Version)
				}
			}
			d.Version = strings.Join(vs, ", ")
		} else {
			d.Note = "install the Hosting Bundle from " + runtimes.DotnetDownload
		}
	}
	switch {
	case used > 0:
		d.Optional = false
		d.Note = strings.TrimSpace(fmt.Sprintf("used by %d site(s); %s", used, d.Note))
		d.Note = strings.TrimSuffix(d.Note, ";")
	case !d.Installed && d.Note == "":
		d.Note = "optional; no site uses it"
	case !d.Installed:
		d.Note = "optional; " + d.Note
	}
	return d
}

// runtimeDependencies are the deps lines of Bun, Deno, Python and .NET.
func (e *Env) runtimeDependencies() ([]Dependency, error) {
	rep, _, err := e.runtimeReport()
	if err != nil {
		return nil, err
	}
	sites, err := e.sites()
	if err != nil {
		return nil, err
	}
	var list []Dependency
	for _, rt := range []string{model.RuntimeBun, model.RuntimeDeno, model.RuntimePython, model.RuntimeDotnet} {
		list = append(list, runtimeDependency(rep, rt, sites))
	}
	return list, nil
}

// runtimeText is a node or worker site's runtime for the site commands:
// "Python 3.12", "Bun (server default)".
func runtimeText(s localapi.Site) string {
	if s.Site == nil || !s.RunsNode() || s.Node == nil {
		return "-"
	}
	rt := s.Node.RuntimeName()
	label := model.RuntimeLabel(rt)
	switch v := s.Node.RuntimeVersion; {
	case rt == model.RuntimeNode && s.Node.NodeVersion != "":
		return label + " " + s.Node.NodeVersion
	case rt == model.RuntimeNode, rt == model.RuntimeCustom:
		return label
	case v != "":
		return label + " " + v
	}
	return label
}
