// Package nodeversions installs and resolves Node.js runtimes side by side,
// so every site can pin the version it was built for.
package nodeversions

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/procmgr"
)

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

const defaultMirror = "https://nodejs.org/dist"

type Installed struct {
	Version   string  `json:"version"`
	Path      string  `json:"path"`
	Status    string  `json:"status"` // installing | installed | error
	Progress  float64 `json:"progress"`
	Error     string  `json:"error,omitempty"`
	IsDefault bool    `json:"isDefault"`
}

type System struct {
	Version string `json:"version"`
	Path    string `json:"path"`
}

type Available struct {
	Version  string `json:"version"`
	LTS      any    `json:"lts"` // string codename or false
	Date     string `json:"date"`
	Security bool   `json:"security"`
}

type Manager struct {
	dir    string
	tmp    string
	log    *slog.Logger
	mirror func() string
	client *http.Client

	mu      sync.Mutex
	jobs    map[string]*Installed // in-progress or failed installs
	index   []Available
	indexAt time.Time
}

func New(dir, tmp string, log *slog.Logger, mirror func() string) *Manager {
	return &Manager{
		dir: dir, tmp: tmp, log: log, mirror: mirror,
		client: &http.Client{Timeout: 30 * time.Minute},
		jobs:   map[string]*Installed{},
	}
}

func (m *Manager) base() string {
	if m.mirror != nil {
		if v := strings.TrimRight(m.mirror(), "/"); v != "" {
			return v
		}
	}
	return defaultMirror
}

// platform returns the nodejs.org file key, archive suffix and folder name
// for this machine.
func platform() (fileKey, archive, folder string) {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64", "386": "x86"}[runtime.GOARCH]
	switch runtime.GOOS {
	case "windows":
		return "win-" + arch + "-zip", "zip", "win-" + arch
	case "darwin":
		return "osx-" + arch + "-tar", "tar.gz", "darwin-" + arch
	default:
		return "linux-" + arch, "tar.gz", "linux-" + arch
	}
}

func exeIn(dir string) (exe, npm string) {
	if runtime.GOOS == "windows" {
		exe = filepath.Join(dir, "node.exe")
		npm = filepath.Join(dir, "node_modules", "npm", "bin", "npm-cli.js")
	} else {
		exe = filepath.Join(dir, "bin", "node")
		npm = filepath.Join(dir, "lib", "node_modules", "npm", "bin", "npm-cli.js")
	}
	if _, err := os.Stat(npm); err != nil {
		npm = ""
	}
	return
}

// Resolve maps a version to a runtime. "" means the node on PATH.
func (m *Manager) Resolve(version string) (procmgr.NodeRuntime, error) {
	version = strings.TrimPrefix(version, "v")
	if version == "" {
		sys := m.System()
		if sys == nil {
			return procmgr.NodeRuntime{}, errors.New("no Node.js version is selected and node was not found on PATH; install one on the Node.js page")
		}
		_, npm := systemNpm(sys.Path)
		return procmgr.NodeRuntime{Version: sys.Version, Exe: sys.Path, NpmCli: npm}, nil
	}
	dir := filepath.Join(m.dir, version)
	exe, npm := exeIn(dir)
	if _, err := os.Stat(exe); err != nil {
		return procmgr.NodeRuntime{}, fmt.Errorf("Node.js %s is not installed", version)
	}
	return procmgr.NodeRuntime{Version: version, Exe: exe, NpmCli: npm}, nil
}

func systemNpm(nodeExe string) (string, string) {
	dir := filepath.Dir(nodeExe)
	for _, p := range []string{
		filepath.Join(dir, "node_modules", "npm", "bin", "npm-cli.js"),
		filepath.Join(dir, "..", "lib", "node_modules", "npm", "bin", "npm-cli.js"),
	} {
		if _, err := os.Stat(p); err == nil {
			return dir, p
		}
	}
	return dir, ""
}

