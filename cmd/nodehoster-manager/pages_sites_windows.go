package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// levelColor is the text color of a status level.
func levelColor(l desktop.Level) walk.Color {
	switch l {
	case desktop.LevelOK:
		return colorOK
	case desktop.LevelWarning:
		return colorWarning
	case desktop.LevelDown:
		return colorError
	}
	return colorMuted
}

func stateColor(s model.SiteState) (walk.Color, bool) {
	return levelColor(desktop.SiteLevel(s)), true
}

// ---- server home: a dashboard of the service and the machine

type serverPage struct {
	page
	props               table
	bar                 infoBar
	svc, sites, console statCard
	cpu, mem, disk      statCard
}

func (s *serverPage) init(m *manager) *page {
	s.icon = desktop.IconServer
	s.title = func() string {
		if m.info != nil && m.info.Hostname != "" {
			return m.info.Hostname
		}
		h, _ := os.Hostname()
		return h
	}
	s.subtitle = func() string {
		if i := m.info; i != nil && m.connected() {
			return "NodeHoster " + i.Version + " · " + i.OS
		}
		return "NodeHoster server"
	}
	s.update = func() { s.redraw(m) }
	return &s.page
}

func (s *serverPage) content(m *manager) []Widget {
	return []Widget{
		s.bar.widget(),
		cards(s.svc.widget(desktop.IconPower, "Service", false), s.sites.widget(desktop.IconSites, "Sites", false),
			s.console.widget(desktop.IconConsole, "Web console", false)),
		cards(s.cpu.widget(desktop.IconCPU, "CPU", true), s.mem.widget(desktop.IconMemory, "Memory", true),
			s.disk.widget(desktop.IconDisk, "Disk", true)),
		heading("Details"),
		properties(&s.props, "serverDetails"),
	}
}

func (s *serverPage) actionsPane(m *manager) []Widget {
	return pane(
		"Service", m.cmdStart, m.cmdStop, m.cmdRestart,
		"Web console", m.cmdConsole, m.cmdSettings,
		"Sites", m.cmdAddSite, m.cmdImport,
		"Configuration", m.cmdMime, m.cmdMail,
		"Backup", m.cmdBackup, m.cmdRestore,
		"Files", m.cmdDataFolder, m.cmdServerLog,
	)
}

func (s *serverPage) redraw(m *manager) {
	i := m.info
	if m.connected() && i != nil && i.AdminError != "" {
		s.bar.show(barWarning, "Web console unavailable: "+i.AdminError+". Change where it listens, then restart the service.",
			"Web console settings…", func() { adminConsoleDialog(m) })
	} else {
		s.bar.hide()
	}

	lvl := m.serviceLevel()
	detail := ""
	if m.service == "running" && i != nil && m.connected() {
		detail = "up " + desktop.Uptime(i.StartedAt, time.Now())
	}
	s.svc.set(desktop.StateText(model.SiteState(m.service)), detail, levelColor(lvl))

	running, attention := 0, 0
	for _, st := range m.sites {
		if st.Status.State == model.StateRunning {
			running++
		}
		if desktop.SiteNeedsAttention(desktop.SummaryOf(st)) {
			attention++
		}
	}
	switch {
	case !m.connected():
		s.sites.set("–", "not connected", 0)
	case attention > 0:
		s.sites.set(fmt.Sprintf("%d of %d running", running, len(m.sites)), plural(attention, "site")+" need attention", colorWarning)
	default:
		s.sites.set(fmt.Sprintf("%d of %d running", running, len(m.sites)), "", 0)
	}

	switch {
	case !m.connected() || i == nil:
		s.console.set("–", "", 0)
	case i.AdminError != "":
		s.console.set("Unavailable", i.AdminError, colorError)
	default:
		s.console.set("Available", desktop.ConsoleURL(i.AdminURL), colorOK)
	}

	if m.connected() && i != nil {
		s.cpu.set(fmt.Sprintf("%.0f%%", i.CPUPercent), fmt.Sprintf("of %d cores", i.CPUCount), 0)
		s.cpu.meter.set(int(i.CPUPercent))
		mp := desktop.Percent(i.MemUsed, i.MemTotal)
		s.mem.set(desktop.Bytes(i.MemUsed), fmt.Sprintf("of %s (%d%%)", desktop.Bytes(i.MemTotal), mp), 0)
		s.mem.meter.set(mp)
		used := uint64(0)
		if i.DiskTotal > i.DiskFree {
			used = i.DiskTotal - i.DiskFree
		}
		s.disk.set(desktop.Bytes(i.DiskFree)+" free", "of "+desktop.Bytes(i.DiskTotal), 0)
		s.disk.meter.set(desktop.Percent(used, i.DiskTotal))
	} else {
		for _, c := range []*statCard{&s.cpu, &s.mem, &s.disk} {
			c.set("–", "", 0)
			c.meter.set(0)
		}
	}

	props := []prop{p(desktop.IconPower, "Service", desktop.StateText(model.SiteState(m.service)))}
	if i != nil && m.connected() {
		console := desktop.ConsoleURL(i.AdminURL)
		if i.AdminError != "" {
			console = "unavailable: " + i.AdminError
		}
		props = append(props,
			p(desktop.IconTag, "Version", i.Version+" ("+shortCommit(i.Commit)+")"),
			p(desktop.IconServer, "Host name", i.Hostname),
			p(desktop.IconWindows, "Operating system", i.OS),
			p(desktop.IconClock, "Running for", desktop.Uptime(i.StartedAt, time.Now())),
			p(desktop.IconCPU, "CPU", fmt.Sprintf("%.0f%% of %d cores", i.CPUPercent, i.CPUCount)),
			p(desktop.IconMemory, "Memory", desktop.Bytes(i.MemUsed)+" used of "+desktop.Bytes(i.MemTotal)),
			p(desktop.IconDisk, "Disk free", desktop.Bytes(i.DiskFree)+" of "+desktop.Bytes(i.DiskTotal)),
			p(desktop.IconFolder, "Data folder", i.DataDir),
			p(desktop.IconPlug, "Listening on", strings.Join(i.Listeners, ", ")),
			p(desktop.IconConsole, "Web console", console),
			p(desktop.IconSites, "Sites", fmt.Sprintf("%d running of %d", running, len(m.sites))),
		)
	}
	s.props.setProperties(props...)
}

