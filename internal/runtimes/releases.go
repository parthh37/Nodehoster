package runtimes

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// Available is a release that can be installed on this computer.
type Available struct {
	Version string `json:"version"`
	Date    string `json:"date"` // YYYY-MM-DD
}

// source describes where a managed runtime's releases come from: GitHub
// releases of its project, one zip per platform, with a SHA-256 either in
// the release's asset metadata or in a checksum file next to the zip.
type source struct {
	label      string
	repo       string
	tagPrefix  string // bun-v1.2.3, v2.1.4
	minVersion string // Bun 1.1 was the first with Windows builds; Deno 2 the current line
	exe        string // without .exe
	// asset is the zip's name for a platform ("" = none), and the folder
	// inside it that holds the executable ("" = the top).
	asset func(goos, goarch string, baseline bool) (name, folder string)
	// sums is the checksum file listing the zip, used when GitHub has no
	// digest for it.
	sums func(asset string) string
}

var sources = map[string]source{
	model.RuntimeBun: {
		label: "Bun", repo: "oven-sh/bun", tagPrefix: "bun-v", minVersion: "1.1.0", exe: "bun",
		asset: func(goos, goarch string, baseline bool) (string, string) {
			osName := map[string]string{"windows": "windows", "linux": "linux", "darwin": "darwin"}[goos]
			arch := map[string]string{"amd64": "x64", "arm64": "aarch64"}[goarch]
			if osName == "" || arch == "" {
				return "", ""
			}
			name := "bun-" + osName + "-" + arch
			if arch == "x64" && baseline {
				name += "-baseline"
			}
			return name + ".zip", name
		},
		sums: func(string) string { return "SHASUMS256.txt" },
	},
	model.RuntimeDeno: {
		label: "Deno", repo: "denoland/deno", tagPrefix: "v", minVersion: "2.0.0", exe: "deno",
		asset: func(goos, goarch string, _ bool) (string, string) {
			arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[goarch]
			target := map[string]string{"windows": "pc-windows-msvc", "linux": "unknown-linux-gnu", "darwin": "apple-darwin"}[goos]
			if arch == "" || target == "" {
				return "", ""
			}
			return "deno-" + arch + "-" + target + ".zip", ""
		},
		sums: func(asset string) string { return asset + ".sha256sum" },
	},
}

func (m *Manager) source(rt string) (source, error) {
	s, ok := sources[rt]
	if !ok {
		return source{}, errNotManaged
	}
	return s, nil
}

func (m *Manager) assetName(s source) (string, string) {
	return s.asset(m.goos, m.goarch, m.goarch == "amd64" && m.baseline())
}

func (m *Manager) exePath(rt, version string) string {
	s := sources[rt]
	exe := s.exe
	if m.goos == "windows" {
		exe += ".exe"
	}
	return filepath.Join(m.dir, rt, version, exe)
}

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// List returns a runtime's installed versions plus installs in progress
// or failed, newest first.
func (m *Manager) List(rt, defaultVersion string) []Installed {
	out := []Installed{}
	entries, _ := os.ReadDir(filepath.Join(m.dir, rt))
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() || !versionRe.MatchString(e.Name()) {
			continue
		}
		exe := m.exePath(rt, e.Name())
		if _, err := os.Stat(exe); err != nil {
			continue
		}
		seen[e.Name()] = true
		out = append(out, Installed{Version: e.Name(), Path: exe, Status: "installed", Progress: 100, IsDefault: e.Name() == defaultVersion})
	}
	m.mu.Lock()
	for key, j := range m.jobs {
		if r, v, _ := strings.Cut(key, "/"); r == rt && !seen[v] {
			out = append(out, *j)
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return compareVersions(out[i].Version, out[j].Version) > 0 })
	return out
}

// release is the part of GitHub's release API that is read.
type release struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []asset   `json:"assets"`
}

type asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"` // "sha256:<hex>", absent on older releases
}

func (r release) find(name string) *asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

func (m *Manager) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("HTTP %d: GitHub's rate limit for this server's address is used up; try again in an hour", resp.StatusCode)
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v)
}

