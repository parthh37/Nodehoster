package api

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// TestResponseCachePurge fills a proxy site's cache through a real
// listener, reads the statistics in the site status and purges it, with
// the purge limited to operators of the site.
func TestResponseCachePurge(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=300")
		io.WriteString(w, "page "+r.URL.Path)
	}))
	defer up.Close()
	port := freePort(t)
	site := e.createSite(admin, map[string]any{
		"name": "cached", "type": "proxy", "autoStart": true,
		"bindings": []map[string]any{{"protocol": "http", "ip": "127.0.0.1", "port": port}},
		"proxy":    map[string]any{"upstreams": []map[string]any{{"url": up.URL}}},
		"routing":  map[string]any{"cache": map[string]any{"enabled": true}},
	})
	if c := site.Routing.Cache; c.MaxMemoryMB != 64 || c.MaxObjectKB != 1024 || c.VaryByQuery != "all" {
		t.Fatalf("cache defaults = %+v", c)
	}
	other := e.createSite(admin, redirectSite("other", 0))

	client := &http.Client{Timeout: 5 * time.Second}
	fetch := func(path string) string {
		t.Helper()
		var res *http.Response
		var err error
		for i := 0; i < 50; i++ {
			if res, err = client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path)); err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.Header.Get("X-Cache")
	}
	for _, p := range []string{"/a", "/a", "/b/1", "/b/2"} {
		fetch(p)
	}
	if got := fetch("/a"); got != "HIT" {
		t.Fatalf("X-Cache = %q", got)
	}

	status := decodeJSON[model.SiteStatus](t, e.do(http.MethodGet, "/api/sites/"+site.ID+"/status", nil, admin...))
	if c := status.Cache; c == nil || c.Entries != 3 || c.Hits != 2 || c.Misses != 3 || c.Bytes == 0 {
		t.Fatalf("cache stats = %+v", c)
	}

	e.scoped("op", grant(site.ID, model.RoleOperator))
	e.scoped("viewer", grant(site.ID, model.RoleViewer))
	e.scoped("elsewhere", grant(other.ID, model.RoleOperator))
	e.user("serverviewer", model.RoleViewer, false)
	purge := "/api/sites/" + site.ID + "/cache/purge"
	for _, tc := range []struct {
		user string
		want int
	}{
		{"viewer", http.StatusForbidden},
		{"serverviewer", http.StatusForbidden},
		{"elsewhere", http.StatusNotFound},
	} {
		expect(t, e.do(http.MethodPost, purge, map[string]string{}, session(e.login(tc.user))...), tc.want)
	}
	op := session(e.login("op"))
	expect(t, e.do(http.MethodPost, purge, map[string]string{"path": "b/"}, op...), http.StatusUnprocessableEntity)
	rec := e.do(http.MethodPost, purge, map[string]string{"path": "/b"}, op...)
	expect(t, rec, http.StatusOK)
	if n := decodeJSON[map[string]int](t, rec)["purged"]; n != 2 {
		t.Fatalf("purged %d, want 2", n)
	}
	if got := fetch("/a"); got != "HIT" {
		t.Fatalf("/a after purging /b: X-Cache = %q", got)
	}
	rec = e.do(http.MethodPost, purge, nil, admin...)
	expect(t, rec, http.StatusOK)
	if n := decodeJSON[map[string]int](t, rec)["purged"]; n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	if !contains(e.auditActions(), "op:site.cache.purge") {
		t.Errorf("purge not audited: %v", e.auditActions())
	}

	// A recycle (a restart, for a proxy site) empties the cache too.
	fetch("/a")
	expect(t, e.do(http.MethodPost, "/api/sites/"+site.ID+"/recycle", nil, op...), http.StatusOK)
	if got := fetch("/a"); got != "MISS" {
		t.Fatalf("after a recycle: X-Cache = %q", got)
	}
}
