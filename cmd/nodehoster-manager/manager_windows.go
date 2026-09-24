package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/remote"
	"github.com/parthh37/nodehoster/internal/service"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// manager is the main window, laid out like IIS Manager: connections on
// the left, the selected node's page in the middle, its actions on the
// right, and the connection's state in the status bar; a tool bar with the
// commands used most sits on top.
type manager struct {
	mw         *walk.MainWindow
	cl         apiClient        // the server shown: sw (remote_windows.go)
	sw         *switchClient    // switched between pipe and remote servers
	pipe       *localapi.Client // this server's admin pipe
	nav        *navModel
	tree       *walk.TreeView
	headerIcon *walk.ImageView
	header     *walk.Label
	subheader  *walk.Label
	banner     infoBar // the connection's problems, above every page
	pages      map[navKind]*page
	cur        *page
	site       string // the site shown by the site page

	sbService, sbConn, sbActivity *walk.StatusBarItem
	refreshNow                    chan bool // true: also reload server information

	// The latest state, touched on the UI thread only.
	service string
	account string
	info    *model.ServerInfo
	sites   []localapi.Site
	connErr error

	pendingSite  string // shown as soon as a refresh lists it
	pendingTries int    // refreshes that did not list it
	resumeSite   string // the site shown when the connection dropped
	rebuilding   bool   // the tree is being rebuilt: ignore its selection changes

	// What runs in the background, and the last outcome, for the status bar.
	busy     map[int]string
	busyIDs  []int
	nextBusy int
	flash    string
	flashBad bool
	flashAt  time.Time

	// Commands of the menu bar and the tool bar.
	cmdRefresh, cmdStart, cmdStop, cmdRestart      *command
	cmdAddSite, cmdImport, cmdConsole, cmdSettings *command
	cmdDataFolder, cmdServerLog, cmdBackup         *command
	cmdRestore, cmdMime, cmdMail, cmdFind          *command
	cmdConnect, cmdDisconnect                      *command

	server   serverPage
	sitesPg  sitesPage
	sitePg   sitePage
	certs    certsPage
	node     nodePage
	users    usersPage
	activity activityPage
	mail     mailPage
	bans     bansPage
	backups  backupsPage
	updates  updatesPage
	previews previewsPage
}

// page is what the middle and right panes show for a node of the tree.
type page struct {
	content, actions *walk.Composite
	icon             string                // the header's tile
	tile             func() string         // optional: the tile, when it depends on what is shown
	title            func() string         // the header
	subtitle         func() string         // optional: the line under it
	update           func()                // redraw from the manager's state (cheap, frequent)
	load             func()                // fetch what only this page needs (on show and refresh)
	hide             func()                // optional: the page is no longer shown
	search           func() *walk.LineEdit // optional: the field Ctrl+F focuses
}

