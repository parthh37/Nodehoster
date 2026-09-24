package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
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
				m.flashStatus("Could not load "+path+": "+err.Error(), true)
				return
			}
			apply(v)
		})
	}()
}

// searchRow is a search box with a count of what it shows, above a list.
func searchRow(find **walk.LineEdit, count **walk.Label, cue string, t *table, extra ...Widget) Composite {
	children := []Widget{searchBox(find, cue, func(text string) { t.setSearch(text); updateCount(*count, t) })}
	children = append(children, extra...)
	children = append(children, HSpacer{}, Label{AssignTo: count, TextColor: colorMuted})
	return Composite{Layout: HBox{MarginsZero: true, Spacing: 8, Alignment: AlignHNearVCenter}, Children: children}
}

// updateCount refreshes a searchRow's count.
func updateCount(count *walk.Label, t *table) {
	if count == nil {
		return
	}
	shown, total := t.count()
	if shown == total {
		count.SetText(fmt.Sprintf("%d", total))
	} else {
		count.SetText(fmt.Sprintf("%d of %d", shown, total))
	}
}

// ---- certificates

type certView struct {
	model.Certificate
	UsedBy []struct {
		SiteName string `json:"siteName"`
		Binding  string `json:"binding"`
	} `json:"usedBy"`
}

// certLevel is how urgent a certificate is: failed or expired, expiring
// within two weeks, or fine.
func certLevel(c certView) desktop.Level {
	switch {
	case c.Status == "error" || c.Status == "expired" || (c.NotAfter != nil && time.Now().After(*c.NotAfter)):
		return desktop.LevelDown
	case c.NotAfter != nil && time.Until(*c.NotAfter) < 14*24*time.Hour:
		return desktop.LevelWarning
	}
	return desktop.LevelOK
}

type certsPage struct {
	page
	list  table
	certs []certView
	bar   infoBar
	find  *walk.LineEdit
	count *walk.Label

	renew, copyDomains, refresh, console *command
}

func (s *certsPage) init(m *manager) *page {
	s.icon = desktop.IconCertificate
	s.title = func() string { return "Certificates" }
	s.subtitle = func() string {
		return plural(len(s.certs), "certificate") + " · automatic certificates renew by themselves at ⅔ of their lifetime"
	}
	s.search = func() *walk.LineEdit { return s.find }
	s.load = func() { loadInto(m, "/api/certificates", func(v []certView) { s.certs = v; s.redraw(m) }) }
	s.renew = newCommand("Renew now", desktop.IconRefresh, func() { s.renewSelected(m) })
	s.copyDomains = newCommand("Copy domains", desktop.IconCopy, func() {
		if c := s.current(); c != nil {
			walk.Clipboard().SetText(strings.Join(c.Domains, ", "))
		}
	})
	s.refresh = newCommand("Refresh", desktop.IconRefresh, func() { m.refresh(true) })
	s.console = newCommand("Request or import…", desktop.IconConsole, func() { m.openConsolePath("/certificates") })
	s.list.onSelect = func() { setEnabled(s.current() != nil && m.connected(), s.renew, s.copyDomains) }
	s.list.color = func(row, col int) (walk.Color, bool) {
		if row >= len(s.certs) || (col != 2 && col != 5) {
			return 0, false
		}
		if l := certLevel(s.certs[row]); l != desktop.LevelOK {
			return levelColor(l), true
		}
		return 0, false
	}
	s.list.icon = func(row, col int) walk.Image {
		if col != 0 || row >= len(s.certs) {
			return nil
		}
		switch certLevel(s.certs[row]) {
		case desktop.LevelDown:
			return img(desktop.IconError)
		case desktop.LevelWarning:
			return img(desktop.IconWarning)
		}
		return img(desktop.IconCertificate)
	}
	return &s.page
}

func (s *certsPage) current() *certView {
	if i := s.list.current(); i >= 0 && i < len(s.certs) {
		return &s.certs[i]
	}
	return nil
}