// System reports the node found on PATH, if any.
func (m *Manager) System() *System {
	p, err := exec.LookPath("node")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p, "--version").Output()
	if err != nil {
		return nil
	}
	abs, _ := filepath.Abs(p)
	return &System{Version: strings.TrimPrefix(strings.TrimSpace(string(out)), "v"), Path: abs}
}

// List returns installed versions plus installs in progress or failed.
func (m *Manager) List(defaultVersion string) []Installed {
	out := []Installed{}
	entries, _ := os.ReadDir(m.dir)
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() || !versionRe.MatchString(e.Name()) {
			continue
		}
		exe, _ := exeIn(filepath.Join(m.dir, e.Name()))
		if _, err := os.Stat(exe); err != nil {
			continue
		}
		seen[e.Name()] = true
		out = append(out, Installed{Version: e.Name(), Path: exe, Status: "installed", Progress: 100, IsDefault: e.Name() == defaultVersion})
	}
	m.mu.Lock()
	for v, j := range m.jobs {
		if !seen[v] {
			out = append(out, *j)
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return compareVersions(out[i].Version, out[j].Version) > 0 })
	return out
}

func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3 && i < len(pa) && i < len(pb); i++ {
		var x, y int
		fmt.Sscanf(pa[i], "%d", &x)
		fmt.Sscanf(pb[i], "%d", &y)
		if x != y {
			return x - y
		}
	}
	return 0
}

