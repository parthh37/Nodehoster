package desktop

import (
	"fmt"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

// CriticalAlerts are the critical resource alerts firing that nobody has
// silenced: they turn the status icon amber and are listed in its menu.
// Warnings and silenced alerts only show in the consoles.
func CriticalAlerts(sum *localapi.Summary) []localapi.AlertSummary {
	if sum == nil {
		return nil
	}
	var out []localapi.AlertSummary
	for _, a := range sum.Alerts {
		if a.Severity == model.SeverityCritical && !a.Silenced {
			out = append(out, a)
		}
	}
	return out
}

// AlertText is an alert in a menu: "api.example.com: CPU 93% for 5 min
// (limit 85%)", or "Server: …" for the machine's own.
func AlertText(a localapi.AlertSummary) string {
	who := a.Site
	if who == "" {
		who = "Server"
	}
	return who + ": " + a.Message
}

// alertHealth adds the critical alerts to the icon's health.
func alertHealth(h *Health, sum *localapi.Summary) {
	n := len(CriticalAlerts(sum))
	if n == 0 {
		return
	}
	h.Level = max(h.Level, LevelWarning)
	noun := "critical alerts"
	if n == 1 {
		noun = "critical alert"
	}
	h.Summary += fmt.Sprintf(" · %d %s", n, noun)
}
