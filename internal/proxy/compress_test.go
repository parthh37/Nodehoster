package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/parthh37/nodehoster/internal/model"
)

func TestAcceptedEncodings(t *testing.T) {
	cases := []struct {
		header string
		want   []string
	}{
		{"", nil},
		{"identity", nil},
		{"deflate", nil},
		{"gzip", []string{"gzip"}},
		{"gzip, deflate, br", []string{"br", "gzip"}},
		{"gzip, deflate, br, zstd", []string{"br", "gzip"}},
		{"BR, GZIP", []string{"br", "gzip"}},
		{"gzip;q=1.0, br;q=0.5", []string{"gzip", "br"}},
		{"gzip; q=0.8, br; q=0.8", []string{"br", "gzip"}},
		{"br;q=0, gzip", []string{"gzip"}},
		{"br;q=0, gzip;q=0", nil},
		{"*", []string{"br", "gzip"}},
		{"*;q=0.5, gzip", []string{"gzip", "br"}},
		{"gzip;q=0, *", []string{"br"}},
		{"*;q=0", nil},
		{"x-gzip", []string{"gzip"}},
		{"br;q=bogus, gzip", []string{"gzip"}},
	}
	for _, c := range cases {
		if got := acceptedEncodings(c.header); !slices.Equal(got, c.want) {
			t.Errorf("acceptedEncodings(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}

func decodeBody(t *testing.T, enc string, b []byte) string {
	t.Helper()
	var r io.Reader
	switch enc {
	case "br":
		r = brotli.NewReader(bytes.NewReader(b))
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		r = zr
	default:
		return string(b)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("decode %s: %v", enc, err)
	}
	return string(out)
}

func TestCompressHandler(t *testing.T) {
	big := strings.Repeat("hello compression ", 200) // 3600 bytes
	type resp struct {
		status  int
		headers map[string]string
		body    string
		flush   bool
	}
	cases := []struct {
		name    string
		accept  string
		req     map[string]string
		method  string
		resp    resp
		wantEnc string
		vary    bool
	}{
		{name: "brotli preferred", accept: "gzip, br", resp: resp{body: big, headers: map[string]string{"Content-Type": "text/html"}}, wantEnc: "br", vary: true},
		{name: "gzip only", accept: "gzip", resp: resp{body: big, headers: map[string]string{"Content-Type": "application/json"}}, wantEnc: "gzip", vary: true},
		{name: "q-values", accept: "br;q=0.1, gzip", resp: resp{body: big, headers: map[string]string{"Content-Type": "text/css"}}, wantEnc: "gzip", vary: true},
		{name: "client accepts none", accept: "", resp: resp{body: big, headers: map[string]string{"Content-Type": "text/html"}}, vary: true},
		{name: "too small", accept: "br", resp: resp{body: "tiny", headers: map[string]string{"Content-Type": "text/html"}}, vary: true},
		{name: "small with length", accept: "br", resp: resp{body: "tiny", headers: map[string]string{"Content-Type": "text/html", "Content-Length": "4"}}, vary: true},
		{name: "big with length", accept: "br", resp: resp{body: big, headers: map[string]string{"Content-Type": "text/html", "Content-Length": strconv.Itoa(len(big))}}, wantEnc: "br", vary: true},
		{name: "sniffed type", accept: "br", resp: resp{body: "<!doctype html>" + big}, wantEnc: "br", vary: true},
		{name: "image", accept: "br", resp: resp{body: big, headers: map[string]string{"Content-Type": "image/png"}}},
		{name: "event stream", accept: "br", resp: resp{body: big, flush: true, headers: map[string]string{"Content-Type": "text/event-stream"}}},
		{name: "already encoded", accept: "br", resp: resp{body: big, headers: map[string]string{"Content-Type": "text/html", "Content-Encoding": "gzip"}}},
		{name: "no-transform", accept: "br", resp: resp{body: big, headers: map[string]string{"Content-Type": "text/html", "Cache-Control": "public, no-transform"}}},
		{name: "partial content", accept: "br", resp: resp{status: 206, body: big, headers: map[string]string{"Content-Type": "text/html", "Content-Range": "bytes 0-3599/9999"}}},
		{name: "range request", accept: "br", req: map[string]string{"Range": "bytes=0-10"}, resp: resp{body: big, headers: map[string]string{"Content-Type": "text/html"}}},
		{name: "upgrade", accept: "br", req: map[string]string{"Upgrade": "websocket"}, resp: resp{body: big, headers: map[string]string{"Content-Type": "text/html"}}},
		{name: "HEAD", accept: "br", method: http.MethodHead, resp: resp{headers: map[string]string{"Content-Type": "text/html"}}},
		{name: "not found page", accept: "br", resp: resp{status: 404, body: big, headers: map[string]string{"Content-Type": "text/html"}}, wantEnc: "br", vary: true},
		{name: "flushed stream", accept: "gzip", resp: resp{body: big, flush: true, headers: map[string]string{"Content-Type": "application/json"}}, wantEnc: "gzip", vary: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := compressHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range c.resp.headers {
					w.Header().Set(k, v)
				}
				if c.resp.status != 0 {
					w.WriteHeader(c.resp.status)
				}
				if c.resp.flush {
					for chunk := range slices.Chunk([]byte(c.resp.body), 100) {
						w.Write(chunk)
						w.(http.Flusher).Flush()
					}
					return
				}
				io.WriteString(w, c.resp.body)
			}))
			method := c.method
			if method == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, "/", nil)
			req.Header.Set("Accept-Encoding", c.accept)
			for k, v := range c.req {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			res := rec.Result()
			if got := res.Header.Get("Content-Encoding"); got != c.wantEnc && !(c.name == "already encoded" && got == "gzip") {
				t.Fatalf("Content-Encoding = %q, want %q", got, c.wantEnc)
			}
			if got := strings.Contains(res.Header.Get("Vary"), "Accept-Encoding"); got != c.vary {
				t.Errorf("Vary = %q, want Accept-Encoding: %v", res.Header.Get("Vary"), c.vary)
			}
			if c.wantEnc != "" {
				if res.Header.Get("Content-Length") != "" {
					t.Errorf("compressed response keeps Content-Length %s", res.Header.Get("Content-Length"))
				}
				if got := decodeBody(t, c.wantEnc, rec.Body.Bytes()); got != c.resp.body && got != "<!doctype html>"+big {
					t.Fatalf("decoded body differs (%d bytes)", len(got))
				}
				if !c.resp.flush && rec.Body.Len() >= len(c.resp.body) {
					t.Errorf("%d compressed bytes for %d", rec.Body.Len(), len(c.resp.body))
				}
			} else if c.method != http.MethodHead && rec.Body.String() != c.resp.body {
				t.Fatalf("body changed without compression: %d bytes", rec.Body.Len())
			}
		})
	}
}

