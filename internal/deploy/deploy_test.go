package deploy

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/procmgr"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/store"
)

// ---- test harness

type zipEntry struct {
	name string
	body string
	mode os.FileMode // 0 = regular 0644 file; set os.ModeDir / os.ModeSymlink as needed
}

// writeZip builds an archive at dir/name and returns its path.
func writeZip(t *testing.T, dir, name string, entries []zipEntry) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		} else if mode&os.ModePerm == 0 {
			mode |= 0o755
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatalf("zip header %q: %v", e.name, err)
		}
		if _, err := io.WriteString(w, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// activator is a stand-in for core's activateRelease: it records calls and
// points the stored site at the release, as the real one does.
type activator struct {
	st   *store.Store
	mu   sync.Mutex
	hits []string
	gate chan struct{} // if non-nil, Activate blocks until it is closed
	err  error
}

func (a *activator) activate(ctx context.Context, siteID, release string) error {
	if a.gate != nil {
		<-a.gate
	}
	a.mu.Lock()
	a.hits = append(a.hits, release)
	err := a.err
	a.mu.Unlock()
	if err != nil {
		return err
	}
	s, err := a.st.GetSite(ctx, siteID)
	if err != nil {
		return err
	}
	s.ActiveRelease = release
	return a.st.PutSite(ctx, s)
}

func (a *activator) calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.hits...)
}

type harness struct {
	d        *Deployer
	st       *store.Store
	box      *secrets.Box
	act      *activator
	root     string // temp root; sitesDir lives inside it
	sitesDir string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "nodehoster.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secrets.Open(filepath.Join(root, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sitesDir := filepath.Join(root, "sites")
	if err := os.MkdirAll(sitesDir, 0o750); err != nil {
		t.Fatal(err)
	}
	act := &activator{st: st}
	d := New(Options{
		Store: st, Box: box, Log: log, Bus: events.New(st, log, nil, nil), SitesDir: sitesDir,
		Settings: func() model.Settings { return model.Settings{} },
		ResolveNode: func(string) (procmgr.NodeRuntime, error) {
			return procmgr.NodeRuntime{}, errors.New("node is not available in tests")
		},
		Activate: act.activate,
	})
	return &harness{d: d, st: st, box: box, act: act, root: root, sitesDir: sitesDir}
}

// staticSite stores and returns a static site (no Node.js needed).
func (h *harness) staticSite(t *testing.T, id string, mutate func(*model.Site)) *model.Site {
	t.Helper()
	s := &model.Site{ID: id, Name: "site-" + id, Type: model.SiteStatic, Static: &model.StaticConfig{Root: "."}}
	s.Deploy.KeepReleases = 5
	if mutate != nil {
		mutate(s)
	}
	s.ApplyDefaults()
	if err := h.st.PutSite(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
}

// wait blocks until a deployment has finished and its site is released.
func (h *harness) wait(t *testing.T, dep *model.Deployment) *model.Deployment {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		h.d.mu.Lock()
		_, busy := h.d.running[dep.SiteID]
		_, logOpen := h.d.logs[dep.ID]
		h.d.mu.Unlock()
		if !busy && !logOpen {
			got, err := h.st.GetDeployment(context.Background(), dep.ID)
			if err != nil {
				t.Fatalf("get deployment: %v", err)
			}
			if got.Status == "running" {
				t.Fatalf("deployment released the site but is still marked running")
			}
			// finish closes the log file just after releasing the site.
			time.Sleep(20 * time.Millisecond)
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("deployment %s did not finish", dep.ID)
	return nil
}

func (h *harness) events(t *testing.T, siteID string) []model.Event {
	t.Helper()
	list, err := h.st.ListEvents(context.Background(), siteID, 100)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// ---- extractZip

func TestExtractZipStripsSingleTopLevelFolder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	zp := writeZip(t, dir, "app.zip", []zipEntry{
		{name: "myapp/", mode: os.ModeDir},
		{name: "myapp/index.html", body: "<h1>hi</h1>"},
		{name: "myapp/assets/app.js", body: "console.log(1)"},
	})
	dest := filepath.Join(dir, "out")
	n, err := extractZip(zp, dest)
	if err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	if n != 2 {
		t.Errorf("extracted %d files, want 2", n)
	}
	if got := readFile(t, filepath.Join(dest, "index.html")); got != "<h1>hi</h1>" {
		t.Errorf("index.html = %q", got)
	}
	if got := readFile(t, filepath.Join(dest, "assets", "app.js")); got != "console.log(1)" {
		t.Errorf("assets/app.js = %q", got)
	}
	if exists(filepath.Join(dest, "myapp")) {
		t.Error("top-level folder was not stripped")
	}
}

func TestExtractZipKeepsLayoutWithoutCommonRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	zp := writeZip(t, dir, "app.zip", []zipEntry{
		{name: "package.json", body: "{}"},
		{name: "src/server.js", body: "x"},
		{name: `lib\util.js`, body: "win"}, // Windows-style separators
	})
	dest := filepath.Join(dir, "out")
	if _, err := extractZip(zp, dest); err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	for _, p := range []string{"package.json", filepath.Join("src", "server.js"), filepath.Join("lib", "util.js")} {
		if !exists(filepath.Join(dest, p)) {
			t.Errorf("%s was not extracted", p)
		}
	}
}

func TestExtractZipRejectsPathTraversal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		entries []zipEntry
		escape  string // path relative to the temp root that must not be created
	}{
		{"dotdot", []zipEntry{{name: "ok.txt", body: "ok"}, {name: "../evil.txt", body: "pwned"}}, "evil.txt"},
		{"nested dotdot after strip", []zipEntry{{name: "app/ok.txt"}, {name: "app/../../evil.txt", body: "pwned"}}, "evil.txt"},
		{"deep dotdot", []zipEntry{{name: "ok.txt"}, {name: "a/b/../../../../evil.txt", body: "pwned"}}, "evil.txt"},
		{"backslash dotdot", []zipEntry{{name: "ok.txt"}, {name: `..\evil.txt`, body: "pwned"}}, "evil.txt"},
		{"backslash nested", []zipEntry{{name: `app\ok.txt`}, {name: `app\..\..\evil.txt`, body: "pwned"}}, "evil.txt"},
		{"sibling prefix", []zipEntry{{name: "ok.txt"}, {name: "../out-evil/x.txt", body: "pwned"}}, filepath.Join("out-evil", "x.txt")},
		{"dotdot directory", []zipEntry{{name: "ok.txt"}, {name: "../evil-dir/", mode: os.ModeDir}}, "evil-dir"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			zp := writeZip(t, root, "slip.zip", tc.entries)
			dest := filepath.Join(root, "out")
			_, err := extractZip(zp, dest)
			if err == nil || !strings.Contains(err.Error(), "escapes the destination") {
				t.Fatalf("extractZip error = %v, want an escape error", err)
			}
			if exists(filepath.Join(root, tc.escape)) {
				t.Fatalf("archive wrote %s outside the destination", tc.escape)
			}
		})
	}
}