func runManager(openSite string) {
	app, err := walk.InitApp()
	if err != nil {
		log.Fatal(err)
	}
	// Window size, pane widths and list columns are remembered in
	// %APPDATA%\NodeHoster\Manager\manager.ini.
	app.SetOrganizationName("NodeHoster")
	app.SetProductName("Manager")
	settings := walk.NewIniFileSettings("manager.ini")
	settings.SetExpireDuration(180 * 24 * time.Hour)
	if settings.Load() == nil {
		app.SetSettings(settings)
	}

	m := &manager{
		pipe:         localapi.Connect(localapi.Admin, config.DefaultDataDir()),
		sw:           &switchClient{},
		refreshNow:   make(chan bool, 1),
		busy:         map[int]string{},
		pendingSite:  openSite,
		pendingTries: -10, // the first refreshes may come before the service answers
	}
	m.sw.set(m.pipe, nil)
	m.cl = m.sw
	m.nav = newNavModel()
	m.addSavedRoots()
	m.initCommands()

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
		{navBackups, m.backups.init(m), m.backups.content(m), m.backups.actionsPane(m)},
		{navUpdates, m.updates.init(m), m.updates.content(m), m.updates.actionsPane(m)},
		{navPreviews, m.previews.init(m), m.previews.content(m), m.previews.actionsPane(m)},
	}
	m.pages = map[navKind]*page{}
	var contents, actions []Widget
	for _, pg := range pages {
		p := pg.p
		m.pages[pg.kind] = p
		contents = append(contents, Composite{AssignTo: &p.content, Visible: false, Layout: VBox{MarginsZero: true, Spacing: 8}, Children: pg.w})
		actions = append(actions, Composite{AssignTo: &p.actions, Visible: false, Layout: VBox{MarginsZero: true, Spacing: 5}, Children: pg.a})
	}
	m.markLocalOnly()

	host, _ := os.Hostname()
	var icon Property
	if ic := appIcon(); ic != nil {
		icon = ic
	}
	err = MainWindow{
		AssignTo:   &m.mw,
		Name:       "manager",
		Persistent: true,
		Title:      "NodeHoster Manager — " + host,
		Icon:       icon,
		MinSize:    Size{Width: 980, Height: 600},
		Size:       Size{Width: 1360, Height: 820},
		Background: SolidColorBrush{Color: colorSurface},
		Layout:     VBox{MarginsZero: true, SpacingZero: true},
		MenuItems:  m.menuBar(),
		ToolBar: ToolBar{
			ButtonStyle: ToolBarButtonImageBeforeText,
			Items: []MenuItem{
				m.cmdRefresh.toolItem(),
				Separator{},
				m.cmdStart.toolItem(), m.cmdStop.toolItem(), m.cmdRestart.toolItem(),
				Separator{},
				m.cmdAddSite.toolItem(), m.cmdImport.toolItem(),
				Separator{},
				m.cmdConnect.toolItem(),
				Separator{},
				m.cmdConsole.toolItem(), m.cmdDataFolder.toolItem(), m.cmdServerLog.toolItem(),
			},
		},
		Children: []Widget{
			HSplitter{
				Name:        "panes",
				Persistent:  true,
				HandleWidth: 6,
				Children: []Widget{
					Composite{
						StretchFactor: 1,
						Layout:        VBox{Margins: Margins{Left: 8, Top: 8, Right: 2, Bottom: 8}, Spacing: 6},
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
						Layout:        VBox{Margins: Margins{Left: 10, Top: 10, Right: 10, Bottom: 8}, Spacing: 10},
						Children: append([]Widget{
							Composite{
								Layout: HBox{MarginsZero: true, Spacing: 12, Alignment: AlignHNearVCenter},
								Children: []Widget{
									ImageView{AssignTo: &m.headerIcon, MinSize: Size{Width: 32, Height: 32}, MaxSize: Size{Width: 32, Height: 32}},
									Composite{Layout: VBox{MarginsZero: true, SpacingZero: true}, Children: []Widget{
										Label{AssignTo: &m.header, Font: fontTitle, EllipsisMode: EllipsisEnd},
										Label{AssignTo: &m.subheader, TextColor: colorMuted, EllipsisMode: EllipsisEnd},
									}},
								},
							},
							m.banner.widget(),
						}, contents...),
					},
					ScrollView{
						StretchFactor:   1,
						HorizontalFixed: true,
						Background:      SolidColorBrush{Color: colorSurface},
						Layout:          VBox{Margins: Margins{Left: 6, Top: 10, Right: 10, Bottom: 8}, Spacing: 6},
						Children: append([]Widget{
							Label{Text: "Actions", Font: Font{Family: "Segoe UI Semibold", PointSize: 11}},
						}, append(actions, VSpacer{})...),
					},
				},
			},
		},
		StatusBarItems: []StatusBarItem{
			{AssignTo: &m.sbService, Width: 190, OnClicked: func() { m.showKind(navServer) }},
			{AssignTo: &m.sbConn, Width: 400},
			{AssignTo: &m.sbActivity, Width: 360},
		},
	}.Create()
	if err != nil {
		fatal(err)
	}
	armSortables()
	syncCommands()

	m.tree.SetExpanded(m.nav.root, true)
	m.tree.SetCurrentItem(m.nav.root)
	m.updateActivity()
	go m.poll()
	app.Run()
	if app.Settings() != nil {
		settings.Save()
	}
}

