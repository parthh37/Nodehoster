// Package logship sends NodeHoster's logs to central log systems: syslog
// (RFC 5424 over UDP, TCP or TLS), Seq (CLEF) and any HTTP endpoint that
// takes JSON. Shipping never slows what is logged: records go into a
// bounded queue per target, and when a collector is slow or down the
// oldest queued records are dropped (and counted), not the app.
package logship

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Record is one log entry, whatever its source.
type Record struct {
	Time     time.Time `json:"time"`
	Source   string    `json:"source"` // server | app | access | event | audit
	Level    string    `json:"level"`  // debug | info | warning | error
	Message  string    `json:"message"`
	SiteID   string    `json:"siteId,omitempty"`
	SiteName string    `json:"site,omitempty"`
	Instance *int      `json:"instance,omitempty"`
	Stream   string    `json:"stream,omitempty"` // stdout | stderr | system (app)
	// Access log fields (source access).
	Access *AccessFields `json:"access,omitempty"`
	// Attrs are the server log's attributes, an event's type, an audit
	// entry's user, action and target.
	Attrs map[string]string `json:"attrs,omitempty"`
}

type AccessFields struct {
	Method     string  `json:"method"`
	Path       string  `json:"path"`
	Status     int     `json:"status"`
	Bytes      int64   `json:"bytes"`
	DurationMs float64 `json:"durationMs"`
	ClientIP   string  `json:"clientIp"`
	Host       string  `json:"host"`
	UserAgent  string  `json:"userAgent,omitempty"`
	Referer    string  `json:"referer,omitempty"`
	Protocol   string  `json:"protocol,omitempty"` // HTTP/1.1, HTTP/2.0, HTTP/3.0
}

// AppLevel is the level of a line of application output: stdout is info;
// stderr is a warning, or an error when it says so (Node.js writes both
// warnings and uncaught exceptions there).
func AppLevel(stream, text string) string {
	switch stream {
	case "stderr":
		head := strings.ToLower(text)
		if len(head) > 200 {
			head = head[:200]
		}
		for _, w := range []string{"error", "exception", "fatal", "panic", "unhandled", "    at "} {
			if strings.Contains(head, w) {
				return "error"
			}
		}
		return "warning"
	case "system":
		return "info"
	}
	return "info"
}

// AccessLevel is an access record's level from its status code.
func AccessLevel(status int) string {
	switch {
	case status >= 500:
		return "error"
	case status >= 400:
		return "warning"
	}
	return "info"
}

// Field limits. A request line or a log message can be megabytes (a 1 MiB
// query string, an app printing a whole document): shipped whole, a queue
// of them would hold gigabytes while a collector is down, and most
// collectors refuse such events anyway (Seq's default event limit is
// 256 KiB, syslog relays cut at 8 KiB).
const (
	maxMessage = 16 << 10 // message
	maxField   = 4 << 10  // path, user agent, referer, host, method, an attribute's value
	maxAttrs   = 64       // attributes per record
	truncMark  = "…[truncated]"
)

// cut shortens s to at most max bytes (on a UTF-8 boundary), marking it.
func cut(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	n := max - len(truncMark)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + truncMark, true
}

// limit truncates the record's oversized fields. The access fields and the
// attributes are copied before they are changed: the caller may still use
// them.
func (r *Record) limit() {
	r.Message, _ = cut(r.Message, maxMessage)
	if a := r.Access; a != nil {
		c := *a
		var t [5]bool
		c.Method, t[0] = cut(c.Method, maxField)
		c.Path, t[1] = cut(c.Path, maxField)
		c.Host, t[2] = cut(c.Host, maxField)
		c.UserAgent, t[3] = cut(c.UserAgent, maxField)
		c.Referer, t[4] = cut(c.Referer, maxField)
		if t != [5]bool{} {
			r.Access = &c
		}
	}
	big := len(r.Attrs) > maxAttrs
	for k, v := range r.Attrs {
		if len(k) > 256 || len(v) > maxField {
			big = true
			break
		}
	}
	if !big {
		return
	}
	keys := make([]string, 0, len(r.Attrs))
	for k := range r.Attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys) // which attributes are kept must not be random
	attrs := make(map[string]string, min(len(keys), maxAttrs))
	for _, k := range keys[:min(len(keys), maxAttrs)] {
		v, _ := cut(r.Attrs[k], maxField)
		k, _ = cut(k, 256)
		attrs[k] = v
	}
	if len(keys) > maxAttrs {
		attrs["truncated"] = "true"
	}
	r.Attrs = attrs
}

// size is roughly what the record takes in memory and on the wire, for the
// queue's and the batches' byte budgets.
func (r *Record) size() int {
	n := 256 + len(r.Message) + len(r.SiteID) + len(r.SiteName) + len(r.Stream) + len(r.Source) + len(r.Level)
	if a := r.Access; a != nil {
		n += 128 + len(a.Method) + len(a.Path) + len(a.Host) + len(a.UserAgent) + len(a.Referer) + len(a.ClientIP)
	}
	for k, v := range r.Attrs {
		n += 16 + len(k) + len(v)
	}
	return n
}
