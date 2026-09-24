package runtimes

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestParseChecksum(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	cases := []struct{ text, name, want string }{
		{"09a5  bun-darwin-aarch64-profile.zip\n" + sha + "  bun-windows-x64.zip\n", "bun-windows-x64.zip", sha},
		{sha + " *deno-x86_64-unknown-linux-gnu.zip\n", "deno-x86_64-unknown-linux-gnu.zip", sha},
		// Deno's Windows checksums are PowerShell's Get-FileHash output.
		{"Algorithm : SHA256\r\nHash      : " + strings.ToUpper(sha) + "\r\nPath      : C:\\a\\deno\\target\\release\\deno-x86_64-pc-windows-msvc.zip\r\n", "deno-x86_64-pc-windows-msvc.zip", sha},
		{sha + "  bun-linux-x64.zip\n", "bun-windows-x64.zip", ""},
		{"Hash : not-a-hash\n", "x.zip", ""},
	}
	for _, c := range cases {
		if got := parseChecksum(c.text, c.name); got != c.want {
			t.Errorf("parseChecksum(%q, %q) = %q, want %q", c.text, c.name, got, c.want)
		}
	}
}

func TestAssetNames(t *testing.T) {
	cases := []struct {
		rt, goos, goarch string
		baseline         bool
		name, folder     string
	}{
		{model.RuntimeBun, "windows", "amd64", false, "bun-windows-x64.zip", "bun-windows-x64"},
		{model.RuntimeBun, "windows", "amd64", true, "bun-windows-x64-baseline.zip", "bun-windows-x64-baseline"},
		{model.RuntimeBun, "windows", "arm64", true, "bun-windows-aarch64.zip", "bun-windows-aarch64"},
		{model.RuntimeBun, "linux", "amd64", false, "bun-linux-x64.zip", "bun-linux-x64"},
		{model.RuntimeDeno, "windows", "amd64", true, "deno-x86_64-pc-windows-msvc.zip", ""},
		{model.RuntimeDeno, "windows", "arm64", false, "deno-aarch64-pc-windows-msvc.zip", ""},
		{model.RuntimeDeno, "darwin", "arm64", false, "deno-aarch64-apple-darwin.zip", ""},
		{model.RuntimeDeno, "windows", "386", false, "", ""},
	}
	for _, c := range cases {
		name, folder := sources[c.rt].asset(c.goos, c.goarch, c.baseline)
		if name != c.name || folder != c.folder {
			t.Errorf("%s %s/%s baseline=%v: %q %q", c.rt, c.goos, c.goarch, c.baseline, name, folder)
		}
	}
}

func TestParsers(t *testing.T) {
	if v := parseToolVersion("1.1.30\n"); v != "1.1.30" {
		t.Errorf("bun: %q", v)
	}
	if v := parseToolVersion("deno 2.1.4 (stable, release, x86_64-pc-windows-msvc)\nv8 13.0.245.12-rusty\ntypescript 5.6.2\n"); v != "2.1.4" {
		t.Errorf("deno: %q", v)
	}
	for out, want := range map[string]string{"Python 3.12.1\r\n": "3.12.1", "Python 3.13.0rc1": "", "Python 2.7.18": "2.7.18", "": "", "python: not found": ""} {
		if got := parsePythonVersion(out); got != want {
			t.Errorf("parsePythonVersion(%q) = %q, want %q", out, got, want)
		}
	}
	py := parsePyList(" -V:3.13 *        C:\\Program Files\\Python313\\python.exe\r\n" +
		" -V:3.12          C:\\Users\\svc\\AppData\\Local\\Programs\\Python\\Python312\\python.exe\r\n" +
		" -3.9-64        C:\\Python39\\python.exe *\r\n" +
		" -V:ContinuumAnalytics/Anaconda39-64 C:\\Anaconda3\\python.exe\r\n" +
		"No installed Pythons found!\r\n" +
		" -V:3.11 (store)\r\n")
	want := []string{`C:\Program Files\Python313\python.exe`, `C:\Users\svc\AppData\Local\Programs\Python\Python312\python.exe`, `C:\Python39\python.exe`, `C:\Anaconda3\python.exe`}
	if !slices.Equal(py, want) {
		t.Errorf("parsePyList:\n got %q\nwant %q", py, want)
	}
	rts := parseDotnetRuntimes("Microsoft.AspNetCore.App 8.0.11 [C:\\Program Files\\dotnet\\shared\\Microsoft.AspNetCore.App]\r\n" +
		"Microsoft.NETCore.App 6.0.36 [C:\\Program Files\\dotnet\\shared\\Microsoft.NETCore.App]\r\n" +
		"Microsoft.NETCore.App 8.0.11 [C:\\Program Files\\dotnet\\shared\\Microsoft.NETCore.App]\r\n" +
		"Microsoft.WindowsDesktop.App 8.0.11 [C:\\Program Files\\dotnet\\shared\\Microsoft.WindowsDesktop.App]\r\n" +
		"garbage\r\n")
	if len(rts) != 4 || rts[0].Name != "Microsoft.AspNetCore.App" || rts[1].Version != "8.0.11" || rts[2].Version != "6.0.36" {
		t.Fatalf("parseDotnetRuntimes: %+v", rts)
	}
	if rts[0].Path != `C:\Program Files\dotnet\shared\Microsoft.AspNetCore.App` {
		t.Errorf("path %q", rts[0].Path)
	}
	if v := newestNetCore(rts); v != "8.0.11" {
		t.Errorf("newest %q", v)
	}
}

