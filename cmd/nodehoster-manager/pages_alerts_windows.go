package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- Alerts: resource alerts firing and pending, and the recent ones,
// with Silence and Acknowledge (like acknowledging an alert in Azure
// Monitor). Rules are edited in the web console (Settings › Alerts, and a
// site's Alerts tab).

type alertsPage struct {
	page
	list    model.AlertList
	history []model.Alert
	loaded  bool
	loading bool
	at      time.Time // last load
	bar     infoBar
	active  table
	recent  table
	count   *walk.Label

	critical, warning, pending, state statCard

	silence, ack, unsilence, openSite, rules, refresh *command
}

func (s *alertsPage) init(m *manager) *page {
	s.icon = desktop.IconWarning
	s.title = func() string { return "Alerts" }
	s.subtitle = func() string {
		if !s.loaded {
			return "Resource alerts"
		}
		return plural(len(s.list.Firing), "alert") + " firing · " + plural(len(s.list.Pending), "condition") + " pending"
	}
	s.load = func() { s.reload(m) }
	// The manager refreshes every few seconds; alerts are evaluated every
	// 15 s, so reading them as often is enough.
	s.update = func() {
		if time.Since(s.at) >= 15*time.Second {
			s.reload(m)
		}
		s.enable(m)
	}
	s.silence = newCommand("Silence…", desktop.IconPause, func() { s.silenceSelected(m, false) })
	s.ack = newCommand("Acknowledge", desktop.IconOK, func() { s.silenceSelected(m, true) })
	s.unsilence = newCommand("Unsilence", desktop.IconStart, func() { s.unsilenceSelected(m) })
	s.openSite = newCommand("Open site", desktop.IconSites, func() {
		if a := s.selected(); a != nil && a.SiteID != "" {
			m.showSite(a.SiteID)
		}
	})
	s.rules = newCommand("Alert rules…", desktop.IconConsole, func() { m.openConsolePath("/settings/alerts") })
	s.refresh = newCommand("Refresh", desktop.IconRefresh, func() { s.reload(m) })
	s.active.onSelect = func() { s.enable(m) }
	s.active.color = func(row, col int) (walk.Color, bool) {
		if a := s.activeAt(row); a != nil && col == 0 {
			switch {
			case a.State == model.AlertPending:
				return colorMuted, true
			case a.Severity == model.SeverityCritical:
				return colorError, true
			}
			return colorWarning, true
		}
		return 0, false
	}
	s.active.icon = func(row, col int) walk.Image {
		if a := s.activeAt(row); a != nil && col == 0 {
			switch {
			case a.Silence != nil:
				return img(desktop.IconPause)
			case a.State == model.AlertPending:
				return img(desktop.IconClock)
			case a.Severity == model.SeverityCritical:
				return img(desktop.IconError)
			}
			return img(desktop.IconWarning)
		}
		return nil
	}
	return &s.page
}

func (s *alertsPage) content(m *manager) []Widget {
	return []Widget{
		s.bar.widget(),
		cards(s.critical.widget(desktop.IconError, "Critical", false), s.warning.widget(desktop.IconWarning, "Warnings", false),
			s.pending.widget(desktop.IconClock, "Pending", false), s.state.widget(desktop.IconSettings, "Alerts", false)),
		heading("Firing and pending"),
		s.active.viewWith(tableOpts{name: "alertsActive", sortable: true, onActivate: s.openSite.trigger,
			menu: menu(s.silence, s.ack, s.unsilence, nil, s.openSite)},
			col("Severity", 90), col("Site", 160), col("Alert", 420), col("Since", 130), col("Silenced", 170)),
		Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{heading("Recent alerts"), HSpacer{}, Label{AssignTo: &s.count, TextColor: colorMuted}}},
		s.recent.viewWith(tableOpts{name: "alertsRecent", sortable: true},
			col("Fired", 130), col("Site", 160), col("Severity", 80), col("Alert", 420), col("Resolved", 130)),
		hint("An alert fires when its condition has held for its rule's period, and resolves once the condition has been clear for the recovery period. " +
			"Silenced alerts notify nobody; an acknowledged one stays quiet until it resolves."),
	}
}

func (s *alertsPage) actionsPane(m *manager) []Widget {
	return pane(
		"Alerts", s.rules, s.refresh,
		"Selected alert", s.silence, s.ack, s.unsilence, s.openSite,
	)
}

func (s *alertsPage) reload(m *manager) {
	if s.loading || !m.connected() {
		s.enable(m)
		return
	}
	s.loading, s.at = true, time.Now()
	go func() {
		var l model.AlertList
		var h []model.Alert
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := m.cl.Get(ctx, "/api/alerts", &l)
		if err == nil {
			err = m.cl.Get(ctx, "/api/alerts/history?limit=100", &h)
		}
		cancel()
		m.mw.Synchronize(func() {
			s.loading = false
			if err != nil {
				s.bar.show(barError, "Could not load the alerts: "+err.Error(), "Retry", func() { s.reload(m) })
				s.enable(m)
				return
			}
			s.list, s.history, s.loaded = l, h, true
			s.redraw(m)
		})
	}()
}

// all is the active list as shown: firing first, then pending.
func (s *alertsPage) all() []model.Alert {
	return append(append([]model.Alert{}, s.list.Firing...), s.list.Pending...)
}

