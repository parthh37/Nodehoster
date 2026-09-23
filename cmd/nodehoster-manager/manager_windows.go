package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/service"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// manager is the main window, laid out like IIS Manager: connections on
// the left, the selected node's page in the middle, its actions on the
// right, and the connection's state in the status bar.
type manager struct {
	mw     *walk.MainWindow
	cl     *localapi.Client
	nav    *navModel
	tree   *walk.TreeView
	header *walk.Label
	pages  map[navKind]*page
	cur    *page
	site   string // the site shown by the site page

	sbService, sbConn, sbActivity *walk.StatusBarItem
	dots                          map[desktop.Level]*walk.Icon
	refreshNow                    chan bool // true: also reload server information

	// The latest state, touched on the UI thread only.
	service string
	account string
	info    *model.ServerInfo
	sites   []localapi.Site
	connErr error

	pendingSite string // shown as soon as a refresh lists it
	rebuilding  bool   // the tree is being rebuilt: ignore its selection changes

	server   serverPage
	sitesPg  sitesPage
	sitePg   sitePage
	certs    certsPage
	node     nodePage
	users    usersPage
	activity activityPage
	mail     mailPage
	bans     bansPage
}

// page is what the middle and right panes show for a node of the tree.
type page struct {
	content, actions *walk.Composite
	title            func() string
	update           func() // redraw from the manager's state (cheap, frequent)
	load             func() // fetch what only this page needs (on show and refresh)
	hide             func() // optional: the page is no longer shown
}

