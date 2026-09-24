package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

// fakeServer answers like a NodeHoster web console for the token "good".
func fakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer good" {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"not signed in"}`))
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/server/info", auth(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(model.ServerInfo{Version: "9.9.9", Hostname: "WEB02", CPUPercent: 12.5, CPUCount: 4, MemTotal: 8 << 30, MemUsed: 2 << 30})
	}))
	mux.HandleFunc("/api/sites", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"a","status":{"state":"running"}},{"id":"b","status":{"state":"failed"}},
			{"id":"c","status":{"state":"stopped"}},{"id":"d","status":{"state":"running"}},{"id":"e","status":{"state":"degraded"}}]`))
	}))
	mux.HandleFunc("/api/auth/me", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"user":{"username":"hub","role":"admin"},"access":{"role":"operator"}}`))
	}))
	s := httptest.NewTLSServer(mux)
	t.Cleanup(s.Close)
	return s
}

func clientFor(t *testing.T, base, fp string) *http.Client {
	t.Helper()
	tr, err := NewTransport(base, fp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.CloseIdleConnections)
	return NewClient(tr)
}

func TestProbeAndPinning(t *testing.T) {
	t.Parallel()
	s := fakeServer(t)
	pc, err := Probe(context.Background(), s.URL)
	if err != nil {
		t.Fatal(err)
	}
	want := Fingerprint(s.Certificate().Raw)
	if pc.Fingerprint != want || pc.Verified || pc.VerifyError == "" || pc.NotAfter.IsZero() {
		t.Fatalf("probe = %+v, want fingerprint %s, not verified", pc, want)
	}

	// Pinned: the self-signed certificate is accepted.
	h := Check(context.Background(), clientFor(t, s.URL, want), s.URL, "good")
	if !h.Reachable || h.Version != "9.9.9" || h.Hostname != "WEB02" || h.CPUCount != 4 || h.MemUsed != 2<<30 {
		t.Fatalf("health = %+v", h)
	}
	if h.Sites != 5 || h.Running != 2 || h.Failed != 1 || h.Stopped != 1 || h.Degraded != 1 {
		t.Fatalf("site counts = %+v", h)
	}
	if h.User != "hub" || h.Role != model.RoleOperator || h.CheckedAt == nil {
		t.Fatalf("identity = %+v", h)
	}

	// Not pinned: refused as untrusted.
	h = Check(context.Background(), clientFor(t, s.URL, ""), s.URL, "good")
	if h.Reachable || !strings.Contains(h.Error, "not issued by a trusted authority") {
		t.Fatalf("unpinned: %+v", h)
	}
	// Another pin: refused, naming the certificate presented.
	other := strings.Repeat("AB", 32)
	h = Check(context.Background(), clientFor(t, s.URL, other), s.URL, "good")
	if h.Reachable || !strings.Contains(h.Error, "does not match the pinned fingerprint") || !strings.Contains(h.Error, model.FormatFingerprint(want)) {
		t.Fatalf("wrong pin: %+v", h)
	}
	// A wrong token.
	h = Check(context.Background(), clientFor(t, s.URL, want), s.URL, "bad")
	if h.Reachable || !strings.Contains(h.Error, "refused the API token") {
		t.Fatalf("bad token: %+v", h)
	}
}