func shortCommit(c string) string {
	if len(c) > 8 {
		return c[:8]
	}
	return c
}

// ---- the commands of a site, shared by the list (for the selected site)
// and the site page (for the site shown)

type siteCommands struct {
	target func() string // the site's ID, or ""

	open, start, stop, restart, recycle, browse, explore *command
	deploy, purge, basic, bindings, env, rewrite, mime   *command
	console, remove                                      *command
	swap                                                 *command // deployment slots (pages_slots_windows.go)
}

func newSiteCommands(m *manager, target func() string) *siteCommands {
	c := &siteCommands{target: target}
	on := func(fn func(id string)) func() {
		return func() {
			if id := c.target(); id != "" {
				fn(id)
			}
		}
	}
	c.open = newCommand("Open", desktop.IconOpen, on(func(id string) { m.showSite(id) }))
	c.start = newCommand("Start", desktop.IconStart, on(func(id string) { m.siteAction(id, "start") }))
	c.stop = newCommand("Stop", desktop.IconStop, on(func(id string) { m.siteAction(id, "stop") }))
	c.restart = newCommand("Restart", desktop.IconRestart, on(func(id string) { m.siteAction(id, "restart") }))
	c.recycle = newCommand("Recycle (no downtime)", desktop.IconRecycle, on(func(id string) { m.siteAction(id, "recycle") }))
	c.browse = newCommand("Browse", desktop.IconBrowse, on(m.browseSite))
	c.explore = newCommand("Explore folder", desktop.IconFolder, on(m.exploreSite))
	c.deploy = newCommand("Deploy a .zip…", desktop.IconDeploy, on(m.deployZip))
	c.purge = newCommand("Purge response cache…", desktop.IconEraser, on(m.purgeCache))
	c.basic = newCommand("Basic settings…", desktop.IconSettings, on(func(id string) { basicSettingsDialog(m, id) }))
	c.bindings = newCommand("Bindings…", desktop.IconLink, on(func(id string) { bindingsDialog(m, id) }))
	c.env = newCommand("Environment…", desktop.IconBraces, on(func(id string) { envDialog(m, id) }))
	c.rewrite = newCommand("URL Rewrite…", desktop.IconRoute, on(func(id string) { rewriteDialog(m, id) }))
	c.mime = newCommand("MIME types…", desktop.IconFileCode, on(func(id string) { siteMimeDialog(m, id) }))
	c.console = newCommand("Open in web console", desktop.IconConsole, on(func(id string) { m.openConsolePath("/sites/" + url.PathEscape(id)) }))
	c.remove = newCommand("Remove…", desktop.IconRemove, on(m.removeSite))
	c.swap = newCommand("Swap slots…", desktop.IconToggle, on(m.swapSlot))
	return c
}

func (c *siteCommands) enable(m *manager) {
	st := m.siteByID(c.target())
	ok := st != nil && m.connected()
	running := ok && st.Status.State != model.StateStopped
	setEnabled(ok, c.open, c.basic, c.bindings, c.rewrite, c.mime, c.remove, c.purge)
	setEnabled(ok && !running, c.start)
	setEnabled(running, c.stop, c.restart)
	setEnabled(running && st.RunsNode(), c.recycle)
	setEnabled(ok && len(st.Bindings) > 0, c.browse)
	setEnabled(ok && sitePath(st.Site) != "", c.explore, c.deploy)
	setEnabled(ok && st.Node != nil, c.env)
	setEnabled(ok && len(st.Slots) > 0, c.swap)
	setEnabled(ok && m.info != nil && m.info.AdminURL != "" && m.info.AdminError == "", c.console)
}

// ---- the sites list

var siteFilters = []string{"All sites", "Running", "Stopped", "Needing attention"}

type sitesPage struct {
	page
	list  table
	cmds  *siteCommands
	find  *walk.LineEdit
	show  *walk.ComboBox
	count *walk.Label
	empty infoBar

	// Per row of the list, for its icons and filter.
	types     []model.SiteType
	states    []model.SiteState
	attention []bool
}

func (s *sitesPage) init(m *manager) *page {
	s.icon = desktop.IconSites
	s.title = func() string { return "Sites" }
	s.subtitle = func() string {
		running := 0
		for _, st := range m.sites {
			if st.Status.State == model.StateRunning {
				running++
			}
		}
		return fmt.Sprintf("%s · %d running", plural(len(m.sites), "site"), running)
	}
	s.update = func() { s.redraw(m) }
	s.search = func() *walk.LineEdit { return s.find }
	s.cmds = newSiteCommands(m, s.list.selected)
	s.list.onSelect = func() { s.cmds.enable(m) }
	s.list.color = func(row, col int) (walk.Color, bool) {
		if col != 1 || row >= len(s.states) {
			return 0, false
		}
		return stateColor(s.states[row])
	}
	s.list.icon = func(row, col int) walk.Image {
		if col != 0 || row >= len(s.states) {
			return nil
		}
		return asImage(siteIcon(string(s.types[row]), desktop.SiteLevel(s.states[row])))
	}
	s.list.match = func(row int) bool {
		if row >= len(s.states) {
			return true
		}
		switch s.filterIndex() {
		case 1:
			return s.states[row] == model.StateRunning
		case 2:
			return s.states[row] == model.StateStopped
		case 3:
			return s.attention[row]
		}
		return true
	}
	return &s.page
}

func (s *sitesPage) filterIndex() int {
	if s.show == nil {
		return 0
	}
	return s.show.CurrentIndex()
}