func TestPickPython(t *testing.T) {
	list := []Interpreter{{Version: "3.13.1", Path: "a"}, {Version: "3.12.8", Path: "b"}, {Version: "3.12.1", Path: "c"}, {Version: "3.1.0", Path: "d"}}
	for want, path := range map[string]string{"": "a", "3.12": "b", "3.12.1": "c", "3": "a", "3.1": "d"} {
		in, err := pickPython(list, want)
		if err != nil || in.Path != path {
			t.Errorf("pickPython(%q) = %v %v, want %s", want, in, err, path)
		}
	}
	if _, err := pickPython(list, "3.11"); err == nil || !strings.Contains(err.Error(), "3.13.1, 3.12.8") {
		t.Errorf("missing version: %v", err)
	}
	if _, err := pickPython(nil, ""); err == nil || !strings.Contains(err.Error(), PythonDownload) {
		t.Errorf("no python: %v", err)
	}
}

// fakeTools stands in for the programs detection runs.
type fakeTools struct {
	mu    sync.Mutex
	paths map[string]string // name -> path (lookPath)
	out   map[string]string // "exe args" -> output
	calls int
}

func (f *fakeTools) lookPath(name string) (string, error) {
	if p, ok := f.paths[name]; ok {
		return p, nil
	}
	return "", errors.New("not found")
}

func (f *fakeTools) output(_ context.Context, exe string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if out, ok := f.out[exe+" "+strings.Join(args, " ")]; ok {
		return []byte(out), nil
	}
	return nil, errors.New("exit status 1")
}

