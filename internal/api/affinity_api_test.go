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

// TestSessionAffinityEndToEnd saves a proxy site with affinity through the
// API and checks a real listener pins clients with the server's key.
func TestSessionAffinityEndToEnd(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	var ups []string
	for _, name := range []string{"a", "b", "c"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, name) }))
		t.Cleanup(srv.Close)
		ups = append(ups, srv.URL)
	}
	port := freePort(t)
	body := map[string]any{
		"name": "sticky", "type": "proxy", "autoStart": true,
		"bindings": []map[string]any{{"protocol": "http", "ip": "127.0.0.1", "port": port}},
		"proxy":    map[string]any{"upstreams": []map[string]any{{"url": ups[0]}, {"url": ups[1]}, {"url": ups[2]}}},
		"routing":  map[string]any{"affinity": map[string]any{"enabled": true, "cookieName": "bad name"}},
	}
	rec := e.do(http.MethodPost, "/api/sites", body, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "routing.affinity.cookieName" {
		t.Fatalf("error field = %q", f)
	}
	body["routing"] = map[string]any{"affinity": map[string]any{"enabled": true}}
	site := e.createSite(admin, body)
	if a := site.Routing.Affinity; !a.Enabled || a.CookieName != model.DefaultAffinityCookie {
		t.Fatalf("stored affinity = %+v", a)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	client := &http.Client{Timeout: 5 * time.Second}
	var res *http.Response
	var err error
	for i := 0; i < 50; i++ { // the listener opens asynchronously
		if res, err = client.Get(url); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	first, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var ck *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == model.DefaultAffinityCookie {
			ck = c
		}
	}
	if ck == nil || !ck.HttpOnly {
		t.Fatalf("affinity cookie = %+v", ck)
	}
	for range 10 {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if string(got) != string(first) {
			t.Fatalf("pinned to %s, answered by %s", first, got)
		}
	}
}
