//go:build !windows

// These tests run the commands against a real core behind the local admin
// endpoint, over its Unix socket (on Windows the endpoint is a named pipe
// with a fixed, machine-wide name that a test cannot own).

package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/localapi/localserver"
	"github.com/parthh37/nodehoster/internal/model"
)

type server struct {
	t    *testing.T
	c    *core.Core
	root string
	cl   *localapi.Client
}

func newServer(t *testing.T) *server {
	t.Helper()
	// Short: Unix socket paths are limited to ~104 bytes.
	root, err := os.MkdirTemp("", "nhcli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	boot := config.DefaultBootstrap()
	boot.Admin.Listen = "127.0.0.1:0"
	c, err := core.Open(config.NewPaths(root), boot, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Shutdown)
	// No node binary may run: deployments must not find one on PATH.
	s := c.Settings()
	s.DefaultNodeVersion = "99.0.0-test"
	if _, err := c.UpdateSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	srv := localserver.Serve(c)
	t.Cleanup(srv.Close)
	return &server{t: t, c: c, root: root, cl: localapi.Connect(localapi.Admin, root)}
}

type result struct {
	code           int
	stdout, stderr string
}

// run runs a command line as `nodehoster <args>` would.
func (s *server) run(args ...string) result {
	return s.runWith(nil, false, args...)
}

func (s *server) runWith(stdin io.Reader, interactive bool, args ...string) result {
	s.t.Helper()
	var out, errb bytes.Buffer
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	e := &Env{Stdout: &out, Stderr: &errb, Stdin: stdin, Client: s.cl, Interactive: interactive}
	code := Run(e, args)
	return result{code, out.String(), errb.String()}
}

func (r result) expect(t *testing.T, code int) result {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit code %d, want %d\nstdout: %s\nstderr: %s", r.code, code, r.stdout, r.stderr)
	}
	return r
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func (s *server) createSite(site *model.Site) localapi.Site {
	s.t.Helper()
	var out localapi.Site
	if err := s.cl.Post(context.Background(), "/api/sites", site, &out); err != nil {
		s.t.Fatal(err)
	}
	return out
}

func redirectSite(name string, port int) *model.Site {
	return &model.Site{Name: name, Type: model.SiteRedirect,
		Bindings: []model.Binding{{Protocol: "http", IP: "127.0.0.1", Port: port}},
		Redirect: &model.RedirectConfig{TargetURL: "https://example.com", StatusCode: 301}}
}

func writeZip(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for n, body := range files {
		w, _ := zw.Create(n)
		io.WriteString(w, body)
	}
	zw.Close()
	f.Close()
	return path
}

func TestParseInterspersed(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		pos        string
		n          int
		follow     bool
		wantErrStr string
	}{
		{[]string{"shop"}, "shop", 100, false, ""},
		{[]string{"shop", "-f", "-n", "5"}, "shop", 5, true, ""},
		{[]string{"-n=7", "shop", "--f"}, "shop", 7, true, ""},
		{[]string{"-f", "--", "-odd-name", "x"}, "-odd-name,x", 100, true, ""},
		{[]string{"shop", "--nope"}, "", 0, false, "flag provided but not defined: -nope"},
		{[]string{"shop", "-n", "many"}, "", 0, false, "invalid value"},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		n := fs.Int("n", 100, "")
		f := fs.Bool("f", false, "")
		pos, err := parseInterspersed(fs, tc.args)
		if tc.wantErrStr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErrStr) {
				t.Errorf("%v: error %v, want %q", tc.args, err, tc.wantErrStr)
			}
			continue
		}
		if err != nil || strings.Join(pos, ",") != tc.pos || *n != tc.n || *f != tc.follow {
			t.Errorf("%v: pos %v n %d f %v err %v", tc.args, pos, *n, *f, err)
		}
	}
}

func TestCommandTable(t *testing.T) {
	c, rest := find([]string{"site", "start", "shop"})
	if c == nil || c.Name != "site start" || len(rest) != 1 || rest[0] != "shop" {
		t.Fatalf("find = %v %v", c, rest)
	}
	if c, _ := find([]string{"site"}); c != nil {
		t.Fatal("\"site\" alone matched a command")
	}
	if !Has([]string{"site"}) || !Has([]string{"cert", "list"}) || Has([]string{"run"}) || Has([]string{"service", "start"}) || Has(nil) {
		t.Fatal("Has does not match the table")
	}
	// Every command has a unique name and a summary, and groups are in
	// the help order.
	seen := map[string]bool{}
	for _, c := range Commands() {
		if seen[c.Name] || c.Summary == "" || c.Setup == nil {
			t.Errorf("command %q duplicated or incomplete", c.Name)
		}
		seen[c.Name] = true
	}
	if cmds := Commands(); strings.Fields(cmds[0].Name)[0] != "site" || !strings.Contains(Usage(), "cert renew <id|name|domain>") {
		t.Errorf("usage:\n%s", Usage())
	}
}

