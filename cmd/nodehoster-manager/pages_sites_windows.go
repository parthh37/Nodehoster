package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- server home

type serverPage struct {
	page
	props                                                  table
	banner                                                 *walk.Label
	start, stop, restart, console, consoleSettings, backup *walk.LinkLabel
	mime, mail                                             *walk.LinkLabel
}

func (s *serverPage) init(m *manager) *page {
	s.title = func() string {
		if m.info != nil && m.info.Hostname != "" {
			return m.info.Hostname
		}
		h, _ := os.Hostname()
		return h
	}
	s.update = func() { s.redraw(m) }
	return &s.page
}

func (s *serverPage) content(m *manager) []Widget {
	return []Widget{
		Label{AssignTo: &s.banner, Visible: false, TextColor: colorError, Font: Font{Bold: true}},
		properties(&s.props),
	}
}

func (s *serverPage) actionsPane(m *manager) []Widget {
	return []Widget{
		heading("Service"),
		link(&s.start, "Start", func() { m.serviceAction("start") }),
		link(&s.stop, "Stop", func() { m.serviceAction("stop") }),
		link(&s.restart, "Restart", func() { m.serviceAction("restart") }),
		heading("Web console"),
		link(&s.console, "Open in browser", m.openConsole),
		link(&s.consoleSettings, "Settings…", func() { adminConsoleDialog(m) }),
		heading("Configuration"),
		link(&s.mime, "MIME types…", func() { serverMimeDialog(m) }),
		link(&s.mail, "SMTP E-mail…", func() { mailPropertiesDialog(m) }),
		link(&s.backup, "Back up…", m.backupConfig),
		link(nil, "Open data folder", func() { shellOpen(m.dataDir()) }),
		link(nil, "Open server log", func() { shellOpen(filepath.Join(m.dataDir(), "logs", "nodehoster.log")) }),
	}
}

func (s *serverPage) redraw(m *manager) {
	setEnabled(m.service == "stopped", s.start)
	setEnabled(m.service == "running", s.stop, s.restart)
	setEnabled(m.connected(), s.consoleSettings, s.backup, s.mime, s.mail)
	setEnabled(m.connected() && m.info != nil && m.info.AdminURL != "", s.console)

	banner := ""
	switch {
	case m.service == "not installed":
		banner = "The NodeHoster service is not installed. Run the installer, or from an elevated prompt: nodehoster service install"
	case m.service != "running" && m.service != "unknown":
		banner = "The NodeHoster service is " + m.service + ". The sites it hosts are offline."
	case errors.Is(m.connErr, localapi.ErrNotRunning):
		banner = "The service is running but does not answer on the local admin pipe yet."
	case m.connErr != nil:
		banner = "Cannot connect to the service: " + m.connErr.Error()
	case m.info != nil && m.info.AdminError != "":
		banner = "Web console unavailable: " + m.info.AdminError + ". Change it with Web console → Settings, then restart the service."
	}
	s.banner.SetText(banner)
	s.banner.SetVisible(banner != "")

	pairs := [][2]string{{"Service", m.service}}
	if i := m.info; i != nil && m.connected() {
		console := desktop.ConsoleURL(i.AdminURL)
		if i.AdminError != "" {
			console = "unavailable: " + i.AdminError
		}
		running := 0
		for _, st := range m.sites {
			if st.Status.State == model.StateRunning {
				running++
			}
		}
		pairs = append(pairs,
			[2]string{"Version", i.Version + " (" + shortCommit(i.Commit) + ")"},
			[2]string{"Host name", i.Hostname},
			[2]string{"Operating system", i.OS},
			[2]string{"Running for", desktop.Uptime(i.StartedAt, time.Now())},
			[2]string{"CPU", fmt.Sprintf("%.0f%% of %d cores", i.CPUPercent, i.CPUCount)},
			[2]string{"Memory", desktop.Bytes(i.MemUsed) + " used of " + desktop.Bytes(i.MemTotal)},
			[2]string{"Disk free", desktop.Bytes(i.DiskFree) + " of " + desktop.Bytes(i.DiskTotal)},
			[2]string{"Data folder", i.DataDir},
			[2]string{"Listening on", strings.Join(i.Listeners, ", ")},
			[2]string{"Web console", console},
			[2]string{"Sites", fmt.Sprintf("%d running of %d", running, len(m.sites))},
		)
	}
	s.props.setProperties(pairs...)
}

