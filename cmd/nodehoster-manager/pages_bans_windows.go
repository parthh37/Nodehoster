package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

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
	status  *walk.Label
	unban   *walk.LinkLabel
}

func (s *bansPage) init(m *manager) *page {
	s.title = func() string { return "Banned IP addresses" }
	s.load = func() { s.reload(m) }
	s.list.onSelect = s.enable
	s.list.color = func(row, col int) (walk.Color, bool) {
		if row < len(s.bans) && col == 3 && s.bans[row].ExpiresAt == nil {
			return colorError, true
		}
		return 0, false
	}
	return &s.page
}

func (s *bansPage) content(m *manager) []Widget {
	list := s.list.view(nil, col("Address", 220), col("Reason", 320), col("Banned", 130), col("Until", 130), col("Ban", 50))
	list.StretchFactor = 3
	return []Widget{
		Label{AssignTo: &s.status, Font: Font{Bold: true}},
		list,
		Label{Text: "Banned addresses are refused by every site (except those exempt) and by the web console. Loopback and the trusted proxies are never banned. Rules and trap paths are set in the web console under Settings › Security.", TextColor: colorMuted},
	}
}

func (s *bansPage) actionsPane(m *manager) []Widget {
	return []Widget{
		heading("Banned IP addresses"),
		link(nil, "Ban an address…", func() { s.add(m) }),
		link(nil, "Refresh", func() { m.refresh(true) }),
		heading("Selected address"),
		link(&s.unban, "Unban…", func() { s.remove(m) }),
	}
}

// reload reads the bans and whether automatic banning is on.
func (s *bansPage) reload(m *manager) {
	s.enable()
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
				s.status.SetText("Could not load the bans: " + err.Error())
				return
			}
			s.bans, s.enabled = list, st.IPBan.Enabled
			s.redraw()
		})
	}()
}

func (s *bansPage) redraw() {
	state := "Automatic banning is off (bans added by hand still apply)"
	if s.enabled {
		state = "Automatic banning is on"
	}
	s.status.SetText(fmt.Sprintf("%s — %d address(es) banned", state, len(s.bans)))
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
	s.enable()
}

func (s *bansPage) enable() {
	setEnabled(s.list.selected() != "", s.unban)
}

func (s *bansPage) add(m *manager) {
	addr, ok := inputDialog(m.mw, "Ban an address", "IP address or range (e.g. 203.0.113.7, 198.51.100.0/24, 2001:db8::/64):", "", false)
	if addr = strings.TrimSpace(addr); !ok || addr == "" {
		return
	}
	mins, ok := inputDialog(m.mw, "Ban an address", "Ban for how many minutes? (0 = until removed)", "60", false)
	if !ok {
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(mins))
	if err != nil || n < 0 {
		walk.MsgBox(m.mw, "Ban an address", "Enter a number of minutes, or 0.", walk.MsgBoxIconError)
		return
	}
	m.do("Banning "+addr, func(ctx context.Context) error {
		return m.cl.Post(ctx, "/api/bans", model.BanRequest{Address: addr, Minutes: n, Reason: "banned from NodeHoster Manager"}, nil)
	})
}

func (s *bansPage) remove(m *manager) {
	addr := s.list.selected()
	if addr == "" || !m.confirm("Unban", "Lift the ban of "+addr+"? Its requests are answered again right away, and its ban history is forgotten.") {
		return
	}
	m.do("Unbanning "+addr, func(ctx context.Context) error {
		return m.cl.Delete(ctx, "/api/bans/"+url.PathEscape(addr))
	})
}