func (s *certsPage) content(m *manager) []Widget {
	return []Widget{
		s.bar.widget(),
		searchRow(&s.find, &s.count, "Search names, domains, sites", &s.list),
		s.list.viewWith(tableOpts{name: "certificates", sortable: true, menu: menu(s.renew, s.copyDomains, nil, s.refresh)},
			col("Name", 180), col("Domains", 240), col("Status", 110), col("Issuer", 170),
			col("Expires", 100), col("Expiry", 150), col("Renews", 70), col("Used by", 200)),
		hint("Request certificates from Let's Encrypt or another ACME CA, and import or export PFX/PEM files, in the web console. Automatic certificates renew by themselves."),
	}
}

func (s *certsPage) actionsPane(m *manager) []Widget {
	return pane(
		"Certificates", s.console, s.refresh,
		"Selected certificate", s.renew, s.copyDomains,
	)
}

func (s *certsPage) redraw(m *manager) {
	keys := make([]string, len(s.certs))
	rows := make([][]string, len(s.certs))
	bad, soon := 0, 0
	now := time.Now()
	for i, c := range s.certs {
		expires, expiry := "–", "–"
		if c.NotAfter != nil {
			expires = c.NotAfter.Local().Format("2006-01-02")
			expiry = desktop.ExpiryText(*c.NotAfter, now)
		}
		var used []string
		for _, u := range c.UsedBy {
			used = append(used, u.SiteName)
		}
		status := c.Status
		if c.LastError != "" {
			status += ": " + c.LastError
		}
		switch certLevel(c) {
		case desktop.LevelDown:
			bad++
		case desktop.LevelWarning:
			soon++
		}
		keys[i] = c.ID
		rows[i] = []string{c.Name, strings.Join(c.Domains, ", "), status, c.Issuer, expires, expiry, yesNo(c.AutoRenew), strings.Join(used, ", ")}
	}
	s.list.set(keys, rows)
	updateCount(s.count, &s.list)
	switch {
	case bad > 0:
		s.bar.show(barError, plural(bad, "certificate")+" expired or failed to renew. Select one to see why, then renew it.", "", nil)
	case soon > 0:
		s.bar.show(barWarning, plural(soon, "certificate")+" expire within two weeks.", "", nil)
	default:
		s.bar.hide()
	}
	setEnabled(s.current() != nil && m.connected(), s.renew, s.copyDomains)
}