func (s *sitesPage) content(m *manager) []Widget {
	c := s.cmds
	return []Widget{
		s.empty.widget(),
		Composite{Layout: HBox{MarginsZero: true, Spacing: 8, Alignment: AlignHNearVCenter}, Children: []Widget{
			searchBox(&s.find, "Search sites, bindings, types", func(text string) { s.list.setSearch(text); s.updateCount() }),
			Label{Text: "Show:"},
			ComboBox{AssignTo: &s.show, Model: siteFilters, CurrentIndex: 0, OnCurrentIndexChanged: func() {
				s.list.refilter()
				s.updateCount()
			}},
			HSpacer{},
			Label{AssignTo: &s.count, TextColor: colorMuted},
		}},
		s.list.viewWith(tableOpts{
			name:       "sites",
			sortable:   true,
			onActivate: func() { m.showSite(s.list.selected()) },
			onDelete:   c.remove.trigger,
			menu: menu(c.open, nil, c.start, c.stop, c.restart, c.recycle, nil, c.browse, c.explore, c.deploy, c.swap, c.purge, nil,
				c.basic, c.bindings, c.env, c.rewrite, c.mime, nil, c.remove),
		},
			col("Name", 180), col("Status", 90), col("Type", 140), col("Bindings", 280),
			colR("Instances", 70), colR("CPU", 60), colR("Memory", 80), colR("Requests/s", 80)),
	}
}

func (s *sitesPage) actionsPane(m *manager) []Widget {
	c := s.cmds
	return pane(
		"Sites", m.cmdAddSite, m.cmdImport,
		"Selected site", c.open, c.start, c.stop, c.restart, c.recycle, c.browse, c.explore,
		"Deploy", c.deploy, c.swap, c.purge,
		"Edit", c.basic, c.bindings, c.env, c.rewrite, c.mime,
		"Remove", c.remove,
	)
}

func (s *sitesPage) redraw(m *manager) {
	keys := make([]string, len(m.sites))
	rows := make([][]string, len(m.sites))
	s.types = make([]model.SiteType, len(m.sites))
	s.states = make([]model.SiteState, len(m.sites))
	s.attention = make([]bool, len(m.sites))
	for i, st := range m.sites {
		cpu, mem := desktop.SiteUsage(st.Status)
		usage := [2]string{"–", "–"}
		if st.RunsNode() {
			usage = [2]string{fmt.Sprintf("%.0f%%", cpu), desktop.Bytes(mem)}
		}
		keys[i] = st.ID
		s.types[i] = st.Type
		s.states[i] = st.Status.State
		s.attention[i] = desktop.SiteNeedsAttention(desktop.SummaryOf(st))
		rows[i] = []string{
			st.Name, desktop.StateText(st.Status.State), desktop.SiteTypeText(st.Type), desktop.BindingsText(st.Bindings),
			desktop.InstancesText(st.Site, st.Status), usage[0], usage[1], fmt.Sprintf("%.1f", st.Status.Traffic.RPS),
		}
	}
	s.list.set(keys, rows)
	s.cmds.enable(m)
	s.updateCount()
	if m.connected() && len(m.sites) == 0 {
		s.empty.show(barInfo, "There are no sites yet. Add one, or import your sites from IIS, iisnode or PM2.", "Add site…", func() { addSiteDialog(m) })
	} else {
		s.empty.hide()
	}
}

func (s *sitesPage) updateCount() {
	if s.count == nil {
		return
	}
	shown, total := s.list.count()
	if shown == total {
		s.count.SetText(plural(total, "site"))
	} else {
		s.count.SetText(fmt.Sprintf("%d of %d sites", shown, total))
	}
}

// ---- actions on one site, shared by the list and the site page

func (m *manager) siteAction(id, action string) {
	name := id
	if s := m.siteByID(id); s != nil {
		name = s.Name
	}
	verb := map[string]string{"start": "Starting", "stop": "Stopping", "restart": "Restarting", "recycle": "Recycling"}[action]
	m.do(verb+" "+name, func(ctx context.Context) error { return m.cl.SiteAction(ctx, id, action) })
}

func (m *manager) browseSite(id string) {
	if s := m.siteByID(id); s != nil && len(s.Bindings) > 0 {
		shellOpen(desktop.BrowseURL(s.Bindings[0]))
	}
}

func (m *manager) exploreSite(id string) {
	if st := m.siteByID(id); st != nil {
		if p := sitePath(st.Site); p != "" {
			shellOpen(p)
		}
	}
}

func (m *manager) removeSite(id string) {
	s := m.siteByID(id)
	if s == nil {
		return
	}
	name := s.Name
	switch ask(m.mw, "Remove site", "Remove the site "+name+"?",
		"The site stops and its configuration is deleted. The application folder it runs from is never deleted.",
		walk.TaskDialogSystemIconWarning,
		[2]string{"Remove it and delete its deployed releases and logs", "Frees the space the site's releases and logs use in the data folder."},
		[2]string{"Remove it and keep its files", "Its releases and logs stay in the data folder."},
	) {
	case 0:
		m.do("Removing "+name, func(ctx context.Context) error {
			return m.cl.Delete(ctx, "/api/sites/"+url.PathEscape(id)+"?deleteFiles=true")
		})
	case 1:
		m.do("Removing "+name, func(ctx context.Context) error { return m.cl.Delete(ctx, "/api/sites/"+url.PathEscape(id)) })
	}
}

// deployZip uploads a .zip as a new release of the site, as the web
// console's "Deploy" does: it is extracted, built, and activated without
// downtime; the release it replaces stays for rollback.
func (m *manager) deployZip(id string) {
	s := m.siteByID(id)
	if s == nil {
		return
	}
	name := s.Name
	fd := walk.FileDialog{Title: "Deploy a .zip to " + name, Filter: "Zip archives (*.zip)|*.zip"}
	if ok, _ := fd.ShowOpen(m.mw); !ok {
		return
	}
	path := fd.FilePath
	if ask(m.mw, "Deploy", "Deploy "+filepath.Base(path)+" to "+name+"?",
		"The archive becomes a new release: it is extracted, the site's install and build commands run, and the site switches to it without downtime. "+
			"The current release stays available: activate it again in Deployments to roll back.",
		walk.TaskDialogSystemIconInformation, [2]string{"Deploy", ""}) != 0 {
		return
	}
	m.long("Deploying "+filepath.Base(path)+" to "+name, func(ctx context.Context) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		var dep model.Deployment
		return m.cl.UploadFile(ctx, "/api/sites/"+url.PathEscape(id)+"/deploy/zip", "file", filepath.Base(path), f, &dep)
	})
}

