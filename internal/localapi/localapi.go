// Package localapi is the desktop manager's channel to the server: two
// local endpoints (named pipes on Windows, Unix sockets elsewhere) that do
// not depend on the web console's port, certificate or accounts.
//
//	Admin   the full API (api.LocalHandler) for elevated Administrators.
//	        The operating system authenticates the caller by the pipe's
//	        security descriptor; there is no password to lose.
//	Status  a read-only summary (site names and states, recent events)
//	        for the notification-area icon, which runs unelevated in every
//	        interactive session. It exposes no configuration.
//
// This package is the client side and what both sides share; the server
// is localserver, so that the desktop programs do not link the server.
package localapi

import (
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// Endpoint names one of the two local listeners.
type Endpoint int

const (
	Admin Endpoint = iota
	Status
)

func (e Endpoint) String() string {
	if e == Admin {
		return "admin"
	}
	return "status"
}

// Summary is what the status endpoint serves: enough for a tray icon and
// its menu, nothing an unprivileged user should not see.
type Summary struct {
	Version    string        `json:"version"`
	StartedAt  time.Time     `json:"startedAt"`
	AdminURL   string        `json:"adminUrl,omitempty"`
	AdminError string        `json:"adminError,omitempty"`
	Sites      []SiteSummary `json:"sites"`
}

type SiteSummary struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Type      model.SiteType  `json:"type"`
	AutoStart bool            `json:"autoStart"`
	State     model.SiteState `json:"state"`
	Message   string          `json:"message,omitempty"`
	Instances int             `json:"instances"` // configured (Node.js sites)
	Ready     int             `json:"ready"`     // instances serving traffic
}

// Notice is an operational event as the status endpoint streams it: the
// site's name instead of its ID, and no event types a tray would not show.
type Notice struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Type    string    `json:"type"`
	Site    string    `json:"site,omitempty"`
	Message string    `json:"message"`
}
