package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- Web application firewall: each site's mode, and the requests it
// blocked (or, in detect mode, would have), with "exclude" for a false
// positive. The rules, paranoia levels and exclusion editing are in the
// web console; this page covers what an administrator on the server needs
// quickly: see what is being blocked, switch a site between off, detect
// and block, and let a wrongly blocked request through.

type wafPage struct {
	page
	tabs           *walk.TabWidget
	events, sites  table
	list           []model.WAFEvent
	action         *walk.ComboBox
	findEvents     *walk.LineEdit
	findSites      *walk.LineEdit
	countEvents    *walk.Label
	countSites     *walk.Label
	bar            infoBar
	loaded         time.Time
	blocking, seen int
	cfgs           map[string]model.WAFConfig // by site, as last loaded

	refresh, details, exclude, copyID, setMode, console *command
}

var wafActionFilters = []string{"All requests", "Blocked", "Detected only"}

var (
	wafModes     = []string{model.WAFOff, model.WAFDetect, model.WAFBlock}
	wafModeNames = []string{"Off — requests are not inspected", "Detect — log what would be blocked, block nothing", "Block — refuse attacks with 403"}
)

func (s *wafPage) init(m *manager) *page {
	s.icon = desktop.IconShield
	s.title = func() string { return "Web application firewall" }
	s.subtitle = func() string {
		return fmt.Sprintf("%s blocking of %s · the last 500 events", plural(s.blocking, "site"), plural(s.seen, "site"))
	}
	s.search = func() *walk.LineEdit {
		if s.tabs != nil && s.tabs.CurrentIndex() == 1 {
			return s.findSites
		}
		return s.findEvents
	}
	s.load = func() { s.reload(m) }
	s.update = func() {
		if time.Since(s.loaded) > 15*time.Second {
			s.reload(m)
		}
	}
	s.refresh = newCommand("Refresh", desktop.IconRefresh, func() { s.reload(m) })
	s.details = newCommand("Details…", desktop.IconEye, func() { s.showDetails(m) })
	s.exclude = newCommand("Exclude the rules…", desktop.IconUnlock, func() { s.excludeEvent(m) })
	s.copyID = newCommand("Copy request ID", desktop.IconCopy, func() {
		if ev := s.currentEvent(); ev != nil {
			walk.Clipboard().SetText(ev.ID)
		}
	})
	s.setMode = newCommand("Set mode…", desktop.IconShield, func() { s.chooseMode(m) })
	s.console = newCommand("Rules and exclusions…", desktop.IconConsole, func() {
		if id := s.currentSiteID(); id != "" {
			m.openConsolePath("/sites/" + url.PathEscape(id) + "/firewall")
			return
		}
		m.openConsolePath("/firewall")
	})
	s.events.onSelect = func() { s.enable(m) }
	s.sites.onSelect = func() { s.enable(m) }
	s.events.match = func(row int) bool {
		if row >= len(s.list) || s.action == nil {
			return true
		}
		switch s.action.CurrentIndex() {
		case 1:
			return s.list[row].Action == model.WAFActionBlocked
		case 2:
			return s.list[row].Action == model.WAFActionDetected
		}
		return true
	}
	s.events.color = func(row, col int) (walk.Color, bool) {
		if row < len(s.list) && col == 1 {
			if s.list[row].Action == model.WAFActionBlocked {
				return colorError, true
			}
			return colorWarning, true
		}
		return 0, false
	}
	s.events.icon = func(row, col int) walk.Image {
		if row >= len(s.list) || col != 1 {
			return nil
		}
		if s.list[row].Action == model.WAFActionBlocked {
			return img(desktop.IconBan)
		}
		return img(desktop.IconEye)
	}
	s.sites.color = func(row, col int) (walk.Color, bool) {
		if col == 1 && row < len(s.sites.rows) && s.sites.rows[row][1] == "Off" {
			return colorMuted, true
		}
		return 0, false
	}
	return &s.page
}

func (s *wafPage) content(m *manager) []Widget {
	eventMenu := menu(s.details, s.exclude, s.copyID, nil, s.setMode, s.refresh)
	return []Widget{
		s.bar.widget(),
		TabWidget{AssignTo: &s.tabs, OnCurrentIndexChanged: func() { s.enable(m); m.updateCommands() }, Pages: []TabPage{
			{Title: "Blocked requests", Image: img(desktop.IconBan), Layout: VBox{}, Children: []Widget{
				searchRow(&s.findEvents, &s.countEvents, "Search addresses, paths, rules and request IDs", &s.events,
					Label{Text: "Show:"},
					ComboBox{AssignTo: &s.action, Model: wafActionFilters, CurrentIndex: 0, OnCurrentIndexChanged: func() {
						s.events.refilter()
						updateCount(s.countEvents, &s.events)
					}}),
				s.events.viewWith(tableOpts{name: "waf-events", sortable: true, onActivate: s.details.trigger, menu: eventMenu},
					col("Time", 140), col("Action", 90), col("Site", 130), col("Client", 120), col("Request", 260), col("Rules", 230), colR("Score", 60), col("Request ID", 130)),
			}},
			{Title: "Sites", Image: img(desktop.IconSites), Layout: VBox{}, Children: []Widget{
				searchRow(&s.findSites, &s.countSites, "Search sites", &s.sites),
				s.sites.viewWith(tableOpts{name: "waf-sites", sortable: true, onActivate: s.setMode.trigger, menu: menu(s.setMode, s.console, nil, s.refresh)},
					col("Site", 180), col("Mode", 90), colR("Paranoia", 70), colR("Threshold", 75), colR("Exclusions", 80), colR("Blocked", 70), colR("Detected", 70)),
			}},
		}},
		hint("Counts are since the service started. Paranoia levels, thresholds and exclusions by path, argument, cookie or header are edited on each site's Firewall tab in the web console."),
	}
}