func shortCommit(c string) string {
	if len(c) > 8 {
		return c[:8]
	}
	return c
}

func (m *manager) backupConfig() {
	dlg := walk.FileDialog{
		Title:    "Back up the NodeHoster configuration",
		Filter:   "Backup (*.json)|*.json",
		FilePath: "nodehoster-backup-" + time.Now().Format("20060102-150405") + ".json",
	}
	if ok, _ := dlg.ShowSave(m.mw); !ok {
		return
	}
	path := dlg.FilePath
	if filepath.Ext(path) == "" {
		path += ".json"
	}
	m.do("Backing up the configuration", func(ctx context.Context) error {
		var raw json.RawMessage
		if err := m.cl.Get(ctx, "/api/backup", &raw); err != nil {
			return err
		}
		return os.WriteFile(path, raw, 0o600)
	})
}

// ---- the sites list

type sitesPage struct {
	page
	list                                                table
	open, start, stop, restart, recycle, browse, remove *walk.LinkLabel
}

func (s *sitesPage) init(m *manager) *page {
	s.title = func() string { return "Sites" }
	s.update = func() { s.redraw(m) }
	s.list.onSelect = func() { s.enable(m) }
	s.list.color = func(row, col int) (walk.Color, bool) {
		if col != 1 || row >= len(m.sites) {
			return 0, false
		}
		return stateColor(m.sites[row].Status.State)
	}
	return &s.page
}

func (s *sitesPage) content(m *manager) []Widget {
	return []Widget{
		s.list.view(func() { m.showSite(s.list.selected()) },
			col("Name", 170), col("Status", 90), col("Type", 80), col("Bindings", 300),
			col("Instances", 70), col("CPU", 60), col("Memory", 80), col("Requests/s", 80)),
	}
}

func (s *sitesPage) actionsPane(m *manager) []Widget {
	sel := func(fn func(id string)) func() {
		return func() {
			if id := s.list.selected(); id != "" {
				fn(id)
			}
		}
	}
	return []Widget{
		heading("Sites"),
		link(nil, "Add site…", func() { addSiteDialog(m) }),
		link(nil, "Import from IIS…", func() { importIISDialog(m) }),
		heading("Selected site"),
		link(&s.open, "Open", sel(func(id string) { m.showSite(id) })),
		link(&s.start, "Start", sel(func(id string) { m.siteAction(id, "start") })),
		link(&s.stop, "Stop", sel(func(id string) { m.siteAction(id, "stop") })),
		link(&s.restart, "Restart", sel(func(id string) { m.siteAction(id, "restart") })),
		link(&s.recycle, "Recycle", sel(func(id string) { m.siteAction(id, "recycle") })),
		link(&s.browse, "Browse", sel(m.browseSite)),
		link(&s.remove, "Remove…", sel(m.removeSite)),
	}
}

func (s *sitesPage) redraw(m *manager) {
	keys := make([]string, len(m.sites))
	rows := make([][]string, len(m.sites))
	for i, st := range m.sites {
		cpu, mem := desktop.SiteUsage(st.Status)
		usage := [2]string{"–", "–"}
		if st.RunsNode() {
			usage = [2]string{fmt.Sprintf("%.0f%%", cpu), desktop.Bytes(mem)}
		}
		keys[i] = st.ID
		rows[i] = []string{
			st.Name, desktop.StateText(st.Status.State), string(st.Type), desktop.BindingsText(st.Bindings),
			desktop.InstancesText(st.Site, st.Status), usage[0], usage[1], fmt.Sprintf("%.1f", st.Status.Traffic.RPS),
		}
	}
	s.list.set(keys, rows)
	s.enable(m)
}