func TestExtractZipSingleDotDotRootIsContained(t *testing.T) {
	t.Parallel()
	// When every entry shares "../" it is treated as the common root folder
	// and stripped, which keeps the files inside the destination.
	root := t.TempDir()
	zp := writeZip(t, root, "a.zip", []zipEntry{{name: "../evil.txt", body: "x"}})
	dest := filepath.Join(root, "out")
	if _, err := extractZip(zp, dest); err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	if exists(filepath.Join(root, "evil.txt")) {
		t.Fatal("file escaped the destination")
	}
	if !exists(filepath.Join(dest, "evil.txt")) {
		t.Fatal("file was not extracted inside the destination")
	}
}

func TestExtractZipAbsoluteNamesStayInside(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	zp := writeZip(t, root, "abs.zip", []zipEntry{
		{name: "ok.txt", body: "ok"},
		{name: "/abs/evil.txt", body: "x"},
	})
	dest := filepath.Join(root, "out")
	if _, err := extractZip(zp, dest); err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	if !exists(filepath.Join(dest, "abs", "evil.txt")) {
		t.Fatal("absolute entry was not rebased under the destination")
	}
}

func TestExtractZipSkipsSymlinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	secret := filepath.Join(root, "secret.txt")
	os.WriteFile(secret, []byte("top secret"), 0o600)
	zp := writeZip(t, root, "link.zip", []zipEntry{
		{name: "index.html", body: "hi"},
		{name: "link", body: secret, mode: os.ModeSymlink | 0o777},
	})
	dest := filepath.Join(root, "out")
	n, err := extractZip(zp, dest)
	if err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	if n != 1 {
		t.Errorf("extracted %d files, want 1 (the symlink must be skipped)", n)
	}
	if exists(filepath.Join(dest, "link")) {
		t.Fatal("a symlink from the archive was created")
	}
}

func TestExtractZipIgnoresMacOSMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	zp := writeZip(t, root, "mac.zip", []zipEntry{
		{name: "site/index.html", body: "hi"},
		{name: "__MACOSX/site/._index.html", body: "junk"},
	})
	dest := filepath.Join(root, "out")
	n, err := extractZip(zp, dest)
	if err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	if n != 1 || !exists(filepath.Join(dest, "index.html")) {
		t.Errorf("extracted %d files; index.html present=%v", n, exists(filepath.Join(dest, "index.html")))
	}
	if exists(filepath.Join(dest, "__MACOSX")) {
		t.Error("__MACOSX metadata was extracted")
	}
}

func TestExtractZipPreservesExecutableBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions do not apply on Windows")
	}
	t.Parallel()
	root := t.TempDir()
	zp := writeZip(t, root, "x.zip", []zipEntry{
		{name: "run.sh", body: "#!/bin/sh\n", mode: 0o755},
		{name: "data.txt", body: "d", mode: 0o666},
	})
	dest := filepath.Join(root, "out")
	if _, err := extractZip(zp, dest); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(filepath.Join(dest, "run.sh"))
	if st.Mode()&0o100 == 0 {
		t.Errorf("run.sh mode = %v, want owner-executable", st.Mode())
	}
	st, _ = os.Stat(filepath.Join(dest, "data.txt"))
	if st.Mode()&0o002 != 0 || st.Mode()&0o111 != 0 {
		t.Errorf("data.txt mode = %v, want not world-writable and not executable", st.Mode())
	}
}

func TestExtractZipInvalidArchive(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "bad.zip")
	os.WriteFile(p, []byte("this is not a zip"), 0o644)
	if _, err := extractZip(p, filepath.Join(root, "out")); err == nil || !strings.Contains(err.Error(), "open zip") {
		t.Fatalf("error = %v, want an open zip error", err)
	}
}

