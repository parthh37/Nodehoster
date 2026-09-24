package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestNotBuilt: a binary built before the console (dist holds only the
// committed .gitkeep) says how to build it, on every path.
func TestNotBuilt(t *testing.T) {
	h := handler(fstest.MapFS{"dist/.gitkeep": {}})
	for _, p := range []string{"/", "/sites/abc", "/.gitkeep", "/assets/x.js"} {
		rec := get(h, p)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "npm run build") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
}

func TestBuilt(t *testing.T) {
	h := handler(fstest.MapFS{
		"dist/.gitkeep":        {},
		"dist/index.html":      {Data: []byte("<html>console</html>")},
		"dist/assets/app-1.js": {Data: []byte("js")},
	})
	for _, tc := range []struct {
		path, body string
		code       int
	}{
		{"/", "console", 200},
		{"/sites/abc/logs", "console", 200}, // a client-side route
		{"/assets/app-1.js", "js", 200},
		{"/assets/missing.js", "", 404},
		{"/.gitkeep", "", 404},
	} {
		rec := get(h, tc.path)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.body) {
			t.Errorf("%s: %d %q", tc.path, rec.Code, rec.Body.String())
		}
	}
	if cc := get(h, "/assets/app-1.js").Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("asset Cache-Control = %q", cc)
	}
}