func (s *certsPage) renewSelected(m *manager) {
	c := s.current()
	if c == nil {
		return
	}
	id, name := c.ID, c.Name
	m.do("Renewing "+name, func(ctx context.Context) error {
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
	list       table
	paths      []string
	statuses   []string
	defaults   []bool
	installing bool
	loading    bool

	install, remove, folder, refresh *command
}

func (s *nodePage) init(m *manager) *page {
	s.icon = desktop.IconNodeVersion
	s.title = func() string { return "Node.js versions" }
	s.subtitle = func() string {
		return "Versions downloaded from nodejs.org and verified by SHA-256; each site can pin its own"
	}
	s.load = func() { s.reload(m) }
	// While a version downloads, follow its progress.
	s.update = func() {
		if s.installing {
			s.reload(m)
		}
	}
	s.install = newCommand("Install a version…", desktop.IconDownload, func() { s.installVersion(m) })
	s.remove = newCommand("Remove…", desktop.IconRemove, func() { s.removeSelected(m) })
	s.folder = newCommand("Open folder", desktop.IconFolder, func() {
		if i := s.list.current(); i >= 0 && i < len(s.paths) && s.paths[i] != "" {
			shellOpen(s.paths[i])
		}
	})
	s.refresh = newCommand("Refresh", desktop.IconRefresh, func() { m.refresh(true) })
	s.list.onSelect = func() { s.enable(m) }
	s.list.icon = func(row, col int) walk.Image {
		switch {
		case row >= len(s.defaults):
			return nil
		case col == 0:
			return img(desktop.IconNode)
		case col == 1 && s.defaults[row]:
			return img(desktop.IconStar)
		case col == 2:
			switch {
			case strings.HasPrefix(s.statuses[row], "installing"):
				return img(desktop.IconClock)
			case strings.Contains(s.statuses[row], ": "):
				return img(desktop.IconError)
			}
		}
		return nil
	}
	return &s.page
}

func (s *nodePage) reload(m *manager) {
	if s.loading || !m.connected() {
		return
	}
	s.loading = true
	loadInto(m, "/api/node/versions", func(v nodeVersions) {
		s.loading = false
		s.redraw(m, v)
	})
	// loadInto reports failures itself; let the next tick try again.
	time.AfterFunc(30*time.Second, func() { m.mw.Synchronize(func() { s.loading = false }) })
}

func (s *nodePage) content(m *manager) []Widget {
	return []Widget{
		s.list.viewWith(tableOpts{name: "nodeVersions", sortable: true, onDelete: s.remove.trigger,
			menu: menu(s.folder, nil, s.remove, nil, s.install, s.refresh)},
			col("Version", 110), col("Default", 70), col("Status", 160), col("Path", 420)),
		hint("The server's default version is chosen in the web console (Settings). A version a site pins cannot be removed while the site uses it."),
	}
}

func (s *nodePage) actionsPane(m *manager) []Widget {
	return pane(
		"Node.js", s.install, s.refresh,
		"Selected version", s.folder, s.remove,
	)
}

func (s *nodePage) redraw(m *manager, v nodeVersions) {
	var keys []string
	var rows [][]string
	s.paths, s.statuses, s.defaults, s.installing = nil, nil, nil, false
	for _, in := range v.Installed {
		status := in.Status
		if in.Status == "installing" {
			status = fmt.Sprintf("installing (%.0f%%)", in.Progress*100)
			s.installing = true
		}
		if in.Error != "" {
			status += ": " + in.Error
		}
		keys = append(keys, "v:"+in.Version)
		rows = append(rows, []string{in.Version, yesNo(in.IsDefault), status, in.Path})
		s.paths, s.statuses, s.defaults = append(s.paths, in.Path), append(s.statuses, status), append(s.defaults, in.IsDefault)
	}
	if v.System != nil {
		keys = append(keys, "system")
		rows = append(rows, []string{v.System.Version, "", "on PATH (installed outside NodeHoster)", v.System.Path})
		s.paths, s.statuses, s.defaults = append(s.paths, v.System.Path), append(s.statuses, ""), append(s.defaults, false)
	}
	s.list.set(keys, rows)
	s.enable(m)
}

func (s *nodePage) enable(m *manager) {
	sel := s.list.selected()
	setEnabled(m.connected(), s.install)
	setEnabled(strings.HasPrefix(sel, "v:") && m.connected(), s.remove)
	setEnabled(sel != "", s.folder)
}

func (s *nodePage) installVersion(m *manager) {
	v, ok := installNodeDialog(m)
	if !ok {
		return
	}
	s.installing = true
	m.do("Installing Node.js "+v, func(ctx context.Context) error {
		return m.cl.Post(ctx, "/api/node/versions", map[string]string{"version": v}, nil)
	})
}

func (s *nodePage) removeSelected(m *manager) {
	v := strings.TrimPrefix(s.list.selected(), "v:")
	if v == "" || v == "system" {
		return
	}
	if ask(m.mw, "Remove Node.js", "Remove Node.js "+v+"?", "Its files are deleted. Sites that pin this version cannot start until they use another one.",
		walk.TaskDialogSystemIconWarning, [2]string{"Remove", ""}) != 0 {
		return
	}
	m.do("Removing Node.js "+v, func(ctx context.Context) error { return m.cl.Delete(ctx, "/api/node/versions/"+url.PathEscape(v)) })
}

// ---- web console users

type usersPage struct {
	page
	list  table
	users []model.User
	find  *walk.LineEdit
	count *walk.Label

	add, resetPassword, resetTOTP, toggle, role, del *command
}

func (s *usersPage) init(m *manager) *page {
	s.icon = desktop.IconUsers
	s.title = func() string { return "Web console users" }
	s.subtitle = func() string {
		return plural(len(s.users), "account") + " · Windows administrators use this manager without one"
	}
	s.search = func() *walk.LineEdit { return s.find }
	s.load = func() { loadInto(m, "/api/users", func(v []model.User) { s.users = v; s.redraw(m) }) }
	s.add = newCommand("Add user…", desktop.IconAdd, func() { s.addUser(m) })
	s.resetPassword = newCommand("Reset password…", desktop.IconKey, func() { s.setPassword(m) })
	s.resetTOTP = newCommand("Reset two-factor…", desktop.IconPhone, func() { s.resetTwoFactor(m) })
	s.toggle = newCommand("Disable", desktop.IconToggle, func() { s.toggleDisabled(m) })
	s.role = newCommand("Role and site access…", desktop.IconIDCard, func() { s.changeAccess(m) })
	s.del = newCommand("Delete…", desktop.IconRemove, func() { s.remove(m) })
	s.list.onSelect = func() { s.enable(m) }
	s.list.color = func(row, col int) (walk.Color, bool) {
		if row < len(s.users) && s.users[row].Disabled {
			return colorMuted, true
		}
		return 0, false
	}
	s.list.icon = func(row, col int) walk.Image {
		if row >= len(s.users) {
			return nil
		}
		u := s.users[row]
		switch {
		case col == 0 && u.Disabled:
			return asImage(icoOff(desktop.IconUser))
		case col == 0:
			return img(desktop.IconUser)
		case col == 3 && u.TOTPEnabled:
			return img(desktop.IconShield)
		}
		return nil
	}
	return &s.page
}

func (s *usersPage) content(m *manager) []Widget {
	return []Widget{
		searchRow(&s.find, &s.count, "Search users, roles, sites", &s.list),
		s.list.viewWith(tableOpts{name: "users", sortable: true, onActivate: s.role.trigger, onDelete: s.del.trigger,
			menu: menu(s.role, s.resetPassword, s.resetTOTP, s.toggle, nil, s.del)},
			col("User name", 180), col("Role", 90), col("Site access", 170), col("Two-factor", 90), col("Status", 150), col("Last sign-in", 160)),
		hint("These accounts sign in to the web console. The desktop manager needs none: Windows administrators use it directly."),
	}
}

func (s *usersPage) actionsPane(m *manager) []Widget {
	return pane(
		"Users", s.add,
		"Selected user", s.role, s.resetPassword, s.resetTOTP, s.toggle, s.del,
	)
}

func (s *usersPage) redraw(m *manager) {
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
		if u.SSO {
			status += " (single sign-on)" // no password until one is set here
		}
		keys[i] = u.ID
		rows[i] = []string{u.Username, string(u.Role), siteAccessLabel(m, u), map[bool]string{true: "On", false: "Off"}[u.TOTPEnabled], status, last}
	}
	s.list.set(keys, rows)
	updateCount(s.count, &s.list)
	s.enable(m)
}

func (s *usersPage) current() *model.User {
	if i := s.list.current(); i >= 0 && i < len(s.users) {
		return &s.users[i]
	}
	return nil
}

func (s *usersPage) enable(m *manager) {
	u := s.current()
	setEnabled(m.connected(), s.add)
	setEnabled(u != nil && m.connected(), s.resetPassword, s.toggle, s.role, s.del)
	setEnabled(u != nil && u.TOTPEnabled && m.connected(), s.resetTOTP)
	if u != nil && u.Disabled {
		s.toggle.setText("Enable")
	} else {
		s.toggle.setText("Disable")
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

func (s *usersPage) addUser(m *manager) {
	name, ok := inputDialog(m.mw, "Add user", desktop.IconUser, "User name of the new web console account:", "", false)
	if !ok || strings.TrimSpace(name) == "" {
		return
	}
	role, grants, ok := accessDialog(m, "Add user — access of "+name, model.RoleViewer, nil)
	if !ok {
		return
	}
	pw := randomPassword()
	m.do("Adding "+name, func(ctx context.Context) error {
		err := m.cl.Post(ctx, "/api/users", map[string]any{"username": strings.TrimSpace(name), "password": pw, "role": role, "sites": grants}, nil)
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
	if u == nil {
		return
	}
	if ask(m.mw, "Reset password", "Give "+u.Username+" a new random password?",
		"They must change it when they next sign in, and their sessions end now.",
		walk.TaskDialogSystemIconWarning, [2]string{"Reset the password", ""}) != 0 {
		return
	}
	pw, name := randomPassword(), u.Username
	s.put(m, u, "Resetting the password of "+name, map[string]any{"password": pw}, func() {
		showSecretDialog(m.mw, "Password reset", name+" signs in with this password once, then chooses their own:", pw)
	})
}

func (s *usersPage) resetTwoFactor(m *manager) {
	u := s.current()
	if u == nil {
		return
	}
	if ask(m.mw, "Reset two-factor", "Turn off two-factor authentication for "+u.Username+"?",
		"Use this when they lost their authenticator; they can enroll again after signing in.",
		walk.TaskDialogSystemIconWarning, [2]string{"Turn it off", ""}) != 0 {
		return
	}
	s.put(m, u, "Resetting two-factor authentication", map[string]any{"resetTotp": true}, nil)
}

func (s *usersPage) toggleDisabled(m *manager) {
	if u := s.current(); u != nil {
		verb := "Disabling "
		if u.Disabled {
			verb = "Enabling "
		}
		s.put(m, u, verb+u.Username, map[string]any{"disabled": !u.Disabled}, nil)
	}
}

func (s *usersPage) changeAccess(m *manager) {
	u := s.current()
	if u == nil {
		return
	}
	if role, grants, ok := accessDialog(m, "Role and site access — "+u.Username, u.Role, u.Sites); ok {
		s.put(m, u, "Changing the access of "+u.Username, map[string]any{"role": role, "sites": grants}, nil)
	}
}

func (s *usersPage) remove(m *manager) {
	u := s.current()
	if u == nil {
		return
	}
	if ask(m.mw, "Delete user", "Delete the web console user "+u.Username+"?", "They can no longer sign in; their sessions and API tokens end now.",
		walk.TaskDialogSystemIconWarning, [2]string{"Delete", ""}) != 0 {
		return
	}
	id, name := u.ID, u.Username
	m.do("Deleting "+name, func(ctx context.Context) error { return m.cl.Delete(ctx, "/api/users/"+url.PathEscape(id)) })
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

var levelFilters = []string{"All levels", "Errors", "Warnings and errors", "Information"}

type activityPage struct {
	page
	tabs                    *walk.TabWidget
	events, audit           table
	levels                  []string
	level                   *walk.ComboBox
	findEvents, findAudit   *walk.LineEdit
	countEvents, countAudit *walk.Label
	loaded                  time.Time

	refresh, details, copyRow *command
}

func (s *activityPage) init(m *manager) *page {
	s.icon = desktop.IconActivity
	s.title = func() string { return "Events and audit log" }
	s.subtitle = func() string {
		if s.loaded.IsZero() {
			return "The last 500 of each"
		}
		return "The last 500 of each · refreshed at " + s.loaded.Format("15:04:05")
	}
	s.search = func() *walk.LineEdit {
		if s.tabs != nil && s.tabs.CurrentIndex() == 1 {
			return s.findAudit
		}
		return s.findEvents
	}
	s.load = func() { s.reload(m) }
	// Kept current while shown, without asking the server every 3 s.
	s.update = func() {
		if time.Since(s.loaded) > 15*time.Second {
			s.reload(m)
		}
	}
	s.refresh = newCommand("Refresh", desktop.IconRefresh, func() { s.reload(m) })
	s.details = newCommand("Details…", desktop.IconEye, func() { s.showDetails(m) })
	s.copyRow = newCommand("Copy", desktop.IconCopy, func() {
		if _, r := s.currentRow(); r != nil {
			walk.Clipboard().SetText(strings.Join(r, "\t"))
		}
	})
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
	s.events.icon = func(row, col int) walk.Image {
		if row >= len(s.levels) || col != 1 {
			return nil
		}
		switch s.levels[row] {
		case "error":
			return img(desktop.IconError)
		case "warning":
			return img(desktop.IconWarning)
		}
		return img(desktop.IconInfo)
	}
	s.events.match = func(row int) bool {
		if row >= len(s.levels) || s.level == nil {
			return true
		}
		switch s.level.CurrentIndex() {
		case 1:
			return s.levels[row] == "error"
		case 2:
			return s.levels[row] == "error" || s.levels[row] == "warning"
		case 3:
			return s.levels[row] != "error" && s.levels[row] != "warning"
		}
		return true
	}
	s.audit.icon = func(row, col int) walk.Image {
		if col == 1 {
			return img(desktop.IconUser)
		}
		return nil
	}
	return &s.page
}

func (s *activityPage) reload(m *manager) {
	if !m.connected() {
		return
	}
	s.loaded = time.Now()
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
		updateCount(s.countEvents, &s.events)
		if m.cur == &s.page {
			m.updateHeader()
		}
	})
	loadInto(m, "/api/audit?limit=500", func(list []model.AuditEntry) {
		keys := make([]string, len(list))
		rows := make([][]string, len(list))
		for i, a := range list {
			keys[i] = fmt.Sprint(a.ID)
			rows[i] = []string{a.Time.Local().Format("2006-01-02 15:04:05"), a.User, a.IP, a.Action, a.Target, a.Detail}
		}
		s.audit.set(keys, rows)
		updateCount(s.countAudit, &s.audit)
	})
}

// currentRow is the list shown and its current row's cells.
func (s *activityPage) currentRow() (*table, []string) {
	t := &s.events
	if s.tabs != nil && s.tabs.CurrentIndex() == 1 {
		t = &s.audit
	}
	if r := t.current(); r >= 0 {
		return t, t.rows[r]
	}
	return t, nil
}

func (s *activityPage) showDetails(m *manager) {
	t, r := s.currentRow()
	if r == nil {
		return
	}
	if t == &s.events {
		icon := walk.TaskDialogSystemIconInformation
		switch r[1] {
		case "error":
			icon = walk.TaskDialogSystemIconError
		case "warning":
			icon = walk.TaskDialogSystemIconWarning
		}
		site := r[3]
		if site == "" {
			site = "the server"
		}
		notify(m.mw, "Event", r[4], "", fmt.Sprintf("Time: %s\nLevel: %s\nType: %s\nSite: %s", r[0], r[1], r[2], site), icon)
		return
	}
	notify(m.mw, "Audit log entry", r[3]+" "+r[4], r[5],
		fmt.Sprintf("Time: %s\nUser: %s\nFrom: %s", r[0], r[1], r[2]), walk.TaskDialogSystemIconInformation)
}

func (s *activityPage) content(m *manager) []Widget {
	rowMenu := menu(s.details, s.copyRow, nil, s.refresh)
	return []Widget{
		TabWidget{AssignTo: &s.tabs, OnCurrentIndexChanged: func() { m.updateCommands() }, Pages: []TabPage{
			{Title: "Events", Image: img(desktop.IconActivity), Layout: VBox{}, Children: []Widget{
				searchRow(&s.findEvents, &s.countEvents, "Search events", &s.events,
					Label{Text: "Show:"},
					ComboBox{AssignTo: &s.level, Model: levelFilters, CurrentIndex: 0, OnCurrentIndexChanged: func() {
						s.events.refilter()
						updateCount(s.countEvents, &s.events)
					}}),
				s.events.viewWith(tableOpts{name: "events", sortable: true, onActivate: s.details.trigger, menu: rowMenu},
					col("Time", 140), col("Level", 90), col("Type", 130), col("Site", 140), col("Message", 420)),
			}},
			{Title: "Audit log", Image: img(desktop.IconIDCard), Layout: VBox{}, Children: []Widget{
				searchRow(&s.findAudit, &s.countAudit, "Search the audit log", &s.audit),
				s.audit.viewWith(tableOpts{name: "audit", sortable: true, onActivate: s.details.trigger, menu: menu(s.details, s.copyRow, nil, s.refresh)},
					col("Time", 140), col("User", 180), col("From", 110), col("Action", 150), col("Target", 160), col("Detail", 240)),
			}},
		}},
	}
}

func (s *activityPage) actionsPane(m *manager) []Widget {
	return pane(
		"Activity", s.refresh,
		"Selected entry", s.details, s.copyRow,
	)
}