func TestCommonRoot(t *testing.T) {
	t.Parallel()
	mk := func(names ...string) []*zip.File {
		var out []*zip.File
		for _, n := range names {
			out = append(out, &zip.File{FileHeader: zip.FileHeader{Name: n}})
		}
		return out
	}
	cases := []struct {
		names []string
		want  string
	}{
		{[]string{"app/", "app/a.js", "app/lib/b.js"}, "app/"},
		{[]string{"app/a.js", "other/b.js"}, ""},
		{[]string{"app/a.js", "top.txt"}, ""},
		{[]string{`app\a.js`, `app\b.js`}, "app/"},
		{[]string{"app/a.js", "__MACOSX/app/._a.js"}, "app/"},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := commonRoot(mk(tc.names...)); got != tc.want {
			t.Errorf("commonRoot(%q) = %q, want %q", tc.names, got, tc.want)
		}
	}
}

// ---- helpers

func TestRedact(t *testing.T) {
	t.Parallel()
	got := redact("https://user:s3cret-token@github.com/org/repo.git")
	if strings.Contains(got, "s3cret-token") || strings.Contains(got, "user:") {
		t.Errorf("redact leaked credentials: %q", got)
	}
	if !strings.Contains(got, "github.com/org/repo.git") {
		t.Errorf("redact lost the repository: %q", got)
	}
	for _, s := range []string{"https://github.com/org/repo.git", "git@github.com:org/repo.git", `C:\repos\app`} {
		if got := redact(s); got != s {
			t.Errorf("redact(%q) = %q, want unchanged", s, got)
		}
	}
}

func TestPrependPath(t *testing.T) {
	t.Parallel()
	sep := string(os.PathListSeparator)
	env := prependPath([]string{"A=1", "Path=/usr/bin"}, "/opt/node")
	if env[1] != "Path=/opt/node"+sep+"/usr/bin" {
		t.Errorf("case-insensitive PATH not prepended: %q", env)
	}
	env = prependPath([]string{"A=1"}, "/opt/node")
	if env[len(env)-1] != "PATH=/opt/node" {
		t.Errorf("missing PATH not added: %q", env)
	}
}

// ---- shared paths