// initCommands makes the commands of the menu bar and the tool bar; the
// server page shows some of them too.
func (m *manager) initCommands() {
	m.cmdRefresh = newCommand("Refresh", desktop.IconRefresh, func() { m.refresh(true) }).withShortcut(0, walk.KeyF5)
	m.cmdStart = newCommand("Start service", desktop.IconStart, func() { m.serviceAction("start") }).withShort("Start")
	m.cmdStop = newCommand("Stop service", desktop.IconStop, func() { m.serviceAction("stop") }).withShort("Stop")
	m.cmdRestart = newCommand("Restart service", desktop.IconRestart, func() { m.serviceAction("restart") }).withShort("Restart")
	m.cmdAddSite = newCommand("Add site…", desktop.IconAdd, func() { addSiteDialog(m) }).withShortcut(walk.ModControl, walk.KeyN)
	m.cmdImport = newCommand("Import from IIS…", desktop.IconImport, func() { importIISDialog(m) }).withShort("Import")
	m.cmdConsole = newCommand("Open web console", desktop.IconConsole, m.openConsole).withShort("Web console")
	m.cmdSettings = newCommand("Web console settings…", desktop.IconSettings, func() { adminConsoleDialog(m) })
	m.cmdDataFolder = newCommand("Open data folder", desktop.IconFolder, func() { shellOpen(m.dataDir()) }).withShort("Data folder")
	m.cmdServerLog = newCommand("Open server log", desktop.IconLog, func() { shellOpen(filepath.Join(m.dataDir(), "logs", "nodehoster.log")) }).withShort("Server log")
	m.cmdBackup = newCommand("Back up to a file…", desktop.IconDownload, m.backupConfig)
	m.cmdRestore = newCommand("Restore from a file…", desktop.IconUpload, func() { restoreFromFile(m) })
	m.cmdMime = newCommand("MIME types…", desktop.IconFileCode, func() { serverMimeDialog(m) })
	m.cmdMail = newCommand("SMTP E-mail…", desktop.IconMail, func() { mailPropertiesDialog(m) })
	m.cmdFind = newCommand("Find in list", desktop.IconSearch, m.focusSearch).withShortcut(walk.ModControl, walk.KeyF)
	m.cmdConnect = newCommand("Connect to a server…", desktop.IconGlobe, func() { connectDialog(m) }).withShort("Connect")
	m.cmdDisconnect = newCommand("Remove connection…", desktop.IconRemove, m.disconnect)
}

