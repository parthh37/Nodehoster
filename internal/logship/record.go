// Package logship sends NodeHoster's logs to central log systems: syslog
// (RFC 5424 over UDP, TCP or TLS), Seq (CLEF) and any HTTP endpoint that
// takes JSON. Shipping never slows what is logged: records go into a
// bounded queue per target, and when a collector is slow or down the
// oldest queued records are dropped (and counted), not the app.
package logship

import (
	"strings"
	"time"
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