func TestLinkSharedSeedsAndPersists(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "s1", func(s *model.Site) {
		s.Deploy.SharedPaths = []string{".env", "uploads", "config/app.json", " data/ "}
	})
	logf, err := os.Create(filepath.Join(h.root, "link.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logf.Close() })
	dl := &depLog{f: logf, subs: map[chan string]struct{}{}}

	shared := model.SharedDir(h.sitesDir, site.ID)

	// First release ships a .env: it seeds the shared copy.
	rel1 := model.ReleaseDir(h.sitesDir, site.ID, "r1")
	os.MkdirAll(rel1, 0o750)
	os.WriteFile(filepath.Join(rel1, ".env"), []byte("SECRET=from-release-1"), 0o640)
	if err := h.d.linkShared(site, rel1, dl); err != nil {
		t.Fatalf("linkShared r1: %v", err)
	}
	if got := readFile(t, filepath.Join(shared, ".env")); got != "SECRET=from-release-1" {
		t.Errorf("shared .env = %q, want the seeded content", got)
	}
	if st, err := os.Stat(filepath.Join(shared, "uploads")); err != nil || !st.IsDir() {
		t.Errorf("shared uploads directory not created: %v", err)
	}
	if st, err := os.Stat(filepath.Join(shared, "config", "app.json")); err != nil || st.IsDir() {
		t.Errorf("shared config/app.json not created as a file: %v", err)
	}
	if st, err := os.Stat(filepath.Join(shared, "data")); err != nil || !st.IsDir() {
		t.Errorf("shared data directory (whitespace-trimmed) not created: %v", err)
	}
	if got := readFile(t, filepath.Join(rel1, ".env")); got != "SECRET=from-release-1" {
		t.Errorf("release 1 .env = %q", got)
	}

	// Content written to the shared folder is visible through the release.
	os.WriteFile(filepath.Join(shared, "uploads", "photo.jpg"), []byte("jpeg"), 0o640)
	if got := readFile(t, filepath.Join(rel1, "uploads", "photo.jpg")); got != "jpeg" {
		t.Errorf("upload not visible through release 1: %q", got)
	}

	// A later release that ships its own .env gets the shared one instead.
	rel2 := model.ReleaseDir(h.sitesDir, site.ID, "r2")
	os.MkdirAll(filepath.Join(rel2, "uploads"), 0o750)
	os.WriteFile(filepath.Join(rel2, ".env"), []byte("SECRET=from-release-2"), 0o640)
	if err := h.d.linkShared(site, rel2, dl); err != nil {
		t.Fatalf("linkShared r2: %v", err)
	}
	if got := readFile(t, filepath.Join(rel2, ".env")); got != "SECRET=from-release-1" {
		t.Errorf("release 2 .env = %q, want the shared copy", got)
	}
	if got := readFile(t, filepath.Join(shared, ".env")); got != "SECRET=from-release-1" {
		t.Errorf("shared .env was overwritten by a later release: %q", got)
	}
	if got := readFile(t, filepath.Join(rel2, "uploads", "photo.jpg")); got != "jpeg" {
		t.Errorf("upload not visible through release 2: %q", got)
	}
}

func TestLinkSharedRejectsEscapingPaths(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	logf, err := os.Create(filepath.Join(h.root, "link.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logf.Close() })
	dl := &depLog{f: logf, subs: map[chan string]struct{}{}}
	abs := filepath.Join(h.root, "outside")
	for _, p := range []string{"..", "../outside", "a/../../outside", `..\outside`, ".", "", abs} {
		site := h.staticSite(t, "esc", func(s *model.Site) { s.Deploy.SharedPaths = []string{p} })
		rel := model.ReleaseDir(h.sitesDir, site.ID, "r1")
		os.MkdirAll(rel, 0o750)
		err := h.d.linkShared(site, rel, dl)
		if runtime.GOOS != "windows" && p == `..\outside` {
			// On POSIX a backslash is an ordinary file name character.
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "must be relative") {
			t.Errorf("shared path %q: error = %v, want a rejection", p, err)
		}
	}
	for _, p := range []string{filepath.Join(h.root, "outside"), filepath.Join(h.sitesDir, "outside")} {
		if exists(p) {
			t.Errorf("an escaping shared path created %s", p)
		}
	}
}

// ---- full deployments

func TestDeployZipSucceeds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "zip-ok", nil)
	zp := writeZip(t, h.root, "upload.zip", []zipEntry{
		{name: "dist/index.html", body: "<p>v1</p>"},
		{name: "dist/app.js", body: "1"},
	})
	dep, err := h.d.DeployZip(context.Background(), site, zp, "alice")
	if err != nil {
		t.Fatalf("DeployZip: %v", err)
	}
	// Only immutable fields are read here: the worker goroutine mutates the
	// returned record (see TestDeployReturnsSnapshot).
	if dep.Source != "zip" || dep.User != "alice" || dep.SiteID != site.ID {
		t.Errorf("initial deployment source/user/site = %q/%q/%q", dep.Source, dep.User, dep.SiteID)
	}
	got := h.wait(t, dep)
	if got.Status != "succeeded" {
		log, _ := h.d.Log(site.ID, dep.ID)
		t.Fatalf("status = %s (%s)\n%s", got.Status, got.Message, log)
	}
	if got.FinishedAt == nil {
		t.Error("FinishedAt not set")
	}
	if want := model.ReleaseDir(h.sitesDir, site.ID, dep.ID); got.ReleaseDir != want {
		t.Errorf("ReleaseDir = %q, want %q", got.ReleaseDir, want)
	}
	if b := readFile(t, filepath.Join(got.ReleaseDir, "index.html")); b != "<p>v1</p>" {
		t.Errorf("index.html = %q", b)
	}
	if exists(zp) {
		t.Error("the uploaded zip was not removed")
	}
	if calls := h.act.calls(); len(calls) != 1 || calls[0] != dep.ID {
		t.Errorf("Activate calls = %v, want [%s]", calls, dep.ID)
	}
	s, _ := h.st.GetSite(context.Background(), site.ID)
	if s.ActiveRelease != dep.ID {
		t.Errorf("active release = %q", s.ActiveRelease)
	}
	log, err := h.d.Log(site.ID, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"extracted 2 files", "activating release " + dep.ID, "deployment succeeded"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
	evs := h.events(t, site.ID)
	if len(evs) == 0 || evs[0].Type != events.DeploySucceeded {
		t.Errorf("events = %+v, want a deploy.succeeded event", evs)
	}
	if _, _, _, _, ok := h.d.Subscribe(dep.ID); ok {
		t.Error("Subscribe reports a finished deployment as running")
	}
}

func TestDeployReturnsSnapshot(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "snapshot", nil)
	zp := writeZip(t, h.root, "s.zip", []zipEntry{{name: "index.html", body: "x"}})
	dep, err := h.d.DeployZip(context.Background(), site, zp, "u")
	if err != nil {
		t.Fatal(err)
	}
	h.wait(t, dep)
	// The value handed to the caller must be a snapshot taken at start.
	if dep.Status != "running" || dep.FinishedAt != nil {
		t.Fatalf("returned deployment was mutated after return: status=%q finishedAt=%v", dep.Status, dep.FinishedAt)
	}
}

func TestDeployZipWithPathTraversalFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "zip-slip", nil)
	zp := writeZip(t, h.root, "evil.zip", []zipEntry{
		{name: "index.html", body: "hi"},
		{name: "../../../../evil.txt", body: "pwned"},
	})
	dep, err := h.d.DeployZip(context.Background(), site, zp, "mallory")
	if err != nil {
		t.Fatalf("DeployZip: %v", err)
	}
	got := h.wait(t, dep)
	if got.Status != "failed" {
		t.Fatalf("status = %s, want failed", got.Status)
	}
	if !strings.Contains(got.Message, "escapes the destination") {
		t.Errorf("message = %q", got.Message)
	}
	if got.ReleaseDir != "" {
		t.Errorf("failed deployment kept ReleaseDir %q", got.ReleaseDir)
	}
	if exists(model.ReleaseDir(h.sitesDir, site.ID, dep.ID)) {
		t.Error("failed release folder was not removed")
	}
	if exists(filepath.Join(h.root, "evil.txt")) || exists(filepath.Join(h.sitesDir, "evil.txt")) {
		t.Fatal("archive escaped the release folder")
	}
	if calls := h.act.calls(); len(calls) != 0 {
		t.Errorf("a failed deployment was activated: %v", calls)
	}
	if exists(zp) {
		t.Error("the uploaded zip was not removed after failure")
	}
	evs := h.events(t, site.ID)
	if len(evs) == 0 || evs[0].Type != events.DeployFailed || evs[0].Level != "error" {
		t.Errorf("events = %+v, want a deploy.failed error", evs)
	}
}