func runManager(openSite string) {
	app, err := walk.InitApp()
	if err != nil {
		log.Fatal(err)
	}
	m := &manager{
		cl:          localapi.Connect(localapi.Admin, config.DefaultDataDir()),
		refreshNow:  make(chan bool, 1),
		dots:        map[desktop.Level]*walk.Icon{},
		pendingSite: openSite,
	}
	for _, l := range []desktop.Level{desktop.LevelOK, desktop.LevelWarning, desktop.LevelDown, desktop.LevelNotInstalled} {
		if ic, err := walk.NewIconFromImage(desktop.StatusDot(l, 16)); err == nil {
			m.dots[l] = ic
		}
	}
	appIcon, _ := walk.NewIconFromImage(desktop.AppIcon(32))
	m.nav = newNavModel(appIcon)

	pages := []struct {
		kind navKind
		p    *page
		w    []Widget // content
		a    []Widget // actions
	}{
		{navServer, m.server.init(m), m.server.content(m), m.server.actionsPane(m)},
		{navSites, m.sitesPg.init(m), m.sitesPg.content(m), m.sitesPg.actionsPane(m)},
		{navSite, m.sitePg.init(m), m.sitePg.content(m), m.sitePg.actionsPane(m)},
		{navCerts, m.certs.init(m), m.certs.content(m), m.certs.actionsPane(m)},
		{navNode, m.node.init(m), m.node.content(m), m.node.actionsPane(m)},
		{navUsers, m.users.init(m), m.users.content(m), m.users.actionsPane(m)},
		{navActivity, m.activity.init(m), m.activity.content(m), m.activity.actionsPane(m)},
		{navMail, m.mail.init(m), m.mail.content(m), m.mail.actionsPane(m)},
		{navBans, m.bans.init(m), m.bans.content(m), m.bans.actionsPane(m)},
	}
	m.pages = map[navKind]*page{}
	var contents, actions []Widget
	for _, pg := range pages {
		p := pg.p
		m.pages[pg.kind] = p
		contents = append(contents, Composite{AssignTo: &p.content, Visible: false, Layout: VBox{MarginsZero: true}, Children: pg.w})
		actions = append(actions, Composite{AssignTo: &p.actions, Visible: false, Layout: VBox{MarginsZero: true, Spacing: 4}, Children: append(pg.a, VSpacer{})})
	}

	host, _ := os.Hostname()
	err = MainWindow{
		AssignTo: &m.mw,
		Title:    "NodeHoster Manager — " + host,
		Icon:     appIcon,
		MinSize:  Size{Width: 900, Height: 560},
		Size:     Size{Width: 1280, Height: 760},
		Layout:   VBox{MarginsZero: true, SpacingZero: true},
		MenuItems: []MenuItem{
			Menu{Text: "&File", Items: []MenuItem{
				Action{Text: "&Refresh", Shortcut: Shortcut{Key: walk.KeyF5}, OnTriggered: func() { m.refresh(true) }},
				Separator{},
				Action{Text: "E&xit", OnTriggered: func() { m.mw.Close() }},
			}},
			Menu{Text: "&Service", Items: []MenuItem{
				Action{Text: "&Start", OnTriggered: func() { m.serviceAction("start") }},
				Action{Text: "S&top", OnTriggered: func() { m.serviceAction("stop") }},
				Action{Text: "&Restart", OnTriggered: func() { m.serviceAction("restart") }},
			}},
			Menu{Text: "&View", Items: []MenuItem{
				Action{Text: "Open &web console", OnTriggered: m.openConsole},
				Action{Text: "Open &data folder", OnTriggered: func() { shellOpen(m.dataDir()) }},
				Action{Text: "Open server &log", OnTriggered: func() { shellOpen(filepath.Join(m.dataDir(), "logs", "nodehoster.log")) }},
			}},
			Menu{Text: "&Help", Items: []MenuItem{
				Action{Text: "&About NodeHoster Manager", OnTriggered: m.about},
			}},
		},
		Children: []Widget{
			HSplitter{
				HandleWidth: 4,
				Children: []Widget{
					Composite{
						StretchFactor: 1,
						Layout:        VBox{Margins: Margins{Left: 6, Top: 6, Right: 2, Bottom: 6}},
						Children: []Widget{
							heading("Connections"),
							TreeView{
								AssignTo:             &m.tree,
								Model:                m.nav,
								OnCurrentItemChanged: m.navigate,
							},
						},
					},
					Composite{
						StretchFactor: 5,
						Layout:        VBox{Margins: Margins{Left: 8, Top: 6, Right: 8, Bottom: 6}},
						Children: append([]Widget{
							Label{AssignTo: &m.header, Font: Font{PointSize: 14}},
						}, contents...),
					},
					Composite{
						StretchFactor: 1,
						Layout:        VBox{Margins: Margins{Left: 6, Top: 6, Right: 6, Bottom: 6}},
						Children:      append([]Widget{Label{Text: "Actions", Font: Font{PointSize: 11, Bold: true}}}, actions...),
					},
				},
			},
		},
		StatusBarItems: []StatusBarItem{
			{AssignTo: &m.sbService, Width: 180},
			{AssignTo: &m.sbConn, Width: 360},
			{AssignTo: &m.sbActivity, Width: 300},
		},
	}.Create()
	if err != nil {
		fatal(err)
	}

	m.tree.SetExpanded(m.nav.root, true)
	m.tree.SetCurrentItem(m.nav.root)
	go m.poll()
	app.Run()
}

// ---- refreshing

// poll reads the service state and the sites every few seconds (sooner
// when asked) and applies them on the UI thread. The pipe is local, so this
// is cheap; server information (which samples CPU) is read less often.
func (m *manager) poll() {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	full := true
	for n := 0; ; n++ {
		svc, err := service.Status()
		if err != nil {
			svc = "unknown"
		}
		var sites []localapi.Site
		var info *model.ServerInfo
		var account string
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		sites, err = m.cl.Sites(ctx)
		if err == nil && (full || n%10 == 0) {
			info, _ = m.cl.ServerInfo(ctx)
			var who struct{ Account string }
			if m.cl.Get(ctx, "/api/local/whoami", &who) == nil {
				account = who.Account
			}
		}
		cancel()
		m.mw.Synchronize(func() { m.apply(svc, sites, info, account, err) })

		select {
		case <-tick.C:
			full = false
		case full = <-m.refreshNow:
		}
	}
}

// refresh asks for an immediate reload; full also reloads the current
// page's own data.
func (m *manager) refresh(full bool) {
	select {
	case m.refreshNow <- full:
	default:
	}
	if full && m.cur != nil && m.cur.load != nil {
		m.cur.load()
	}
}