// activeAt is the alert of a model row of the active list.
func (s *alertsPage) activeAt(row int) *model.Alert {
	if row < len(s.list.Firing) {
		return &s.list.Firing[row]
	}
	if row -= len(s.list.Firing); row < len(s.list.Pending) {
		return &s.list.Pending[row]
	}
	return nil
}

func (s *alertsPage) selected() *model.Alert {
	id := s.active.selected()
	for _, a := range s.all() {
		if a.ID == id {
			return &a
		}
	}
	return nil
}

func alertSite(a model.Alert) string {
	if a.SiteID == "" {
		return "(server)"
	}
	return a.SiteName
}

func alertTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

func (s *alertsPage) redraw(m *manager) {
	var crit, warn int
	for _, a := range s.list.Firing {
		if a.Severity == model.SeverityCritical {
			crit++
		} else {
			warn++
		}
	}
	colored := func(n int, c walk.Color) walk.Color {
		if n > 0 {
			return c
		}
		return 0
	}
	s.critical.set(fmt.Sprint(crit), "firing", colored(crit, colorError))
	s.warning.set(fmt.Sprint(warn), "firing", colored(warn, colorWarning))
	s.pending.set(fmt.Sprint(len(s.list.Pending)), "past a limit, not yet for long enough", 0)
	switch {
	case !s.list.Enabled:
		s.state.set("Off", "turn on in the web console", colorMuted)
		s.bar.show(barInfo, "Alerts are off: no rule is evaluated. Turn them on in the web console (Settings › Alerts).", "Alert rules…", s.rules.trigger)
	case crit > 0:
		s.state.set("On", "", colorOK)
		s.bar.show(barWarning, plural(crit, "critical alert")+" firing.", "", nil)
	default:
		s.state.set("On", "", colorOK)
		s.bar.hide()
	}

	all := s.all()
	keys := make([]string, len(all))
	rows := make([][]string, len(all))
	for i, a := range all {
		sev := a.Severity
		if a.State == model.AlertPending {
			sev = "pending"
		}
		silenced := ""
		if si := a.Silence; si != nil {
			silenced = "acknowledged by " + si.By
			if si.Until != nil {
				silenced = "until " + si.Until.Local().Format("Jan 2 15:04") + " by " + si.By
			}
		}
		since := a.Since
		keys[i] = a.ID
		rows[i] = []string{sev, alertSite(a), a.Message, alertTime(&since), silenced}
	}
	s.active.set(keys, rows)

	keys = make([]string, len(s.history))
	rows = make([][]string, len(s.history))
	for i, a := range s.history {
		resolved := alertTime(a.ResolvedAt)
		if a.State == model.AlertFiring {
			resolved = "firing"
		}
		keys[i] = a.ID
		rows[i] = []string{alertTime(a.FiredAt), alertSite(a), a.Severity, a.Message, resolved}
	}
	s.recent.set(keys, rows)
	if s.count != nil {
		s.count.SetText(plural(len(s.history), "alert"))
	}
	if m.cur == &s.page {
		m.updateHeader()
	}
	s.enable(m)
}

func (s *alertsPage) enable(m *manager) {
	a := s.selected()
	on := a != nil && m.connected()
	setEnabled(on, s.silence, s.ack)
	setEnabled(on && a.Silence != nil, s.unsilence)
	setEnabled(a != nil && a.SiteID != "", s.openSite)
}

var (
	silenceMinutes = []int{30, 60, 4 * 60, 24 * 60, 7 * 24 * 60}
	silenceNames   = []string{"30 minutes", "1 hour", "4 hours", "1 day", "1 week"}
)

// silenceSelected silences the selected alert for a while, or until it
// resolves (ack).
func (s *alertsPage) silenceSelected(m *manager, ack bool) {
	a := s.selected()
	if a == nil {
		return
	}
	req := model.AlertSilenceRequest{}
	if !ack {
		var dur *walk.ComboBox
		var note *walk.LineEdit
		ok := runDialog(m.mw, "Silence alert", Size{Width: 480}, []Widget{
			intro(desktop.IconPause, "No notification or reminder is sent for this alert while it is silenced. If it resolves and fires again before the silence ends, it stays quiet."),
			Label{Text: alertSite(*a) + ": " + a.Message, EllipsisMode: EllipsisEnd},
			Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
				Label{Text: "Silence for:"}, ComboBox{AssignTo: &dur, Model: silenceNames, CurrentIndex: 1},
				Label{Text: "Note:"}, LineEdit{AssignTo: &note, CueBanner: "why (optional)", MaxLength: 500},
			}},
		}, func(dlg *walk.Dialog) bool {
			req = model.AlertSilenceRequest{Minutes: silenceMinutes[max(dur.CurrentIndex(), 0)], Note: strings.TrimSpace(note.Text())}
			return true
		})
		if !ok {
			return
		}
	}
	what := "Silencing the alert"
	if ack {
		what = "Acknowledging the alert"
	}
	id := a.ID
	m.do(what, func(ctx context.Context) error {
		return m.cl.Post(ctx, "/api/alerts/"+url.PathEscape(id)+"/silence", req, nil)
	})
	s.at = time.Time{} // reload at the next refresh
}

func (s *alertsPage) unsilenceSelected(m *manager) {
	a := s.selected()
	if a == nil {
		return
	}
	id := a.ID
	m.do("Unsilencing the alert", func(ctx context.Context) error {
		return m.cl.Delete(ctx, "/api/alerts/"+url.PathEscape(id)+"/silence")
	})
	s.at = time.Time{}
}
