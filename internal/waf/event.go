package waf

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// NewRequestID returns a request ID for the block page and the event:
// 16 hex digits, enough to find one request among years of events.
func NewRequestID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NewEvent describes a request the firewall blocked or would have. The
// client's text (path, host, User-Agent) is truncated and has control
// characters escaped, so that it can neither flood the store nor forge
// lines in a log or a notification. The query string is left out: it can
// carry secrets, and the matches show what mattered.
func NewEvent(r *http.Request, id, siteID, clientIP, action string, res Result, now time.Time) model.WAFEvent {
	return model.WAFEvent{
		ID: id, Time: now, SiteID: siteID, Action: action, ClientIP: clientIP,
		Method: safeText(r.Method, 16), Host: safeText(r.Host, 255), Path: safeText(r.URL.Path, 1024),
		UserAgent: safeText(r.UserAgent(), 512),
		Score:     res.Score, Threshold: res.Threshold, Paranoia: res.Paranoia, Matches: res.Matches,
	}
}
