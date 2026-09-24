//go:build !windows

// These tests run the endpoints over Unix sockets. On Windows the same
// handlers sit behind named pipes with fixed, machine-wide names, which a
// test cannot open while a NodeHoster service is installed.

package localserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

func newServer(t *testing.T) (*core.Core, string) {
	t.Helper()
	// Short: Unix socket paths are limited to ~104 bytes.
	root, err := os.MkdirTemp("", "nhla")
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
	srv := Serve(c)
	t.Cleanup(srv.Close)
	return c, root
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

func redirectSite(name string, port int) *model.Site {
	return &model.Site{
		Name: name, Type: model.SiteRedirect,
		Bindings: []model.Binding{{Protocol: "http", IP: "127.0.0.1", Port: port}},
		Redirect: &model.RedirectConfig{TargetURL: "https://example.com", StatusCode: 301},
	}
}

func TestAdminEndpointManagesSitesWithoutLogin(t *testing.T) {
	c, root := newServer(t)
	cl := localapi.Connect(localapi.Admin, root)
	ctx := context.Background()

	var who struct{ Account string }
	if err := cl.Get(ctx, "/api/local/whoami", &who); err != nil {
		t.Fatal(err)
	}
	if who.Account == "" {
		t.Fatal("whoami returned no account")
	}

	var created localapi.Site
	if err := cl.Post(ctx, "/api/sites", redirectSite("desk", freePort(t)), &created); err != nil {
		t.Fatal(err)
	}
	if err := cl.SiteAction(ctx, created.ID, "start"); err != nil {
		t.Fatal(err)
	}
	sites, err := cl.Sites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].Name != "desk" || sites[0].Status.State != model.StateRunning {
		t.Fatalf("sites = %+v", sites)
	}

	// Changes are audited under the Windows (here: Unix) account.
	list, err := c.Store.ListAudit(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range list {
		if a.Action == "site.start" && a.User == who.Account+" (desktop)" && a.IP == "local" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no desktop audit entry for site.start in %+v", list)
	}
}

func TestAdminEndpointReportsAPIErrors(t *testing.T) {
	_, root := newServer(t)
	cl := localapi.Connect(localapi.Admin, root)
	ctx := context.Background()

	err := cl.Post(ctx, "/api/sites", &model.Site{Name: "x", Type: "bogus"}, nil)
	var apiErr *localapi.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 422 || apiErr.Field == "" {
		t.Fatalf("invalid site: err = %#v, want a 422 validation error with a field", err)
	}
	// A Windows administrator is not a NodeHoster user: account endpoints
	// are not served.
	if err := cl.Get(ctx, "/api/auth/me", nil); !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("/api/auth/me: err = %v, want 404", err)
	}
}

func TestSocketPermissions(t *testing.T) {
	_, root := newServer(t)
	for e, want := range map[localapi.Endpoint]os.FileMode{localapi.Admin: 0o600, localapi.Status: 0o666} {
		fi, err := os.Stat(localapi.SocketPath(e, root))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s socket mode = %v, want %v", e, got, want)
		}
	}
}

func TestStatusEndpoint(t *testing.T) {
	c, root := newServer(t)
	ctx := context.Background()
	s, err := c.CreateSite(ctx, redirectSite("public", freePort(t)))
	if err != nil {
		t.Fatal(err)
	}

	cl := localapi.Connect(localapi.Status, root)
	sum, err := cl.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Sites) != 1 || sum.Sites[0].Name != "public" || sum.Sites[0].ID != s.ID {
		t.Fatalf("summary = %+v", sum)
	}
	// The status endpoint is read-only and serves nothing else.
	var apiErr *localapi.Error
	if err := cl.Get(ctx, "/api/sites", nil); !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("/api/sites on the status endpoint: err = %v, want 404", err)
	}
}

func TestStatusStream(t *testing.T) {
	c, root := newServer(t)
	s, err := c.CreateSite(context.Background(), redirectSite("streamed", freePort(t)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got := make(chan string, 16)
	go localapi.Connect(localapi.Status, root).Stream(ctx, "/status/stream", func(event string, data []byte) {
		got <- event + " " + string(data)
	})
	waitFor := func(prefix, contains string) {
		t.Helper()
		for {
			select {
			case m := <-got:
				if strings.HasPrefix(m, prefix) && strings.Contains(m, contains) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("no %q event containing %q", prefix, contains)
			}
		}
	}
	waitFor("summary ", `"name":"streamed"`)

	// Notices carry the site's name; routine events are not forwarded.
	c.Bus.Info("site.started", s.ID, "started")
	c.Bus.Error("site.crashed", s.ID, "exited with code 1")
	waitFor("notice ", `"site":"streamed"`)
}

// TestRemoteDownNotice: every interactive user reads the status pipe, so a
// connected server being unreachable is told by name only, without its
// URL or the error.
func TestRemoteDownNotice(t *testing.T) {
	c, _ := newServer(t)
	s, err := c.CreateServer(context.Background(), model.ServerConnection{Name: "web02", URL: "https://10.0.0.5:8484", Token: "nh_t"})
	if err != nil {
		t.Fatal(err)
	}
	for msg, want := range map[string]string{
		"Server web02 (" + s.URL + ") is unreachable: cannot connect: connection refused": "Server web02 is unreachable",
		"Server gone (https://gone.example) is unreachable: x":                            "A connected server is unreachable",
	} {
		n, ok := notice(c, model.Event{Level: "warning", Type: events.RemoteDown, Message: msg})
		if !ok || n.Message != want || strings.Contains(n.Message, "10.0.0.5") {
			t.Errorf("notice of %q = %+v, want %q", msg, n, want)
		}
	}
}

func TestNotRunning(t *testing.T) {
	root, err := os.MkdirTemp("", "nhla")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	_, err = localapi.Connect(localapi.Status, root).Summary(context.Background())
	if !errors.Is(err, localapi.ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}