// purgeCache empties the site's response cache.
func (m *manager) purgeCache(id string) {
	s := m.siteByID(id)
	if s == nil {
		return
	}
	name := s.Name
	if ask(m.mw, "Purge response cache", "Purge the response cache of "+name+"?",
		"Cached responses are dropped; the next requests go to the application. Nothing happens if the site does not cache responses.",
		walk.TaskDialogSystemIconInformation, [2]string{"Purge", ""}) != 0 {
		return
	}
	m.do("Purging the cache of "+name, func(ctx context.Context) error {
		var out struct {
			Purged int `json:"purged"`
		}
		err := m.cl.Post(ctx, "/api/sites/"+url.PathEscape(id)+"/cache/purge", map[string]string{}, &out)
		if err == nil {
			m.mw.Synchronize(func() {
				notify(m.mw, "Purge response cache", "The cache of "+name+" is empty.", plural(out.Purged, "cached response")+" dropped.", "",
					walk.TaskDialogSystemIconInformation)
			})
		}
		return err
	})
}

// saveSite sends an edited copy of a site. Masked secrets are sent back
// masked; the server keeps their stored values.
func (m *manager) saveSite(site *model.Site, then func()) {
	m.do("Saving "+site.Name, func(ctx context.Context) error {
		_, err := m.cl.UpdateSite(ctx, site)
		if err == nil && then != nil {
			m.mw.Synchronize(then)
		}
		return err
	})
}

// ---- one site

type sitePage struct {
	page
	cmds                                           *siteCommands
	tabs                                           *walk.TabWidget
	bar                                            infoBar
	status, instCard, traffic, resources           statCard
	props, instances, bindings, env, deploy, tasks table
	taskViews                                      []model.TaskView
	deps                                           []model.Deployment
	logView                                        *walk.TextEdit
	logState                                       *walk.Label
	logSearch                                      *walk.LineEdit
	logs                                           *logFollower

	browse                                             [4]*command
	browseURL                                          [4]string
	viewLogs, viewDeploys                              *command
	editBindings, editEnv, browseBinding               *command
	deployCmd, activate, depLog, depRefresh            *command
	runTask, cancelTask, taskLogs, taskRefresh         *command
	pause, clearView, copyLog, saveLog, logFolder, clr *command
}

const (
	tabOverview = iota
	tabBindings
	tabEnvironment
	tabLogs
	tabDeployments
	tabTasks
)

func (s *sitePage) init(m *manager) *page {
	s.icon = desktop.IconSites
	s.tile = func() string {
		if st := m.siteByID(m.site); st != nil {
			return desktop.SiteTypeIcon(string(st.Type))
		}
		return desktop.IconSites
	}
	s.title = func() string {
		if st := m.siteByID(m.site); st != nil {
			return st.Name
		}
		return "Site"
	}
	s.subtitle = func() string {
		st := m.siteByID(m.site)
		if st == nil {
			return ""
		}
		parts := []string{desktop.SiteTypeText(st.Type)}
		if len(st.Bindings) > 0 {
			b := desktop.BindingText(st.Bindings[0])
			if n := len(st.Bindings) - 1; n > 0 {
				b += fmt.Sprintf(" (+%d)", n)
			}
			parts = append(parts, b)
		}
		if st.AutoStart {
			parts = append(parts, "starts with the service")
		}
		return strings.Join(parts, " · ")
	}
	s.update = func() { s.redraw(m) }
	s.load = func() { s.loadTab(m) }
	s.hide = func() { s.logs.stop() }
	s.page.search = func() *walk.LineEdit {
		if s.tabs != nil && s.tabs.CurrentIndex() == tabLogs {
			return s.logSearch
		}
		return nil
	}
	s.logs = &logFollower{m: m}
	s.cmds = newSiteCommands(m, func() string { return m.site })
	for i := range s.browse {
		i := i
		s.browse[i] = newCommand("Browse", desktop.IconBrowse, func() { shellOpen(s.browseURL[i]) })
	}
	s.viewLogs = newCommand("View logs", desktop.IconTerminal, func() { s.tabs.SetCurrentIndex(tabLogs) })
	s.viewDeploys = newCommand("Deployments", desktop.IconPackage, func() { s.tabs.SetCurrentIndex(tabDeployments) })
	s.editBindings = newCommand("Edit bindings…", desktop.IconEdit, func() { bindingsDialog(m, m.site) })
	s.browseBinding = newCommand("Browse", desktop.IconBrowse, func() {
		if st := m.siteByID(m.site); st != nil {
			if i := s.bindings.current(); i >= 0 && i < len(st.Bindings) {
				shellOpen(desktop.BrowseURL(st.Bindings[i]))
			}
		}
	})
	s.editEnv = newCommand("Edit environment…", desktop.IconEdit, func() { envDialog(m, m.site) })
	s.deployCmd = s.cmds.deploy
	s.activate = newCommand("Activate (roll back)…", desktop.IconHistory, func() { s.activateRelease(m) })
	s.depLog = newCommand("View log", desktop.IconLog, func() { s.showDeployLog(m) })
	s.depRefresh = newCommand("Refresh", desktop.IconRefresh, func() { s.loadTab(m) })
	s.runTask = newCommand("Run now", desktop.IconRun, func() { s.runSelectedTask(m) })
	s.cancelTask = newCommand("Cancel run", desktop.IconCancel, func() { s.cancelSelectedTask(m) })
	s.taskLogs = newCommand("Open run logs", desktop.IconFolder, func() {
		shellOpen(filepath.Join(m.dataDir(), "logs", "sites", m.site, "tasks"))
	})
	s.taskRefresh = newCommand("Refresh", desktop.IconRefresh, func() { s.loadTab(m) })
	s.pause = newCommand("Pause", desktop.IconPause, func() { s.togglePause() })
	s.clearView = newCommand("Clear view", desktop.IconEraser, func() { s.logs.clear() })
	s.copyLog = newCommand("Copy", desktop.IconCopy, func() { walk.Clipboard().SetText(s.logs.text()) })
	s.saveLog = newCommand("Save as…", desktop.IconSave, func() { s.logs.saveAs() })
	s.logFolder = newCommand("Open log folder", desktop.IconFolder, func() {
		shellOpen(filepath.Join(m.dataDir(), "logs", "sites", m.site))
	})
	s.clr = newCommand("Delete log files…", desktop.IconRemove, func() { s.clearLogFiles(m) })

	s.instances.color = func(row, col int) (walk.Color, bool) {
		if col != 3 || row >= len(s.instances.rows) {
			return 0, false
		}
		return levelColor(instanceLevel(s.instances.rows[row][3])), true
	}
	s.instances.icon = func(row, col int) walk.Image {
		if col != 0 || row >= len(s.instances.rows) {
			return nil
		}
		return asImage(dotIcon(instanceLevel(s.instances.rows[row][3])))
	}
	s.bindings.icon = func(row, col int) walk.Image {
		if col != 0 || row >= len(s.bindings.rows) {
			return nil
		}
		if s.bindings.rows[row][0] == "https" {
			return img(desktop.IconHTTPS)
		}
		return img(desktop.IconHTTP)
	}
	s.bindings.onSelect = func() { setEnabled(s.bindings.current() >= 0, s.browseBinding) }
	s.env.icon = func(row, col int) walk.Image {
		if col != 0 || row >= len(s.env.rows) {
			return nil
		}
		switch s.env.rows[row][2] {
		case desktop.EnvSecret:
			return img(desktop.IconLock)
		case desktop.EnvStore:
			return img(desktop.IconKey)
		}
		return img(desktop.IconBraces)
	}
	s.deploy.icon = func(row, col int) walk.Image {
		if row >= len(s.deps) {
			return nil
		}
		switch col {
		case 0:
			return img(runIcon(s.deps[row].Status))
		case 5:
			if s.deploy.rows[row][5] == "Yes" {
				return img(desktop.IconStar)
			}
		}
		return nil
	}
	s.deploy.onSelect = func() { s.enableTabCommands(m) }
	s.tasks.icon = func(row, col int) walk.Image {
		if row >= len(s.taskViews) {
			return nil
		}
		switch col {
		case 0:
			return img(desktop.IconTasks)
		case 4:
			if r := s.taskViews[row].LastRun; r != nil {
				return img(runIcon(r.Status))
			}
		}
		return nil
	}
	s.tasks.onSelect = func() { s.enableTabCommands(m) }
	return &s.page
}

