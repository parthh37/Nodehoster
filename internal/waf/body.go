package waf

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/parthh37/nodehoster/internal/model"
)

// Request bodies. Only what the rules can read is buffered: form-encoded,
// JSON, multipart (fields and file names; file contents are skipped) and
// text, and bodies whose Content-Type Go cannot parse (as text: the
// application's parser may be more lenient). A gzip or deflate body (as
// Express's body-parser accepts) is inflated for inspection, never beyond
// the inspection limit, and passed on as it came; a body in another
// encoding cannot be read, and fails closed (rule 920240). Binary bodies,
// and anything past the inspection limit, stream to the application as
// they arrive.

const (
	// maxJSONDepth bounds the nesting of a JSON body: deeper, rule 920205
	// fires and the body is inspected as text.
	maxJSONDepth = 64
	// maxNameLen bounds a JSON value's dotted name; longer ones are cut.
	maxNameLen = 256
	// DefaultMaxBufferedBytes is the server-wide budget for body bytes held
	// for inspection at once (see SetMaxBufferedBytes).
	DefaultMaxBufferedBytes = 64 << 20
)

// buffered is the server-wide budget of body bytes held for inspection:
// each site may buffer up to 4 MB of each request, and slow clients can
// hold many requests open. When it is spent, bodies are not buffered: in
// block mode the request is refused (Result.Busy, 503), in detect mode
// the body goes uninspected.
var buffered byteBudget

func init() { buffered.limit.Store(DefaultMaxBufferedBytes) }

type byteBudget struct {
	used, limit atomic.Int64
}

// SetMaxBufferedBytes sets the server-wide budget for body bytes held for
// inspection, and returns the previous one.
func SetMaxBufferedBytes(n int64) int64 { return buffered.limit.Swap(n) }

func (b *byteBudget) take(n int64) bool {
	for {
		u := b.used.Load()
		if u+n > b.limit.Load() {
			return false
		}
		if b.used.CompareAndSwap(u, u+n) {
			return true
		}
	}
}

func (b *byteBudget) give(n int64) { b.used.Add(-n) }

// hold is a body's share of the budget, given back once, when the
// application has read the buffered start of the body, closed it, or the
// request ended, whichever comes first.
type hold struct {
	once sync.Once
	n    int64
}

func (h *hold) release() { h.once.Do(func() { buffered.give(h.n) }) }

type bodyKind int

const (
	bodyNone bodyKind = iota
	bodyForm
	bodyJSON
	bodyMultipart
	bodyText
	bodyXML // text, from paranoia level 2
)

func classify(mt string) bodyKind {
	switch {
	case mt == "application/x-www-form-urlencoded":
		return bodyForm
	case mt == "application/json" || strings.HasSuffix(mt, "+json") || mt == "text/json":
		return bodyJSON
	case mt == "multipart/form-data":
		return bodyMultipart
	case mt == "application/xml" || mt == "text/xml" || strings.HasSuffix(mt, "+xml"):
		return bodyXML
	case strings.HasPrefix(mt, "text/") || mt == "application/graphql" || mt == "application/x-ndjson" || mt == "application/csp-report":
		return bodyText
	}
	return bodyNone
}

// replayBody gives the application the buffered start of a body, then
// the rest as it arrives (or the error reading it stopped at).
type replayBody struct {
	io.Reader
	orig io.Closer
	hold *hold
}

func (b *replayBody) Close() error {
	b.hold.release()
	return b.orig.Close()
}

// heldReader is the buffered start of a body; read to its end, its share
// of the budget is given back. (Not embedding the bytes.Reader keeps its
// WriteTo from bypassing Read.)
type heldReader struct {
	r    *bytes.Reader
	hold *hold
}

