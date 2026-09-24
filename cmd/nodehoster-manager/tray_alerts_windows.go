package main

import (
	"fmt"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/tailscale/walk"
)

// alertItems lists the critical resource alerts firing (not silenced) in
// the status icon's menu, under the sites needing attention; each opens
// the manager at its site (or the server, for the machine's own).
func (t *tray) alertItems(add func(icon, text string, enabled bool, fn func()) *walk.Action) {
	crit := desktop.CriticalAlerts(t.sum)
	for i, al := range crit {
		if i == 5 {
			add("", fmt.Sprintf("and %d more critical alerts…", len(crit)-5), false, nil)
			return
		}
		text := []rune(desktop.AlertText(al))
		if len(text) > 90 {
			text = append(text[:89], '…')
		}
		siteID := al.SiteID
		add(desktop.IconWarning, string(text), true, func() {
			if siteID != "" {
				startManager("--site", siteID)
			} else {
				startManager()
			}
		})
	}
}