func TestUsageErrors(t *testing.T) {
	s := newServer(t)
	for _, args := range [][]string{
		{"site"},
		{"site", "frobnicate"},
		{"logs"},
		{"site", "show", "a", "b"},
		{"logs", "shop", "--bogus"},
		{"deploy", "shop"},
		{"deploy", "shop", "--zip", "a.zip", "--git"},
		{"deploy", "shop", "--zip", "a.zip", "--branch", "main"},
		{"events", "-n", "0"},
	} {
		r := s.run(args...).expect(t, ExitUsage)
		if !strings.HasPrefix(r.stderr, "error: ") || r.stdout != "" {
			t.Errorf("%v: stdout %q stderr %q", args, r.stdout, r.stderr)
		}
	}
	r := s.run("site", "list", "--help").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "Usage: nodehoster site list") || !strings.Contains(r.stdout, "--json") {
		t.Errorf("help: %s", r.stdout)
	}
	r = s.run("site")
	if !strings.Contains(r.stderr, "incomplete command") || !strings.Contains(r.stderr, "nodehoster site recycle <site>") || strings.Contains(r.stderr, "cert list") {
		t.Errorf("group usage: %s", r.stderr)
	}
}

func TestNotRunning(t *testing.T) {
	root, _ := os.MkdirTemp("", "nhcli")
	defer os.RemoveAll(root)
	var out, errb bytes.Buffer
	code := Run(&Env{Stdout: &out, Stderr: &errb, Client: localapi.Connect(localapi.Admin, root)}, []string{"site", "list"})
	if code != ExitError || !strings.Contains(errb.String(), "NodeHoster is not running") || !strings.Contains(errb.String(), "nodehoster service start") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestSiteCommands(t *testing.T) {
	s := newServer(t)
	s.run("site", "list").expect(t, ExitOK)
	shop := s.createSite(redirectSite("Shop", freePort(t)))
	blog := s.createSite(redirectSite("blog", freePort(t)))

	r := s.run("site", "list").expect(t, ExitOK)
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "NAME") || !strings.HasPrefix(lines[1], "blog ") || !strings.HasPrefix(lines[2], "Shop ") ||
		!strings.Contains(lines[2], "http 127.0.0.1:") || !strings.Contains(lines[2], shop.ID) {
		t.Fatalf("site list:\n%s", r.stdout)
	}
	// --json anywhere is the API's JSON.
	for _, args := range [][]string{{"--json", "site", "list"}, {"site", "list", "--json"}} {
		r = s.run(args...).expect(t, ExitOK)
		var list []localapi.Site
		if err := json.Unmarshal([]byte(r.stdout), &list); err != nil || len(list) != 2 {
			t.Fatalf("%v: %v\n%s", args, err, r.stdout)
		}
	}

	// A site is named case-insensitively, or by ID.
	r = s.run("site", "show", "SHOP").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "ID:") || !strings.Contains(r.stdout, shop.ID) || !strings.Contains(r.stdout, "Redirect to:") {
		t.Fatalf("site show:\n%s", r.stdout)
	}
	r = s.run("site", "show", blog.ID, "--json").expect(t, ExitOK)
	var one localapi.Site
	if json.Unmarshal([]byte(r.stdout), &one) != nil || one.Name != "blog" {
		t.Fatalf("site show --json:\n%s", r.stdout)
	}
	r = s.run("site", "show", "nope").expect(t, ExitError)
	if !strings.Contains(r.stderr, `no site is named "nope"`) {
		t.Fatalf("unknown site: %s", r.stderr)
	}

	r = s.run("site", "start", "shop").expect(t, ExitOK)
	if r.stdout != "Started Shop (running).\n" {
		t.Fatalf("start: %q", r.stdout)
	}
	r = s.run("site", "stop", shop.ID, "--json").expect(t, ExitOK)
	var st model.SiteStatus
	if json.Unmarshal([]byte(r.stdout), &st) != nil || st.State != model.StateStopped {
		t.Fatalf("stop --json: %s", r.stdout)
	}
	r = s.run("site", "recycle", "shop").expect(t, ExitOK)
	if r.stdout != "Recycled Shop (running).\n" {
		t.Fatalf("recycle: %q", r.stdout)
	}

	r = s.run("events", "--site", "shop").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "site.started") || !strings.Contains(r.stdout, "Shop") || strings.Contains(r.stdout, "server.started") {
		t.Fatalf("events --site:\n%s", r.stdout)
	}
	r = s.run("events", "-n", "1", "--json").expect(t, ExitOK)
	var evs []model.Event
	if json.Unmarshal([]byte(r.stdout), &evs) != nil || len(evs) != 1 {
		t.Fatalf("events --json: %s", r.stdout)
	}
}