func (s *sitesPage) enable(m *manager) {
	st := m.siteByID(s.list.selected())
	setEnabled(st != nil, s.open, s.remove)
	running := st != nil && st.Status.State != model.StateStopped
	setEnabled(st != nil && !running, s.start)
	setEnabled(running, s.stop, s.restart)
	setEnabled(running && st.RunsNode(), s.recycle)
	setEnabled(st != nil && len(st.Bindings) > 0, s.browse)
}

func stateColor(s model.SiteState) (walk.Color, bool) {
	switch desktop.SiteLevel(s) {
	case desktop.LevelOK:
		return colorOK, true
	case desktop.LevelWarning:
		return colorWarning, true
	case desktop.LevelDown:
		return colorError, true
	}
	return colorMuted, true
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

func (m *manager) removeSite(id string) {
	s := m.siteByID(id)
	if s == nil {
		return
	}
	switch walk.MsgBox(m.mw, "Remove site",
		fmt.Sprintf("Remove the site %q?\n\nYes: remove it and delete its deployed releases and logs.\nNo: remove it and keep its files.", s.Name),
		walk.MsgBoxYesNoCancel|walk.MsgBoxIconWarning) {
	case walk.DlgCmdYes:
		m.do("Removing "+s.Name, func(ctx context.Context) error {
			return m.cl.Delete(ctx, "/api/sites/"+url.PathEscape(id)+"?deleteFiles=true")
		})
	case walk.DlgCmdNo:
		m.do("Removing "+s.Name, func(ctx context.Context) error { return m.cl.Delete(ctx, "/api/sites/"+url.PathEscape(id)) })
	}
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
	tabs                                           *walk.TabWidget
	props, instances, bindings, env, deploy, tasks table
	taskViews                                      []model.TaskView
	logView                                        *walk.TextEdit
	logState                                       *walk.Label
	logs                                           *logFollower

	start, stop, restart, recycle *walk.LinkLabel
	browse                        [4]*walk.LinkLabel
	browseURL                     [4]string
	explore, remove, activate     *walk.LinkLabel
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
	s.title = func() string {
		if st := m.siteByID(m.site); st != nil {
			return st.Name
		}
		return "Site"
	}
	s.update = func() { s.redraw(m) }
	s.load = func() { s.loadTab(m) }
	s.hide = func() { s.logs.stop() }
	s.logs = &logFollower{m: m}
	s.instances.color = func(row, col int) (walk.Color, bool) {
		if col != 3 {
			return 0, false
		}
		switch s.instances.Value(row, col) {
		case "ready":
			return colorOK, true
		case "starting", "stopping":
			return colorWarning, true
		}
		return colorError, true
	}
	return &s.page
}

func (s *sitePage) content(m *manager) []Widget {
	editBindings := func() { bindingsDialog(m, m.site) }
	editEnv := func() { envDialog(m, m.site) }
	return []Widget{
		TabWidget{
			AssignTo:              &s.tabs,
			OnCurrentIndexChanged: func() { s.loadTab(m) },
			Pages: []TabPage{
				{Title: "Overview", Layout: VBox{}, Children: []Widget{
					properties(&s.props),
					Label{Text: "Instances", Font: Font{Bold: true}},
					s.instances.view(nil, col("#", 40), col("PID", 70), col("Port", 60), col("State", 80), col("Restarts", 70),
						col("CPU", 60), col("Memory", 80), col("Heap", 80), col("Event loop lag", 100), col("Requests", 80), col("Started", 140)),
				}},
				{Title: "Bindings", Layout: VBox{}, Children: []Widget{
					s.bindings.view(editBindings, col("Type", 60), col("IP address", 120), col("Port", 60), col("Host name", 220), col("Certificate", 200)),
					Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{HSpacer{}, PushButton{Text: "Edit bindings…", OnClicked: editBindings}}},
				}},
				{Title: "Environment", Layout: VBox{}, Children: []Widget{
					s.env.view(editEnv, col("Name", 220), col("Value", 360), col("Secret", 60)),
					Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{HSpacer{}, PushButton{Text: "Edit environment…", OnClicked: editEnv}}},
				}},
				{Title: "Logs", Layout: VBox{}, Children: []Widget{
					Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
						Label{AssignTo: &s.logState},
						HSpacer{},
						PushButton{Text: "Clear view", OnClicked: func() { s.logs.clear() }},
						PushButton{Text: "Open log folder", OnClicked: func() {
							shellOpen(filepath.Join(m.dataDir(), "logs", "sites", m.site))
						}},
					}},
					TextEdit{AssignTo: &s.logView, ReadOnly: true, VScroll: true, HScroll: true, Font: Font{Family: "Consolas", PointSize: 9}},
				}},
				{Title: "Deployments", Layout: VBox{}, Children: []Widget{
					s.deploy.view(nil, col("Started", 140), col("Source", 70), col("Status", 80), col("Commit / message", 300), col("By", 120), col("Active", 60)),
					Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
						HSpacer{},
						PushButton{Text: "Activate selected release", OnClicked: func() { s.activateRelease(m) }},
					}},
				}},
				{Title: "Tasks", Layout: VBox{}, Children: []Widget{
					s.tasks.view(func() { s.runTask(m) }, col("Task", 160), col("Schedule", 130), col("Command", 150),
						col("Next run", 130), col("Last result", 300)),
					Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
						Label{Text: "Tasks are defined in the web console (site → Tasks).", TextColor: colorMuted},
						HSpacer{},
						PushButton{Text: "Refresh", OnClicked: func() { s.loadTab(m) }},
						PushButton{Text: "Open run logs", OnClicked: func() {
							shellOpen(filepath.Join(m.dataDir(), "logs", "sites", m.site, "tasks"))
						}},
						PushButton{Text: "Run now", OnClicked: func() { s.runTask(m) }},
					}},
				}},
			},
		},
	}
}