func (m *manager) apply(svc string, sites []localapi.Site, info *model.ServerInfo, account string, err error) {
	m.service, m.connErr = svc, err
	if err == nil {
		m.sites = sites
	} else {
		m.sites = nil
	}
	if info != nil {
		m.info = info
	}
	if account != "" {
		m.account = account
	}
	m.syncTree()
	m.updateStatusBar()
	if m.cur != nil {
		m.header.SetText(m.cur.title())
		if m.cur.update != nil {
			m.cur.update()
		}
	}
	if m.pendingSite != "" && m.showSite(m.pendingSite) {
		m.pendingSite = ""
	}
}

func (m *manager) connected() bool { return m.connErr == nil }

func (m *manager) updateStatusBar() {
	m.sbService.SetText("Service: " + m.service)
	switch {
	case m.connErr == nil:
		who := m.account
		if who == "" {
			who = "administrator"
		}
		m.sbConn.SetText("Connected over the local admin pipe as " + who)
	case errors.Is(m.connErr, localapi.ErrNotRunning):
		m.sbConn.SetText("Not connected: the service is not running")
	default:
		m.sbConn.SetText("Not connected: " + m.connErr.Error())
	}
}

func (m *manager) setActivity(s string) { m.sbActivity.SetText(s) }

// ---- navigation

func (m *manager) navigate() {
	if m.rebuilding {
		return
	}
	item, _ := m.tree.CurrentItem().(*navItem)
	if item == nil {
		return
	}
	m.site = item.siteID
	m.show(m.pages[item.kind])
}

func (m *manager) show(p *page) {
	if m.cur != p {
		if m.cur != nil {
			if m.cur.hide != nil {
				m.cur.hide()
			}
			m.cur.content.SetVisible(false)
			m.cur.actions.SetVisible(false)
		}
		m.cur = p
		p.content.SetVisible(true)
		p.actions.SetVisible(true)
	}
	m.header.SetText(p.title())
	if p.update != nil {
		p.update()
	}
	if p.load != nil {
		p.load()
	}
}

// showSite selects a site in the tree (by ID or name), if it is there.
func (m *manager) showSite(idOrName string) bool {
	for _, it := range m.nav.sites.children {
		if it.siteID == idOrName || it.text == idOrName {
			m.tree.SetExpanded(m.nav.sites, true)
			m.tree.SetCurrentItem(it)
			return true
		}
	}
	return false
}

// syncTree mirrors the sites under the Sites node. Renames, additions and
// removals rebuild the node; state changes only swap the dots.
func (m *manager) syncTree() {
	n := m.nav.sites
	same := len(n.children) == len(m.sites)
	for i := 0; same && i < len(m.sites); i++ {
		same = n.children[i].siteID == m.sites[i].ID && n.children[i].text == m.sites[i].Name
	}
	if same {
		for i, s := range m.sites {
			if ic := m.dots[desktop.SiteLevel(s.Status.State)]; n.children[i].image != ic {
				n.children[i].image = ic
				m.nav.PublishItemChanged(n.children[i])
			}
		}
		return
	}
	// Resetting the node deletes its items, and the tree control moves the
	// selection through the neighbors of a deleted selected item, raising
	// a change for each; none of those is the user's choice.
	onSite := m.cur == m.pages[navSite]
	m.rebuilding = true
	n.children = n.children[:0]
	for _, s := range m.sites {
		n.children = append(n.children, &navItem{kind: navSite, text: s.Name, siteID: s.ID, parent: n, image: m.dots[desktop.SiteLevel(s.Status.State)]})
	}
	m.nav.PublishItemsReset(n)
	m.tree.SetExpanded(n, true)
	m.rebuilding = false
	if onSite {
		// Follow the site shown (by ID, so through a rename); if it was
		// removed, fall back to the list.
		if !m.showSite(m.site) {
			m.tree.SetCurrentItem(n)
		}
		// Selecting the item the control already landed on raises no
		// change, so sync the page explicitly.
		m.navigate()
	}
}

// ---- actions

// do runs fn off the UI thread, reports its error, and refreshes.
func (m *manager) do(what string, fn func(ctx context.Context) error) {
	m.setActivity(what + "…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		err := fn(ctx)
		cancel()
		m.mw.Synchronize(func() {
			m.setActivity("")
			if err != nil {
				m.errorBox(what, err)
			}
			m.refresh(true)
		})
	}()
}

