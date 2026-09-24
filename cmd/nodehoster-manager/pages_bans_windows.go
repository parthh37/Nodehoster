package main

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- Banned IP addresses: automatic IP banning's list, like IIS's
// Dynamic IP Restrictions. The pipe is never subject to bans, so a local
// administrator who banned themselves (or was banned for mistyping the web
// console password) lifts the ban here.

type bansPage struct {
	page
	list    table
	bans    []model.Ban
	enabled bool
	bar     infoBar
	find    *walk.LineEdit
	count   *walk.Label

	add, unban, copyAddr, refresh, rules *command
}

func (s *bansPage) init(m *manager) *page {
	s.icon = desktop.IconBans
	s.title = func() string { return "Banned IP addresses" }
	s.subtitle = func() string {
		state := "automatic banning is off"
		if s.enabled {
			state = "automatic banning is on"
		}
		return plural(len(s.bans), "address") + " banned · " + state
	}
	s.search = func() *walk.LineEdit { return s.find }
	s.load = func() { s.reload(m) }
	s.add = newCommand("Ban an address…", desktop.IconBan, func() { s.banAddress(m) })
	s.unban = newCommand("Unban…", desktop.IconUnlock, func() { s.remove(m) })
	s.copyAddr = newCommand("Copy address", desktop.IconCopy, func() {
		if a := s.list.selected(); a != "" {
			walk.Clipboard().SetText(a)
		}
	})
	s.refresh = newCommand("Refresh", desktop.IconRefresh, func() { m.refresh(true) })
	s.rules = newCommand("Rules and trap paths…", desktop.IconConsole, func() { m.openConsolePath("/settings/security") })
	s.list.onSelect = func() { s.enable(m) }
	s.list.color = func(row, col int) (walk.Color, bool) {
		if row < len(s.bans) && col == 3 && s.bans[row].ExpiresAt == nil {
			return colorError, true
		}
		return 0, false
	}
	s.list.icon = func(row, col int) walk.Image {
		if row >= len(s.bans) || col != 0 {
			return nil
		}
		if s.bans[row].Manual {
			return img(desktop.IconUser)
		}
		return img(desktop.IconBan)
	}
	return &s.page
}

func (s *bansPage) content(m *manager) []Widget {
	return []Widget{
		s.bar.widget(),
		searchRow(&s.find, &s.count, "Search addresses and reasons", &s.list),
		s.list.viewWith(tableOpts{name: "bans", sortable: true, onDelete: s.unban.trigger,
			menu: menu(s.unban, s.copyAddr, nil, s.add)},
			col("Address", 220), col("Reason", 330), col("Banned", 140), col("Until", 140), colR("Strikes", 60)),
		hint("Banned addresses are refused by every site (except those exempt) and by the web console. Loopback and the trusted proxies are never banned."),
	}
}

func (s *bansPage) actionsPane(m *manager) []Widget {
	return pane(
		"Banned IP addresses", s.add, s.refresh, s.rules,
		"Selected address", s.unban, s.copyAddr,
	)
}

// reload reads the bans and whether automatic banning is on.
func (s *bansPage) reload(m *manager) {
	s.enable(m)
	if !m.connected() {
		return
	}
	go func() {
		var list []model.Ban
		var st model.Settings
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := m.cl.Get(ctx, "/api/bans", &list)
		if err == nil {
			err = m.cl.Get(ctx, "/api/settings", &st)
		}
		cancel()
		m.mw.Synchronize(func() {
			if err != nil {
				s.bar.show(barError, "Could not load the bans: "+err.Error(), "Retry", func() { s.reload(m) })
				return
			}
			s.bans, s.enabled = list, st.IPBan.Enabled
			s.redraw(m)
		})
	}()
}

func (s *bansPage) redraw(m *manager) {
	if s.enabled {
		s.bar.show(barOK, "Automatic banning is on: failed sign-ins, scans and trap paths ban the client for a while, longer for repeat offenders.",
			"Rules and trap paths…", s.rules.trigger)
	} else {
		s.bar.show(barInfo, "Automatic banning is off. Bans added by hand still apply.", "Turn it on in the web console…", s.rules.trigger)
	}
	keys := make([]string, len(s.bans))
	rows := make([][]string, len(s.bans))
	for i, b := range s.bans {
		until := "until removed"
		if b.ExpiresAt != nil {
			until = b.ExpiresAt.Local().Format("2006-01-02 15:04")
		}
		reason := b.Reason
		if b.Manual && b.CreatedBy != "" {
			reason += " (by " + b.CreatedBy + ")"
		}
		strikes := ""
		if !b.Manual {
			strikes = strconv.Itoa(b.Strikes)
		}
		keys[i] = b.Address
		rows[i] = []string{b.Address, reason, b.CreatedAt.Local().Format("2006-01-02 15:04"), until, strikes}
	}
	s.list.set(keys, rows)
	updateCount(s.count, &s.list)
	if m.cur == &s.page {
		m.updateHeader()
	}
	s.enable(m)
}

func (s *bansPage) enable(m *manager) {
	sel := s.list.selected() != ""
	setEnabled(sel && m.connected(), s.unban)
	setEnabled(sel, s.copyAddr)
	setEnabled(m.connected(), s.add)
}

var (
	banDurations     = []int{15, 60, 24 * 60, 7 * 24 * 60, 30 * 24 * 60, 0}
	banDurationNames = []string{"15 minutes", "1 hour", "1 day", "1 week", "30 days", "Until removed"}
)

// banAddress bans an address or a range by hand, for a while or for good.
func (s *bansPage) banAddress(m *manager) {
	var addr, reason *walk.LineEdit
	var dur *walk.ComboBox
	var req model.BanRequest
	ok := runDialog(m.mw, "Ban an address", Size{Width: 460}, []Widget{
		intro(desktop.IconBan, "Every site and the web console refuse the address for as long as the ban lasts."),
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "IP address or range:"}, LineEdit{AssignTo: &addr, CueBanner: "203.0.113.7, 198.51.100.0/24 or 2001:db8::/64"},
			Label{Text: "Ban for:"}, ComboBox{AssignTo: &dur, Model: banDurationNames, CurrentIndex: 1},
			Label{Text: "Reason:"}, LineEdit{AssignTo: &reason, Text: "banned from NodeHoster Manager"},
		}},
	}, func(dlg *walk.Dialog) bool {
		a := strings.TrimSpace(addr.Text())
		if net.ParseIP(a) == nil {
			if _, _, err := net.ParseCIDR(a); err != nil {
				return invalid(dlg, "Enter an IP address, such as 203.0.113.7, or a range, such as 198.51.100.0/24.")
			}
		}
		req = model.BanRequest{Address: a, Minutes: banDurations[max(dur.CurrentIndex(), 0)], Reason: strings.TrimSpace(reason.Text())}
		if req.Reason == "" {
			req.Reason = "banned from NodeHoster Manager"
		}
		return true
	})
	if !ok {
		return
	}
	m.do("Banning "+req.Address, func(ctx context.Context) error {
		return m.cl.Post(ctx, "/api/bans", req, nil)
	})
}

func (s *bansPage) remove(m *manager) {
	addr := s.list.selected()
	if addr == "" {
		return
	}
	if ask(m.mw, "Unban", "Lift the ban of "+addr+"?", "Its requests are answered again right away, and its ban history is forgotten.",
		walk.TaskDialogSystemIconInformation, [2]string{"Unban", ""}) != 0 {
		return
	}
	m.do("Unbanning "+addr, func(ctx context.Context) error {
		return m.cl.Delete(ctx, "/api/bans/"+url.PathEscape(addr))
	})
}