func (s *sitePage) actionsPane(m *manager) []Widget {
	w := []Widget{
		heading("Manage site"),
		link(&s.start, "Start", func() { m.siteAction(m.site, "start") }),
		link(&s.stop, "Stop", func() { m.siteAction(m.site, "stop") }),
		link(&s.restart, "Restart", func() { m.siteAction(m.site, "restart") }),
		link(&s.recycle, "Recycle", func() { m.siteAction(m.site, "recycle") }),
		heading("Browse site"),
	}
	for i := range s.browse {
		i := i
		w = append(w, link(&s.browse[i], "Browse", func() { shellOpen(s.browseURL[i]) }))
	}
	return append(w,
		heading("Edit site"),
		link(nil, "Basic settings…", func() { basicSettingsDialog(m, m.site) }),
		link(nil, "Bindings…", func() { bindingsDialog(m, m.site) }),
		link(nil, "Environment…", func() { envDialog(m, m.site) }),
		link(nil, "URL Rewrite…", func() { rewriteDialog(m, m.site) }),
		link(nil, "MIME types…", func() { siteMimeDialog(m, m.site) }),
		link(&s.explore, "Explore folder", func() { s.exploreFolder(m) }),
		link(nil, "View logs", func() { s.tabs.SetCurrentIndex(tabLogs) }),
		link(&s.activate, "Deployments", func() { s.tabs.SetCurrentIndex(tabDeployments) }),
		heading("Site"),
		link(&s.remove, "Remove…", func() { m.removeSite(m.site) }),
	)
}

