package proxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/gzip"
)

// Dynamic compression, like IIS's dynamic compression module: Brotli or
// gzip, whichever the client prefers (Brotli when it likes both equally),
// for text-like responses big enough to be worth it.

const (
	// compressMinSize is the smallest body worth compressing: below it the
	// encoding overhead and CPU outweigh the bytes saved.
	compressMinSize = 1024
	// brotliLevel is cheap enough for dynamic responses (levels above 5
	// cost several times the CPU for a few percent). Pre-compressed files
	// can be made at level 11 ahead of time.
	brotliLevel = 4
	gzipLevel   = 5
)

var (
	brPool = sync.Pool{New: func() any { return brotli.NewWriterLevel(nil, brotliLevel) }}
	gzPool = sync.Pool{New: func() any {
		w, _ := gzip.NewWriterLevel(nil, gzipLevel)
		return w
	}}
)

// acceptedEncodings lists the encodings of Accept-Encoding NodeHoster can
// produce ("br", "gzip"), most preferred first by q-value, Brotli first on
// a tie. q=0 refuses an encoding; "*" stands for any not listed.
func acceptedEncodings(header string) []string {
	br, gz, star := -1.0, -1.0, -1.0 // -1 = not mentioned
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(part, ";")
		name = strings.ToLower(strings.TrimSpace(name))
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
				if err != nil || f < 0 {
					f = 0
				}
				q = min(f, 1)
			}
		}
		switch name {
		case "br":
			br = q
		case "gzip", "x-gzip":
			gz = max(gz, q)
		case "*":
			star = q
		}
	}
	if br < 0 {
		br = star
	}
	if gz < 0 {
		gz = star
	}
	var out []string
	switch {
	case br > 0 && br >= gz:
		out = append(out, "br")
		if gz > 0 {
			out = append(out, "gzip")
		}
	case gz > 0:
		out = append(out, "gzip")
		if br > 0 {
			out = append(out, "br")
		}
	}
	return out
}

// negotiateEncoding is the encoding to compress a response with, "" for none.
func negotiateEncoding(header string) string {
	if list := acceptedEncodings(header); len(list) > 0 {
		return list[0]
	}
	return ""
}

// compressible reports whether a Content-Type is text-like. Images, video,
// archives and fonts in woff/woff2 are compressed already. Server-sent
// events must reach the client as they are written, never buffered by an
// encoder.
func compressible(contentType string) bool {
	mt, _, _ := strings.Cut(contentType, ";")
	mt = strings.ToLower(strings.TrimSpace(mt))
	switch {
	case mt == "text/event-stream":
		return false
	case strings.HasPrefix(mt, "text/"), strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"):
		return true
	}
	switch mt {
	case "application/json", "application/javascript", "application/x-javascript", "application/ecmascript",
		"application/xml", "application/wasm", "application/graphql", "application/x-www-form-urlencoded",
		"application/vnd.ms-fontobject", "application/x-font-ttf", "font/ttf", "font/otf", "font/collection",
		"image/x-icon", "image/vnd.microsoft.icon", "image/bmp":
		return true
	}
	return false
}

// compressHandler compresses next's responses. WebSocket upgrades, HEAD
// and range requests pass untouched.
func compressHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" || r.Method == http.MethodHead || r.Header.Get("Range") != "" {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w, enc: negotiateEncoding(r.Header.Get("Accept-Encoding"))}
		defer cw.close()
		next.ServeHTTP(cw, r)
	})
}

// compressWriter decides at the first moment it can: from the headers when
// they settle it (not compressible, already encoded, a Content-Length),
// otherwise after the first compressMinSize bytes or when the handler
// finishes or flushes.
type compressWriter struct {
	http.ResponseWriter
	enc     string
	status  int
	buf     []byte
	decided bool
	br      *brotli.Writer
	gz      *gzip.Writer
	out     io.Writer // the encoder while compressing
}

func (w *compressWriter) WriteHeader(code int) {
	if w.decided {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code) // 100 Continue, 103 Early Hints
		return
	}
	if w.status != 0 {
		return
	}
	w.status = code
	switch w.eligible() {
	case no:
		w.decide(false)
	case yes:
		if n, err := strconv.ParseInt(w.Header().Get("Content-Length"), 10, 64); err == nil {
			w.decide(n >= compressMinSize)
		}
	}
	// maybe: wait for the body to show whether it is big enough (or, with
	// no Content-Type, what it is).
}

type verdict int

const (
	no verdict = iota
	yes
	maybe
)