func TestDeployRunsBuildCommandInRelease(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "build", func(s *model.Site) {
		s.Deploy.InstallCommand = "exit 7" // skipped: there is no package.json
		s.Deploy.BuildCommand = "echo built> built.txt"
	})
	zp := writeZip(t, h.root, "b.zip", []zipEntry{{name: "index.html", body: "x"}})
	dep, err := h.d.DeployZip(context.Background(), site, zp, "u")
	if err != nil {
		t.Fatal(err)
	}
	got := h.wait(t, dep)
	log, _ := h.d.Log(site.ID, dep.ID)
	if got.Status != "succeeded" {
		t.Fatalf("status = %s (%s)\n%s", got.Status, got.Message, log)
	}
	if !strings.Contains(string(log), "no package.json, skipping install") {
		t.Errorf("install step was not skipped:\n%s", log)
	}
	if b := readFile(t, filepath.Join(got.ReleaseDir, "built.txt")); !strings.HasPrefix(b, "built") {
		t.Errorf("built.txt = %q", b)
	}
}

func TestDeployFailingInstallCommandFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "install-fail", func(s *model.Site) { s.Deploy.InstallCommand = "exit 7" })
	zp := writeZip(t, h.root, "b.zip", []zipEntry{{name: "package.json", body: "{}"}})
	dep, err := h.d.DeployZip(context.Background(), site, zp, "u")
	if err != nil {
		t.Fatal(err)
	}
	got := h.wait(t, dep)
	if got.Status != "failed" || !strings.Contains(got.Message, "install command failed") {
		t.Fatalf("deployment = %s (%s), want an install failure", got.Status, got.Message)
	}
	if len(h.act.calls()) != 0 {
		t.Error("a failed deployment was activated")
	}
}

func TestDeployActivationErrorFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.act.err = errors.New("proxy said no")
	site := h.staticSite(t, "act-fail", nil)
	zp := writeZip(t, h.root, "b.zip", []zipEntry{{name: "index.html", body: "x"}})
	dep, err := h.d.DeployZip(context.Background(), site, zp, "u")
	if err != nil {
		t.Fatal(err)
	}
	got := h.wait(t, dep)
	if got.Status != "failed" || !strings.Contains(got.Message, "activate: proxy said no") {
		t.Fatalf("deployment = %s (%s)", got.Status, got.Message)
	}
	if exists(model.ReleaseDir(h.sitesDir, site.ID, dep.ID)) {
		t.Error("release of a failed activation was kept")
	}
}

func TestDeployIsExclusivePerSite(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.act.gate = make(chan struct{})
	site := h.staticSite(t, "busy", nil)
	other := h.staticSite(t, "other", nil)

	first, err := h.d.DeployZip(context.Background(), site, writeZip(t, h.root, "1.zip", []zipEntry{{name: "a.txt"}}), "u")
	if err != nil {
		t.Fatal(err)
	}
	history, lines, done, cancel, ok := h.d.Subscribe(first.ID)
	if !ok {
		t.Fatal("Subscribe: running deployment not found")
	}
	defer cancel()

	second := writeZip(t, h.root, "2.zip", []zipEntry{{name: "b.txt"}})
	if _, err := h.d.DeployZip(context.Background(), site, second, "u"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second deployment error = %v, want ErrBusy", err)
	}
	if exists(second) {
		t.Error("a rejected upload was not removed")
	}

	// Another site is not blocked.
	close(h.act.gate)
	otherDep, err := h.d.DeployZip(context.Background(), other, writeZip(t, h.root, "3.zip", []zipEntry{{name: "c.txt"}}), "u")
	if err != nil {
		t.Fatalf("deployment of another site: %v", err)
	}

	var streamed strings.Builder
	timeout := time.After(30 * time.Second)
loop:
	for {
		select {
		case l := <-lines:
			streamed.WriteString(l)
		case <-done:
			break loop
		case <-timeout:
			t.Fatal("deployment did not finish")
		}
	}
	h.wait(t, first)
	h.wait(t, otherDep)
	// Drain lines buffered before done closed.
	for {
		select {
		case l := <-lines:
			streamed.WriteString(l)
			continue
		default:
		}
		break
	}
	if all := string(history) + streamed.String(); strings.Count(all, "extracting archive") != 1 {
		t.Errorf("backlog and live stream do not have the early output exactly once: %q", all)
	}
	// Activation waits on the gate, opened after Subscribe: its outcome
	// must reach the stream.
	if !strings.Contains(streamed.String(), "deployment succeeded") {
		t.Errorf("live log stream missed output: %q", streamed.String())
	}

	// Once finished, the site accepts a new deployment.
	third, err := h.d.DeployZip(context.Background(), site, writeZip(t, h.root, "4.zip", []zipEntry{{name: "d.txt"}}), "u")
	if err != nil {
		t.Fatalf("deployment after the first finished: %v", err)
	}
	h.wait(t, third)
}

// ---- release retention