func TestCompressWeakensETag(t *testing.T) {
	h := compressHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("ETag", `"v1"`)
		io.WriteString(w, strings.Repeat("x", 4096))
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "br")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if et := rec.Header().Get("ETag"); et != `W/"v1"` {
		t.Fatalf("ETag = %s, want the weak form", et)
	}
}

// TestPrecompressedStatic: file.br and file.gz next to a file are sent as
// they are to clients that accept them, unless they are stale.
func TestPrecompressedStatic(t *testing.T) {
	root := t.TempDir()
	js := strings.Repeat("console.log('hello');\n", 200)
	write := func(name string, data []byte, mod time.Time) {
		p := filepath.Join(root, name)
		os.WriteFile(p, data, 0o644)
		os.Chtimes(p, mod, mod)
	}
	var brBuf, gzBuf bytes.Buffer
	bw := brotli.NewWriterLevel(&brBuf, 11)
	io.WriteString(bw, js)
	bw.Close()
	gw := gzip.NewWriter(&gzBuf)
	io.WriteString(gw, js)
	gw.Close()
	base := time.Now().Add(-time.Hour)
	write("app.js", []byte(js), base)
	write("app.js.br", brBuf.Bytes(), base)
	write("app.js.gz", gzBuf.Bytes(), base)
	write("plain.txt", []byte(strings.Repeat("plain ", 500)), base)

	site := staticSite(root, model.RoutingConfig{Compression: true})
	rt := testServer(model.DefaultSettings()).compileSite(site, nil)

	fetchEnc := func(path, accept string, hdr ...string) *httptest.ResponseRecorder {
		t.Helper()
		return get(t, rt, "http://site"+path, append([]string{"Accept-Encoding", accept}, hdr...)...)
	}
	for _, c := range []struct{ accept, enc string }{{"br, gzip", "br"}, {"gzip", "gzip"}, {"gzip;q=1, br;q=0.5", "gzip"}, {"", ""}} {
		rec := fetchEnc("/app.js", c.accept)
		if rec.Code != 200 || rec.Header().Get("Content-Encoding") != c.enc {
			t.Fatalf("accept %q: %d, Content-Encoding %q, want %q", c.accept, rec.Code, rec.Header().Get("Content-Encoding"), c.enc)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
			t.Errorf("accept %q: Content-Type %s", c.accept, ct)
		}
		if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
			t.Errorf("accept %q: no Vary", c.accept)
		}
		if got := decodeBody(t, c.enc, rec.Body.Bytes()); got != js {
			t.Fatalf("accept %q: body differs", c.accept)
		}
		want := map[string]int{"br": brBuf.Len(), "gzip": gzBuf.Len(), "": len(js)}[c.enc]
		if rec.Body.Len() != want || rec.Header().Get("Content-Length") != strconv.Itoa(want) {
			t.Errorf("accept %q: %d bytes, Content-Length %s, want the file's %d", c.accept, rec.Body.Len(), rec.Header().Get("Content-Length"), want)
		}
	}
	// Ranges and conditional requests work on the variant.
	rec := fetchEnc("/app.js", "br", "Range", "bytes=0-9")
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != 10 || rec.Header().Get("Content-Encoding") != "br" {
		t.Fatalf("range of the variant: %d, %d bytes, %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Encoding"))
	}
	rec = fetchEnc("/app.js", "br", "If-Modified-Since", time.Now().UTC().Format(http.TimeFormat))
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional request: %d", rec.Code)
	}

	// A variant older than the file is ignored: gzip is the fresh one left.
	write("app.js", []byte(js), time.Now())
	write("app.js.gz", gzBuf.Bytes(), time.Now().Add(time.Minute))
	if rec := fetchEnc("/app.js", "br, gzip"); rec.Header().Get("Content-Encoding") != "gzip" || decodeBody(t, "gzip", rec.Body.Bytes()) != js {
		t.Fatalf("stale .br: Content-Encoding %q", rec.Header().Get("Content-Encoding"))
	}

	// Without variants the file is compressed on the fly.
	if rec := fetchEnc("/plain.txt", "br"); rec.Header().Get("Content-Encoding") != "br" {
		t.Fatalf("dynamic compression of a static file: %q", rec.Header().Get("Content-Encoding"))
	}

	// With compression off, variants are not used.
	site = staticSite(root, model.RoutingConfig{})
	rt = testServer(model.DefaultSettings()).compileSite(site, nil)
	if rec := fetchEnc("/app.js", "br"); rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != js {
		t.Fatalf("compression off: Content-Encoding %q", rec.Header().Get("Content-Encoding"))
	}
}