// eligible judges the response by its status and headers. It adds Vary:
// Accept-Encoding to every response that is compressed for some clients,
// so caches keep the variants apart.
func (w *compressWriter) eligible() verdict {
	h := w.Header()
	switch {
	case w.status < 200, w.status == http.StatusNoContent, w.status == http.StatusPartialContent,
		w.status == http.StatusNotModified, w.status == http.StatusSwitchingProtocols:
		return no
	case h.Get("Content-Encoding") != "", h.Get("Content-Range") != "",
		strings.Contains(strings.ToLower(strings.Join(h.Values("Cache-Control"), ",")), "no-transform"):
		return no
	}
	ct := h.Get("Content-Type")
	if ct == "" {
		if _, sniffed := h["Content-Type"]; sniffed || w.enc == "" {
			return no // explicitly suppressed, or nothing to decide
		}
		return maybe
	}
	if !compressible(ct) {
		return no
	}
	addVary(h, "Accept-Encoding")
	if w.enc == "" {
		return no
	}
	return yes
}

// decide sends the header, compressed or not, and what was buffered.
func (w *compressWriter) decide(compress bool) {
	w.decided = true
	if w.status == 0 {
		w.status = http.StatusOK
	}
	h := w.Header()
	if compress {
		h.Del("Content-Length")
		h.Del("Accept-Ranges") // ranges would refer to the uncompressed bytes
		h.Set("Content-Encoding", w.enc)
		// The compressed bytes are a different representation: a strong
		// validator of the original must not claim them.
		if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
			h.Set("ETag", "W/"+et)
		}
		if w.enc == "br" {
			w.br = brPool.Get().(*brotli.Writer)
			w.br.Reset(w.ResponseWriter)
			w.out = w.br
		} else {
			w.gz = gzPool.Get().(*gzip.Writer)
			w.gz.Reset(w.ResponseWriter)
			w.out = w.gz
		}
	}
	w.ResponseWriter.WriteHeader(w.status)
	if len(w.buf) > 0 {
		buf := w.buf
		w.buf = nil
		w.write(buf)
	}
}

func (w *compressWriter) write(b []byte) (int, error) {
	if w.out != nil {
		return w.out.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *compressWriter) Write(b []byte) (int, error) {
	if w.decided {
		return w.write(b)
	}
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
		if w.decided {
			return w.write(b)
		}
	}
	w.buf = append(w.buf, b...)
	if len(w.buf) >= compressMinSize {
		w.settle(false)
	}
	return len(b), nil
}

// settle decides a response that was waiting for its body.
func (w *compressWriter) settle(final bool) {
	h := w.Header()
	if h.Get("Content-Type") == "" && len(w.buf) > 0 {
		h.Set("Content-Type", http.DetectContentType(w.buf)) // as net/http would
	}
	// A flush before any body (ReverseProxy flushes the header of a
	// response of unknown length straight away) commits to compressing a
	// compressible response: its size is unknown, most are big enough.
	if w.eligible() != yes || (final && len(w.buf) < compressMinSize) {
		w.decide(false)
		return
	}
	w.decide(true)
}

// Flush sends what is there now: a flushing handler (long polling,
// streaming JSON) must not be held back waiting for more bytes.
func (w *compressWriter) Flush() {
	if !w.decided {
		if w.status == 0 {
			w.WriteHeader(http.StatusOK)
		}
		if !w.decided {
			w.settle(false)
		}
	}
	if w.br != nil {
		w.br.Flush()
	} else if w.gz != nil {
		w.gz.Flush()
	}
	http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *compressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.decided = true
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *compressWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// close finishes the response once the handler has returned.
func (w *compressWriter) close() {
	if !w.decided {
		if w.status == 0 && len(w.buf) == 0 {
			return // nothing was written; net/http sends its own 200
		}
		if w.status == 0 {
			w.status = http.StatusOK
		}
		w.settle(true)
	}
	if w.br != nil {
		w.br.Close()
		w.br.Reset(nil)
		brPool.Put(w.br)
		w.br = nil
	}
	if w.gz != nil {
		w.gz.Close()
		w.gz.Reset(nil)
		gzPool.Put(w.gz)
		w.gz = nil
	}
	w.out = nil
}

// addVary adds a header name to Vary unless it is listed already.
func addVary(h http.Header, name string) {
	for _, v := range h.Values("Vary") {
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f == "*" || strings.EqualFold(f, name) {
				return
			}
		}
	}
	h.Add("Vary", name)
}

// varyNames lists the header names of a response's Vary, canonical and
// sorted; star reports "Vary: *".
func varyNames(h http.Header) (names []string, star bool) {
	for _, v := range h.Values("Vary") {
		for _, f := range strings.Split(v, ",") {
			f = strings.TrimSpace(f)
			switch {
			case f == "":
			case f == "*":
				star = true
			default:
				if c := http.CanonicalHeaderKey(f); !slices.Contains(names, c) {
					names = append(names, c)
				}
			}
		}
	}
	slices.Sort(names)
	return names, star
}