func TestDeployReleasesRollback(t *testing.T) {
	s := newServer(t)
	site := s.createSite(&model.Site{Name: "shop", Type: model.SiteStatic, Static: &model.StaticConfig{Root: "."}})
	dir := t.TempDir()
	v1 := writeZip(t, dir, "v1.zip", map[string]string{"index.html": "v1"})
	v2 := writeZip(t, dir, "v2.zip", map[string]string{"index.html": "v2"})

	s.run("rollback", "shop").expect(t, ExitError) // no release yet

	r := s.run("deploy", "shop", "--zip", v1).expect(t, ExitOK)
	if strings.Count(r.stdout, "extracting archive") != 1 || !strings.Contains(r.stdout, "succeeded.") {
		t.Fatalf("deploy output:\n%s", r.stdout)
	}
	// With --json the log goes to stderr and stdout is the deployment.
	r = s.run("deploy", "SHOP", "--zip", v2, "--json").expect(t, ExitOK)
	var dep2 model.Deployment
	if err := json.Unmarshal([]byte(r.stdout), &dep2); err != nil || dep2.Status != "succeeded" || !strings.Contains(r.stderr, "extracting archive") {
		t.Fatalf("deploy --json: %v\nstdout %s\nstderr %s", err, r.stdout, r.stderr)
	}

	r = s.run("releases", "shop").expect(t, ExitOK)
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "*  "+dep2.ID) || strings.HasPrefix(lines[2], "*") {
		t.Fatalf("releases:\n%s", r.stdout)
	}

	// Rollback goes to the previous successful release...
	r = s.run("rollback", "shop").expect(t, ExitOK)
	cur, _ := s.c.Site(site.ID)
	if cur.ActiveRelease == dep2.ID || cur.ActiveRelease == "" || !strings.Contains(r.stdout, cur.ActiveRelease) {
		t.Fatalf("rollback: active %q\n%s", cur.ActiveRelease, r.stdout)
	}
	// ...and there is none before the first.
	r = s.run("rollback", "shop").expect(t, ExitError)
	if !strings.Contains(r.stderr, "no earlier successful release") {
		t.Fatalf("rollback past the first: %s", r.stderr)
	}
	// A named release can be activated.
	s.run("rollback", "shop", dep2.ID, "--json").expect(t, ExitOK)
	if cur, _ = s.c.Site(site.ID); cur.ActiveRelease != dep2.ID {
		t.Fatalf("active = %q", cur.ActiveRelease)
	}
	s.run("rollback", "shop", "no-such-release").expect(t, ExitError)

	// A failed deployment is exit code 1, with the reason.
	bad := writeZip(t, dir, "bad.zip", map[string]string{"../../escape.txt": "x"})
	r = s.run("deploy", "shop", "--zip", bad).expect(t, ExitError)
	if !strings.Contains(r.stderr, "failed") || !strings.Contains(r.stderr, "escapes") {
		t.Fatalf("failed deploy: %s", r.stderr)
	}
	r = s.run("deploy", "shop", "--zip", filepath.Join(dir, "missing.zip")).expect(t, ExitError)
	// A git deployment without a repository is refused by the service.
	r = s.run("deploy", "shop", "--git").expect(t, ExitError)
	if r.stderr == "" {
		t.Fatal("no message")
	}
}

func TestLogs(t *testing.T) {
	s := newServer(t)
	s.createSite(redirectSite("shop", freePort(t)))
	r := s.run("logs", "shop", "-n", "10").expect(t, ExitOK)
	if r.stdout != "" {
		t.Fatalf("a redirect site has output: %q", r.stdout)
	}
	r = s.run("logs", "shop", "--json").expect(t, ExitOK)
	if strings.TrimSpace(r.stdout) != "[]" {
		t.Fatalf("logs --json: %q", r.stdout)
	}
	// Following stops cleanly when interrupted (Ctrl+C cancels the context).
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var out, errb bytes.Buffer
	code := Run(&Env{Stdout: &out, Stderr: &errb, Client: s.cl, Ctx: ctx}, []string{"logs", "-f", "shop", "--access"})
	if code != ExitOK {
		t.Fatalf("logs -f: exit %d, %s", code, errb.String())
	}
}

