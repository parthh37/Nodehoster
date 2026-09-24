package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/remote"
	"github.com/parthh37/nodehoster/internal/secrets"
)

// fakeRemote is a NodeHoster web console accepting the token "nh_good".
// While down is set it answers 503; tokenSeen counts requests that
// carried a token.
type fakeRemote struct {
	*httptest.Server
	down      atomic.Bool
	tokenSeen atomic.Int32
}

func newFakeRemote(t *testing.T) *fakeRemote {
	t.Helper()
	f := &fakeRemote{}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			f.tokenSeen.Add(1)
		}
		if f.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Authorization") != "Bearer nh_good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/server/info":
			w.Write([]byte(`{"version":"2.0.0","hostname":"WEB02"}`))
		case "/api/sites":
			w.Write([]byte(`[{"status":{"state":"running"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeRemote) fingerprint() string { return remote.Fingerprint(f.Certificate().Raw) }

func TestServerConnectionsCRUD(t *testing.T) {
	c := testCore(t)
	ctx := context.Background()
	v, err := c.CreateServer(ctx, model.ServerConnection{Name: "web02", URL: "https://web02:8484/", Token: "nh_secret"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Token != secrets.Mask || v.URL != "https://web02:8484" || v.MinRole != model.RoleAdmin || v.ID == "" {
		t.Fatalf("created %+v", v)
	}
	stored, _ := c.ServerConnection(v.ID)
	if !secrets.IsSealed(stored.Token) || c.Box.MustUnseal(stored.Token) != "nh_secret" {
		t.Fatalf("token stored as %q", stored.Token)
	}
	var doc []model.ServerConnection
	if err := c.Store.GetDoc(ctx, serversDoc, &doc); err != nil || len(doc) != 1 || strings.Contains(doc[0].Token, "nh_secret") {
		t.Fatalf("document %+v, %v", doc, err)
	}

	// A duplicate name, a masked token for a new connection.
	var ve *model.ValidationError
	if _, err := c.CreateServer(ctx, model.ServerConnection{Name: "WEB02", URL: "https://web03", Token: "x"}); !errors.As(err, &ve) || ve.Field != "name" {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := c.CreateServer(ctx, model.ServerConnection{Name: "web03", URL: "https://web03", Token: secrets.Mask}); !errors.As(err, &ve) || ve.Field != "token" {
		t.Fatalf("masked token: %v", err)
	}

	// The mask keeps the token, while the URL reaches the same server.
	v.Name, v.MinRole, v.URL = "Web 02", model.RoleOperator, "https://web02:8484/console"
	u, err := c.UpdateServer(ctx, v.ID, v.ServerConnection)
	if err != nil || u.Name != "Web 02" || u.MinRole != model.RoleOperator {
		t.Fatalf("update: %+v, %v", u, err)
	}
	if again, _ := c.ServerConnection(v.ID); again.Token != stored.Token {
		t.Fatal("the masked token was not kept")
	}
	// Another host: the token must be entered again.
	u.URL = "https://evil.example.com"
	if _, err := c.UpdateServer(ctx, v.ID, u.ServerConnection); !errors.As(err, &ve) || ve.Field != "token" {
		t.Fatalf("masked token to another host: %v", err)
	}
	u.Token = "nh_new"
	if _, err := c.UpdateServer(ctx, v.ID, u.ServerConnection); err != nil {
		t.Fatal(err)
	}
	if again, _ := c.ServerConnection(v.ID); c.Box.MustUnseal(again.Token) != "nh_new" {
		t.Fatal("the new token was not stored")
	}

	if _, err := c.UpdateServer(ctx, "nope", u.ServerConnection); !errors.Is(err, ErrServerNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	if _, err := c.DeleteServer(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteServer(ctx, v.ID); !errors.Is(err, ErrServerNotFound) {
		t.Fatalf("delete again: %v", err)
	}
	if len(c.ServerViews(nil)) != 0 {
		t.Fatal("not deleted")
	}
}

func countEvents(t *testing.T, c *Core, typ string) int {
	t.Helper()
	list, err := c.Store.ListEvents(context.Background(), "", 500)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range list {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func TestServerHealthAndEvents(t *testing.T) {
	c := testCore(t)
	ctx := context.Background()
	f := newFakeRemote(t)
	v, err := c.CreateServer(ctx, model.ServerConnection{Name: "web02", URL: f.URL, Token: "nh_good", Fingerprint: f.fingerprint()})
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.CheckServer(ctx, v.ID)
	if err != nil || !h.Reachable || h.Version != "2.0.0" || h.Running != 1 || h.Since == nil {
		t.Fatalf("health %+v, %v", h, err)
	}
	since := *h.Since

	f.down.Store(true)
	h, _ = c.CheckServer(ctx, v.ID)
	if h.Reachable || !strings.Contains(h.Error, "503") || countEvents(t, c, events.RemoteDown) != 0 {
		t.Fatalf("one failure: %+v, events %d", h, countEvents(t, c, events.RemoteDown))
	}
	if !h.Since.After(since) {
		t.Fatal("since did not move with the state")
	}
	for range 3 {
		c.CheckServer(ctx, v.ID)
	}
	if n := countEvents(t, c, events.RemoteDown); n != 1 {
		t.Fatalf("remote.down raised %d times, want once", n)
	}
	f.down.Store(false)
	c.CheckServer(ctx, v.ID)
	c.CheckServer(ctx, v.ID)
	if n := countEvents(t, c, events.RemoteUp); n != 1 {
		t.Fatalf("remote.up raised %d times, want once", n)
	}
	if views := c.ServerViews(nil); len(views) != 1 || !views[0].Health.Reachable || views[0].Token != secrets.Mask {
		t.Fatalf("views %+v", views)
	}

	// The monitor checks every connection; a cancelled check records
	// nothing (shutting down is not an outage).
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	f.down.Store(true)
	for range 3 {
		c.checkAllServers(cctx)
		c.CheckServer(cctx, v.ID)
	}
	if views := c.ServerViews(nil); !views[0].Health.Reachable {
		t.Fatalf("a cancelled check was recorded: %+v", views[0].Health)
	}
	if _, err := c.CheckServer(ctx, "nope"); !errors.Is(err, ErrServerNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestTestServer(t *testing.T) {
	c := testCore(t)
	ctx := context.Background()
	f := newFakeRemote(t)

	// Not pinned: the certificate is shown, the token is not sent.
	res, err := c.TestServer(ctx, model.ServerTest{URL: f.URL, Token: "nh_good"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Trusted || res.Certificate == nil || res.Certificate.Fingerprint != f.fingerprint() || res.Health.Reachable || f.tokenSeen.Load() != 0 {
		t.Fatalf("untrusted: %+v (tokens seen %d)", res, f.tokenSeen.Load())
	}
	// Pinned: the token is tried.
	res, err = c.TestServer(ctx, model.ServerTest{URL: f.URL, Token: "nh_good", Fingerprint: model.FormatFingerprint(f.fingerprint())})
	if err != nil || !res.Trusted || !res.Health.Reachable || res.Health.Hostname != "WEB02" {
		t.Fatalf("pinned: %+v, %v", res, err)
	}
	// Pinned to another certificate: refused before the token is sent.
	seen := f.tokenSeen.Load()
	res, _ = c.TestServer(ctx, model.ServerTest{URL: f.URL, Token: "nh_good", Fingerprint: strings.Repeat("0", 64)})
	if res.Trusted || res.Health.Reachable || f.tokenSeen.Load() != seen {
		t.Fatalf("wrong pin: %+v", res)
	}

	// The stored token of a connection, only towards the same server.
	v, err := c.CreateServer(ctx, model.ServerConnection{Name: "web02", URL: f.URL, Token: "nh_good", Fingerprint: f.fingerprint()})
	if err != nil {
		t.Fatal(err)
	}
	res, err = c.TestServer(ctx, model.ServerTest{ID: v.ID, URL: f.URL, Token: secrets.Mask, Fingerprint: f.fingerprint()})
	if err != nil || !res.Health.Reachable {
		t.Fatalf("stored token: %+v, %v", res, err)
	}
	var ve *model.ValidationError
	if _, err := c.TestServer(ctx, model.ServerTest{ID: v.ID, URL: "https://elsewhere.example", Token: secrets.Mask}); !errors.As(err, &ve) || ve.Field != "token" {
		t.Fatalf("stored token to another server: %v", err)
	}
	if _, err := c.TestServer(ctx, model.ServerTest{URL: "http://web02", Token: "x"}); !errors.As(err, &ve) || ve.Field != "url" {
		t.Fatalf("plain http: %v", err)
	}
}

// TestServersInBackup: connections travel in configuration backups with
// their tokens sealed; a backup from before connections existed keeps
// this server's.
func TestServersInBackup(t *testing.T) {
	c := testCore(t)
	ctx := context.Background()
	if _, err := c.CreateServer(ctx, model.ServerConnection{Name: "web02", URL: "https://web02:8484", Token: "nh_secret"}); err != nil {
		t.Fatal(err)
	}
	data, err := c.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "nh_secret") || !strings.Contains(string(data), `"servers"`) {
		t.Fatalf("backup:\n%s", data)
	}

	// Without the key: kept.
	var doc map[string]any
	json.Unmarshal(data, &doc)
	delete(doc, "servers")
	old, _ := json.Marshal(doc)
	if err := c.Restore(ctx, old); err != nil {
		t.Fatal(err)
	}
	if len(c.ServerConnections()) != 1 {
		t.Fatal("an old backup removed the connections")
	}
	// Empty: removed. Then the backup brings them back, token readable.
	doc["servers"] = []any{}
	none, _ := json.Marshal(doc)
	if err := c.Restore(ctx, none); err != nil || len(c.ServerConnections()) != 0 {
		t.Fatalf("empty list: %v, %d", err, len(c.ServerConnections()))
	}
	if err := c.Restore(ctx, data); err != nil {
		t.Fatal(err)
	}
	list := c.ServerConnections()
	if len(list) != 1 || c.Box.MustUnseal(list[0].Token) != "nh_secret" {
		t.Fatalf("restored %+v", list)
	}
}
