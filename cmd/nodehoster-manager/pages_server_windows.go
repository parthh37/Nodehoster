package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// loadInto fetches path into a fresh T off the UI thread and hands it to
// apply on the UI thread. Failures show in the status bar: the lists keep
// what they had.
func loadInto[T any](m *manager, path string, apply func(T)) {
	go func() {
		var v T
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := m.cl.Get(ctx, path, &v)
		cancel()
		m.mw.Synchronize(func() {
			if err != nil {
				m.setActivity("Could not load " + path + ": " + err.Error())
				return
			}
			apply(v)
		})
	}()
}

// ---- certificates

type certView struct {
	model.Certificate
	UsedBy []struct {
		SiteName string `json:"siteName"`
		Binding  string `json:"binding"`
	} `json:"usedBy"`
}

type certsPage struct {
	page
	list  table
	certs []certView
	renew *walk.LinkLabel
}

func (s *certsPage) init(m *manager) *page {
	s.title = func() string { return "Certificates" }
	s.load = func() { loadInto(m, "/api/certificates", func(v []certView) { s.certs = v; s.redraw() }) }
	s.list.onSelect = func() { setEnabled(s.list.selected() != "", s.renew) }
	s.list.color = func(row, col int) (walk.Color, bool) {
		if row >= len(s.certs) || (col != 2 && col != 5) {
			return 0, false
		}
		c := s.certs[row]
		switch {
		case c.Status == "error" || c.Status == "expired":
			return colorError, true
		case c.NotAfter != nil && time.Until(*c.NotAfter) < 14*24*time.Hour:
			return colorWarning, true
		}
		return 0, false
	}
	return &s.page
}

func (s *certsPage) content(m *manager) []Widget {
	return []Widget{
		s.list.view(nil, col("Name", 180), col("Domains", 220), col("Status", 70), col("Issuer", 160),
			col("Expires", 100), col("Days left", 70), col("Renews", 70), col("Used by", 200)),
		Label{Text: "Request, import and export certificates in the web console. Renewal of automatic certificates runs by itself.", TextColor: colorMuted},
	}
}

func (s *certsPage) actionsPane(m *manager) []Widget {
	return []Widget{
		heading("Certificates"),
		link(&s.renew, "Renew now", func() { s.renewSelected(m) }),
		link(nil, "Refresh", func() { m.refresh(true) }),
	}
}

func (s *certsPage) redraw() {
	keys := make([]string, len(s.certs))
	rows := make([][]string, len(s.certs))
	for i, c := range s.certs {
		expires, days := "–", "–"
		if c.NotAfter != nil {
			expires = c.NotAfter.Local().Format("2006-01-02")
			days = fmt.Sprint(int(time.Until(*c.NotAfter).Hours() / 24))
		}
		var used []string
		for _, u := range c.UsedBy {
			used = append(used, u.SiteName)
		}
		status := c.Status
		if c.LastError != "" {
			status += ": " + c.LastError
		}
		keys[i] = c.ID
		rows[i] = []string{c.Name, strings.Join(c.Domains, ", "), status, c.Issuer, expires, days, yesNo(c.AutoRenew), strings.Join(used, ", ")}
	}
	s.list.set(keys, rows)
	setEnabled(s.list.selected() != "", s.renew)
}

func (s *certsPage) renewSelected(m *manager) {
	id := s.list.selected()
	if id == "" {
		return
	}
	m.do("Renewing the certificate", func(ctx context.Context) error {
		return m.cl.Post(ctx, "/api/certificates/"+url.PathEscape(id)+"/renew", nil, nil)
	})
}

// ---- Node.js versions

type nodeVersions struct {
	System *struct {
		Version string `json:"version"`
		Path    string `json:"path"`
	} `json:"system"`
	Installed []struct {
		Version   string  `json:"version"`
		Path      string  `json:"path"`
		Status    string  `json:"status"`
		Progress  float64 `json:"progress"`
		Error     string  `json:"error"`
		IsDefault bool    `json:"isDefault"`
	} `json:"installed"`
}

type nodePage struct {
	page
	list   table
	remove *walk.LinkLabel
}

func (s *nodePage) init(m *manager) *page {
	s.title = func() string { return "Node.js versions" }
	s.load = func() { loadInto(m, "/api/node/versions", s.redraw) }
	s.list.onSelect = func() { setEnabled(strings.HasPrefix(s.list.selected(), "v:"), s.remove) }
	return &s.page
}

func (s *nodePage) content(m *manager) []Widget {
	return []Widget{
		s.list.view(nil, col("Version", 100), col("Default", 60), col("Status", 120), col("Path", 400)),
		Label{Text: "Choose the server's default version in the web console (Settings); each site can pin its own.", TextColor: colorMuted},
	}
}

func (s *nodePage) actionsPane(m *manager) []Widget {
	return []Widget{
		heading("Node.js"),
		link(nil, "Install version…", func() { s.install(m) }),
		link(&s.remove, "Remove", func() { s.removeSelected(m) }),
		link(nil, "Refresh", func() { m.refresh(true) }),
	}
}

