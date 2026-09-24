package desktop

import (
	"fmt"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

// Level is the overall health the notification-area icon shows.
type Level int

const (
	LevelOK           Level = iota // green: the service runs and no site needs attention
	LevelWarning                   // amber: the service runs, something needs attention
	LevelDown                      // red: the service is stopped or not answering
	LevelNotInstalled              // grey
)

func (l Level) String() string {
	return [...]string{"ok", "warning", "down", "not installed"}[l]
}

// Health is the result of Overall: the level, a one-line summary for the
// icon's tooltip, and the sites counted as needing attention.
type Health struct {
	Level     Level
	Summary   string
	Attention []localapi.SiteSummary
}

// Overall reduces the service state (from the SCM, see service.Status)
// and the status endpoint's summary to what the icon shows. sum is nil when
// the status endpoint could not be read.
func Overall(service string, sum *localapi.Summary) Health {
	switch service {
	case "not installed":
		return Health{Level: LevelNotInstalled, Summary: "Service not installed"}
	case "stopped", "stopping", "paused":
		return Health{Level: LevelDown, Summary: "Service " + service}
	case "starting":
		return Health{Level: LevelWarning, Summary: "Service starting"}
	}
	if sum == nil {
		// The SCM says it runs, but it does not answer: hung, still
		// initializing, or the status pipe failed to open.
		return Health{Level: LevelDown, Summary: "Service not responding"}
	}

	h := Health{Level: LevelOK}
	running := 0
	for _, s := range sum.Sites {
		if s.State == model.StateRunning {
			running++
		}
		if SiteNeedsAttention(s) {
			h.Attention = append(h.Attention, s)
		}
	}
	h.Summary = fmt.Sprintf("Running · %d of %d sites running", running, len(sum.Sites))
	if n := len(h.Attention); n > 0 {
		h.Level = LevelWarning
		verb := "need"
		if n == 1 {
			verb = "needs"
		}
		h.Summary += fmt.Sprintf(" · %d %s attention", n, verb)
	}
	if sum.AdminError != "" {
		h.Level = LevelWarning
		h.Summary += " · web console unavailable"
	}
	return h
}

// SummaryOf summarizes a site as the status endpoint does
// (localserver.summary), so the manager flags the same sites as needing
// attention as the notification-area icon.
func SummaryOf(s localapi.Site) localapi.SiteSummary {
	ss := localapi.SiteSummary{ID: s.ID, Name: s.Name, Type: s.Type, AutoStart: s.AutoStart, State: s.Status.State, Message: s.Status.Message}
	if s.Node != nil {
		ss.Instances = s.Node.Instances
	}
	for _, in := range s.Status.Instances {
		if in.State == "ready" {
			ss.Ready++
		}
	}
	return ss
}

// SiteNeedsAttention decides whether a site turns the icon amber and
// appears under "needs attention" in its menu (a notification is shown when
// a site starts needing attention).
//
// The states are: stopped, starting, running, degraded (some instances
// down or unhealthy), stopping, failed (rapid-fail protection tripped).
// Useful fields beyond State: AutoStart (whether the site is meant to be
// running), and Instances/Ready for Node.js sites.
func SiteNeedsAttention(s localapi.SiteSummary) bool {
	// TODO: decide which states are worth an amber icon on your servers.
	// This placeholder only flags sites that NodeHoster itself gave up on.
	return s.State == model.StateFailed
}