func seedDeployments(t *testing.T, h *harness, site *model.Site, n int) []*model.Deployment {
	t.Helper()
	base := time.Now().Add(-time.Duration(n) * time.Hour)
	var out []*model.Deployment
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("rel-%03d", i)
		dir := model.ReleaseDir(h.sitesDir, site.ID, id)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "index.html"), []byte(id), 0o640)
		dep := &model.Deployment{ID: id, SiteID: site.ID, Source: "zip", Status: "succeeded",
			StartedAt: base.Add(time.Duration(i) * time.Minute), ReleaseDir: dir}
		if err := h.st.PutDeployment(context.Background(), dep); err != nil {
			t.Fatal(err)
		}
		out = append(out, dep)
	}
	return out
}

func TestPruneKeepsActivePreviousAndNewest(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "prune", func(s *model.Site) { s.Deploy.KeepReleases = 2 })
	deps := seedDeployments(t, h, site, 6) // rel-000 (oldest) .. rel-005 (newest)
	// A failed deployment has no release folder and must be ignored.
	h.st.PutDeployment(context.Background(), &model.Deployment{ID: "failed", SiteID: site.ID, Status: "failed", StartedAt: time.Now()})

	// Active: an old release (rolled back); previous: another old one.
	site.ActiveRelease = "rel-001"
	h.st.PutSite(context.Background(), site)
	h.d.prune(context.Background(), site, "rel-000")

	// Newest first: rel-005 and rel-004 fill the two slots, rel-003 and
	// rel-002 go, rel-001 (active) and rel-000 (previous) are protected.
	keep := map[string]bool{"rel-005": true, "rel-004": true, "rel-001": true, "rel-000": true}
	for _, dep := range deps {
		got, err := h.st.GetDeployment(context.Background(), dep.ID)
		if err != nil {
			t.Fatal(err)
		}
		dirExists := exists(dep.ReleaseDir)
		if keep[dep.ID] && (!dirExists || got.ReleaseDir == "") {
			t.Errorf("%s was pruned but must be kept", dep.ID)
		}
		if !keep[dep.ID] && (dirExists || got.ReleaseDir != "") {
			t.Errorf("%s was kept (dir=%v, record=%q) but should be pruned", dep.ID, dirExists, got.ReleaseDir)
		}
	}
}

// A scheduled task run that started before later deployments still runs
// in its release: pruning keeps it until the run has ended.
func TestPruneKeepsReleasesInUse(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var asked string
	h.d.opts.InUse = func(siteID string) []string { asked = siteID; return []string{"rel-001"} }
	site := h.staticSite(t, "inuse", func(s *model.Site) { s.Deploy.KeepReleases = 1 })
	deps := seedDeployments(t, h, site, 4)
	site.ActiveRelease = "rel-003"
	h.st.PutSite(context.Background(), site)
	h.d.prune(context.Background(), site, "rel-002")

	if asked != site.ID {
		t.Errorf("InUse asked for %q", asked)
	}
	for i, want := range []bool{false, true, true, true} {
		got, _ := h.st.GetDeployment(context.Background(), deps[i].ID)
		if exists(deps[i].ReleaseDir) != want || (got.ReleaseDir != "") != want {
			t.Errorf("%s: kept=%v, want %v", deps[i].ID, exists(deps[i].ReleaseDir), want)
		}
	}
}

func TestPruneKeepsConfiguredCount(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "count", func(s *model.Site) { s.Deploy.KeepReleases = 3 })
	deps := seedDeployments(t, h, site, 6)
	site.ActiveRelease = "rel-005"
	h.st.PutSite(context.Background(), site)
	h.d.prune(context.Background(), site, "rel-004")

	var kept []string
	for _, dep := range deps {
		if exists(dep.ReleaseDir) {
			kept = append(kept, dep.ID)
		}
	}
	if want := []string{"rel-003", "rel-004", "rel-005"}; strings.Join(kept, ",") != strings.Join(want, ",") {
		t.Errorf("kept releases %v, want %v", kept, want)
	}
}

func TestPruneKeepsAtLeastOneRelease(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "zero", nil)
	// ApplyDefaults turns 0 into 5; store a raw 0 (as an old document could
	// hold) to exercise prune's own floor.
	site.Deploy.KeepReleases = 0
	if err := h.st.PutSite(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	deps := seedDeployments(t, h, site, 3)
	h.d.prune(context.Background(), site, "")
	if !exists(deps[2].ReleaseDir) {
		t.Error("KeepReleases=0 removed the newest release")
	}
	if exists(deps[0].ReleaseDir) || exists(deps[1].ReleaseDir) {
		t.Error("older releases were not pruned")
	}
}

func TestPruneBoundsHistory(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "history", func(s *model.Site) { s.Deploy.KeepReleases = 1 })
	seedDeployments(t, h, site, 105)
	site.ActiveRelease = "rel-104"
	h.st.PutSite(context.Background(), site)
	h.d.prune(context.Background(), site, "rel-000") // the oldest is still "previous"

	list, err := h.st.ListDeployments(context.Background(), site.ID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 101 {
		t.Errorf("history has %d records, want 100 plus the protected previous release", len(list))
	}
	if _, err := h.st.GetDeployment(context.Background(), "rel-000"); err != nil {
		t.Error("the previous release's record was deleted")
	}
	if _, err := h.st.GetDeployment(context.Background(), "rel-001"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("record beyond the history limit still exists: %v", err)
	}
}

// ---- rollback

