package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/logship"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
)

// logShipping stores log shipping settings through the API.
func (e *env) logShipping(admin []opt, targets []any) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := e.do(http.MethodGet, "/api/settings", nil, admin...)
	expect(e.t, rec, http.StatusOK)
	s := decodeJSON[map[string]any](e.t, rec)
	s["logShipping"] = map[string]any{"targets": targets}
	return e.do(http.MethodPut, "/api/settings", s, admin...)
}

// sink is an HTTP collector for shipped records.
type sink struct {
	mu   sync.Mutex
	recs []logship.Record
	auth []string
}

func (s *sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var batch []logship.Record
	json.NewDecoder(r.Body).Decode(&batch)
	s.mu.Lock()
	s.recs = append(s.recs, batch...)
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	s.mu.Unlock()
}

func (s *sink) find(source, text string) *logship.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.recs {
		if s.recs[i].Source == source && strings.Contains(s.recs[i].Message, text) {
			return &s.recs[i]
		}
	}
	return nil
}

func TestLogShippingSettingsAndDelivery(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	col := &sink{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	rec := e.logShipping(admin, []any{
		map[string]any{"name": "collector", "type": "http", "enabled": true, "sources": []string{"app", "audit", "event"},
			"http": map[string]any{"url": srv.URL, "format": "json", "headers": []any{map[string]any{"name": "Authorization", "value": "Bearer s3cret", "secret": true}}}},
		map[string]any{"name": "seq", "type": "seq", "enabled": false, "sources": []string{"server"}, "seq": map[string]any{"url": "https://seq.example", "apiKey": "seq-key"}},
	})
	expect(t, rec, http.StatusOK)
	body := rec.Body.String()
	if strings.Contains(body, "s3cret") || strings.Contains(body, "seq-key") || strings.Count(body, secrets.Mask) != 2 {
		t.Errorf("secrets not masked: %s", body)
	}
	stored := e.c.Settings().LogShipping.Targets
	if !secrets.IsSealed(stored[1].Seq.APIKey) || stored[0].ID == "" || stored[1].MinLevel != "info" {
		t.Errorf("stored = %+v", stored)
	}
	// Saving the masked settings back keeps the secrets.
	s := decodeJSON[map[string]any](t, e.do(http.MethodGet, "/api/settings", nil, admin...))
	expect(t, e.do(http.MethodPut, "/api/settings", s, admin...), http.StatusOK)
	if e.c.Box.MustUnseal(e.c.Settings().LogShipping.Targets[0].HTTP.Headers[0].Value) != "Bearer s3cret" {
		t.Error("the masked header value was not kept")
	}

	// Audit entries, events and the sites' output arrive, with the site's name.
	site := e.createSite(admin, redirectSite("shipped", 0))
	e.c.Procs.Logs(site.ID).System("hello from the app")
	e.c.Bus.Info("site.started", site.ID, "shipped started")
	deadline := time.Now().Add(10 * time.Second)
	for col.find("app", "hello from the app") == nil || col.find("audit", "site.create") == nil || col.find("event", "shipped started") == nil {
		if time.Now().After(deadline) {
			t.Fatalf("records not delivered: %+v", col.recs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if r := col.find("app", "hello"); r.SiteName != "shipped" || r.Stream != "system" || r.SiteID != site.ID {
		t.Errorf("app record = %+v", r)
	}
	if r := col.find("audit", "site.create"); r.Attrs["user"] != "root" || r.Attrs["action"] != "site.create" {
		t.Errorf("audit record = %+v", r)
	}
	col.mu.Lock()
	if col.auth[0] != "Bearer s3cret" {
		t.Errorf("Authorization = %q", col.auth[0])
	}
	col.mu.Unlock()

	rec = e.do(http.MethodGet, "/api/logshipping/status", nil, admin...)
	expect(t, rec, http.StatusOK)
	st := decodeJSON[[]logship.Status](t, rec)
	if len(st) != 2 || st[0].Sent == 0 || !st[0].Enabled || st[1].Enabled {
		t.Errorf("status = %+v", st)
	}

	// Test: with the saved (masked) header, and against a dead endpoint.
	target := s["logShipping"].(map[string]any)["targets"].([]any)[0]
	expect(t, e.do(http.MethodPost, "/api/logshipping/test", target, admin...), http.StatusNoContent)
	if r := col.find("server", "Test message"); r == nil {
		t.Error("the test message did not arrive")
	}
	dead := map[string]any{"name": "dead", "type": "http", "sources": []string{"app"}, "http": map[string]any{"url": "http://127.0.0.1:1/x"}}
	rec = e.do(http.MethodPost, "/api/logshipping/test", dead, admin...)
	expect(t, rec, http.StatusBadGateway)

	// Validation.
	rec = e.logShipping(admin, []any{map[string]any{"name": "sys", "type": "syslog", "enabled": true, "sources": []string{"server"}, "syslog": map[string]any{"address": "no-port"}}})
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "logShipping.targets[0].syslog.address" {
		t.Errorf("field = %q", f)
	}
}

// A header turned from secret to plain text comes back masked: its sealed
// value must neither be stored as the plain value (then shown and sent as
// is) nor decrypted, so the value has to be entered again.
func TestLogShippingHeaderSecretToPlain(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	expect(t, e.logShipping(admin, []any{
		map[string]any{"name": "collector", "type": "http", "enabled": false, "sources": []string{"app"},
			"http": map[string]any{"url": "https://logs.example/in", "headers": []any{map[string]any{"name": "X-Key", "value": "k3y", "secret": true}}}},
	}), http.StatusOK)

	s := decodeJSON[map[string]any](t, e.do(http.MethodGet, "/api/settings", nil, admin...))
	target := s["logShipping"].(map[string]any)["targets"].([]any)[0].(map[string]any)
	hd := target["http"].(map[string]any)["headers"].([]any)[0].(map[string]any)
	hd["secret"] = false // value still the mask
	rec := e.do(http.MethodPut, "/api/settings", s, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "logShipping.targets[0].http.headers[0].value" {
		t.Errorf("field = %q", f)
	}
	expect(t, e.do(http.MethodPost, "/api/logshipping/test", target, admin...), http.StatusUnprocessableEntity)
	if h := e.c.Settings().LogShipping.Targets[0].HTTP.Headers[0]; !h.Secret || e.c.Box.MustUnseal(h.Value) != "k3y" {
		t.Errorf("stored header = %+v", h)
	}

	// Entered again, it is stored as plain text.
	hd["value"] = "plain-value"
	expect(t, e.do(http.MethodPut, "/api/settings", s, admin...), http.StatusOK)
	if h := e.c.Settings().LogShipping.Targets[0].HTTP.Headers[0]; h.Secret || h.Value != "plain-value" {
		t.Errorf("stored header = %+v", h)
	}
}

func TestSiteLogSearch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	site := e.createSite(admin, redirectSite("searchable", 0))
	other := e.createSite(admin, redirectSite("other", 0))
	sink := e.c.Procs.Logs(site.ID)
	base := time.Now().Add(-time.Hour)
	for i := range 50 {
		stream := "stdout"
		if i%5 == 0 {
			stream = "stderr"
		}
		sink.Write(model.LogLine{Time: base.Add(time.Duration(i) * time.Minute), Stream: stream, Instance: 0, Text: "Order " + string(rune('A'+i%26)) + " processed"})
	}
	search := func(opts []opt, siteID string, params url.Values) *httptest.ResponseRecorder {
		return e.do(http.MethodGet, "/api/sites/"+siteID+"/logs/search?"+params.Encode(), nil, opts...)
	}
	res := func(rec *httptest.ResponseRecorder) SearchResult {
		t.Helper()
		expect(t, rec, http.StatusOK)
		return decodeJSON[SearchResult](t, rec)
	}

	r := res(search(admin, site.ID, url.Values{"q": {"order b"}}))
	if len(r.Lines) != 2 || r.Lines[0].Text != "Order B processed" || !r.Lines[0].Time.After(r.Lines[1].Time) {
		t.Errorf("substring search = %+v", r.Lines)
	}
	// stderr has lines 0, 5, … 45: letters A F K P U Z E J O T.
	r = res(search(admin, site.ID, url.Values{"q": {`^Order [A-F] `}, "regex": {"1"}, "stream": {"stderr"}}))
	if len(r.Lines) != 3 || r.Lines[0].Text != "Order E processed" || r.Lines[2].Text != "Order A processed" || r.Lines[0].Stream != "stderr" {
		t.Errorf("regex + stream = %+v", r.Lines)
	}
	r = res(search(admin, site.ID, url.Values{"limit": {"5"}}))
	if len(r.Lines) != 5 || r.Cursor == "" || r.Truncated {
		t.Errorf("limit = %d lines, cursor %q", len(r.Lines), r.Cursor)
	}
	r2 := res(search(admin, site.ID, url.Values{"limit": {"5"}, "cursor": {r.Cursor}}))
	if len(r2.Lines) != 5 || !r2.Lines[0].Time.Before(r.Lines[4].Time) {
		t.Errorf("next page = %+v", r2.Lines)
	}
	r = res(search(admin, site.ID, url.Values{"since": {"10m"}}))
	if len(r.Lines) != 0 {
		t.Errorf("since 10m found %d (the lines are older)", len(r.Lines))
	}
	r = res(search(admin, site.ID, url.Values{"since": {base.Add(45 * time.Minute).Format(time.RFC3339)}}))
	if len(r.Lines) != 5 {
		t.Errorf("since = %d lines", len(r.Lines))
	}
	expect(t, search(admin, site.ID, url.Values{"q": {"(unclosed"}, "regex": {"1"}}), http.StatusUnprocessableEntity)
	expect(t, search(admin, site.ID, url.Values{"q": {strings.Repeat("a", 600)}, "regex": {"1"}}), http.StatusUnprocessableEntity)
	expect(t, search(admin, site.ID, url.Values{"cursor": {"nope"}}), http.StatusUnprocessableEntity)
	expect(t, search(admin, site.ID, url.Values{"source": {"db"}}), http.StatusUnprocessableEntity)
	res(search(admin, site.ID, url.Values{"source": {"access"}}))

	// Access: a viewer grant on the site is enough; other sites are not found.
	viewer := session(e.login(e.scoped("agency", grant(site.ID, model.RoleViewer)).Username))
	res(search(viewer, site.ID, url.Values{"q": {"order"}}))
	expect(t, search(viewer, other.ID, url.Values{}), http.StatusNotFound)
	// The server log is for administrators.
	expect(t, e.do(http.MethodGet, "/api/server/logs/search", nil, viewer...), http.StatusForbidden)
	sv := session(e.login(e.user("watcher", model.RoleViewer, false).Username))
	expect(t, e.do(http.MethodGet, "/api/server/logs/search", nil, sv...), http.StatusForbidden)
	expect(t, e.do(http.MethodGet, "/api/logshipping/status", nil, sv...), http.StatusForbidden)
	expect(t, e.do(http.MethodPost, "/api/logshipping/test", map[string]any{}, sv...), http.StatusForbidden)
	res(search(sv, site.ID, url.Values{}))
}

func TestServerLogSearch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	// The harness logs to io.Discard: write a server log file directly.
	lines := []string{
		`time=2026-03-01T02:30:00.000Z level=INFO msg="starting NodeHoster" version=dev`,
		`time=2026-03-01T02:31:00.000Z level=WARN msg="certificate files unreadable" cert=x`,
		`time=2026-03-01T02:32:00.000Z level=ERROR msg="webhook delivery failed" err="HTTP 500"`,
	}
	if err := os.WriteFile(filepath.Join(e.c.Paths.Logs, "nodehoster.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	rec := e.do(http.MethodGet, "/api/server/logs/search?level=warning", nil, admin...)
	expect(t, rec, http.StatusOK)
	r := decodeJSON[SearchResult](t, rec)
	if len(r.Lines) != 2 || r.Lines[0].Stream != "error" || r.Lines[1].Stream != "warning" {
		t.Errorf("level=warning = %+v", r.Lines)
	}
	rec = e.do(http.MethodGet, "/api/server/logs/search?q=STARTING", nil, admin...)
	if r := decodeJSON[SearchResult](t, rec); len(r.Lines) != 1 || r.Lines[0].Stream != "info" {
		t.Errorf("q = %+v", r.Lines)
	}
	expect(t, e.do(http.MethodGet, "/api/server/logs/search?level=loud", nil, admin...), http.StatusUnprocessableEntity)
}

// TestSiteLogClear: clearing works whether or not the site has written a
// log file yet (a site that never ran has none).
func TestSiteLogClear(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	site := e.createSite(admin, redirectSite("quiet", 0))
	sink := e.c.Procs.Logs(site.ID)
	if _, err := os.Stat(sink.Path()); !os.IsNotExist(err) {
		t.Fatalf("a new site has a log file: %v", err)
	}
	expect(t, e.do(http.MethodPost, "/api/sites/"+site.ID+"/logs/clear", nil, admin...), http.StatusNoContent)

	sink.Write(model.LogLine{Time: time.Now(), Stream: "stdout", Text: "hello"})
	expect(t, e.do(http.MethodPost, "/api/sites/"+site.ID+"/logs/clear", nil, admin...), http.StatusNoContent)
	if st, err := os.Stat(sink.Path()); err != nil || st.Size() != 0 {
		t.Fatalf("log file after clear: %v %v", st, err)
	}
	if n := len(sink.Recent(0)); n != 0 {
		t.Fatalf("%d recent lines after clear", n)
	}
}
