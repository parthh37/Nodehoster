package runtimes

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

var pyVersionRe = regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)

// maxInterpreters bounds how many Python installations are asked their
// version.
const maxInterpreters = 16

// Python lists the Python interpreters on this server, newest first: those
// the py launcher knows (`py -0p`: every python.org and Microsoft Store-free
// installation registered for all users), python and python3 on PATH, and
// the standard folders (a Windows service keeps the PATH it started with,
// so an interpreter installed later is not on it until a restart).
func (m *Manager) Python() []Interpreter {
	d := m.cached("python", func() detection {
		type cand struct{ path, source string }
		var cands []cand
		seen := map[string]bool{}
		add := func(p, source string) {
			if p == "" {
				return
			}
			if !model.RuntimeVersionIsPath(p) {
				if abs, err := filepath.Abs(p); err == nil {
					p = abs
				}
			}
			key := p
			if m.goos == "windows" {
				key = strings.ToLower(p)
				// The Microsoft Store's python.exe is an alias that opens the
				// Store when Python is not installed from it; a service
				// cannot use it either way.
				if strings.Contains(key, `\windowsapps\`) {
					return
				}
			}
			if seen[key] {
				return
			}
			seen[key] = true
			cands = append(cands, cand{p, source})
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if m.goos == "windows" {
			if py, err := m.lookPath("py"); err == nil {
				if out, err := m.output(ctx, py, "-0p"); err == nil {
					for _, e := range parsePyList(string(out)) {
						add(e, "py")
					}
				}
			}
		}
		for _, name := range []string{"python3", "python"} {
			if p, err := m.lookPath(name); err == nil {
				add(p, "path")
			}
		}
		for _, pattern := range m.pythonFolders() {
			matches, _ := m.glob(pattern)
			sort.Sort(sort.Reverse(sort.StringSlice(matches)))
			for _, p := range matches {
				add(p, "folder")
			}
		}
		var list []Interpreter
		for _, c := range cands {
			if len(list) == maxInterpreters {
				break
			}
			vctx, vcancel := context.WithTimeout(ctx, 10*time.Second)
			out, err := m.output(vctx, c.path, "--version")
			vcancel()
			v := parsePythonVersion(string(out))
			if err != nil || v == "" {
				continue
			}
			list = append(list, Interpreter{Version: v, Path: c.path, Source: c.source})
		}
		sort.SliceStable(list, func(i, j int) bool { return compareVersions(list[i].Version, list[j].Version) > 0 })
		return detection{python: list}
	})
	return append([]Interpreter(nil), d.python...)
}

// pythonFolders are where Python installers put interpreters for all users.
func (m *Manager) pythonFolders() []string {
	if m.goos != "windows" {
		return []string{"/usr/local/bin/python3.*[0-9]", "/usr/bin/python3.*[0-9]"}
	}
	var out []string
	for _, env := range []string{"ProgramFiles", "ProgramW6432"} {
		if pf := m.getenv(env); pf != "" {
			out = append(out, filepath.Join(pf, "Python3*", "python.exe"))
		}
	}
	if sd := m.getenv("SystemDrive"); sd != "" {
		out = append(out, filepath.Join(sd+`\`, "Python3*", "python.exe"))
	}
	return out
}

// pythonVersion asks an interpreter given by path for its version.
func (m *Manager) pythonVersion(exe string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := m.output(ctx, exe, "--version")
	return parsePythonVersion(string(out))
}

// pickPython selects an interpreter for a site: want "" = the newest,
// "3.12" = the newest 3.12.x ("3" = the newest 3.x).
func pickPython(list []Interpreter, want string) (Interpreter, error) {
	if len(list) == 0 {
		return Interpreter{}, fmt.Errorf("Python was not found on this server; install it for all users from %s (NodeHoster does not install Python)", PythonDownload)
	}
	if want == "" {
		return list[0], nil
	}
	for _, in := range list {
		if in.Version == want || strings.HasPrefix(in.Version, want+".") {
			return in, nil
		}
	}
	var have []string
	for _, in := range list {
		have = append(have, in.Version)
	}
	return Interpreter{}, fmt.Errorf("Python %s was not found on this server (found %s); install it from %s", want, strings.Join(have, ", "), PythonDownload)
}

// parsePyList reads `py -0p`. The launcher of Python 3.11 and later prints
// " -V:3.12 *        C:\Python312\python.exe" (the star marks its
// default); older ones " -3.9-64        C:\Python39\python.exe *".
// Entries without a path (an installation the launcher cannot run) are
// skipped.
func parsePyList(out string) []string {
	var paths []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "-") {
			continue
		}
		// Drop the tag, then the default marker on either side of the path.
		i := strings.IndexAny(line, " \t")
		if i < 0 {
			continue
		}
		rest := strings.TrimSpace(line[i:])
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "*"))
		rest = strings.TrimSpace(strings.TrimSuffix(rest, "*"))
		if rest == "" || !(strings.Contains(rest, `\`) || strings.Contains(rest, "/")) {
			continue
		}
		paths = append(paths, rest)
	}
	return paths
}

// parsePythonVersion reads `python --version` ("Python 3.12.1"; Python 2
// wrote it to stderr, so it is not found and not offered).
func parsePythonVersion(out string) string {
	v, ok := strings.CutPrefix(strings.TrimSpace(out), "Python ")
	if !ok {
		return ""
	}
	v, _, _ = strings.Cut(v, " ")
	if !pyVersionRe.MatchString(v) {
		return ""
	}
	return v
}

// Dotnet finds the .NET host and its runtimes, or returns nil.
func (m *Manager) Dotnet() *Dotnet {
	d := m.cached("dotnet", func() detection {
		host := ""
		if p, err := m.lookPath("dotnet"); err == nil {
			host = p
			if !model.RuntimeVersionIsPath(p) {
				host, _ = filepath.Abs(p)
			}
		} else {
			for _, p := range m.dotnetFolders() {
				if _, err := os.Stat(p); err == nil {
					host = p
					break
				}
			}
		}
		if host == "" {
			return detection{}
		}
		return detection{dotnet: &Dotnet{Host: host, Runtimes: m.dotnetRuntimes(host)}}
	})
	if d.dotnet == nil {
		return nil
	}
	c := *d.dotnet
	c.Runtimes = append([]DotnetRuntime(nil), c.Runtimes...)
	return &c
}

// dotnetFolders are where the .NET installers put dotnet(.exe).
func (m *Manager) dotnetFolders() []string {
	if m.goos != "windows" {
		return []string{"/usr/local/share/dotnet/dotnet", "/usr/share/dotnet/dotnet", "/usr/lib/dotnet/dotnet"}
	}
	var out []string
	for _, env := range []string{"ProgramFiles", "ProgramW6432"} {
		if pf := m.getenv(env); pf != "" {
			out = append(out, filepath.Join(pf, "dotnet", "dotnet.exe"))
		}
	}
	return out
}

func (m *Manager) dotnetRuntimes(host string) []DotnetRuntime {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := m.output(ctx, host, "--list-runtimes")
	if err != nil {
		return []DotnetRuntime{}
	}
	return parseDotnetRuntimes(string(out))
}

// parseDotnetRuntimes reads `dotnet --list-runtimes`:
// "Microsoft.AspNetCore.App 8.0.11 [C:\Program Files\dotnet\shared\Microsoft.AspNetCore.App]".
func parseDotnetRuntimes(out string) []DotnetRuntime {
	list := []DotnetRuntime{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		name, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		version, dir, _ := strings.Cut(strings.TrimSpace(rest), " ")
		dir = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(dir), "["), "]")
		if !strings.Contains(name, ".") || version == "" || version[0] < '0' || version[0] > '9' {
			continue
		}
		list = append(list, DotnetRuntime{Name: name, Version: version, Path: dir})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Name != list[j].Name {
			return list[i].Name < list[j].Name
		}
		return compareVersions(list[i].Version, list[j].Version) > 0
	})
	return list
}
