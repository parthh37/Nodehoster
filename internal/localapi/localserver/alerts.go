package localserver

import (
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/localapi"
)

// alertSummaries are the resource alerts firing, for the status icon: it
// turns amber for critical ones that nobody silenced.
func alertSummaries(c *core.Core) []localapi.AlertSummary {
	var out []localapi.AlertSummary
	for _, a := range c.Alerts.List().Firing {
		out = append(out, localapi.AlertSummary{
			ID: a.ID, SiteID: a.SiteID, Site: a.SiteName, Severity: a.Severity,
			Message: a.Message, Silenced: a.Silence != nil,
		})
	}
	return out
}
