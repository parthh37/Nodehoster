package rewrite

import (
	"bufio"
	"bytes"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

// MaxBody is the largest response whose body outbound rules rewrite.
// Larger responses pass through unchanged rather than being held in
// memory.
const MaxBody = 8 << 20

var (
	tagRe  = regexp.MustCompile(`(?is)<([a-z]+)(\s[^>]*)>`)
	attrRe = regexp.MustCompile(`(?is)(\s)([a-z][a-z0-9_:-]*)(\s*=\s*)("[^"]*"|'[^']*'|[^\s"'>]+)`)
)

// ResponseWriter wraps w so the outbound rules apply to the response.
// The caller must call the returned function when the handler has
// returned, to send a collected body. When the engine has no outbound
// rules it returns w itself and a no-op.
func (e *Engine) ResponseWriter(w http.ResponseWriter, r *http.Request, env Env) (http.ResponseWriter, func()) {
	if !e.HasOutbound() {
		return w, func() {}
	}
	ow := &outWriter{ResponseWriter: w, e: e, r: r, env: env, original: r.URL.RequestURI()}
	return ow, ow.finish
}

type outWriter struct {
	http.ResponseWriter
	e        *Engine
	r        *http.Request
	env      Env
	original string

	status int
	buf    *bytes.Buffer // non-nil while the body is being collected
	html   bool
}

func (w *outWriter) scope() *scope {
	sc := &scope{maps: w.e.maps}
	sc.vars = func(name string) string {
		if h, ok := strings.CutPrefix(name, "RESPONSE_"); ok {
			if h == "STATUS" {
				return strconv.Itoa(w.status)
			}
			return w.Header().Get(strings.ReplaceAll(h, "_", "-"))
		}
		return w.e.requestVar(name, w.r, w.env, w.original)
	}
	return sc
}

func (w *outWriter) WriteHeader(code int) {
	if code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = code
	w.rewriteHeaders()
	if w.wantsBody() {
		w.buf = &bytes.Buffer{}
		w.Header().Del("Content-Length")
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *outWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.buf == nil {
		return w.ResponseWriter.Write(b)
	}
	if w.buf.Len()+len(b) > MaxBody {
		// Too big to rewrite: send what we have as it is and stream the rest.
		pending := w.buf
		w.buf = nil
		w.ResponseWriter.WriteHeader(w.status)
		if _, err := w.ResponseWriter.Write(pending.Bytes()); err != nil {
			return 0, err
		}
		return w.ResponseWriter.Write(b)
	}
	return w.buf.Write(b)
}

// Flush is ignored while collecting a body; a rewritten body is sent whole.
func (w *outWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.buf == nil {
		http.NewResponseController(w.ResponseWriter).Flush()
	}
}

func (w *outWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *outWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *outWriter) finish() {
	if w.buf == nil {
		return
	}
	body := w.e.rewriteBody(w.buf.Bytes(), w.html, w.scope())
	w.buf = nil
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.ResponseWriter.WriteHeader(w.status)
	w.ResponseWriter.Write(body)
}

// rewriteHeaders applies the header rules to every value of their header.
func (w *outWriter) rewriteHeaders() {
	h := w.Header()
	done := map[string]bool{}
	for _, rule := range w.e.out {
		if rule.Scope != "header" {
			continue
		}
		name := http.CanonicalHeaderKey(rule.Header)
		if done[name] {
			continue
		}
		done[name] = true
		vals := h.Values(name)
		for i, v := range vals {
			vals[i] = w.e.rewriteValue(v, "header", name, "", w.scope())
		}
	}
}

// wantsBody reports whether body or tag rules could apply to the response.
func (w *outWriter) wantsBody() bool {
	if w.r.Method == http.MethodHead || w.status == http.StatusNoContent || w.status == http.StatusNotModified ||
		w.status == http.StatusSwitchingProtocols || w.Header().Get("Content-Encoding") != "" {
		return false
	}
	ct, _, _ := mime.ParseMediaType(w.Header().Get("Content-Type"))
	w.html = ct == "text/html" || ct == "application/xhtml+xml"
	text := w.html || strings.HasPrefix(ct, "text/") || ct == "application/json" || ct == "application/javascript" ||
		ct == "application/xml" || strings.HasSuffix(ct, "+xml") || strings.HasSuffix(ct, "+json")
	if !text || ct == "text/event-stream" {
		return false
	}
	for _, rule := range w.e.out {
		if rule.Scope == "body" || (rule.Scope == "tags" && w.html) {
			return true
		}
	}
	return false
}

// rewriteValue runs the rules of one scope over a single value (a header
// value or a tag attribute). A matching rule replaces the whole value, as
// IIS outbound rules do; "stop" ends processing for that value.
func (e *Engine) rewriteValue(v, scopeKind, header, tag string, sc *scope) string {
	for _, rule := range e.out {
		if rule.Scope != scopeKind {
			continue
		}
		if scopeKind == "header" && !strings.EqualFold(rule.Header, header) {
			continue
		}
		if scopeKind == "tags" && !rule.tags[tag] {
			continue
		}
		m := rule.re.FindStringSubmatch(v)
		if (m != nil) == rule.Negate {
			continue
		}
		sc.rule, sc.named, sc.cond = m, rule.named, nil
		if !e.conditionsMatch(rule.conds, rule.MatchAny, sc) {
			continue
		}
		if rule.Action == "rewrite" {
			v = rule.value.expand(sc)
		}
		if rule.Stop {
			break
		}
	}
	return v
}

func (e *Engine) rewriteBody(body []byte, html bool, sc *scope) []byte {
	s := string(body)
	if html && e.hasScope("tags") {
		s = tagRe.ReplaceAllStringFunc(s, func(tag string) string {
			m := tagRe.FindStringSubmatch(tag)
			name := strings.ToLower(m[1])
			attrs, ok := model.OutboundTags[name]
			if !ok {
				return tag
			}
			return "<" + m[1] + attrRe.ReplaceAllStringFunc(m[2], func(a string) string {
				am := attrRe.FindStringSubmatch(a)
				if !containsFold(attrs, am[2]) {
					return a
				}
				val, quote := am[4], ""
				if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') {
					quote, val = val[:1], val[1:len(val)-1]
				}
				nv := e.rewriteValue(val, "tags", "", name, sc)
				if nv == val {
					return a
				}
				if quote == "" {
					quote = `"`
				}
				return am[1] + am[2] + am[3] + quote + strings.ReplaceAll(nv, quote, "&#"+strconv.Itoa(int(quote[0]))+";") + quote
			}) + ">"
		})
	}
	for _, rule := range e.out {
		if rule.Scope != "body" || rule.Negate {
			continue
		}
		replaced := false
		s = replaceAll(rule.re, s, func(m []string) string {
			sc.rule, sc.named, sc.cond = m, rule.named, nil
			if !e.conditionsMatch(rule.conds, rule.MatchAny, sc) {
				return m[0]
			}
			replaced = true
			if rule.Action != "rewrite" {
				return m[0]
			}
			return rule.value.expand(sc)
		})
		if replaced && rule.Stop {
			break
		}
	}
	return []byte(s)
}

// RewritesBodies reports whether outbound rules read response bodies. The
// proxy then asks upstreams for uncompressed responses, since a
// compressed body cannot be rewritten (the site's own compression, if
// on, still applies afterwards).
func (e *Engine) RewritesBodies() bool {
	return e != nil && (e.hasScope("body") || e.hasScope("tags"))
}

func (e *Engine) hasScope(s string) bool {
	for _, r := range e.out {
		if r.Scope == s {
			return true
		}
	}
	return false
}

// replaceAll replaces every match of re in s with what fn returns for its
// submatches.
func replaceAll(re *regexp.Regexp, s string, fn func([]string) string) string {
	idx := re.FindAllStringSubmatchIndex(s, -1)
	if idx == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range idx {
		m := make([]string, len(loc)/2)
		for i := range m {
			if loc[2*i] >= 0 {
				m[i] = s[loc[2*i]:loc[2*i+1]]
			}
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(fn(m))
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
