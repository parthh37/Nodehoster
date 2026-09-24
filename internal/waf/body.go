package waf

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

// Request bodies. Only what the rules can read is buffered: form-encoded,
// JSON, multipart (the text fields and file names; file contents are
// skipped) and text. Binary and compressed bodies, and anything past the
// inspection limit, stream to the application as they arrive.

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
}

func (b *replayBody) Close() error { return b.orig.Close() }

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
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		in.request(920190, model.WAFInHeader, "Content-Type", ct)
		return
	}
	kind := classify(mt)
	if kind == bodyXML && in.e.paranoia < 2 {
		kind = bodyNone
	}
	if enc := r.Header.Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") {
		kind = bodyNone // compressed: the rules cannot read it
	}
	if kind == bodyNone {
		return
	}
	boundary := params["boundary"]
	if kind == bodyMultipart && boundary == "" {
		in.request(920190, model.WAFInHeader, "Content-Type", ct)
		return
	}

	// Read one byte more than the limit, to know whether there is more.
	// ReadAll grows its buffer as data comes, so a small chunked body does
	// not cost the whole limit.
	limit := int64(in.e.bodyLimit)
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
	switch {
	case rerr != nil:
		// The client went away or the body is too large (MaxBytesReader):
		// the application sees the same error after what was read.
		r.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(buf), errReader{rerr}), orig: orig}
	case more:
		r.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(buf), orig), orig: orig}
	default:
		r.Body = &replayBody{Reader: bytes.NewReader(buf), orig: orig}
	}
	truncated := more || rerr != nil
	if more {
		buf = buf[:limit]
	}
	if len(buf) == 0 {
		return
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

// json inspects a JSON body: object keys as argument names (dotted paths,
// arrays transparent: {"post":{"tags":["a"]}} names "post.tags"), string
// values as arguments. A body that is not JSON is inspected as text.
func (in *inspection) json(b []byte, truncated bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	type frame struct {
		object    bool
		expectKey bool
		name      string // of the container
		key       string // current key, in an object
	}
	var stack []frame
	name := func() string {
		if len(stack) == 0 {
			return ""
		}
		f := stack[len(stack)-1]
		if f.object {
			return f.key
		}
		return f.name
	}
	// afterValue marks the current object's key as used.
	afterValue := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].expectKey = true
		}
	}
	for {
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
				stack = append(stack, frame{object: v == '{', expectKey: v == '{', name: name()})
			default:
				stack = stack[:len(stack)-1]
				afterValue()
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].expectKey {
				f := &stack[n-1]
				f.key = v
				if f.name != "" {
					f.key = f.name + "." + v
				}
				f.expectKey = false
				in.value(tArgName, model.WAFInArgName, f.key, v, false)
				continue
			}
			in.value(tArg, model.WAFInArg, name(), v, false)
			afterValue()
		default:
			afterValue()
		}
	}
}

// multipart inspects a multipart form: field names, text fields and the
// names of uploaded files (as sent: Go's Part.FileName would strip a
// path traversal).
func (in *inspection) multipart(b []byte, boundary string, truncated bool) {
	mr := multipart.NewReader(bytes.NewReader(b), boundary)
	for {
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
		if !textPart(p.Header) {
			continue
		}
		data, err := io.ReadAll(p)
		in.value(tArg, model.WAFInArg, field, string(data), false)
		if err != nil {
			return
		}
	}
}

// textPart reports whether a part without a file name holds text.
func textPart(h textproto.MIMEHeader) bool {
	ct := h.Get("Content-Type")
	if ct == "" {
		return true
	}
	mt, _, _ := mime.ParseMediaType(ct)
	return classify(mt) != bodyNone || mt == "text/plain"
}
