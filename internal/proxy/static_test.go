package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestStaticTraversal guards against requests escaping the site root. On
// Windows a decoded %5C is a path separator, so it must be refused outright.
func TestStaticTraversal(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "www")
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "index.html"), []byte("ok"), 0o644)
	os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SECRET"), 0o644)
	h := &staticHandler{root: root, index: []string{"index.html"}}

	for _, target := range []string{
		"/x%5C..%5C..%5Csecret.txt",
		"/..%5Csecret.txt",
		"/a%5C..%5C.env",
		"/.env",
		"/../secret.txt",
		"/C:%5Cwindows%5Cwin.ini",
		"/index.html::$DATA",
		"/index.html%00",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://site"+target, nil))
		if rec.Code != http.StatusNotFound || rec.Body.String() == "SECRET" {
			t.Errorf("%s: status %d body %q", target, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://site/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("index: %d %q", rec.Code, rec.Body.String())
	}
}

// TestInformationalStatus: a 100 Continue or 103 Early Hints must not be
// recorded as the response status or swallow the real one.
func TestInformationalStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &responseWriter{ResponseWriter: rec}
	w.WriteHeader(http.StatusContinue)
	w.WriteHeader(http.StatusEarlyHints)
	w.WriteHeader(http.StatusCreated)
	if w.status != http.StatusCreated {
		t.Fatalf("recorded status %d, want 201", w.status)
	}
}