// Available lists the releases published for this computer, newest first
// (cached for an hour: GitHub allows a server 60 unauthenticated API
// requests an hour).
func (m *Manager) Available(ctx context.Context, rt string) ([]Available, error) {
	s, err := m.source(rt)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if c, ok := m.avail[rt]; ok && time.Since(c.at) < time.Hour {
		m.mu.Unlock()
		return c.list, nil
	}
	m.mu.Unlock()
	name, _ := m.assetName(s)
	if name == "" {
		return nil, fmt.Errorf("%s publishes no build for %s/%s", s.label, m.goos, m.goarch)
	}
	var list []release
	if err := m.getJSON(ctx, fmt.Sprintf("%s/repos/%s/releases?per_page=100", m.api, s.repo), &list); err != nil {
		return nil, fmt.Errorf("list %s releases: %w", s.label, err)
	}
	out := []Available{}
	for _, r := range list {
		v, ok := strings.CutPrefix(r.TagName, s.tagPrefix)
		if !ok || r.Draft || r.Prerelease || !versionRe.MatchString(v) || compareVersions(v, s.minVersion) < 0 {
			continue
		}
		a := r.find(name)
		if a == nil || (digestOf(*a) == "" && r.find(s.sums(name)) == nil) {
			continue // nothing to verify it with: never offered
		}
		date := ""
		if !r.PublishedAt.IsZero() {
			date = r.PublishedAt.Format("2006-01-02")
		}
		out = append(out, Available{Version: v, Date: date})
	}
	sort.Slice(out, func(i, j int) bool { return compareVersions(out[i].Version, out[j].Version) > 0 })
	m.mu.Lock()
	m.avail[rt] = availCache{list: out, at: time.Now()}
	m.mu.Unlock()
	return out, nil
}

// Install downloads, verifies and unpacks a version in the background.
func (m *Manager) Install(rt, version string) error {
	s, err := m.source(rt)
	if err != nil {
		return err
	}
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if !versionRe.MatchString(version) {
		return fmt.Errorf("%q is not a version number", version)
	}
	if _, err := os.Stat(m.exePath(rt, version)); err == nil {
		return fmt.Errorf("%s %s is already installed", s.label, version)
	}
	key := rt + "/" + version
	m.mu.Lock()
	if j, ok := m.jobs[key]; ok && j.Status == "installing" {
		m.mu.Unlock()
		return fmt.Errorf("%s %s is already being installed", s.label, version)
	}
	job := &Installed{Version: version, Status: "installing"}
	m.jobs[key] = job
	m.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		err := m.install(ctx, rt, s, version, job)
		m.mu.Lock()
		if err != nil {
			m.log.Error("runtime install failed", "runtime", rt, "version", version, "err", err)
			job.Status, job.Error = "error", err.Error()
		} else {
			m.log.Info("runtime installed", "runtime", rt, "version", version)
			delete(m.jobs, key)
		}
		m.mu.Unlock()
		if m.OnInstalled != nil {
			m.OnInstalled(rt, version, err)
		}
	}()
	return nil
}

func (m *Manager) setProgress(job *Installed, p float64) {
	m.mu.Lock()
	job.Progress = p
	m.mu.Unlock()
}

