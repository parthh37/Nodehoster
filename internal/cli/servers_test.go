//go:build !windows

package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/api"
	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/remote"
	"github.com/parthh37/nodehoster/internal/store"
)

// TestRemoteServerCommands saves a connection to a real server's web
// console (self-signed, trusted on first use) and runs commands against it
// the way `nodehoster --server web02 ...` does. Not parallel: it replaces
// where connections are saved.
func TestRemoteServerCommands(t *testing.T) {
	root, err := os.MkdirTemp("", "nhrem")
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
	ctx := context.Background()
	u := &store.UserRecord{User: model.User{ID: uuid.NewString(), Username: "ops", Role: model.RoleOperator}}
	if err := c.Store.PutUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	token, _, err := c.Auth.CreateToken(ctx, u.ID, "cli", 0)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(api.Handler(c))
	defer srv.Close()
	if _, err := c.CreateSite(ctx, &model.Site{Name: "remote-shop", Type: model.SiteRedirect, Redirect: &model.RedirectConfig{TargetURL: "https://example.com", StatusCode: 301}}); err != nil {
		t.Fatal(err)
	}

	saved := filepath.Join(t.TempDir(), "connections.json")
	old := savedPath
	savedPath = func() (string, error) { return saved, nil }
	t.Cleanup(func() { savedPath = old })

	run := func(client *localapi.Client, stdin string, interactive bool, args ...string) result {
		t.Helper()
		var out, errb bytes.Buffer
		e := &Env{Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(stdin), Client: client, Interactive: interactive}
		return result{Run(e, args), out.String(), errb.String()}
	}
	local := localapi.Connect(localapi.Admin, root) // not listening: the server commands do not use it

	// Not interactive (token from the environment): the untrusted
	// certificate is shown, nothing saved.
	t.Setenv(tokenEnv, token)
	r := run(local, "", false, "server", "add", "web02", srv.URL)
	os.Unsetenv(tokenEnv)
	r.expect(t, ExitUsage)
	fp := remote.Fingerprint(srv.Certificate().Raw)
	if !strings.Contains(r.stdout+r.stderr, model.FormatFingerprint(fp)) || !strings.Contains(r.stderr, "--fingerprint") {
		t.Fatalf("no fingerprint shown:\n%s\n%s", r.stdout, r.stderr)
	}
	// Refusing the certificate saves nothing either.
	run(local, token+"\nn\n", true, "server", "add", "web02", srv.URL).expect(t, ExitError)
	if list, _ := remote.LoadSavedFrom(saved); len(list) != 0 {
		t.Fatalf("saved %+v", list)
	}
	// Token asked, certificate trusted.
	r = run(local, token+"\ny\n", true, "server", "add", "web02", srv.URL).expect(t, ExitOK)
	if !strings.Contains(r.stdout, "as ops (operator)") {
		t.Fatalf("add:\n%s", r.stdout)
	}
	raw, _ := os.ReadFile(saved)
	if strings.Contains(string(raw), token) {
		t.Fatal("the token is saved in clear") // plain: base64 outside Windows, DPAPI on Windows
	}
	// A wrong pin is refused.
	run(local, token+"\n", true, "server", "add", "web03", srv.URL, "--fingerprint", strings.Repeat("00", 32)).expect(t, ExitError)

	r = run(local, "", false, "--json", "server", "list").expect(t, ExitOK)
	if !strings.Contains(r.stdout, `"name": "web02"`) || !strings.Contains(r.stdout, fp) || !strings.Contains(r.stdout, `"tokenSaved": true`) {
		t.Fatalf("list:\n%s", r.stdout)
	}
	r = run(local, "", false, "server", "test", "WEB02").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "Sites: 1") {
		t.Fatalf("test:\n%s", r.stdout)
	}

	// --server web02: the commands run there, with the token's role.
	cl, err := remoteClient("web02", "")
	if err != nil {
		t.Fatal(err)
	}
	r = run(cl, "", false, "site", "list").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "remote-shop") {
		t.Fatalf("remote site list:\n%s", r.stdout)
	}
	run(cl, "", false, "site", "start", "remote-shop").expect(t, ExitOK)
	r = run(cl, "", false, "backup", filepath.Join(t.TempDir(), "b.json")).expect(t, ExitError) // operators cannot
	if !strings.Contains(r.stderr, "role") {
		t.Fatalf("backup as operator: %s", r.stderr)
	}
	r = run(cl, "", false, "deps").expect(t, ExitUsage)
	if !strings.Contains(r.stderr, "cannot target --server") {
		t.Fatalf("deps: %s", r.stderr)
	}

	// A URL needs a token; a bad one is refused with a clear message.
	if _, err := remoteClient("https://web09.example:8484", ""); err == nil || !strings.Contains(err.Error(), "needs an API token") {
		t.Fatalf("URL without token: %v", err)
	}
	if _, err := remoteClient("web09", ""); err == nil || !strings.Contains(err.Error(), "no connection is named") {
		t.Fatalf("unknown name: %v", err)
	}
	t.Setenv(fingerprintEnv, fp)
	bad, err := remoteClient(srv.URL, "nh_wrong")
	if err != nil {
		t.Fatal(err)
	}
	r = run(bad, "", false, "site", "list").expect(t, ExitError)
	if !strings.Contains(r.stderr, "refused the API token") {
		t.Fatalf("bad token: %s", r.stderr)
	}

	run(local, "", false, "server", "remove", "web02").expect(t, ExitOK)
	run(local, "", false, "server", "remove", "web02").expect(t, ExitError)
}