func (h heldReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	if h.r.Len() == 0 {
		h.hold.release()
	}
	return n, err
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func (in *inspection) body(r *http.Request) {
	if r.Body == nil || r.Body == http.NoBody || (r.ContentLength == 0 && len(r.TransferEncoding) == 0) {
		return
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return
	}
	var kind bodyKind
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		// Go's parser is strict; the application's may still take
		// "application/json;;" for JSON. Inspected as text, not skipped.
		in.request(920190, model.WAFInHeader, "Content-Type", ct)
		kind = bodyText
	} else if kind = classify(mt); kind == bodyXML && in.e.paranoia < 2 {
		kind = bodyNone
	}
	boundary := params["boundary"]
	if kind == bodyMultipart && boundary == "" {
		in.request(920190, model.WAFInHeader, "Content-Type", ct)
		kind = bodyText
	}
	if kind == bodyNone || in.done {
		return
	}
	enc, ok := contentEncoding(r.Header.Values("Content-Encoding"))
	if !ok {
		// Brotli, zstd, several codings: the rules cannot read it, and the
		// application may (body-parser 2 inflates Brotli).
		in.incomplete(920240, "Content-Encoding: "+enc)
		return
	}

	// Read one byte more than the limit, to know whether there is more.
	// ReadAll grows its buffer as data comes, so a small chunked body does
	// not cost the whole limit (the budget counts it all, though).
	limit := int64(in.e.bodyLimit)
	size := limit + 1
	if r.ContentLength > 0 && r.ContentLength <= limit {
		size = r.ContentLength
	}
	if !buffered.take(size) {
		in.busy()
		return
	}
	h := &hold{n: size}
	var buf []byte
	var rerr error
	if r.ContentLength > 0 && r.ContentLength <= limit {
		buf = make([]byte, r.ContentLength)
		var n int
		// A body shorter than announced ends in io.ErrUnexpectedEOF, which
		// the application's request then ends with too.
		n, rerr = io.ReadFull(r.Body, buf)
		buf = buf[:n]
	} else {
		buf, rerr = io.ReadAll(io.LimitReader(r.Body, limit+1))
	}
	orig := r.Body
	more := int64(len(buf)) > limit
	start := heldReader{bytes.NewReader(buf), h}
	switch {
	case rerr != nil:
		// The client went away or the body is too large (MaxBytesReader):
		// the application sees the same error after what was read.
		r.Body = &replayBody{Reader: io.MultiReader(start, errReader{rerr}), orig: orig, hold: h}
	case more:
		r.Body = &replayBody{Reader: io.MultiReader(start, orig), orig: orig, hold: h}
	default:
		r.Body = &replayBody{Reader: start, orig: orig, hold: h}
	}
	if len(buf) == 0 {
		h.release()
		return
	}
	// Whatever the application does with the body, the request's end
	// gives the budget back (the server cancels its context then).
	context.AfterFunc(r.Context(), h.release)
	truncated := more || rerr != nil
	if more {
		buf = buf[:limit]
	}

	if enc != "" {
		// Inflated up to the limit and no further: a small body that
		// inflates to gigabytes costs no more than one that does not.
		if !buffered.take(limit + 1) {
			in.busy()
			return
		}
		defer buffered.give(limit + 1)
		plain, err := inflate(enc, buf, limit)
		switch {
		case int64(len(plain)) > limit:
			plain, truncated = plain[:limit], true
		case err != nil && !truncated:
			// Not gzip or deflate after all: the application cannot read
			// it either. What could be inflated is inspected.
			in.request(920200, model.WAFInBody, "", "invalid "+enc+" body")
			truncated = true
		case err != nil:
			truncated = true // cut at the limit, or the client went away
		}
		buf = plain
		if len(buf) == 0 {
			return
		}
	}

	switch kind {
	case bodyForm:
		in.query(string(buf))
	case bodyJSON:
		in.json(buf, truncated)
	case bodyMultipart:
		in.multipart(buf, boundary, truncated)
	case bodyText, bodyXML:
		in.value(tBody, model.WAFInBody, "", string(buf), false)
	}
}

// busy notes that a body could not be buffered: in block mode the request
// is refused, in detect mode the body goes uninspected.
func (in *inspection) busy() {
	if in.e.mode == model.WAFBlock {
		in.res.Busy, in.done = true, true
	}
}