func newTestManager(t *testing.T, goos string, f *fakeTools) *Manager {
	t.Helper()
	dir := t.TempDir()
	m := New(filepath.Join(dir, "runtimes"), filepath.Join(dir, "tmp"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.goos, m.goarch = goos, "amd64"
	m.baseline = func() bool { return false }
	if f != nil {
		m.lookPath, m.output = f.lookPath, f.output
		m.getenv = func(string) string { return "" }
		m.glob = func(string) ([]string, error) { return nil, nil }
	}
	return m
}

func TestDetectPythonOnWindows(t *testing.T) {
	f := &fakeTools{
		paths: map[string]string{"py": `C:\Windows\py.exe`, "python": `C:\Users\svc\AppData\Local\Microsoft\WindowsApps\python.exe`, "python3": `C:\Python311\python.exe`},
		out: map[string]string{
			`C:\Windows\py.exe -0p`:                           " -V:3.13 *        C:\\Program Files\\Python313\\python.exe\r\n -V:3.11          C:\\Python311\\python.exe\r\n",
			`C:\Program Files\Python313\python.exe --version`: "Python 3.13.1\r\n",
			`C:\Python311\python.exe --version`:               "Python 3.11.9\r\n",
		},
	}
	m := newTestManager(t, "windows", f)
	list := m.Python()
	if len(list) != 2 || list[0].Version != "3.13.1" || list[0].Source != "py" || list[1].Path != `C:\Python311\python.exe` {
		t.Fatalf("detected %+v", list)
	}
	for _, in := range list {
		if strings.Contains(in.Path, "WindowsApps") {
			t.Fatal("the Microsoft Store alias was offered")
		}
	}
	calls := f.calls
	m.Python()
	if f.calls != calls {
		t.Fatal("detection was not cached")
	}
	m.Refresh()
	m.Python()
	if f.calls == calls {
		t.Fatal("Refresh did not detect again")
	}
	exe, err := m.Resolve(model.RuntimePython, "3.11")
	if err != nil || exe.Exe != `C:\Python311\python.exe` || exe.Version != "3.11.9" {
		t.Fatalf("resolve 3.11: %+v %v", exe, err)
	}
	if _, err := m.Resolve(model.RuntimePython, "3.10"); err == nil {
		t.Fatal("resolved a Python that is not there")
	}
	r := m.Report(model.RuntimeDefaults{Python: "3.11"})
	if r.Python[0].IsDefault || !r.Python[1].IsDefault || r.Dotnet != nil {
		t.Fatalf("report %+v", r)
	}
}

func TestDetectDotnet(t *testing.T) {
	f := &fakeTools{
		paths: map[string]string{"dotnet": "/usr/share/dotnet/dotnet"},
		out: map[string]string{
			"/usr/share/dotnet/dotnet --list-runtimes": "Microsoft.AspNetCore.App 9.0.0 [/usr/share/dotnet/shared/Microsoft.AspNetCore.App]\nMicrosoft.NETCore.App 9.0.0 [/usr/share/dotnet/shared/Microsoft.NETCore.App]\n",
		},
	}
	m := newTestManager(t, "linux", f)
	d := m.Dotnet()
	if d == nil || d.Host != "/usr/share/dotnet/dotnet" || len(d.Runtimes) != 2 {
		t.Fatalf("dotnet %+v", d)
	}
	exe, err := m.Resolve(model.RuntimeDotnet, "")
	if err != nil || exe.Exe != "/usr/share/dotnet/dotnet" || exe.Version != "9.0.0" {
		t.Fatalf("resolve: %+v %v", exe, err)
	}
	none := newTestManager(t, "windows", &fakeTools{}) // no standard folders either
	if _, err := none.Resolve(model.RuntimeDotnet, ""); err == nil || !strings.Contains(err.Error(), DotnetDownload) {
		t.Fatalf("no .NET: %v", err)
	}
	if _, err := none.Resolve(model.RuntimeDotnet, "/nowhere/dotnet"); err == nil {
		t.Fatal("a missing host was accepted")
	}
}

// release server: a fake GitHub with one Bun and two Deno releases.
type releaseServer struct {
	*httptest.Server
	zips     map[string][]byte
	sums     map[string]string
	digests  map[string]string
	requests []string
	mu       sync.Mutex
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, body)
	}
	zw.Close()
	return buf.Bytes()
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func newReleaseServer(t *testing.T) *releaseServer {
	rs := &releaseServer{zips: map[string][]byte{}, sums: map[string]string{}, digests: map[string]string{}}
	rs.zips["bun-v1.2.0/bun-linux-x64.zip"] = zipOf(t, map[string]string{"bun-linux-x64/bun": "#!/bin/sh\necho 1.2.0\n"})
	rs.sums["bun-v1.2.0/SHASUMS256.txt"] = sha(rs.zips["bun-v1.2.0/bun-linux-x64.zip"]) + "  bun-linux-x64.zip\n"
	rs.zips["v2.1.0/deno-x86_64-unknown-linux-gnu.zip"] = zipOf(t, map[string]string{"deno": "deno binary"})
	rs.digests["v2.1.0/deno-x86_64-unknown-linux-gnu.zip"] = "sha256:" + sha(rs.zips["v2.1.0/deno-x86_64-unknown-linux-gnu.zip"])
	rs.zips["v2.0.0/deno-x86_64-unknown-linux-gnu.zip"] = zipOf(t, map[string]string{"deno": "old deno"})
	rs.sums["v2.0.0/deno-x86_64-unknown-linux-gnu.zip.sha256sum"] = strings.Repeat("0", 64) + "  deno-x86_64-unknown-linux-gnu.zip\n" // wrong
	rs.zips["v1.46.0/deno-x86_64-unknown-linux-gnu.zip"] = zipOf(t, map[string]string{"deno": "too old"})
	rs.zips["v2.2.0/deno-x86_64-unknown-linux-gnu.zip"] = zipOf(t, map[string]string{"deno": "unverifiable"}) // no checksum at all

	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.requests = append(rs.requests, r.URL.Path)
		rs.mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/") && strings.Contains(r.URL.Path, "/releases"):
			var tags []string
			repo := strings.Split(r.URL.Path, "/")[3]
			prefix := map[string]string{"bun": "bun-v", "deno": "v"}[repo]
			for key := range rs.zips {
				if tag, _, _ := strings.Cut(key, "/"); strings.HasPrefix(tag, prefix) && (repo == "deno") == !strings.HasPrefix(tag, "bun-") {
					tags = append(tags, tag)
				}
			}
			var list []release
			for _, tag := range tags {
				rel := release{TagName: tag, PublishedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}
				for key, b := range rs.zips {
					if kt, name, _ := strings.Cut(key, "/"); kt == tag {
						_ = b
						rel.Assets = append(rel.Assets, asset{Name: name, URL: rs.URL + "/dl/" + key, Digest: rs.digests[key]})
					}
				}
				for key := range rs.sums {
					if kt, name, _ := strings.Cut(key, "/"); kt == tag {
						rel.Assets = append(rel.Assets, asset{Name: name, URL: rs.URL + "/dl/" + key})
					}
				}
				list = append(list, rel)
			}
			if tag, ok := strings.CutPrefix(r.URL.Path, "/repos/"+strings.Join(strings.Split(r.URL.Path, "/")[2:4], "/")+"/releases/tags/"); ok {
				for _, rel := range list {
					if rel.TagName == tag {
						json.NewEncoder(w).Encode(rel)
						return
					}
				}
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(list)
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			key := strings.TrimPrefix(r.URL.Path, "/dl/")
			if b, ok := rs.zips[key]; ok {
				w.Write(b)
			} else if s, ok := rs.sums[key]; ok {
				io.WriteString(w, s)
			} else {
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(rs.Close)
	return rs
}

func waitInstall(t *testing.T, m *Manager, rt, version string) Installed {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, in := range m.List(rt, "") {
			if in.Version == version && in.Status != "installing" {
				return in
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s %s did not finish installing", rt, version)
	return Installed{}
}

func TestInstallManagedRuntimes(t *testing.T) {
	rs := newReleaseServer(t)
	m := newTestManager(t, "linux", nil)
	m.api = rs.URL
	var mu sync.Mutex
	var done []string
	m.OnInstalled = func(rt, v string, err error) {
		mu.Lock()
		done = append(done, fmt.Sprintf("%s %s %v", rt, v, err != nil))
		mu.Unlock()
	}

	avail, err := m.Available(context.Background(), model.RuntimeDeno)
	if err != nil {
		t.Fatal(err)
	}
	var versions []string
	for _, a := range avail {
		versions = append(versions, a.Version)
	}
	// 1.46 is below the supported line; 2.2.0 has nothing to verify it with.
	if !slices.Equal(versions, []string{"2.1.0", "2.0.0"}) || avail[0].Date != "2025-01-02" {
		t.Fatalf("available %+v", avail)
	}

	// Bun: SHA-256 from SHASUMS256.txt, the executable from the zip's folder.
	if err := m.Install(model.RuntimeBun, "v1.2.0"); err != nil {
		t.Fatal(err)
	}
	if in := waitInstall(t, m, model.RuntimeBun, "1.2.0"); in.Status != "installed" {
		t.Fatalf("bun: %+v", in)
	}
	exe, err := m.Resolve(model.RuntimeBun, "1.2.0")
	if err != nil || exe.Exe != filepath.Join(m.dir, "bun", "1.2.0", "bun") {
		t.Fatalf("resolve bun: %+v %v", exe, err)
	}
	if st, err := os.Stat(exe.Exe); err != nil || st.Mode()&0o100 == 0 {
		t.Fatalf("bun is not executable: %v", err)
	}
	if err := m.Install(model.RuntimeBun, "1.2.0"); err == nil {
		t.Fatal("installed twice")
	}

	// Deno: GitHub's digest.
	if err := m.Install(model.RuntimeDeno, "2.1.0"); err != nil {
		t.Fatal(err)
	}
	if in := waitInstall(t, m, model.RuntimeDeno, "2.1.0"); in.Status != "installed" {
		t.Fatalf("deno: %+v", in)
	}
	// A download that does not match its checksum is not installed.
	m.Install(model.RuntimeDeno, "2.0.0")
	if in := waitInstall(t, m, model.RuntimeDeno, "2.0.0"); in.Status != "error" || !strings.Contains(in.Error, "checksum mismatch") {
		t.Fatalf("tampered: %+v", in)
	}
	if _, err := os.Stat(filepath.Join(m.dir, "deno", "2.0.0")); err == nil {
		t.Fatal("a tampered download was unpacked")
	}
	// Neither is one with no checksum at all.
	m.Install(model.RuntimeDeno, "2.2.0")
	if in := waitInstall(t, m, model.RuntimeDeno, "2.2.0"); in.Status != "error" || !strings.Contains(in.Error, "unverified") {
		t.Fatalf("unverifiable: %+v", in)
	}
	list := m.List(model.RuntimeDeno, "2.1.0")
	if len(list) != 3 || list[0].Version != "2.2.0" || !list[1].IsDefault {
		t.Fatalf("list %+v", list)
	}
	mu.Lock()
	if !slices.Contains(done, "bun 1.2.0 false") || !slices.Contains(done, "deno 2.0.0 true") {
		t.Fatalf("OnInstalled calls %v", done)
	}
	mu.Unlock()

	// Removing forgets a failed install and deletes an installed one.
	if err := m.Remove(model.RuntimeDeno, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(model.RuntimeDeno, "2.1.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve(model.RuntimeDeno, "2.1.0"); err == nil {
		t.Fatal("removed version still resolves")
	}
	if err := m.Remove(model.RuntimeDeno, "2.1.0"); err == nil {
		t.Fatal("removed twice")
	}
	if err := m.Install(model.RuntimePython, "3.12.0"); !errors.Is(err, errNotManaged) {
		t.Fatalf("python install: %v", err)
	}
	if err := m.Install(model.RuntimeBun, "latest"); err == nil {
		t.Fatal("installed a non-version")
	}
}

func TestResolveManagedFallsBackToPath(t *testing.T) {
	f := &fakeTools{paths: map[string]string{"bun": "/usr/local/bin/bun"}, out: map[string]string{"/usr/local/bin/bun --version": "1.1.30\n"}}
	m := newTestManager(t, "linux", f)
	exe, err := m.Resolve(model.RuntimeBun, "")
	if err != nil || exe.Exe != "/usr/local/bin/bun" || exe.Version != "1.1.30" {
		t.Fatalf("resolve: %+v %v", exe, err)
	}
	if _, err := m.Resolve(model.RuntimeDeno, ""); err == nil || !strings.Contains(err.Error(), "Runtimes page") {
		t.Fatalf("no deno: %v", err)
	}
	if _, err := m.Resolve(model.RuntimeBun, "9.9.9"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("missing version: %v", err)
	}
}

func TestExtractZipRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "evil.zip")
	os.WriteFile(p, zipOf(t, map[string]string{"../evil": "x"}), 0o644)
	if err := extractZip(p, filepath.Join(dir, "out"), ""); err == nil {
		t.Fatal("an entry escaping the destination was extracted")
	}
}