func TestActivateRollback(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "rb", nil)
	other := h.staticSite(t, "rb-other", nil)
	deps := seedDeployments(t, h, site, 3)
	ctx := context.Background()

	dep, err := h.d.Activate(ctx, site, deps[0].ID)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if dep.ID != deps[0].ID {
		t.Errorf("activated %s", dep.ID)
	}
	if calls := h.act.calls(); len(calls) != 1 || calls[0] != deps[0].ID {
		t.Errorf("Activate calls = %v", calls)
	}
	if evs := h.events(t, site.ID); len(evs) == 0 || !strings.Contains(evs[0].Message, "rolled back to "+deps[0].ID) {
		t.Errorf("events = %+v, want a rollback event", evs)
	}

	// Another site's deployment is invisible.
	if _, err := h.d.Activate(ctx, other, deps[1].ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-site activation error = %v, want ErrNotFound", err)
	}
	if _, err := h.d.Activate(ctx, site, "does-not-exist"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown deployment error = %v, want ErrNotFound", err)
	}

	// Failed and pruned deployments cannot be activated.
	h.st.PutDeployment(ctx, &model.Deployment{ID: "failed", SiteID: site.ID, Status: "failed", StartedAt: time.Now()})
	if _, err := h.d.Activate(ctx, site, "failed"); err == nil {
		t.Error("a failed deployment was activated")
	}
	pruned := *deps[1]
	pruned.ReleaseDir = ""
	h.st.PutDeployment(ctx, &pruned)
	if _, err := h.d.Activate(ctx, site, pruned.ID); err == nil {
		t.Error("a pruned deployment was activated")
	}
	os.RemoveAll(deps[2].ReleaseDir)
	if _, err := h.d.Activate(ctx, site, deps[2].ID); err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Errorf("missing release folder error = %v", err)
	}

	h.act.mu.Lock()
	h.act.err = errors.New("boom")
	h.act.mu.Unlock()
	if _, err := h.d.Activate(ctx, site, deps[0].ID); err == nil || err.Error() != "boom" {
		t.Errorf("activation error = %v, want it propagated", err)
	}
}

func TestDeploymentLogIsScopedToSite(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "logs", nil)
	zp := writeZip(t, h.root, "x.zip", []zipEntry{{name: "index.html"}})
	dep, err := h.d.DeployZip(context.Background(), site, zp, "u")
	if err != nil {
		t.Fatal(err)
	}
	h.wait(t, dep)
	if _, err := h.d.Log(site.ID, dep.ID); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if _, err := h.d.Log("another-site", dep.ID); err == nil {
		t.Error("a deployment log was readable through another site")
	}
}

// ---- git

func TestDeployGitRequiresRepository(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "nogit", nil)
	if _, err := h.d.DeployGit(context.Background(), site, "", "git", "u"); err == nil || !strings.Contains(err.Error(), "no git repository") {
		t.Fatalf("error = %v", err)
	}
}

// TestMain lets the test binary double as a fake git executable, so git
// deployments can be tested without git, a network or a real repository.
func TestMain(m *testing.M) {
	if rec := os.Getenv("NH_FAKE_GIT_RECORD"); rec != "" {
		os.Exit(fakeGit(rec))
	}
	os.Exit(m.Run())
}

func fakeGit(record string) int {
	args := os.Args[1:]
	var b strings.Builder
	b.WriteString("ARGS\x00" + strings.Join(args, "\x00") + "\n")
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") {
			b.WriteString("ENV\x00" + kv + "\n")
		}
	}
	f, err := os.OpenFile(record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 3
	}
	f.WriteString(b.String())
	f.Close()
	switch {
	case len(args) > 0 && args[0] == "clone":
		dest := args[len(args)-1]
		os.MkdirAll(filepath.Join(dest, ".git"), 0o750)
		os.WriteFile(filepath.Join(dest, ".git", "config"), []byte("[core]\n"), 0o640)
		os.WriteFile(filepath.Join(dest, "index.html"), []byte("from git"), 0o640)
		fmt.Println("Cloning into '" + dest + "'...")
	case len(args) > 2 && args[0] == "-C" && args[2] == "log":
		fmt.Print("0123456789abcdef0123456789abcdef01234567\nInitial commit\n")
	case len(args) > 0 && args[0] == "init": // DeployRef: git init -q <dir>
		os.MkdirAll(filepath.Join(args[len(args)-1], ".git"), 0o750)
	case len(args) > 2 && args[0] == "-C" && args[2] == "fetch":
		if os.Getenv("NH_FAKE_GIT_FAIL_FETCH") != "" {
			fmt.Fprintln(os.Stderr, "fatal: couldn't find remote ref")
			return 128
		}
	case len(args) > 2 && args[0] == "-C" && slices.Contains(args, "checkout"):
		os.WriteFile(filepath.Join(args[1], "index.html"), []byte("from ref"), 0o640)
	}
	return 0
}

func installFakeGit(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot locate the test binary:", err)
	}
	bin := t.TempDir()
	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}
	src, err := os.ReadFile(self)
	if err != nil {
		t.Skip("cannot read the test binary:", err)
	}
	if err := os.WriteFile(filepath.Join(bin, name), src, 0o755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "git-calls.txt")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NH_FAKE_GIT_RECORD", record)
	return record
}