func (m *manager) menuBar() []MenuItem {
	var view []MenuItem
	for i, it := range m.nav.root.children {
		kind := it.kind
		a := Action{Text: it.text, Image: asImage(it.image), OnTriggered: func() { m.showKind(kind) }}
		if i < 9 {
			a.Shortcut = Shortcut{Modifiers: walk.ModControl, Key: walk.Key1 + walk.Key(i+1)}
		}
		view = append(view, a)
	}
	serverView := Action{Text: "Server home", Image: img(desktop.IconServer), OnTriggered: func() { m.showKind(navServer) },
		Shortcut: Shortcut{Modifiers: walk.ModControl, Key: walk.Key1}}
	view = append([]MenuItem{serverView}, view...)
	view = append(view, Separator{}, m.cmdFind.barItem())

	return []MenuItem{
		Menu{Text: "&File", Items: []MenuItem{
			m.cmdRefresh.barItem(),
			Separator{},
			m.cmdAddSite.barItem(),
			m.cmdImport.barItem(),
			Separator{},
			m.cmdConnect.barItem(),
			m.cmdDisconnect.barItem(),
			Separator{},
			m.cmdBackup.barItem(),
			m.cmdRestore.barItem(),
			Separator{},
			Action{Text: "E&xit", Image: img(desktop.IconExit), OnTriggered: func() { m.mw.Close() }},
		}},
		Menu{Text: "&Service", Items: []MenuItem{
			m.cmdStart.barItem(),
			m.cmdStop.barItem(),
			m.cmdRestart.barItem(),
		}},
		Menu{Text: "&View", Items: view},
		Menu{Text: "&Tools", Items: []MenuItem{
			m.cmdConsole.barItem(),
			m.cmdSettings.barItem(),
			Separator{},
			m.cmdMime.barItem(),
			m.cmdMail.barItem(),
			Separator{},
			m.cmdDataFolder.barItem(),
			m.cmdServerLog.barItem(),
		}},
		Menu{Text: "&Help", Items: []MenuItem{
			Action{Text: "&About NodeHoster Manager", Image: img(desktop.IconInfo), OnTriggered: m.about},
		}},
	}
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
		// The server selected in the tree: a remote one has no Windows
		// service here, it runs when it answers.
		cl, conn, gen := m.sw.snapshot()
		svc, err := "running", error(nil)
		if conn == nil {
			if svc, err = service.Status(); err != nil {
				svc = "unknown"
			}
		}
		var sites []localapi.Site
		var info *model.ServerInfo
		var account string
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		sites, err = cl.Sites(ctx)
		if err == nil && (full || n%10 == 0) {
			info, _ = cl.ServerInfo(ctx)
			account = m.whoami(ctx, cl)
		}
		cancel()
		if err != nil && conn != nil {
			svc = "unknown"
		}
		m.mw.Synchronize(func() {
			if m.sw.gen.Load() == gen { // not the previous server's
				m.apply(svc, sites, info, account, err)
			}
		})

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
	if err != nil && m.cur == m.pages[navSite] && m.site != "" {
		// The connection dropped (the service restarts, say): the tree
		// loses the sites and falls back to the list; come back to this
		// site once it answers again, unless the user went elsewhere.
		m.resumeSite = m.site
	}
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
	m.updateBanner()
	m.updateCommands()
	if m.cur != nil {
		m.updateHeader()
		if m.cur.update != nil {
			m.cur.update()
		}
	}
	if m.resumeSite != "" && err == nil {
		if m.cur == m.pages[navSites] {
			m.showSite(m.resumeSite)
		}
		m.resumeSite = ""
	}
	if m.pendingSite != "" && err == nil {
		if m.showSite(m.pendingSite) {
			m.pendingSite = ""
		} else if m.pendingTries++; m.pendingTries > 3 {
			m.pendingSite = "" // removed meanwhile
		}
	}
}

// openWhenListed shows a site as soon as a refresh lists it.
func (m *manager) openWhenListed(id string) {
	m.pendingSite, m.pendingTries = id, 0
}

func (m *manager) connected() bool { return m.connErr == nil }

// serviceLevel is the service state as a status color.
func (m *manager) serviceLevel() desktop.Level {
	switch m.service {
	case "running":
		if m.connected() {
			return desktop.LevelOK
		}
		return desktop.LevelWarning
	case "starting", "stopping":
		return desktop.LevelWarning
	case "stopped", "paused":
		return desktop.LevelDown
	}
	return desktop.LevelNotInstalled
}

func (m *manager) updateStatusBar() {
	if conn := m.remoteConn(); conn != nil {
		m.updateRemoteStatus(conn)
		return
	}
	m.sbService.SetIcon(dotIcon(m.serviceLevel()))
	m.sbService.SetText("Service: " + desktop.StateText(model.SiteState(m.service)))
	switch {
	case m.connErr == nil:
		who := m.account
		if who == "" {
			who = "administrator"
		}
		m.sbConn.SetIcon(ico(desktop.IconPlug))
		m.sbConn.SetText("Connected over the local admin pipe as " + who)
	case errors.Is(m.connErr, localapi.ErrNotRunning):
		m.sbConn.SetIcon(icoOff(desktop.IconPlug))
		m.sbConn.SetText("Not connected: the service is not running")
	default:
		m.sbConn.SetIcon(ico(desktop.IconError))
		m.sbConn.SetText("Not connected: " + m.connErr.Error())
	}
	m.updateActivity()
}