func (m *manager) errorBox(what string, err error) { m.errorBoxFor(m.mw, what, err) }

// errorBoxFor is errorBox owned by a dialog that is still open.
func (m *manager) errorBoxFor(owner walk.Form, what string, err error) {
	msg := err.Error()
	var apiErr *localapi.Error
	if errors.As(err, &apiErr) && apiErr.Field != "" {
		msg = fmt.Sprintf("%s (%s)", apiErr.Message, apiErr.Field)
	}
	walk.MsgBox(owner, what, msg, walk.MsgBoxIconError)
}

func (m *manager) confirm(title, msg string) bool {
	return walk.MsgBox(m.mw, title, msg, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) == walk.DlgCmdYes
}

func (m *manager) serviceAction(cmd string) {
	if cmd != "start" && !m.confirm("NodeHoster service", fmt.Sprintf("%s the NodeHoster service? Every site it hosts goes offline until it is running again.", map[string]string{"stop": "Stop", "restart": "Restart"}[cmd])) {
		return
	}
	m.do(map[string]string{"start": "Starting the service", "stop": "Stopping the service", "restart": "Restarting the service"}[cmd],
		func(context.Context) error { return controlService(cmd) })
}

func (m *manager) openConsole() {
	if m.info == nil || m.info.AdminURL == "" {
		walk.MsgBox(m.mw, "Web console", "The web console is not available. Use Server → Web console settings to change where it listens.", walk.MsgBoxIconInformation)
		return
	}
	shellOpen(desktop.ConsoleURL(m.info.AdminURL))
}

func (m *manager) dataDir() string {
	if m.info != nil && m.info.DataDir != "" {
		return m.info.DataDir
	}
	return config.DefaultDataDir()
}

func (m *manager) about() {
	v := "unknown (not connected)"
	if m.info != nil {
		v = m.info.Version
	}
	walk.MsgBox(m.mw, "About NodeHoster Manager",
		fmt.Sprintf("NodeHoster Manager %s\nServer %s\n\nManages this server over a local named pipe that only elevated Administrators can open, independent of the web console.", config.Version, v),
		walk.MsgBoxIconInformation)
}

// siteByID returns the site from the latest refresh.
func (m *manager) siteByID(id string) *localapi.Site {
	for i := range m.sites {
		if m.sites[i].ID == id {
			return &m.sites[i]
		}
	}
	return nil
}

// ---- the connections tree

type navKind int

const (
	navServer navKind = iota
	navSites
	navSite
	navCerts
	navNode
	navUsers
	navActivity
	navMail
	navBans
)

type navItem struct {
	kind     navKind
	text     string
	siteID   string
	parent   *navItem
	children []*navItem
	image    *walk.Icon
}

func (n *navItem) Text() string { return n.text }
func (n *navItem) Parent() walk.TreeItem {
	if n.parent == nil {
		return nil // an untyped nil: a typed one would not compare equal to nil
	}
	return n.parent
}
func (n *navItem) ChildCount() int             { return len(n.children) }
func (n *navItem) ChildAt(i int) walk.TreeItem { return n.children[i] }
func (n *navItem) HasChild() bool              { return len(n.children) > 0 }
func (n *navItem) Image() interface{} {
	if n.image == nil {
		return nil
	}
	return n.image
}

type navModel struct {
	walk.TreeModelBase
	root, sites *navItem
}

func newNavModel(icon *walk.Icon) *navModel {
	host, _ := os.Hostname()
	root := &navItem{kind: navServer, text: host, image: icon}
	m := &navModel{root: root}
	m.sites = &navItem{kind: navSites, text: "Sites", parent: root}
	root.children = []*navItem{
		m.sites,
		{kind: navCerts, text: "Certificates", parent: root},
		{kind: navMail, text: "SMTP E-mail", parent: root},
		{kind: navNode, text: "Node.js versions", parent: root},
		{kind: navUsers, text: "Web console users", parent: root},
		{kind: navBans, text: "Banned IP addresses", parent: root},
		{kind: navActivity, text: "Events and audit log", parent: root},
	}
	return m
}

func (m *navModel) RootCount() int           { return 1 }
func (m *navModel) RootAt(int) walk.TreeItem { return m.root }
