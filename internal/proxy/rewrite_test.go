package proxy

import (
	"compress/gzip"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func testServer(settings model.Settings) *Server {
	return &Server{
		deps: Deps{
			Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			Settings: func() model.Settings { return settings },
		},
		stdLog: log.New(io.Discard, "", 0),
		stats:  map[string]*siteStats{},
	}
}

func staticSite(root string, r model.RoutingConfig) *model.Site {
	s := &model.Site{ID: "s", Name: "files", Type: model.SiteStatic, Static: &model.StaticConfig{Root: root}, Routing: r}
	s.ApplyDefaults()
	return s
}

func get(t *testing.T, h http.Handler, target string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestStaticMimeTypes(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"app.js", "data.webmanifest", "font.woff2", "notes.xyz", "LICENSE", "model.usdz", "index.html"} {
		os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644)
	}
	settings := model.DefaultSettings()
	settings.Mime.Types = []model.MimeMap{{Extension: ".xyz", Type: "text/x-server"}, {Extension: ".usdz", Type: "model/server"}}
	rt := testServer(settings).compileSite(staticSite(root, model.RoutingConfig{
		MimeTypes: []model.MimeMap{{Extension: ".USDZ", Type: "model/site"}},
	}), nil)

	for path, want := range map[string]string{
		"/app.js":           "text/javascript; charset=utf-8", // never the registry's text/plain
		"/data.webmanifest": "application/manifest+json",
		"/font.woff2":       "font/woff2",
		"/notes.xyz":        "text/x-server", // server mapping
		"/model.usdz":       "model/site",    // site overrides server overrides built-in
		"/LICENSE":          "application/octet-stream",
		"/":                 "text/html; charset=utf-8",
	} {
		rec := get(t, rt, path)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != want {
			t.Errorf("%s: %d %q, want %q", path, rec.Code, rec.Header().Get("Content-Type"), want)
		}
	}

	// Refusing unknown extensions, like IIS: 404, unless "." maps files
	// without one.
	rt = testServer(settings).compileSite(staticSite(root, model.RoutingConfig{
		UnknownMimeTypes: model.UnknownMimeDeny,
		MimeTypes:        []model.MimeMap{{Extension: ".", Type: "text/plain"}},
	}), nil)
	os.WriteFile(filepath.Join(root, "backup.bak"), []byte("secret"), 0o644)
	if rec := get(t, rt, "/backup.bak"); rec.Code != 404 || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("unknown extension served: %d", rec.Code)
	}
	if rec := get(t, rt, "/LICENSE"); rec.Code != 200 || rec.Header().Get("Content-Type") != "text/plain" {
		t.Errorf("extensionless mapping: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// A rewrite to an absolute URL proxies there; outbound rules fix up the
// backend's Location header and links; compression still applies after.
func TestRewriteProxyOutboundAndCompression(t *testing.T) {
	var gotHost, gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotPath = r.Host, r.URL.RequestURI()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Location", "http://backend.local/moved")
		var out io.Writer = w
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			// Compressed whenever it may be: the proxy must still see HTML.
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			defer zw.Close()
			out = zw
		}
		io.WriteString(out, `<a href="http://backend.local/page">`+strings.Repeat("x", 2000)+`</a>`)
	}))
	defer backend.Close()

	site := staticSite(t.TempDir(), model.RoutingConfig{
		Compression: true,
		Rewrites: []model.RewriteRule{{
			Enabled: true, Match: "^/blog/(.*)", Action: "rewrite", Target: backend.URL + "/{R:1}", QueryString: "append",
		}},
		OutboundRules: []model.OutboundRule{
			{Enabled: true, Scope: "header", Header: "Location", Match: "^http://backend\\.local/(.*)", Action: "rewrite", Value: "/blog/{R:1}"},
			{Enabled: true, Scope: "tags", Tags: []string{"a"}, Match: "^http://backend\\.local/(.*)", Action: "rewrite", Value: "/blog/{R:1}"},
		},
	})
	rt := testServer(model.DefaultSettings()).compileSite(site, nil)
	rec := get(t, rt, "http://example.com/blog/post?id=1", "Accept-Encoding", "gzip")

	if gotPath != "/post?id=1" || strings.HasPrefix(gotHost, "example.com") {
		t.Errorf("backend saw path %q, host %q", gotPath, gotHost)
	}
	if rec.Header().Get("Location") != "/blog/moved" {
		t.Errorf("Location: %q", rec.Header().Get("Location"))
	}
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("response not compressed: %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	if !strings.HasPrefix(string(body), `<a href="/blog/page">`) {
		t.Errorf("body: %.60s", body)
	}
}