func TestDeployGitKeepsTokenOutOfArgsAndLogs(t *testing.T) {
	// Not parallel: it changes PATH to put a fake git first.
	const token = "fake-git-token-for-tests-9f3a"
	const urlPassword = "url-embedded-password"
	h := newHarness(t)
	record := installFakeGit(t)

	sealed, err := h.box.Seal(token)
	if err != nil {
		t.Fatal(err)
	}
	repo := "https://deploy:" + urlPassword + "@git.example.invalid/org/app.git"
	site := h.staticSite(t, "git", func(s *model.Site) {
		s.Deploy.Git = model.GitSource{Repo: repo, Branch: "main", Token: sealed}
	})

	dep, err := h.d.DeployGit(context.Background(), site, "", "git", "alice")
	if err != nil {
		t.Fatalf("DeployGit: %v", err)
	}
	got := h.wait(t, dep)
	log, _ := h.d.Log(site.ID, dep.ID)
	if got.Status != "succeeded" {
		t.Fatalf("status = %s (%s)\n%s", got.Status, got.Message, log)
	}

	calls := readFile(t, record)
	var cloneArgs []string
	env := map[string]string{}
	for _, line := range strings.Split(calls, "\n") {
		parts := strings.Split(line, "\x00")
		switch parts[0] {
		case "ARGS":
			if len(parts) > 1 && parts[1] == "clone" {
				cloneArgs = parts[1:]
			}
		case "ENV":
			if k, v, ok := strings.Cut(parts[1], "="); ok {
				env[k] = v
			}
		}
	}
	if cloneArgs == nil {
		t.Fatalf("git clone was not invoked; calls:\n%q", calls)
	}
	want := []string{"clone", "--depth", "1", "--single-branch", "--branch", "main", repo, got.ReleaseDir}
	if strings.Join(cloneArgs, " ") != strings.Join(want, " ") {
		t.Errorf("clone args = %q, want %q", cloneArgs, want)
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	for _, a := range cloneArgs {
		if strings.Contains(a, token) || strings.Contains(a, basic) {
			t.Errorf("the token appears in git's arguments: %q", a)
		}
	}
	if env["GIT_CONFIG_KEY_0"] != "http.extraHeader" || env["GIT_CONFIG_VALUE_0"] != "Authorization: Basic "+basic {
		t.Errorf("token was not passed through git's environment config: %v", env)
	}
	if env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want 0 (never prompt)", env["GIT_TERMINAL_PROMPT"])
	}

	for _, secret := range []string{token, basic, urlPassword, sealed} {
		if strings.Contains(string(log), secret) {
			t.Errorf("deployment log contains a secret (%q):\n%s", secret, log)
		}
	}
	if !strings.Contains(string(log), "git clone https://") || !strings.Contains(string(log), "(main)") {
		t.Errorf("deployment log lacks the redacted clone line:\n%s", log)
	}

	if got.Commit != "0123456789abcdef0123456789abcdef01234567" || got.Message != "Initial commit" {
		t.Errorf("commit = %q message = %q", got.Commit, got.Message)
	}
	if exists(filepath.Join(got.ReleaseDir, ".git")) {
		t.Error(".git (which may hold credentials) was left in the release")
	}
	if b := readFile(t, filepath.Join(got.ReleaseDir, "index.html")); b != "from git" {
		t.Errorf("index.html = %q", b)
	}
	if got.Source != "git" || got.User != "alice" {
		t.Errorf("deployment source/user = %q/%q", got.Source, got.User)
	}
}

func TestDeployGitWithoutTokenSetsNoAuthHeader(t *testing.T) {
	h := newHarness(t)
	record := installFakeGit(t)
	site := h.staticSite(t, "public", func(s *model.Site) {
		s.Deploy.Git = model.GitSource{Repo: "https://git.example.invalid/org/public.git"}
	})
	dep, err := h.d.DeployGit(context.Background(), site, "feature/x", "webhook", "webhook")
	if err != nil {
		t.Fatal(err)
	}
	got := h.wait(t, dep)
	if got.Status != "succeeded" {
		t.Fatalf("status = %s (%s)", got.Status, got.Message)
	}
	calls := readFile(t, record)
	if strings.Contains(calls, "GIT_CONFIG_VALUE_0") || strings.Contains(calls, "Authorization") {
		t.Errorf("an auth header was configured without a token:\n%q", calls)
	}
	if !strings.Contains(calls, "--branch\x00feature/x") {
		t.Errorf("the requested branch was not cloned:\n%q", calls)
	}
}

// TestLogSubscribeSplitsOutput: a viewer gets what was written before it
// subscribed from the file and the rest live, each line once. The stream
// used to subscribe and then read the whole file, so a line written in
// between came twice (the console showed "extracting archive" twice).
func TestLogSubscribeSplitsOutput(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dep.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	l := &depLog{f: f, path: path, subs: map[chan string]struct{}{}, done: make(chan struct{})}
	defer f.Close()
	l.printf("before")
	ch, offset, cancel := l.subscribe()
	defer cancel()
	// Written after subscribing and before the file is read: the window.
	l.printf("extracting archive")
	backlog := string(readPrefix(path, offset))
	if !strings.HasSuffix(backlog, "before\n") || strings.Contains(backlog, "extracting") {
		t.Fatalf("backlog = %q, want the line written before subscribing only", backlog)
	}
	select {
	case live := <-ch:
		if !strings.HasSuffix(live, "extracting archive\n") {
			t.Fatalf("live = %q", live)
		}
	default:
		t.Fatal("the line written after subscribing was not sent live")
	}
	select {
	case extra := <-ch:
		t.Fatalf("unexpected live output %q", extra)
	default:
	}
	// The file itself has everything, for viewers of the finished log.
	if data, _ := os.ReadFile(path); strings.Count(string(data), "\n") != 2 {
		t.Fatalf("log file = %q", data)
	}
}
