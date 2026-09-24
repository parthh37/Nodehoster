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
	// A folder moved from IIS: web.config holds connection strings.
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "web.config"), []byte("SECRET"), 0o644)
	os.WriteFile(filepath.Join(root, "sub", "Web.config"), []byte("SECRET"), 0o644)
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
		"/web.config",
		"/WEB.CONFIG",
		"/web.config.",
		"/web.config%20",
		"/sub/Web.config",
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

// TestStaticShortNames: on NTFS, an 8.3 short name opens the file it
// belongs to (ENV~1 is .env, WEB~1.CON is web.config, GIT~1/config is
// .git/config), so anything shaped like one is refused. Other names with
// a tilde are served. The files are created under the alias names,
// standing in for what Windows resolves.
func TestStaticShortNames(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "GIT~1"), 0o755)
	for _, f := range []string{"ENV~1", "web~12.con", "WEB~1.CON", "PROGRA~1.TXT", "~1", "GIT~1/config",
		"a~b.txt", "~file", "file.js~", "a~1b.txt", "photo~2023.jpeg", "longname~1.txt", "jquery~1.min.js"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(f)), []byte("SECRET"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &staticHandler{root: root}
	for _, tc := range []struct {
		target string
		served bool
	}{
		{"/ENV~1", false},
		{"/env~1", false},
		{"/WEB~1.CON", false},
		{"/web~12.con", false},
		{"/WEB~1.CON.", false},
		{"/WEB~1.CON%20", false},
		{"/PROGRA~1.TXT", false},
		{"/~1", false},
		{"/GIT~1/config", false},
		{"/a~b.txt", true},
		{"/~file", true},
		{"/file.js~", true},
		{"/a~1b.txt", true},
		{"/photo~2023.jpeg", true},
		{"/longname~1.txt", true},
		{"/jquery~1.min.js", true},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://site"+tc.target, nil))
		want := http.StatusNotFound
		if tc.served {
			want = http.StatusOK
		}
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", tc.target, rec.Code, want)
		}
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