// updateBanner explains, above every page, why the manager cannot manage
// the server right now, with the fix at hand.
func (m *manager) updateBanner() {
	if conn := m.remoteConn(); conn != nil {
		m.updateRemoteBanner(conn)
		return
	}
	switch {
	case m.service == "not installed":
		m.banner.show(barError, "The NodeHoster service is not installed. Run the installer, or from an elevated prompt: nodehoster service install", "", nil)
	case m.service == "stopped" || m.service == "paused":
		m.banner.show(barError, "The NodeHoster service is "+m.service+". The sites it hosts are offline.", "Start the service", func() { m.serviceAction("start") })
	case m.service == "starting":
		m.banner.show(barInfo, "The NodeHoster service is starting…", "", nil)
	case m.service == "stopping":
		m.banner.show(barWarning, "The NodeHoster service is stopping. The sites it hosts go offline.", "", nil)
	case errors.Is(m.connErr, localapi.ErrNotRunning):
		m.banner.show(barWarning, "The service is running but does not answer on the local admin pipe yet.", "Retry", func() { m.refresh(true) })
	case m.connErr != nil:
		m.banner.show(barError, "Cannot connect to the service: "+m.connErr.Error(), "Retry", func() { m.refresh(true) })
	default:
		m.banner.hide()
	}
}

// updateCommands enables the menu bar's and tool bar's commands.
func (m *manager) updateCommands() {
	installed := m.service != "not installed" && m.service != "unknown" && m.service != ""
	setEnabled(installed && (m.service == "stopped" || m.service == "paused"), m.cmdStart)
	setEnabled(m.service == "running", m.cmdStop, m.cmdRestart)
	setEnabled(m.connected(), m.cmdAddSite, m.cmdImport, m.cmdSettings, m.cmdBackup, m.cmdRestore, m.cmdMime, m.cmdMail)
	setEnabled(m.connected() && (m.remote() || m.info != nil && m.info.AdminURL != "" && m.info.AdminError == ""), m.cmdConsole)
	setEnabled(m.cur != nil && m.cur.search != nil, m.cmdFind)
	setEnabled(true, m.cmdDataFolder, m.cmdServerLog) // this server only (setEnabled)
	setEnabled(m.remote(), m.cmdDisconnect)
}

// updateHeader shows the current page's title, subtitle and tile. It runs
// on every refresh, so it only touches what changed: setting a label's
// text or visibility lays the window out again, which flickers.
func (m *manager) updateHeader() {
	p := m.cur
	setLabel(m.header, p.title())
	sub := ""
	if p.subtitle != nil {
		sub = p.subtitle()
	}
	setLabel(m.subheader, sub)
	setVisible(m.subheader, sub != "")
	icon := p.icon
	if p.tile != nil {
		icon = p.tile()
	}
	m.headerIcon.SetImage(asImage(tileIcon(icon))) // a no-op for the same image
}

// ---- the status bar's activity: what runs, else how the last one went

// begin shows that what runs, until the returned function is called.
func (m *manager) begin(what string) (done func()) {
	id := m.nextBusy
	m.nextBusy++
	m.busy[id] = what
	m.busyIDs = append(m.busyIDs, id)
	m.updateActivity()
	return func() {
		delete(m.busy, id)
		m.busyIDs = slices.DeleteFunc(m.busyIDs, func(x int) bool { return x == id })
		m.updateActivity()
	}
}

// flashStatus shows an outcome in the status bar for a while.
func (m *manager) flashStatus(text string, bad bool) {
	m.flash, m.flashBad, m.flashAt = text, bad, time.Now()
	m.updateActivity()
}