func (s *nodePage) redraw(v nodeVersions) {
	var keys []string
	var rows [][]string
	for _, in := range v.Installed {
		status := in.Status
		if in.Status == "installing" {
			status = fmt.Sprintf("installing (%.0f%%)", in.Progress*100)
		}
		if in.Error != "" {
			status += ": " + in.Error
		}
		keys = append(keys, "v:"+in.Version)
		rows = append(rows, []string{in.Version, yesNo(in.IsDefault), status, in.Path})
	}
	if v.System != nil {
		keys = append(keys, "system")
		rows = append(rows, []string{v.System.Version, "", "on PATH", v.System.Path})
	}
	s.list.set(keys, rows)
}

func (s *nodePage) install(m *manager) {
	v, ok := inputDialog(m.mw, "Install Node.js", "Version to download from nodejs.org, for example 22.12.0:", "", false)
	if !ok || strings.TrimSpace(v) == "" {
		return
	}
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	m.do("Installing Node.js "+v, func(ctx context.Context) error {
		return m.cl.Post(ctx, "/api/node/versions", map[string]string{"version": v}, nil)
	})
}

func (s *nodePage) removeSelected(m *manager) {
	v := strings.TrimPrefix(s.list.selected(), "v:")
	if v == "" || v == "system" || !m.confirm("Remove Node.js", "Remove Node.js "+v+"?") {
		return
	}
	m.do("Removing Node.js "+v, func(ctx context.Context) error { return m.cl.Delete(ctx, "/api/node/versions/"+url.PathEscape(v)) })
}

// ---- web console users

type usersPage struct {
	page
	list                                        table
	users                                       []model.User
	resetPassword, resetTOTP, toggle, role, del *walk.LinkLabel
}

func (s *usersPage) init(m *manager) *page {
	s.title = func() string { return "Web console users" }
	s.load = func() { loadInto(m, "/api/users", func(v []model.User) { s.users = v; s.redraw() }) }
	s.list.onSelect = s.enable
	s.list.color = func(row, col int) (walk.Color, bool) {
		if row < len(s.users) && s.users[row].Disabled {
			return colorMuted, true
		}
		return 0, false
	}
	return &s.page
}

func (s *usersPage) content(m *manager) []Widget {
	return []Widget{
		s.list.view(nil, col("User name", 180), col("Role", 90), col("Two-factor", 90), col("Status", 90), col("Last sign-in", 160)),
		Label{Text: "These accounts sign in to the web console. The desktop manager needs none: Windows administrators use it directly.", TextColor: colorMuted},
	}
}

func (s *usersPage) actionsPane(m *manager) []Widget {
	return []Widget{
		heading("Users"),
		link(nil, "Add user…", func() { s.add(m) }),
		heading("Selected user"),
		link(&s.resetPassword, "Reset password…", func() { s.setPassword(m) }),
		link(&s.resetTOTP, "Reset two-factor…", func() { s.resetTwoFactor(m) }),
		link(&s.toggle, "Disable", func() { s.toggleDisabled(m) }),
		link(&s.role, "Change role…", func() { s.changeRole(m) }),
		link(&s.del, "Delete…", func() { s.remove(m) }),
	}
}

func (s *usersPage) redraw() {
	keys := make([]string, len(s.users))
	rows := make([][]string, len(s.users))
	for i, u := range s.users {
		last := "never"
		if u.LastLogin != nil {
			last = u.LastLogin.Local().Format("2006-01-02 15:04")
		}
		status := "Active"
		if u.Disabled {
			status = "Disabled"
		}
		keys[i] = u.ID
		rows[i] = []string{u.Username, string(u.Role), map[bool]string{true: "On", false: "Off"}[u.TOTPEnabled], status, last}
	}
	s.list.set(keys, rows)
	s.enable()
}

func (s *usersPage) current() *model.User {
	id := s.list.selected()
	for i := range s.users {
		if s.users[i].ID == id {
			return &s.users[i]
		}
	}
	return nil
}

func (s *usersPage) enable() {
	u := s.current()
	setEnabled(u != nil, s.resetPassword, s.toggle, s.role, s.del)
	setEnabled(u != nil && u.TOTPEnabled, s.resetTOTP)
	if u != nil && u.Disabled {
		s.toggle.SetText("<a>Enable</a>")
	} else {
		s.toggle.SetText("<a>Disable</a>")
	}
}

func (s *usersPage) put(m *manager, u *model.User, what string, body map[string]any, then func()) {
	id := u.ID
	m.do(what, func(ctx context.Context) error {
		err := m.cl.Put(ctx, "/api/users/"+url.PathEscape(id), body, nil)
		if err == nil && then != nil {
			m.mw.Synchronize(then)
		}
		return err
	})
}