func (s *sitePage) redraw(m *manager) {
	st := m.siteByID(m.site)
	if st == nil {
		s.props.setProperties([2]string{"Site", "not found"})
		return
	}
	running := st.Status.State != model.StateStopped
	setEnabled(!running, s.start)
	setEnabled(running, s.stop, s.restart)
	setEnabled(running && st.RunsNode(), s.recycle)
	setEnabled(sitePath(st.Site) != "", s.explore)

	for i := range s.browse {
		if i < len(st.Bindings) {
			s.browseURL[i] = desktop.BrowseURL(st.Bindings[i])
			s.browse[i].SetText("<a>Browse " + desktop.BindingText(st.Bindings[i]) + "</a>")
		}
		s.browse[i].SetVisible(i < len(st.Bindings))
	}

	pairs := [][2]string{
		{"Status", desktop.StateText(st.Status.State)},
	}
	if st.Status.Message != "" {
		pairs = append(pairs, [2]string{"Message", st.Status.Message})
	}
	pairs = append(pairs,
		[2]string{"Type", string(st.Type)},
		[2]string{"Start automatically", yesNo(st.AutoStart)},
		[2]string{"Bindings", desktop.BindingsText(st.Bindings)},
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
		pairs = append(pairs,
			[2]string{"Application folder", st.Node.AppRoot},
			[2]string{"Entry point", entry},
			[2]string{"Node.js version", version},
			[2]string{"Instances", strconv.Itoa(st.Node.Instances)},
			[2]string{"Restart policy", st.Node.RestartPolicy},
		)
	case st.Static != nil:
		pairs = append(pairs, [2]string{"Folder", st.Static.Root})
	case st.Proxy != nil:
		var ups []string
		for _, u := range st.Proxy.Upstreams {
			ups = append(ups, u.URL)
		}
		pairs = append(pairs, [2]string{"Upstreams", strings.Join(ups, ", ")}, [2]string{"Load balancing", st.Proxy.LoadBalancing})
	case st.Redirect != nil:
		pairs = append(pairs, [2]string{"Redirect to", fmt.Sprintf("%s (%d)", st.Redirect.TargetURL, st.Redirect.StatusCode)})
	}
	if st.ActiveRelease != "" {
		pairs = append(pairs, [2]string{"Active release", st.ActiveRelease})
	}
	t := st.Status.Traffic
	pairs = append(pairs,
		[2]string{"Requests", fmt.Sprintf("%d (%.1f/s), %d errors (5xx), %.0f ms average", t.Requests, t.RPS, t.Status5xx, t.AvgLatencyMs)},
		[2]string{"Created", st.CreatedAt.Local().Format("2006-01-02 15:04")},
	)
	s.props.setProperties(pairs...)

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
		ip := b.IP
		if ip == "" {
			ip = "*"
		}
		keys = append(keys, b.ID)
		rows = append(rows, []string{b.Protocol, ip, strconv.Itoa(b.Port), b.Host, cert})
	}
	s.bindings.set(keys, rows)

	keys, rows = nil, nil
	if st.Node != nil {
		for _, e := range st.Node.Env {
			keys = append(keys, e.Name)
			rows = append(rows, []string{e.Name, e.Value, yesNo(e.Secret)})
		}
	}
	s.env.set(keys, rows)

	// A different site than the logs follow: start over.
	if s.tabs.CurrentIndex() == tabLogs && s.logs.site != m.site {
		s.logs.follow(m.site, s.logView, s.logState)
	}
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
					return
				}
				st := m.siteByID(id)
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
			})
		}()
	case tabTasks:
		id := m.site
		go func() {
			var list []model.TaskView
			err := m.cl.Get(context.Background(), "/api/sites/"+url.PathEscape(id)+"/tasks", &list)
			m.mw.Synchronize(func() {
				if err != nil || id != m.site {
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
			})
		}()
	}
	s.logs.stop()
}