func TestCheckUnreachableAndNotNodeHoster(t *testing.T) {
	t.Parallel()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect/api/server/info" {
			http.Redirect(w, r, "https://elsewhere.example/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	h := Check(context.Background(), clientFor(t, s.URL, ""), s.URL, "t")
	if h.Reachable || !strings.Contains(h.Error, "does not answer like a NodeHoster web console") {
		t.Fatalf("404: %+v", h)
	}
	h = Check(context.Background(), clientFor(t, s.URL+"/redirect", ""), s.URL+"/redirect", "t")
	if h.Reachable || !strings.Contains(h.Error, "redirects to https://elsewhere.example/") {
		t.Fatalf("redirect: %+v", h)
	}
	url := s.URL
	s.Close()
	h = Check(context.Background(), clientFor(t, url, ""), url, "t")
	if h.Reachable || h.Error == "" || h.CheckedAt == nil {
		t.Fatalf("closed: %+v", h)
	}
	if pc, err := Probe(context.Background(), url); pc != nil || err != nil {
		t.Fatalf("probe over http: %+v, %v", pc, err) // no certificate to show
	}
	if _, err := Probe(context.Background(), strings.Replace(url, "http:", "https:", 1)); err == nil {
		t.Fatal("probe of a closed port succeeded")
	}
}

// TestCheckRoleLimits: a server that echoes the role limit applies it; an
// older one ignores it.
func TestCheckRoleLimits(t *testing.T) {
	t.Parallel()
	for _, echo := range []bool{true, false} {
		var sent string
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/server/info":
				w.Write([]byte(`{"version":"1.0.0"}`))
			case "/api/auth/me":
				sent = r.Header.Get(model.RoleLimitHeader)
				if echo && sent != "" {
					w.Header().Set(model.RoleLimitAppliedHeader, sent)
				}
				w.Write([]byte(`{"user":{"username":"hub","role":"admin"},"access":{"role":"admin"}}`))
			default:
				w.Write([]byte(`[]`))
			}
		}))
		h := Check(context.Background(), clientFor(t, s.URL, ""), s.URL, "t")
		s.Close()
		if !h.Reachable || h.User != "hub" || h.Role != model.RoleAdmin || h.RoleLimits != echo {
			t.Errorf("echo %v: %+v", echo, h)
		}
		if sent != string(model.RoleAdmin) {
			t.Errorf("the check sent the limit %q, want admin (which takes nothing away)", sent)
		}
	}
}

func TestSavedConnections(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "NodeHoster", "connections.json")
	if list, err := LoadSavedFrom(p); err != nil || list != nil {
		t.Fatalf("no file: %v, %v", list, err)
	}
	a := Saved{Name: "web02", URL: "WEB02:8484", Token: "nh_secret", Fingerprint: strings.Repeat("ab", 32)}
	if err := a.Validate(nil); err != nil {
		t.Fatal(err)
	}
	b := Saved{URL: "https://web03.example.com:8484/", Token: "nh_other"}
	if err := b.Validate([]Saved{a}); err != nil || b.Name != "web03.example.com:8484" {
		t.Fatalf("default name: %+v, %v", b, err)
	}
	if err := StoreSavedTo(p, []Saved{a, b}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	// Protected with DPAPI for the Windows account; elsewhere the file is
	// its only protection.
	prefix := `"token": "plain:`
	if runtime.GOOS == "windows" {
		prefix = `"token": "dpapi:`
	}
	if strings.Contains(string(raw), "nh_secret\"") || !strings.Contains(string(raw), prefix) {
		t.Fatalf("stored:\n%s", raw)
	}
	if fi, err := os.Stat(p); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("mode: %v, %v", fi.Mode(), err)
	}
	list, err := LoadSavedFrom(p)
	if err != nil || len(list) != 2 || list[0].Token != "nh_secret" || list[0].URL != "https://web02:8484" || list[1].Token != "nh_other" {
		t.Fatalf("loaded %+v, %v", list, err)
	}
	if s, ok := FindSaved(list, "WEB02"); !ok || s.Name != "web02" {
		t.Fatal("by name")
	}
	if s, ok := FindSaved(list, "web03.example.com:8484"); !ok || s.Token != "nh_other" {
		t.Fatal("by URL")
	}
	if _, ok := FindSaved(list, "web04"); ok {
		t.Fatal("found a missing one")
	}

	for _, bad := range []Saved{
		{Name: "WEB02", URL: "https://x", Token: "t"},
		{Name: "x", URL: "http://web05", Token: "t"},
		{Name: "x", URL: "https://web05", Token: ""},
		{Name: "local", URL: "https://web05", Token: "t"},
		{Name: "x", URL: "https://web05", Token: "t", Fingerprint: "12"},
	} {
		if err := bad.Validate(list); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