func (s *wafPage) actionsPane(m *manager) []Widget {
	return pane(
		"Web application firewall", s.refresh, s.console,
		"Selected request", s.details, s.exclude, s.copyID,
		"Site", s.setMode,
	)
}

// reload reads the events, and each site's mode and counters.
func (s *wafPage) reload(m *manager) {
	s.enable(m)
	if !m.connected() {
		return
	}
	s.loaded = time.Now()
	loadInto(m, "/api/waf/events?limit=500", func(list []model.WAFEvent) {
		s.list = list
		names := map[string]string{}
		for _, st := range m.sites {
			names[st.ID] = st.Name
		}
		keys := make([]string, len(list))
		rows := make([][]string, len(list))
		for i, ev := range list {
			keys[i] = strconv.FormatInt(ev.Seq, 10)
			rules := make([]string, len(ev.Matches))
			for j, mt := range ev.Matches {
				rules[j] = strconv.Itoa(mt.RuleID)
			}
			action := "Blocked"
			if ev.Action == model.WAFActionDetected {
				action = "Detected"
			}
			rows[i] = []string{ev.Time.Local().Format("2006-01-02 15:04:05"), action, names[ev.SiteID], ev.ClientIP,
				ev.Method + " " + ev.Path, strings.Join(rules, ", "), fmt.Sprintf("%d/%d", ev.Score, ev.Threshold), ev.ID}
		}
		s.events.set(keys, rows)
		updateCount(s.countEvents, &s.events)
		s.enable(m)
	})
	// Counters come with each site's firewall; the list of sites is the
	// manager's, refreshed every few seconds.
	sites := make([]string, 0, len(m.sites))
	names := map[string]string{}
	for _, st := range m.sites {
		if st.Type != model.SiteWorker {
			sites = append(sites, st.ID)
			names[st.ID] = st.Name
		}
	}
	go func() {
		type row struct {
			id  string
			cfg model.WAFConfig
			bl  int64
			det int64
		}
		var rows []row
		var firstErr error
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		for _, id := range sites {
			var v struct {
				Config model.WAFConfig `json:"config"`
				Stats  struct {
					Blocked  int64 `json:"blocked"`
					Detected int64 `json:"detected"`
				} `json:"stats"`
			}
			if err := m.cl.Get(ctx, "/api/sites/"+url.PathEscape(id)+"/waf", &v); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			rows = append(rows, row{id, v.Config, v.Stats.Blocked, v.Stats.Detected})
		}
		cancel()
		m.mw.Synchronize(func() {
			if firstErr != nil {
				s.bar.show(barError, "Could not load the sites' firewall settings: "+firstErr.Error(), "Retry", func() { s.reload(m) })
			}
			keys := make([]string, len(rows))
			cells := make([][]string, len(rows))
			s.blocking, s.seen = 0, len(rows)
			s.cfgs = map[string]model.WAFConfig{}
			for i, r := range rows {
				mode := map[string]string{model.WAFDetect: "Detect", model.WAFBlock: "Block"}[r.cfg.Mode]
				if mode == "" {
					mode = "Off"
				}
				if r.cfg.Mode == model.WAFBlock {
					s.blocking++
				}
				keys[i] = r.id
				s.cfgs[r.id] = r.cfg
				cells[i] = []string{names[r.id], mode, strconv.Itoa(r.cfg.Paranoia()), strconv.Itoa(r.cfg.Threshold()),
					strconv.Itoa(len(r.cfg.Exclusions)), strconv.FormatInt(r.bl, 10), strconv.FormatInt(r.det, 10)}
			}
			s.sites.set(keys, cells)
			updateCount(s.countSites, &s.sites)
			if firstErr == nil {
				switch {
				case s.seen == 0:
					s.bar.hide()
				case s.blocking == 0:
					s.bar.show(barInfo, "No site blocks attacks yet. Sites in detect mode log what would be blocked: check the list for false positives, then switch them to Block.",
						"Set mode…", s.setMode.trigger)
				default:
					s.bar.hide()
				}
			}
			if m.cur == &s.page {
				m.updateHeader()
			}
			s.enable(m)
		})
	}()
}

func (s *wafPage) currentEvent() *model.WAFEvent {
	if r := s.events.current(); r >= 0 && r < len(s.list) { // a model row
		return &s.list[r]
	}
	return nil
}