func (m *Manager) install(ctx context.Context, rt string, s source, version string, job *Installed) error {
	name, folder := m.assetName(s)
	if name == "" {
		return fmt.Errorf("%s publishes no build for %s/%s", s.label, m.goos, m.goarch)
	}
	var rel release
	if err := m.getJSON(ctx, fmt.Sprintf("%s/repos/%s/releases/tags/%s%s", m.api, s.repo, s.tagPrefix, version), &rel); err != nil {
		return fmt.Errorf("find %s %s: %w", s.label, version, err)
	}
	a := rel.find(name)
	if a == nil {
		return fmt.Errorf("%s %s has no %s", s.label, version, name)
	}
	want := digestOf(*a)
	if want == "" {
		sums := rel.find(s.sums(name))
		if sums == nil {
			return fmt.Errorf("%s %s publishes no SHA-256 for %s; it is not installed unverified", s.label, version, name)
		}
		text, err := m.fetchSmall(ctx, sums.URL)
		if err != nil {
			return fmt.Errorf("fetch the checksum of %s: %w", name, err)
		}
		if want = parseChecksum(text, name); want == "" {
			return fmt.Errorf("%s does not list %s; it is not installed unverified", sums.Name, name)
		}
	}

	os.MkdirAll(m.tmp, 0o750)
	tmp, err := os.CreateTemp(m.tmp, rt+"-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", name, resp.StatusCode)
	}
	h := sha256.New()
	pw := &progressWriter{total: resp.ContentLength, fn: func(p float64) { m.setProgress(job, p*0.9) }}
	if _, err := io.Copy(io.MultiWriter(tmp, h, pw), resp.Body); err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: the download is corrupt or was tampered with", name)
	}

	dest := filepath.Join(m.dir, rt, version)
	partial := dest + ".partial"
	os.RemoveAll(partial)
	strip := ""
	if folder != "" {
		strip = folder + "/"
	}
	if err := extractZip(tmp.Name(), partial, strip); err != nil {
		os.RemoveAll(partial)
		return fmt.Errorf("extract %s: %w", name, err)
	}
	exe := filepath.Join(partial, filepath.Base(m.exePath(rt, version)))
	if _, err := os.Stat(exe); err != nil {
		os.RemoveAll(partial)
		return fmt.Errorf("%s has no %s", name, filepath.Base(exe))
	}
	os.Chmod(exe, 0o755)
	if err := os.Rename(partial, dest); err != nil {
		os.RemoveAll(partial)
		return err
	}
	m.setProgress(job, 100)
	return nil
}

// fetchSmall downloads a checksum file (at most 1 MB).
func (m *Manager) fetchSmall(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), err
}

// Remove deletes an installed version (or forgets a failed install).
func (m *Manager) Remove(rt, version string) error {
	s, err := m.source(rt)
	if err != nil {
		return err
	}
	if !versionRe.MatchString(version) {
		return fmt.Errorf("%q is not a version number", version)
	}
	key := rt + "/" + version
	m.mu.Lock()
	j, ok := m.jobs[key]
	if ok && j.Status == "installing" {
		m.mu.Unlock()
		return errors.New("this version is still being installed")
	}
	delete(m.jobs, key)
	m.mu.Unlock()
	dir := filepath.Join(m.dir, rt, version)
	if _, err := os.Stat(dir); err != nil {
		if ok {
			return nil // a failed install record
		}
		return fmt.Errorf("%s %s is not installed", s.label, version)
	}
	return os.RemoveAll(dir)
}

func digestOf(a asset) string {
	if d, ok := strings.CutPrefix(a.Digest, "sha256:"); ok && len(d) == 64 {
		return d
	}
	return ""
}

// parseChecksum finds a file's SHA-256 in a checksum file: sha256sum's
// "<hex>  <name>" lines (Bun's SHASUMS256.txt, Deno's Unix .sha256sum), or
// PowerShell's Get-FileHash list ("Hash : <HEX>"), which Deno publishes for
// its Windows zips and which covers only the one file.
func parseChecksum(text, name string) string {
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Hash") {
			if h := strings.TrimSpace(v); isHex64(h) {
				return strings.ToLower(h)
			}
			continue
		}
		f := strings.Fields(line)
		if len(f) == 2 && isHex64(f[0]) && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
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

// extractZip unpacks src into dest, keeping only entries under strip (with
// strip removed) and refusing entries that would land outside dest.
func extractZip(src, dest, strip string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if strip != "" {
			rest, ok := strings.CutPrefix(name, strip)
			if !ok {
				continue
			}
			name = rest
		}
		if name == "" {
			continue
		}
		p := filepath.Join(dest, filepath.FromSlash(name))
		if p != dest && !strings.HasPrefix(p, dest+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry %q escapes the destination", f.Name)
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(p, 0o755)
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode().Perm()|0o600)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(w, rc)
		rc.Close()
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// compareVersions compares dotted numeric versions ("3.12.1" > "3.9").
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			fmt.Sscanf(pa[i], "%d", &x)
		}
		if i < len(pb) {
			fmt.Sscanf(pb[i], "%d", &y)
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

// parseToolVersion reads `bun --version` ("1.1.30") and `deno --version`
// ("deno 2.1.4 (stable, release, x86_64-pc-windows-msvc)" on the first
// line).
func parseToolVersion(out string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	for _, f := range strings.Fields(line) {
		f = strings.TrimPrefix(f, "v")
		if versionRe.MatchString(f) {
			return f
		}
	}
	return ""
}