func (m *manager) updateActivity() {
	switch {
	case len(m.busyIDs) > 0:
		text := m.busy[m.busyIDs[len(m.busyIDs)-1]] + "…"
		if n := len(m.busyIDs); n > 1 {
			text += fmt.Sprintf("  (%d tasks running)", n)
		}
		m.sbActivity.SetIcon(ico(desktop.IconClock))
		m.sbActivity.SetText(text)
	case m.flash != "" && time.Since(m.flashAt) < 30*time.Second:
		if m.flashBad {
			m.sbActivity.SetIcon(ico(desktop.IconError))
		} else {
			m.sbActivity.SetIcon(ico(desktop.IconOK))
		}
		m.sbActivity.SetText(m.flash)
	default:
		m.sbActivity.SetIcon(nil)
		m.sbActivity.SetText("Ready")
	}
}

// ---- navigation

func (m *manager) navigate() {
	if m.rebuilding {
		return
	}
	item, _ := m.tree.CurrentItem().(*navItem)
	if item == nil {
		return
	}
	if r := rootOf(item); r != m.nav.root {
		// Another server's node: its pages move under it. Not while the
		// tree reports the selection: the switch rebuilds the tree.
		m.mw.Synchronize(func() {
			if m.activateRoot(r) {
				m.tree.SetCurrentItem(r)
			} else {
				m.tree.SetCurrentItem(m.nav.root)
			}
		})
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
	m.updateHeader()
	m.updateCommands()
	if p.update != nil {
		p.update()
	}
	if p.load != nil {
		p.load()
	}
}

// showKind selects a node of the tree.
func (m *manager) showKind(k navKind) {
	if k == navServer {
		m.tree.SetCurrentItem(m.nav.root)
		return
	}
	for _, it := range m.nav.root.children {
		if it.kind == k {
			m.tree.SetCurrentItem(it)
			return
		}
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

// focusSearch puts the cursor in the current page's search field.
func (m *manager) focusSearch() {
	if m.cur != nil && m.cur.search != nil {
		if le := m.cur.search(); le != nil {
			le.SetFocus()
			le.SetTextSelection(0, -1)
		}
	}
}

// syncTree mirrors the sites under the Sites node. Renames, additions and
// removals rebuild the node; state changes only swap the icons.
func (m *manager) syncTree() {
	n := m.nav.sites
	same := len(n.children) == len(m.sites)
	for i := 0; same && i < len(m.sites); i++ {
		same = n.children[i].siteID == m.sites[i].ID && n.children[i].text == m.sites[i].Name
	}
	if same {
		for i, s := range m.sites {
			if ic := siteIcon(string(s.Type), desktop.SiteLevel(s.Status.State)); n.children[i].image != ic {
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
		n.children = append(n.children, &navItem{kind: navSite, text: s.Name, siteID: s.ID, parent: n,
			image: siteIcon(string(s.Type), desktop.SiteLevel(s.Status.State))})
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

// do runs fn off the UI thread, shows it in the status bar, reports its
// error, and refreshes.
func (m *manager) do(what string, fn func(ctx context.Context) error) {
	m.run(what, 3*time.Minute, fn)
}

// long is do for transfers that may take much longer than a request.
func (m *manager) long(what string, fn func(ctx context.Context) error) {
	m.run(what, 2*time.Hour, fn)
}

func (m *manager) run(what string, timeout time.Duration, fn func(ctx context.Context) error) {
	done := m.begin(what)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := fn(ctx)
		cancel()
		m.mw.Synchronize(func() {
			done()
			if err != nil {
				m.flashStatus(what+" failed", true)
				m.errorBox(what, err)
			} else {
				m.flashStatus(what+" — done", false)
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
	notify(owner, "NodeHoster Manager", what, msg, "", walk.TaskDialogSystemIconError)
}

func (m *manager) confirm(title, msg string) bool {
	return walk.MsgBox(m.mw, title, msg, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) == walk.DlgCmdYes
}

func (m *manager) serviceAction(cmd string) {
	switch cmd {
	case "stop":
		if ask(m.mw, "NodeHoster service", "Stop the NodeHoster service?",
			"Every site it hosts goes offline until the service is started again.",
			walk.TaskDialogSystemIconWarning, [2]string{"Stop the service", ""}) != 0 {
			return
		}
	case "restart":
		if ask(m.mw, "NodeHoster service", "Restart the NodeHoster service?",
			"Every site it hosts goes offline for a few seconds while the service restarts.",
			walk.TaskDialogSystemIconWarning, [2]string{"Restart the service", ""}) != 0 {
			return
		}
	}
	m.do(map[string]string{"start": "Starting the service", "stop": "Stopping the service", "restart": "Restarting the service"}[cmd],
		func(context.Context) error { return controlService(cmd) })
}

func (m *manager) openConsole() {
	if conn := m.remoteConn(); conn != nil {
		shellOpen(conn.URL + "/")
		return
	}
	if m.info == nil || m.info.AdminURL == "" {
		notify(m.mw, "Web console", "The web console is not available.",
			"Use Tools → Web console settings to change where it listens.", "", walk.TaskDialogSystemIconInformation)
		return
	}
	shellOpen(desktop.ConsoleURL(m.info.AdminURL))
}

// openConsolePath opens a page of the web console.
func (m *manager) openConsolePath(p string) {
	if conn := m.remoteConn(); conn != nil {
		shellOpen(conn.URL + p)
		return
	}
	if m.info == nil || m.info.AdminURL == "" {
		m.openConsole() // explains why it is unavailable
		return
	}
	shellOpen(strings.TrimRight(desktop.ConsoleURL(m.info.AdminURL), "/") + p)
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
		v = m.info.Version + " (" + shortCommit(m.info.Commit) + ")"
	}
	notify(m.mw, "About NodeHoster Manager", "NodeHoster Manager "+config.Version,
		"Server "+v+"\n\nManages this server over a local named pipe that only elevated Administrators can open, independent of the web console.",
		"Data folder: "+m.dataDir(), walk.TaskDialogSystemIconInformation)
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
	navBackups
	navUpdates
	navPreviews
)

type navItem struct {
	kind     navKind
	text     string
	siteID   string
	conn     *remote.Saved // a remote server's node (remote_windows.go)
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

// Image is the item's icon. Every item has one: an item without an image
// gets index -1 from walk, which the tree control takes as "ask me later"
// and draws with some other item's icon.
func (n *navItem) Image() interface{} {
	if n.image == nil {
		return nil
	}
	return n.image
}

type navModel struct {
	walk.TreeModelBase
	root, sites *navItem   // root: the node of the server shown, with the pages
	roots       []*navItem // this server, then the remote servers connected to
}

func newNavModel() *navModel {
	host, _ := os.Hostname()
	root := &navItem{kind: navServer, text: host, image: ico(desktop.IconServer)}
	m := &navModel{root: root, roots: []*navItem{root}}
	m.sites = &navItem{kind: navSites, text: "Sites", parent: root, image: ico(desktop.IconSites)}
	root.children = []*navItem{
		m.sites,
		{kind: navPreviews, text: "Preview deployments", parent: root, image: ico(desktop.IconEye)},
		{kind: navCerts, text: "Certificates", parent: root, image: ico(desktop.IconCertificate)},
		{kind: navMail, text: "SMTP E-mail", parent: root, image: ico(desktop.IconMail)},
		{kind: navNode, text: "Node.js versions", parent: root, image: ico(desktop.IconNodeVersion)},
		{kind: navUsers, text: "Web console users", parent: root, image: ico(desktop.IconUsers)},
		{kind: navBans, text: "Banned IP addresses", parent: root, image: ico(desktop.IconBans)},
		{kind: navActivity, text: "Events and audit log", parent: root, image: ico(desktop.IconActivity)},
		{kind: navBackups, text: "Backups", parent: root, image: ico(desktop.IconBackups)},
		{kind: navUpdates, text: "Updates", parent: root, image: ico(desktop.IconDownload)},
	}
	return m
}

func (m *navModel) RootCount() int             { return len(m.roots) }
func (m *navModel) RootAt(i int) walk.TreeItem { return m.roots[i] }