func (s *usersPage) add(m *manager) {
	name, ok := inputDialog(m.mw, "Add user", "User name:", "", false)
	if !ok || strings.TrimSpace(name) == "" {
		return
	}
	role, ok := choiceDialog(m.mw, "Add user", "Role of "+name+":", []string{"admin", "operator", "viewer"}, 2)
	if !ok {
		return
	}
	pw := randomPassword()
	m.do("Adding "+name, func(ctx context.Context) error {
		err := m.cl.Post(ctx, "/api/users", map[string]any{"username": strings.TrimSpace(name), "password": pw, "role": role}, nil)
		if err == nil {
			m.mw.Synchronize(func() {
				showSecretDialog(m.mw, "User added", name+" signs in with this password once, then chooses their own:", pw)
			})
		}
		return err
	})
}

func (s *usersPage) setPassword(m *manager) {
	u := s.current()
	if u == nil || !m.confirm("Reset password", "Give "+u.Username+" a new random password? They must change it when they next sign in, and their sessions end now.") {
		return
	}
	pw, name := randomPassword(), u.Username
	s.put(m, u, "Resetting the password", map[string]any{"password": pw}, func() {
		showSecretDialog(m.mw, "Password reset", name+" signs in with this password once, then chooses their own:", pw)
	})
}

func (s *usersPage) resetTwoFactor(m *manager) {
	u := s.current()
	if u == nil || !m.confirm("Reset two-factor", "Turn off two-factor authentication for "+u.Username+"? Use this when they lost their authenticator; they can enroll again after signing in.") {
		return
	}
	s.put(m, u, "Resetting two-factor authentication", map[string]any{"resetTotp": true}, nil)
}

func (s *usersPage) toggleDisabled(m *manager) {
	if u := s.current(); u != nil {
		s.put(m, u, "Updating "+u.Username, map[string]any{"disabled": !u.Disabled}, nil)
	}
}

func (s *usersPage) changeRole(m *manager) {
	u := s.current()
	if u == nil {
		return
	}
	roles := []string{"admin", "operator", "viewer"}
	cur := 0
	for i, r := range roles {
		if string(u.Role) == r {
			cur = i
		}
	}
	if role, ok := choiceDialog(m.mw, "Change role", "Role of "+u.Username+":", roles, cur); ok {
		s.put(m, u, "Changing the role", map[string]any{"role": role}, nil)
	}
}

func (s *usersPage) remove(m *manager) {
	u := s.current()
	if u == nil || !m.confirm("Delete user", "Delete the web console user "+u.Username+"?") {
		return
	}
	id := u.ID
	m.do("Deleting "+u.Username, func(ctx context.Context) error { return m.cl.Delete(ctx, "/api/users/"+url.PathEscape(id)) })
}

// randomPassword makes a 16-character password from an alphabet without
// look-alike characters, since it is read off the screen.
func randomPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 16)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}

// ---- events and audit log

type activityPage struct {
	page
	tabs          *walk.TabWidget
	events, audit table
	levels        []string
}

func (s *activityPage) init(m *manager) *page {
	s.title = func() string { return "Events and audit log" }
	s.load = func() {
		loadInto(m, "/api/events?limit=500", func(list []model.Event) {
			names := map[string]string{}
			for _, st := range m.sites {
				names[st.ID] = st.Name
			}
			keys := make([]string, len(list))
			rows := make([][]string, len(list))
			s.levels = make([]string, len(list))
			for i, e := range list {
				keys[i] = fmt.Sprint(e.ID)
				s.levels[i] = e.Level
				rows[i] = []string{e.Time.Local().Format("2006-01-02 15:04:05"), e.Level, e.Type, names[e.SiteID], e.Message}
			}
			s.events.set(keys, rows)
		})
		loadInto(m, "/api/audit?limit=500", func(list []model.AuditEntry) {
			keys := make([]string, len(list))
			rows := make([][]string, len(list))
			for i, a := range list {
				keys[i] = fmt.Sprint(a.ID)
				rows[i] = []string{a.Time.Local().Format("2006-01-02 15:04:05"), a.User, a.IP, a.Action, a.Target, a.Detail}
			}
			s.audit.set(keys, rows)
		})
	}
	s.events.color = func(row, col int) (walk.Color, bool) {
		if row >= len(s.levels) || col != 1 {
			return 0, false
		}
		switch s.levels[row] {
		case "error":
			return colorError, true
		case "warning":
			return colorWarning, true
		}
		return 0, false
	}
	return &s.page
}

func (s *activityPage) content(m *manager) []Widget {
	return []Widget{
		TabWidget{AssignTo: &s.tabs, Pages: []TabPage{
			{Title: "Events", Layout: VBox{}, Children: []Widget{
				s.events.view(nil, col("Time", 140), col("Level", 70), col("Type", 120), col("Site", 140), col("Message", 400)),
			}},
			{Title: "Audit log", Layout: VBox{}, Children: []Widget{
				s.audit.view(nil, col("Time", 140), col("User", 180), col("From", 110), col("Action", 140), col("Target", 160), col("Detail", 200)),
			}},
		}},
	}
}

func (s *activityPage) actionsPane(m *manager) []Widget {
	return []Widget{
		heading("Activity"),
		link(nil, "Refresh", func() { m.refresh(true) }),
	}
}