// instanceLevel is the color of a Node.js instance's state.
func instanceLevel(state string) desktop.Level {
	switch state {
	case "ready":
		return desktop.LevelOK
	case "starting", "stopping":
		return desktop.LevelWarning
	}
	return desktop.LevelDown
}

// runIcon is the icon of a deployment's, backup's or task run's result.
func runIcon(status string) string {
	switch status {
	case "succeeded", model.BackupSuccess:
		return desktop.IconOK
	case "running":
		return desktop.IconClock
	case "partial", "skipped", "cancelled", "timeout":
		return desktop.IconWarning
	case "failed":
		return desktop.IconError
	}
	return desktop.IconDot
}

func buttons(cmds ...*command) Composite {
	w := []Widget{HSpacer{}}
	for _, c := range cmds {
		c := c
		v := &commandView{}
		c.views = append(c.views, v)
		w = append(w, PushButton{Text: c.text, Image: c.currentIcon(), Enabled: c.enabled, OnClicked: c.trigger,
			AssignTo: &v.button})
	}
	return Composite{Layout: HBox{MarginsZero: true}, Children: w}
}

func (s *sitePage) content(m *manager) []Widget {
	return []Widget{
		s.bar.widget(),
		TabWidget{
			AssignTo:              &s.tabs,
			OnCurrentIndexChanged: func() { s.loadTab(m); m.updateCommands() },
			Pages: []TabPage{
				{Title: "Overview", Image: img(desktop.IconActivity), Layout: VBox{Spacing: 8}, Children: []Widget{
					cards(s.status.widget(desktop.IconPower, "Status", false), s.instCard.widget(desktop.IconCPU, "Instances", false),
						s.traffic.widget(desktop.IconActivity, "Requests", false), s.resources.widget(desktop.IconMemory, "Memory", false)),
					properties(&s.props, "siteDetails"),
					heading("Instances"),
					s.instances.viewWith(tableOpts{name: "siteInstances"}, col("#", 50), colR("PID", 70), colR("Port", 60), col("State", 80),
						colR("Restarts", 70), colR("CPU", 60), colR("Memory", 80), col("Heap", 120), colR("Event loop lag", 100),
						colR("Requests", 80), col("Started", 140)),
				}},
				{Title: "Bindings", Image: img(desktop.IconLink), Layout: VBox{}, Children: []Widget{
					s.bindings.viewWith(tableOpts{name: "siteBindings", onActivate: s.editBindings.trigger,
						menu: menu(s.browseBinding, s.editBindings)},
						col("Type", 70), col("IP address", 120), colR("Port", 60), col("Host name", 240), col("Certificate", 220)),
					buttons(s.browseBinding, s.editBindings),
				}},
				{Title: "Environment", Image: img(desktop.IconBraces), Layout: VBox{}, Children: []Widget{
					s.env.viewWith(tableOpts{name: "siteEnv", onActivate: s.editEnv.trigger, menu: menu(s.editEnv)},
						col("Name", 240), col("Value", 380), col("Source", 80)),
					hint("Changes apply with a zero-downtime recycle. Secret values are encrypted at rest and never shown again."),
					buttons(s.editEnv),
				}},
				{Title: "Logs", Image: img(desktop.IconTerminal), Layout: VBox{}, Children: []Widget{
					Composite{Layout: HBox{MarginsZero: true, Spacing: 8, Alignment: AlignHNearVCenter}, Children: []Widget{
						Label{AssignTo: &s.logState, Font: fontHeading},
						searchBox(&s.logSearch, "Show only lines containing…", func(text string) { s.logs.setFilter(text) }),
						HSpacer{},
					}},
					TextEdit{AssignTo: &s.logView, ReadOnly: true, VScroll: true, HScroll: true, Font: fontMono},
					buttons(s.pause, s.clearView, s.copyLog, s.saveLog, s.logFolder, s.clr),
				}},
				{Title: "Deployments", Image: img(desktop.IconPackage), Layout: VBox{}, Children: []Widget{
					s.deploy.viewWith(tableOpts{name: "siteDeployments", onActivate: s.depLog.trigger,
						menu: menu(s.activate, s.depLog, nil, s.deployCmd, s.depRefresh)},
						col("Started", 150), col("Source", 80), col("Status", 90), col("Commit / message", 300), col("By", 120), col("Active", 60)),
					hint("Each deployment is a release kept side by side. Activating an older one rolls the site back without downtime. Deploy from git in the web console."),
					buttons(s.deployCmd, s.activate, s.depLog, s.depRefresh),
				}},
				{Title: "Tasks", Image: img(desktop.IconTasks), Layout: VBox{}, Children: []Widget{
					s.tasks.viewWith(tableOpts{name: "siteTasks", onActivate: s.runTask.trigger,
						menu: menu(s.runTask, s.cancelTask, nil, s.taskLogs, s.taskRefresh)},
						col("Task", 170), col("Schedule", 140), col("Command", 160), col("Next run", 140), col("Last result", 300)),
					hint("Scheduled tasks are defined in the web console (site → Tasks)."),
					buttons(s.runTask, s.cancelTask, s.taskLogs, s.taskRefresh),
				}},
			},
		},
	}
}