// Available lists versions published for this platform (cached for an hour).
func (m *Manager) Available(ctx context.Context) ([]Available, error) {
	m.mu.Lock()
	if m.index != nil && time.Since(m.indexAt) < time.Hour {
		idx := m.index
		m.mu.Unlock()
		return idx, nil
	}
	m.mu.Unlock()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, m.base()+"/index.json", nil)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch Node.js release index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetch Node.js release index: HTTP %d", resp.StatusCode)
	}
	var raw []struct {
		Version  string   `json:"version"`
		Date     string   `json:"date"`
		Files    []string `json:"files"`
		LTS      any      `json:"lts"`
		Security bool     `json:"security"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	key, _, _ := platform()
	out := []Available{}
	for _, r := range raw {
		has := false
		for _, f := range r.Files {
			if f == key {
				has = true
				break
			}
		}
		v := strings.TrimPrefix(r.Version, "v")
		// Versions before 16 lack modern platform builds and are end-of-life.
		if !has || compareVersions(v, "16.0.0") < 0 {
			continue
		}
		out = append(out, Available{Version: v, LTS: r.LTS, Date: r.Date, Security: r.Security})
	}
	m.mu.Lock()
	m.index, m.indexAt = out, time.Now()
	m.mu.Unlock()
	return out, nil
}

// Install downloads, verifies and extracts a version in the background.
func (m *Manager) Install(version string) error {
	version = strings.TrimPrefix(version, "v")
	if !versionRe.MatchString(version) {
		return fmt.Errorf("%q is not a version number", version)
	}
	if _, err := m.Resolve(version); err == nil {
		return fmt.Errorf("Node.js %s is already installed", version)
	}
	m.mu.Lock()
	if j, ok := m.jobs[version]; ok && j.Status == "installing" {
		m.mu.Unlock()
		return fmt.Errorf("Node.js %s is already being installed", version)
	}
	job := &Installed{Version: version, Status: "installing"}
	m.jobs[version] = job
	m.mu.Unlock()

	go func() {
		err := m.install(version, job)
		m.mu.Lock()
		defer m.mu.Unlock()
		if err != nil {
			m.log.Error("node install failed", "version", version, "err", err)
			job.Status, job.Error = "error", err.Error()
			return
		}
		m.log.Info("node installed", "version", version)
		delete(m.jobs, version)
	}()
	return nil
}

func (m *Manager) setProgress(job *Installed, p float64) {
	m.mu.Lock()
	job.Progress = p
	m.mu.Unlock()
}

func (m *Manager) install(version string, job *Installed) error {
	_, archive, folder := platform()
	name := fmt.Sprintf("node-v%s-%s", version, folder)
	file := name + "." + archive
	base := fmt.Sprintf("%s/v%s", m.base(), version)

	sums, err := m.fetchSums(base + "/SHASUMS256.txt")
	if err != nil {
		return err
	}
	want, ok := sums[file]
	if !ok {
		return fmt.Errorf("%s is not published for this platform", file)
	}

	os.MkdirAll(m.tmp, 0o750)
	tmp, err := os.CreateTemp(m.tmp, "node-*."+archive)
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	resp, err := m.client.Get(base + "/" + file)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download %s: HTTP %d", file, resp.StatusCode)
	}
	h := sha256.New()
	pw := &progressWriter{total: resp.ContentLength, fn: func(p float64) { m.setProgress(job, p*0.9) }}
	if _, err := io.Copy(io.MultiWriter(tmp, h, pw), resp.Body); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch for %s: the download is corrupt or was tampered with", file)
	}

	dest := filepath.Join(m.dir, version)
	partial := dest + ".partial"
	os.RemoveAll(partial)
	if archive == "zip" {
		err = extractZip(tmp.Name(), partial, name+"/")
	} else {
		err = extractTarGz(tmp.Name(), partial, name+"/")
	}
	if err != nil {
		os.RemoveAll(partial)
		return fmt.Errorf("extract: %w", err)
	}
	if err := os.Rename(partial, dest); err != nil {
		os.RemoveAll(partial)
		return err
	}
	m.setProgress(job, 100)
	return nil
}

func (m *Manager) fetchSums(url string) (map[string]string, error) {
	resp, err := m.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetch checksums: HTTP %d (does this version exist?)", resp.StatusCode)
	}
	out := map[string]string{}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 {
			out[f[1]] = f[0]
		}
	}
	return out, sc.Err()
}

// Remove deletes an installed version.
func (m *Manager) Remove(version string) error {
	if !versionRe.MatchString(version) {
		return fmt.Errorf("%q is not a version number", version)
	}
	m.mu.Lock()
	j, ok := m.jobs[version]
	if ok && j.Status == "installing" {
		m.mu.Unlock()
		return errors.New("this version is still being installed")
	}
	delete(m.jobs, version)
	m.mu.Unlock()
	dir := filepath.Join(m.dir, version)
	if _, err := os.Stat(dir); err != nil {
		if ok {
			return nil // a failed install record
		}
		return fmt.Errorf("Node.js %s is not installed", version)
	}
	return os.RemoveAll(dir)
}

type progressWriter struct {
	total, done int64
	fn          func(float64)
	last        time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.total > 0 && time.Since(p.last) > 200*time.Millisecond {
		p.last = time.Now()
		p.fn(float64(p.done) / float64(p.total) * 100)
	}
	return len(b), nil
}

// safeJoin rejects archive entries that would escape the destination.
func safeJoin(dest, name string) (string, error) {
	p := filepath.Join(dest, name)
	if p != dest && !strings.HasPrefix(p, dest+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry %q escapes the destination", name)
	}
	return p, nil
}

func extractZip(src, dest, strip string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		name := strings.TrimPrefix(f.Name, strip)
		if name == "" || name == f.Name {
			continue
		}
		p, err := safeJoin(dest, filepath.FromSlash(name))
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(p, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode()|0o200)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(w, rc)
		rc.Close()
		w.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTarGz(src, dest, strip string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(h.Name, strip)
		if name == "" || name == h.Name {
			continue
		}
		p, err := safeJoin(dest, filepath.FromSlash(name))
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(p, 0o755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(p), 0o755)
			w, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)|0o200)
			if err != nil {
				return err
			}
			_, err = io.Copy(w, tr)
			w.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.Symlink(h.Linkname, p)
		}
	}
}