// currentSiteID is the site of the selected row: on the Sites tab the
// site, on the events tab the event's.
func (s *wafPage) currentSiteID() string {
	if s.tabs != nil && s.tabs.CurrentIndex() == 1 {
		return s.sites.selected()
	}
	if ev := s.currentEvent(); ev != nil {
		return ev.SiteID
	}
	return ""
}

func (s *wafPage) enable(m *manager) {
	ev := s.currentEvent() != nil && (s.tabs == nil || s.tabs.CurrentIndex() == 0)
	setEnabled(ev, s.details, s.copyID)
	setEnabled(ev && m.connected(), s.exclude)
	setEnabled(s.currentSiteID() != "" && m.connected(), s.setMode)
}

func (s *wafPage) showDetails(m *manager) {
	ev := s.currentEvent()
	if ev == nil {
		return
	}
	var b strings.Builder
	for _, mt := range ev.Matches {
		fmt.Fprintf(&b, "%d  %s (+%d) — %s, in %s\n", mt.RuleID, mt.Severity, mt.Score, mt.Message, mt.Where())
		if mt.Snippet != "" {
			fmt.Fprintf(&b, "      %s\n", mt.Snippet)
		}
	}
	action := "Blocked"
	icon := walk.TaskDialogSystemIconError
	if ev.Action == model.WAFActionDetected {
		action, icon = "Detected (not blocked)", walk.TaskDialogSystemIconWarning
	}
	notify(m.mw, "Firewall event", fmt.Sprintf("%s %s %s", action, ev.Method, ev.Path),
		fmt.Sprintf("From %s to %s at %s. Score %d of %d (paranoia level %d). Request ID %s.\nUser-Agent: %s",
			ev.ClientIP, ev.Host, ev.Time.Local().Format("2006-01-02 15:04:05"), ev.Score, ev.Threshold, ev.Paranoia, ev.ID, ev.UserAgent),
		b.String(), icon)
}

// excludeEvent adds the narrowest exclusion that lets the selected
// request through, after saying what it is.
func (s *wafPage) excludeEvent(m *manager) {
	ev := s.currentEvent()
	if ev == nil {
		return
	}
	x := ev.SuggestExclusion()
	x.Comment = "From request " + ev.ID + " (NodeHoster Manager)"
	wider := x
	wider.Args, wider.Cookies, wider.Headers = nil, nil, nil
	choices := [][2]string{{"Exclude " + x.String(), "The narrowest exclusion that lets this request through."}}
	if wider.String() != x.String() {
		choices = append(choices, [2]string{"Exclude " + wider.String(), "For every argument, cookie and header under the path."})
	}
	choice := ask(m.mw, "Firewall exclusion", "Let requests like this one through?",
		"Only do this for a false positive: an attack matching these rules would then reach the site too. Exclusions can be edited or removed on the site's Firewall tab in the web console.",
		walk.TaskDialogSystemIconWarning, choices...)
	if choice < 0 {
		return
	}
	if choice == 1 {
		x = wider
	}
	m.do("Adding a firewall exclusion", func(ctx context.Context) error {
		return m.cl.Post(ctx, "/api/sites/"+url.PathEscape(ev.SiteID)+"/waf/exclusions", x, nil)
	})
}

// chooseMode switches a site between off, detect and block (and its
// paranoia level), keeping the rest of its firewall configuration.
func (s *wafPage) chooseMode(m *manager) {
	id := s.currentSiteID()
	if id == "" {
		return
	}
	name := id
	for _, st := range m.sites {
		if st.ID == id {
			name = st.Name
		}
	}
	cfg := s.cfgs[id] // as last loaded: only the dialog's starting point
	modeIdx := 0
	for i, md := range wafModes {
		if md == cfg.Mode {
			modeIdx = i
		}
	}
	var mode, paranoia *walk.ComboBox
	ok := runDialog(m.mw, "Web application firewall — "+name, Size{Width: 480}, []Widget{
		intro(desktop.IconShield, "Detect first: it logs what would be blocked without blocking anything. Switch to Block once the events show no false positives (or exclusions cover them)."),
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Mode:"}, ComboBox{AssignTo: &mode, Model: wafModeNames, CurrentIndex: modeIdx},
			Label{Text: "Paranoia level:"}, ComboBox{AssignTo: &paranoia, Model: []string{"1 — standard", "2 — strict", "3 — paranoid"}, CurrentIndex: cfg.Paranoia() - 1},
		}},
	}, func(*walk.Dialog) bool { return true })
	if !ok {
		return
	}
	newMode, level := wafModes[max(mode.CurrentIndex(), 0)], max(paranoia.CurrentIndex(), 0)+1
	m.do("Setting the firewall of "+name, func(ctx context.Context) error {
		// Read what is saved now, so that exclusions added meanwhile in
		// the web console are kept.
		var cur struct {
			Config model.WAFConfig `json:"config"`
		}
		path := "/api/sites/" + url.PathEscape(id) + "/waf"
		if err := m.cl.Get(ctx, path, &cur); err != nil {
			return err
		}
		cur.Config.Mode, cur.Config.ParanoiaLevel = newMode, level
		return m.cl.Put(ctx, path, cur.Config, nil)
	})
}