func (s *sitePage) actionsPane(m *manager) []Widget {
	c := s.cmds
	return pane(
		"Manage site", c.start, c.stop, c.restart, c.recycle,
		"Browse site", s.browse[:],
		"Deploy", c.deploy, s.viewDeploys, c.swap, c.purge,
		"Edit site", c.basic, c.bindings, c.env, c.rewrite, c.mime,
		"Tools", c.explore, s.viewLogs, c.console,
		"Site", c.remove,
	)
}

func (s *sitePage) redraw(m *manager) {
	st := m.siteByID(m.site)
	s.cmds.enable(m)
	s.enableTabCommands(m)
	if st == nil {
		s.props.setProperties(p(desktop.IconInfo, "Site", "not found"))
		s.instances.set(nil, nil)
		s.bindings.set(nil, nil)
		s.env.set(nil, nil)
		s.bar.hide()
		return
	}
	lvl := desktop.SiteLevel(st.Status.State)

	msg := st.Status.Message
	switch st.Status.State {
	case model.StateFailed:
		s.bar.show(barError, "NodeHoster stopped restarting this site after repeated crashes (rapid-fail protection). "+msg,
			"Start the site", s.cmds.start.trigger)
	case model.StateDegraded:
		s.bar.show(barWarning, strings.TrimSpace("Some instances are down or unhealthy. "+msg), "Recycle", s.cmds.recycle.trigger)
	case model.StateStopped:
		s.bar.show(barInfo, strings.TrimSpace("The site is stopped. "+msg), "Start the site", s.cmds.start.trigger)
	default:
		if msg != "" {
			s.bar.show(barInfo, msg, "", nil)
		} else {
			s.bar.hide()
		}
	}

	for i := range s.browse {
		if i < len(st.Bindings) {
			s.browseURL[i] = desktop.BrowseURL(st.Bindings[i])
			s.browse[i].setText("Browse " + desktop.BindingText(st.Bindings[i]))
		}
		s.browse[i].setVisible(i < len(st.Bindings))
	}

	// Cards
	s.status.set(desktop.StateText(st.Status.State), "", levelColor(lvl))
	if st.RunsNode() && st.Node != nil {
		ready := 0
		for _, in := range st.Status.Instances {
			if in.State == "ready" {
				ready++
			}
		}
		instColor := colorOK
		if ready < st.Node.Instances {
			instColor = colorWarning
		}
		if ready == 0 {
			instColor = colorError
		}
		if st.Status.State == model.StateStopped {
			instColor = 0
		}
		s.instCard.set(fmt.Sprintf("%d of %d ready", ready, st.Node.Instances), "restart policy: "+st.Node.RestartPolicy, instColor)
		cpu, mem := desktop.SiteUsage(st.Status)
		s.resources.set(desktop.Bytes(mem), fmt.Sprintf("CPU %.0f%%", cpu), 0)
	} else {
		s.instCard.set("–", "no Node.js process", 0)
		s.resources.set("–", "", 0)
	}
	t := st.Status.Traffic
	trafficColor := walk.Color(0)
	if t.Status5xx > 0 {
		trafficColor = colorWarning
	}
	s.traffic.set(fmt.Sprintf("%.1f/s", t.RPS), fmt.Sprintf("%d total · %d errors (5xx) · %.0f ms", t.Requests, t.Status5xx, t.AvgLatencyMs), trafficColor)

	props := []prop{p(desktop.IconPower, "Status", desktop.StateText(st.Status.State))}
	if msg != "" {
		props = append(props, p(desktop.IconInfo, "Message", msg))
	}
	props = append(props,
		p(desktop.SiteTypeIcon(string(st.Type)), "Type", desktop.SiteTypeText(st.Type)),
		p(desktop.IconStart, "Start automatically", yesNo(st.AutoStart)),
		p(desktop.IconLink, "Bindings", desktop.BindingsText(st.Bindings)),
	)
	switch {
	case st.Node != nil:
		entry := st.Node.Script
		if st.Node.NpmScript != "" {
			entry = "npm run " + st.Node.NpmScript
		}
		version := st.Node.NodeVersion
		if version == "" {
			version = "server default"
		}
		props = append(props,
			p(desktop.IconFolder, "Application folder", st.Node.AppRoot),
			p(desktop.IconTerminal, "Entry point", entry),
			p(desktop.IconNode, "Node.js version", version),
			p(desktop.IconCPU, "Instances", strconv.Itoa(st.Node.Instances)),
			p(desktop.IconRestart, "Restart policy", st.Node.RestartPolicy),
		)
	case st.Static != nil:
		props = append(props, p(desktop.IconFolder, "Folder", st.Static.Root))
	case st.Proxy != nil:
		var ups []string
		for _, u := range st.Proxy.Upstreams {
			ups = append(ups, u.URL)
		}
		props = append(props, p(desktop.IconProxy, "Upstreams", strings.Join(ups, ", ")), p(desktop.IconRoute, "Load balancing", st.Proxy.LoadBalancing))
	case st.Redirect != nil:
		props = append(props, p(desktop.IconRedirect, "Redirect to", fmt.Sprintf("%s (%d)", st.Redirect.TargetURL, st.Redirect.StatusCode)))
	}
	if st.ActiveRelease != "" {
		props = append(props, p(desktop.IconPackage, "Active release", st.ActiveRelease))
	}
	props = append(props,
		p(desktop.IconActivity, "Requests", fmt.Sprintf("%d (%.1f/s), %d errors (5xx), %.0f ms average", t.Requests, t.RPS, t.Status5xx, t.AvgLatencyMs)),
		p(desktop.IconClock, "Created", st.CreatedAt.Local().Format("2006-01-02 15:04")),
	)
	s.props.setProperties(props...)

	var keys []string
	var rows [][]string
	for _, in := range st.Status.Instances {
		started := "–"
		if in.StartedAt != nil {
			started = in.StartedAt.Local().Format("2006-01-02 15:04:05")
		}
		heap, lag := "–", "–"
		if in.HeapTotalBytes > 0 {
			heap = desktop.Bytes(in.HeapUsedBytes) + " / " + desktop.Bytes(in.HeapTotalBytes)
			lag = fmt.Sprintf("%.1f ms", in.EventLoopLagMs)
		}
		keys = append(keys, strconv.Itoa(in.Index))
		rows = append(rows, []string{strconv.Itoa(in.Index), strconv.Itoa(in.PID), strconv.Itoa(in.Port), in.State, strconv.Itoa(in.Restarts),
			fmt.Sprintf("%.0f%%", in.CPUPercent), desktop.Bytes(in.MemoryBytes), heap, lag, strconv.FormatInt(in.Requests, 10), started})
	}
	s.instances.set(keys, rows)

	keys, rows = nil, nil
	for _, b := range st.Bindings {
		cert := ""
		if b.Protocol == "https" {
			cert = "Automatic (ACME)"
			if b.CertMode == model.CertModeManual {
				cert = b.CertificateID
			}
		}
		keys = append(keys, b.ID)
		rows = append(rows, []string{b.Protocol, orStar(b.IP), strconv.Itoa(b.Port), b.Host, cert})
	}
	s.bindings.set(keys, rows)

	keys, rows = nil, nil
	if st.Node != nil {
		for _, e := range st.Node.Env {
			v, source := desktop.EnvText(e)
			keys = append(keys, e.Name)
			rows = append(rows, []string{e.Name, v, source})
		}
	}
	s.env.set(keys, rows)

	// A different site than the logs follow: start over.
	if s.tabs.CurrentIndex() == tabLogs && s.logs.site != m.site {
		s.logs.follow(m.site, s.logView, s.logState)
	}
}