// runTask starts the selected scheduled task now, as "Run" in Task
// Scheduler does, and shows the result in the list once it finishes.
func (s *sitePage) runTask(m *manager) {
	taskID := s.tasks.selected()
	if taskID == "" {
		walk.MsgBox(m.mw, "Run task", "Select a task first.", walk.MsgBoxIconInformation)
		return
	}
	name := taskID
	for _, t := range s.taskViews {
		if t.ID == taskID {
			name = t.Name
		}
	}
	id := m.site
	m.do("Starting task "+name, func(ctx context.Context) error {
		err := m.cl.Post(ctx, "/api/sites/"+url.PathEscape(id)+"/tasks/"+url.PathEscape(taskID)+"/run", nil, nil)
		if err == nil {
			// Refresh now (running) and again shortly (a quick task's result).
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
		return err
	})
}

func (s *sitePage) activateRelease(m *manager) {
	dep := s.deploy.selected()
	if dep == "" {
		walk.MsgBox(m.mw, "Activate release", "Select a deployment first.", walk.MsgBoxIconInformation)
		return
	}
	if !m.confirm("Activate release", "Switch the site to the selected release? Its instances are recycled without downtime.") {
		return
	}
	id := m.site
	m.do("Activating the release", func(ctx context.Context) error {
		return m.cl.Post(ctx, "/api/sites/"+url.PathEscape(id)+"/deployments/"+url.PathEscape(dep)+"/activate", nil, nil)
	})
}

func (s *sitePage) exploreFolder(m *manager) {
	if st := m.siteByID(m.site); st != nil {
		if p := sitePath(st.Site); p != "" {
			shellOpen(p)
		}
	}
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

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

// ---- following a site's log

// logFollower shows a site's recent output and then streams new lines.
// Lines arrive on network goroutines and are appended in batches, so a
// chatty application cannot flood the UI thread. Each follow has its own
// buffer: goroutines of a previous site, still winding down, write only to
// theirs, which nobody reads any more.
type logFollower struct {
	m      *manager
	site   string
	view   *walk.TextEdit
	cancel context.CancelFunc
	lines  []string
}

type logBuffer struct {
	mu      sync.Mutex
	pending []string
}

func (b *logBuffer) push(l model.LogLine) {
	prefix := l.Time.Local().Format("15:04:05")
	if l.Instance >= 0 {
		prefix += fmt.Sprintf(" #%d", l.Instance)
	}
	if l.Stream == "stderr" || l.Stream == "system" {
		prefix += " " + l.Stream
	}
	b.mu.Lock()
	b.pending = append(b.pending, prefix+"  "+strings.TrimRight(l.Text, "\r\n"))
	b.mu.Unlock()
}

func (b *logBuffer) take() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	batch := b.pending
	b.pending = nil
	return batch
}

const maxLogLines = 3000

func (f *logFollower) follow(site string, view *walk.TextEdit, state *walk.Label) {
	if f.cancel != nil && f.site == site {
		return
	}
	f.stop()
	f.site, f.view = site, view
	f.clear()
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	buf := &logBuffer{}
	state.SetText("Loading…")
	setState := func(s string) {
		f.m.mw.Synchronize(func() {
			if ctx.Err() == nil {
				state.SetText(s)
			}
		})
	}

	base := "/api/sites/" + url.PathEscape(site) + "/logs"
	go func() {
		var recent []model.LogLine
		if err := f.m.cl.Get(ctx, base+"?lines=500", &recent); err == nil {
			for _, l := range recent {
				buf.push(l)
			}
		}
		for ctx.Err() == nil {
			setState("Live")
			f.m.cl.Stream(ctx, base+"/stream", func(event string, data []byte) {
				var l model.LogLine
				if event == "log" && json.Unmarshal(data, &l) == nil {
					buf.push(l)
				}
			})
			if ctx.Err() == nil {
				setState("Disconnected; reconnecting…")
				select {
				case <-ctx.Done():
				case <-time.After(3 * time.Second):
				}
			}
		}
	}()
	go func() {
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if batch := buf.take(); len(batch) > 0 {
					f.m.mw.Synchronize(func() {
						if ctx.Err() == nil {
							f.appendLines(batch)
						}
					})
				}
			}
		}
	}()
}

func (f *logFollower) appendLines(batch []string) {
	f.lines = append(f.lines, batch...)
	if len(f.lines) > maxLogLines {
		f.lines = f.lines[len(f.lines)-maxLogLines*2/3:]
		f.view.SetText(strings.Join(f.lines, "\r\n") + "\r\n")
		return
	}
	f.view.AppendText(strings.Join(batch, "\r\n") + "\r\n")
}

func (f *logFollower) clear() {
	f.lines = nil
	if f.view != nil {
		f.view.SetText("")
	}
}

func (f *logFollower) stop() {
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	f.site = ""
}