// contentEncoding returns a body's content coding: "" for none (or
// identity), gzip, x-gzip or deflate, which inflate reads; otherwise the
// codings, and false.
func contentEncoding(values []string) (string, bool) {
	var codings []string
	for _, v := range values {
		for _, c := range strings.Split(v, ",") {
			if c = strings.ToLower(strings.TrimSpace(c)); c != "" && c != "identity" {
				codings = append(codings, c)
			}
		}
	}
	switch {
	case len(codings) == 0:
		return "", true
	case len(codings) == 1 && (codings[0] == "gzip" || codings[0] == "x-gzip" || codings[0] == "deflate"):
		return codings[0], true
	}
	return strings.Join(codings, ", "), false
}

// inflate decompresses a gzip or deflate body, returning at most limit+1
// bytes, and the error decompression stopped at, if any.
func inflate(enc string, b []byte, limit int64) ([]byte, error) {
	var zr io.Reader
	if enc == "deflate" {
		// zlib-wrapped, as RFC 9110 says (and Node.js reads); some clients
		// send raw deflate.
		if z, err := zlib.NewReader(bytes.NewReader(b)); err == nil {
			zr = z
		} else {
			zr = flate.NewReader(bytes.NewReader(b))
		}
	} else {
		g, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		zr = g
	}
	var out bytes.Buffer
	_, err := io.Copy(&out, io.LimitReader(zr, limit+1))
	return out.Bytes(), err
}

// json inspects a JSON body: object keys as argument names (dotted paths,
// arrays transparent: {"post":{"tags":["a"]}} names "post.tags"), string
// values as arguments. A body that is not JSON, or nested more than
// maxJSONDepth deep, is inspected as text.
//
// The current name is kept in one buffer, each open container remembering
// only where its name ends in it, so that memory grows with the body, not
// with the square of its depth.
func (in *inspection) json(b []byte, truncated bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	type frame struct {
		object    bool
		expectKey bool
		base      int // len(path) when it opened: the end of its own name
	}
	var stack []frame
	var path []byte // the current value's dotted name
	name := func() string { return string(path[:min(len(path), maxNameLen)]) }
	// afterValue marks the current object's key as used.
	afterValue := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].expectKey = true
		}
	}
	for !in.done {
		tok, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			if !truncated {
				in.request(920200, model.WAFInBody, "", "invalid JSON")
				in.value(tBody, model.WAFInBody, "", string(b), false)
			}
			return
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				if len(stack) == maxJSONDepth {
					in.request(920205, model.WAFInBody, "", "more than "+strconv.Itoa(maxJSONDepth)+" levels")
					in.value(tBody, model.WAFInBody, "", string(b), false)
					return
				}
				stack = append(stack, frame{object: v == '{', expectKey: v == '{', base: len(path)})
			default:
				path = path[:stack[len(stack)-1].base]
				stack = stack[:len(stack)-1]
				afterValue()
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].expectKey {
				f := &stack[n-1]
				path = path[:f.base]
				if f.base > 0 {
					path = append(path, '.')
				}
				path = append(path, v...)
				f.expectKey = false
				in.value(tArgName, model.WAFInArgName, name(), v, false)
				continue
			}
			in.value(tArg, model.WAFInArg, name(), v, false)
			afterValue()
		default:
			afterValue()
		}
	}
}

// multipart inspects a multipart form: field names, fields and the names
// of uploaded files (as sent: Go's Part.FileName would strip a path
// traversal). A part without a file name is a field whatever its
// Content-Type, as busboy (multer) and formidable take it.
func (in *inspection) multipart(b []byte, boundary string, truncated bool) {
	mr := multipart.NewReader(bytes.NewReader(b), boundary)
	for !in.done {
		p, err := mr.NextPart()
		if err == io.EOF {
			return
		}
		if err != nil {
			if !truncated {
				in.request(920200, model.WAFInBody, "", "invalid multipart body")
			}
			return
		}
		_, params, _ := mime.ParseMediaType(p.Header.Get("Content-Disposition"))
		field := params["name"]
		in.value(tArgName, model.WAFInArgName, field, field, false)
		if fn, ok := params["filename"]; ok {
			in.value(tFile, model.WAFInFile, field, fn, false)
			continue
		}
		data, err := io.ReadAll(p)
		in.value(tArg, model.WAFInArg, field, string(data), false)
		if err != nil {
			return
		}
	}
}