// enableTabCommands enables the buttons of the tabs.
func (s *sitePage) enableTabCommands(m *manager) {
	st := m.siteByID(m.site)
	ok := st != nil && m.connected()
	setEnabled(ok, s.editBindings, s.depRefresh, s.taskRefresh, s.clr)
	setEnabled(ok && st.Node != nil, s.editEnv)
	setEnabled(ok && s.bindings.current() >= 0, s.browseBinding)
	dep := s.deploy.current()
	setEnabled(ok && dep >= 0, s.depLog)
	setEnabled(ok && dep >= 0 && dep < len(s.deps) && s.deps[dep].ID != st.ActiveRelease && s.deps[dep].Status == "succeeded", s.activate)
	task := s.tasks.current()
	setEnabled(ok && task >= 0, s.runTask)
	setEnabled(ok && task >= 0 && task < len(s.taskViews) && len(s.taskViews[task].Running) > 0, s.cancelTask)
}

// loadTab fetches what the current tab needs beyond the site itself.
func (s *sitePage) loadTab(m *manager) {
	switch s.tabs.CurrentIndex() {
	case tabLogs:
		s.logs.follow(m.site, s.logView, s.logState)
		return
	case tabDeployments:
		id := m.site
		go func() {
			var list []model.Deployment
			err := m.cl.Get(context.Background(), "/api/sites/"+url.PathEscape(id)+"/deployments", &list)
			m.mw.Synchronize(func() {
				if err != nil || id != m.site {
					if err != nil {
						m.flashStatus("Could not load the deployments: "+err.Error(), true)
					}
					return
				}
				st := m.siteByID(id)
				s.deps = list
				var keys []string
				var rows [][]string
				for _, d := range list {
					what := d.Commit
					if d.Message != "" {
						what = strings.TrimSpace(what + " " + d.Message)
					}
					keys = append(keys, d.ID)
					rows = append(rows, []string{d.StartedAt.Local().Format("2006-01-02 15:04:05"), d.Source, d.Status, what, d.User,
						yesNo(st != nil && st.ActiveRelease == d.ID)})
				}
				s.deploy.set(keys, rows)
				s.enableTabCommands(m)
			})
		}()
	case tabTasks:
		id := m.site
		go func() {
			var list []model.TaskView
			err := m.cl.Get(context.Background(), "/api/sites/"+url.PathEscape(id)+"/tasks", &list)
			m.mw.Synchronize(func() {
				if err != nil || id != m.site {
					if err != nil {
						m.flashStatus("Could not load the tasks: "+err.Error(), true)
					}
					return
				}
				s.taskViews = list
				now := time.Now()
				var keys []string
				var rows [][]string
				for _, t := range list {
					next := "–"
					if t.NextRunAt != nil {
						next = t.NextRunAt.Local().Format("2006-01-02 15:04")
					}
					how := "node " + t.Script
					if t.NpmScript != "" {
						how = "npm run " + t.NpmScript
					}
					last := desktop.TaskRunText(t.LastRun, now)
					if t.Queued {
						last += "; another run is queued"
					}
					keys = append(keys, t.ID)
					rows = append(rows, []string{t.Name, desktop.ScheduleText(t.ScheduledTask), how, next, last})
				}
				s.tasks.set(keys, rows)
				s.enableTabCommands(m)
			})
		}()
	}
	s.logs.stop()
}