func TestCertCommands(t *testing.T) {
	s := newServer(t)
	s.run("cert", "list").expect(t, ExitOK)
	var a, b certView
	ctx := context.Background()
	if err := s.cl.Post(ctx, "/api/certificates/selfsigned", map[string]any{"name": "intranet", "domains": []string{"intranet.local", "wiki.local"}}, &a); err != nil {
		t.Fatal(err)
	}
	if err := s.cl.Post(ctx, "/api/certificates/selfsigned", map[string]any{"name": "wiki", "domains": []string{"wiki.local"}}, &b); err != nil {
		t.Fatal(err)
	}
	r := s.run("cert", "list").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "intranet.local, wiki.local") || !strings.Contains(r.stdout, a.ID) || !strings.Contains(r.stdout, "EXPIRES") {
		t.Fatalf("cert list:\n%s", r.stdout)
	}
	r = s.run("cert", "list", "--json").expect(t, ExitOK)
	var list []certView
	if json.Unmarshal([]byte(r.stdout), &list) != nil || len(list) != 2 {
		t.Fatalf("cert list --json: %s", r.stdout)
	}

	// Found by ID, name or a domain only one covers; the service then
	// refuses to renew a self-signed certificate.
	for _, ref := range []string{a.ID, "INTRANET", "intranet.local"} {
		r = s.run("cert", "renew", ref).expect(t, ExitError)
		if !strings.Contains(r.stderr, "only ACME certificates") {
			t.Errorf("renew %s: %s", ref, r.stderr)
		}
	}
	r = s.run("cert", "renew", "wiki.local").expect(t, ExitError)
	if !strings.Contains(r.stderr, "several certificates cover wiki.local") {
		t.Errorf("ambiguous: %s", r.stderr)
	}
	r = s.run("cert", "renew", "nothing.local").expect(t, ExitError)
	if !strings.Contains(r.stderr, "no certificate") {
		t.Errorf("unknown: %s", r.stderr)
	}
}

func TestBackupRestore(t *testing.T) {
	s := newServer(t)
	s.createSite(redirectSite("shop", freePort(t)))
	file := filepath.Join(t.TempDir(), "backup.json")
	r := s.run("backup", file).expect(t, ExitOK)
	if !strings.Contains(r.stdout, "Configuration saved to") {
		t.Fatalf("backup: %s", r.stdout)
	}
	st, err := os.Stat(file)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v", st, err)
	}
	data, _ := os.ReadFile(file)
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil || doc["sites"] == nil {
		t.Fatalf("backup content: %s", data)
	}
	r = s.run("backup", "-").expect(t, ExitOK)
	if !json.Valid([]byte(r.stdout)) {
		t.Fatal("backup - is not JSON")
	}

	// Restoring asks first; without a terminal, --yes is required.
	r = s.run("restore", file).expect(t, ExitUsage)
	if !strings.Contains(r.stderr, "--yes") {
		t.Fatalf("restore without --yes: %s", r.stderr)
	}
	s.runWith(strings.NewReader("n\n"), true, "restore", file).expect(t, ExitError)
	r = s.runWith(strings.NewReader("y\n"), true, "restore", file).expect(t, ExitOK)
	if !strings.Contains(r.stdout, "[y/N]") || !strings.Contains(r.stdout, "restored") {
		t.Fatalf("restore: %s", r.stdout)
	}
	s.run("restore", file, "--yes", "--json").expect(t, ExitOK)
	s.run("restore", filepath.Join(t.TempDir(), "missing.json"), "--yes").expect(t, ExitError)
	os.WriteFile(file, []byte("not a backup"), 0o600)
	s.run("restore", file, "--yes").expect(t, ExitError)
}

// TestAccessDenied: a caller the endpoint refuses (on Windows, a prompt
// that is not elevated) is told how to get in.
func TestAccessDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	s := newServer(t)
	sock := localapi.SocketPath(localapi.Admin, s.root)
	if err := os.Chmod(sock, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(sock, 0o600)
	r := s.run("site", "list").expect(t, ExitError)
	if !strings.Contains(r.stderr, "Run as administrator") {
		t.Fatalf("stderr: %s", r.stderr)
	}
}