// selectedTask is the task selected in the Tasks tab.
func (s *sitePage) selectedTask() *model.TaskView {
	if i := s.tasks.current(); i >= 0 && i < len(s.taskViews) {
		return &s.taskViews[i]
	}
	return nil
}

// runSelectedTask starts the selected scheduled task now, as "Run" in
// Task Scheduler does, and shows the result in the list once it finishes.
func (s *sitePage) runSelectedTask(m *manager) {
	t := s.selectedTask()
	if t == nil {
		return
	}
	taskID, name, id := t.ID, t.Name, m.site
	m.do("Starting task "+name, func(ctx context.Context) error {
		err := m.cl.Post(ctx, "/api/sites/"+url.PathEscape(id)+"/tasks/"+url.PathEscape(taskID)+"/run", nil, nil)
		if err == nil {
			s.reloadTasksSoon(m, id)
		}
		return err
	})
}

// cancelSelectedTask cancels the runs in progress of the selected task.
func (s *sitePage) cancelSelectedTask(m *manager) {
	t := s.selectedTask()
	if t == nil || len(t.Running) == 0 {
		return
	}
	runs, name, id := t.Running, t.Name, m.site
	if ask(m.mw, "Cancel run", "Cancel the running "+name+"?", "Its process tree is killed; the run is recorded as cancelled.",
		walk.TaskDialogSystemIconWarning, [2]string{"Cancel the run", ""}) != 0 {
		return
	}
	m.do("Cancelling "+name, func(ctx context.Context) error {
		for _, run := range runs {
			if err := m.cl.Post(ctx, "/api/sites/"+url.PathEscape(id)+"/runs/"+url.PathEscape(run)+"/cancel", nil, nil); err != nil {
				return err
			}
		}
		s.reloadTasksSoon(m, id)
		return nil
	})
}

// reloadTasksSoon refreshes the tasks now (running) and again shortly (a
// quick task's result).
func (s *sitePage) reloadTasksSoon(m *manager, id string) {
	for _, d := range []time.Duration{0, 3 * time.Second} {
		time.AfterFunc(d, func() {
			m.mw.Synchronize(func() {
				if m.site == id && s.tabs.CurrentIndex() == tabTasks {
					s.loadTab(m)
				}
			})
		})
	}
}

func (s *sitePage) selectedDeployment() *model.Deployment {
	if i := s.deploy.current(); i >= 0 && i < len(s.deps) {
		return &s.deps[i]
	}
	return nil
}

func (s *sitePage) activateRelease(m *manager) {
	d := s.selectedDeployment()
	if d == nil {
		return
	}
	dep, id := d.ID, m.site
	if ask(m.mw, "Activate release", "Switch the site to the release of "+d.StartedAt.Local().Format("2006-01-02 15:04")+"?",
		"Its instances are recycled without downtime. The release in use now stays available.",
		walk.TaskDialogSystemIconInformation, [2]string{"Activate", ""}) != 0 {
		return
	}
	m.do("Activating the release", func(ctx context.Context) error {
		return m.cl.Post(ctx, "/api/sites/"+url.PathEscape(id)+"/deployments/"+url.PathEscape(dep)+"/activate", nil, nil)
	})
}

// showDeployLog shows the output of the selected deployment.
func (s *sitePage) showDeployLog(m *manager) {
	d := s.selectedDeployment()
	if d == nil {
		return
	}
	dep, id, when := d.ID, m.site, d.StartedAt.Local().Format("2006-01-02 15:04:05")
	go func() {
		var buf strings.Builder
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, _, err := m.cl.Download(ctx, "/api/sites/"+url.PathEscape(id)+"/deployments/"+url.PathEscape(dep)+"/log", &buf)
		cancel()
		m.mw.Synchronize(func() {
			if err != nil {
				m.errorBox("Deployment log", err)
				return
			}
			textDialog(m.mw, "Deployment of "+when, desktop.IconPackage, buf.String())
		})
	}()
}

func (s *sitePage) togglePause() {
	s.logs.setPaused(!s.logs.paused)
	if s.logs.paused {
		s.pause.setText("Resume")
		s.pause.setIcon(desktop.IconStart)
	} else {
		s.pause.setText("Pause")
		s.pause.setIcon(desktop.IconPause)
	}
}

func (s *sitePage) clearLogFiles(m *manager) {
	st := m.siteByID(m.site)
	if st == nil {
		return
	}
	id, name := st.ID, st.Name
	if ask(m.mw, "Delete log files", "Delete the log files of "+name+"?",
		"The output the site's processes wrote so far is deleted from the data folder. The site keeps running and logging.",
		walk.TaskDialogSystemIconWarning, [2]string{"Delete the log files", ""}) != 0 {
		return
	}
	m.do("Deleting the logs of "+name, func(ctx context.Context) error {
		err := m.cl.Post(ctx, "/api/sites/"+url.PathEscape(id)+"/logs/clear", nil, nil)
		if err == nil {
			m.mw.Synchronize(s.logs.clear)
		}
		return err
	})
}

// sitePath is the folder a site serves from, if it has one.
func sitePath(s *model.Site) string {
	switch {
	case s.Node != nil:
		return s.Node.AppRoot
	case s.Static != nil:
		return s.Static.Root
	}
	return ""
}
