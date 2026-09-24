package main

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- Preview deployments: the temporary sites the push webhook makes per
// pull request or branch of a git-deployed site. They are sites (in the
// Sites list too); this page shows them with what they preview, their
// address and state, and deploys them again or deletes them. Their
// settings are the parent site's, in the web console (site → Previews).

type previewRow struct {
	parentID, parent string
	v                model.PreviewView
}

type previewsPage struct {
	page
	list  table
	rows  []previewRow
	bar   infoBar
	find  *walk.LineEdit
	count *walk.Label
	busy  bool // a load is in flight

	open, browse, pullRequest, redeploy, remove, refresh, settings *command
}

func (s *previewsPage) init(m *manager) *page {
	s.icon = desktop.IconEye
	s.title = func() string { return "Preview deployments" }
	s.subtitle = func() string {
		parents := map[string]bool{}
		for _, r := range s.rows {
			parents[r.parentID] = true
		}
		return fmt.Sprintf("%s of %s", plural(len(s.rows), "preview"), plural(len(parents), "site"))
	}
	s.search = func() *walk.LineEdit { return s.find }
	s.load = func() { s.reload(m) }
	s.update = func() {
		// While a preview deploys or is deleted, follow it at the pace of
		// the manager's refresh.
		for _, r := range s.rows {
			if r.v.State == model.PreviewDeploying || r.v.State == model.PreviewDeleting {
				s.reload(m)
				break
			}
		}
		s.enable(m)
	}
	s.open = newCommand("Open site", desktop.IconOpen, func() {
		if r := s.selected(); r != nil {
			m.showSite(r.v.ID)
		}
	})
	s.browse = newCommand("Browse", desktop.IconBrowse, func() {
		if r := s.selected(); r != nil {
			shellOpen(r.v.Preview.URL)
		}
	})
	s.pullRequest = newCommand("Open pull request", desktop.IconLink, func() {
		if r := s.selected(); r != nil && r.v.Preview.PRURL != "" {
			shellOpen(r.v.Preview.PRURL)
		}
	})
	s.redeploy = newCommand("Deploy again", desktop.IconDeploy, func() { s.redeployPreview(m) })
	s.remove = newCommand("Delete…", desktop.IconRemove, func() { s.deletePreview(m) })
	s.refresh = newCommand("Refresh", desktop.IconRefresh, func() { s.reload(m) })
	s.settings = newCommand("Preview settings…", desktop.IconConsole, func() {
		id := ""
		if r := s.selected(); r != nil {
			id = r.parentID
		}
		if id == "" {
			m.openConsolePath("/sites")
			return
		}
		m.openConsolePath("/sites/" + url.PathEscape(id) + "/previews")
	})
	s.list.onSelect = func() { s.enable(m) }
	s.list.color = func(row, col int) (walk.Color, bool) {
		if row >= len(s.rows) || col != 2 {
			return 0, false
		}
		return levelColor(previewLevel(s.rows[row].v.State)), true
	}
	s.list.icon = func(row, col int) walk.Image {
		if row >= len(s.rows) || col != 0 {
			return nil
		}
		return asImage(dotIcon(previewLevel(s.rows[row].v.State)))
	}
	return &s.page
}

func previewLevel(state string) desktop.Level {
	switch state {
	case model.PreviewReady:
		return desktop.LevelOK
	case model.PreviewFailed:
		return desktop.LevelDown
	case model.PreviewDeploying, model.PreviewPending, model.PreviewDeleting:
		return desktop.LevelWarning
	}
	return desktop.LevelNotInstalled
}

func (s *previewsPage) content(m *manager) []Widget {
	return []Widget{
		s.bar.widget(),
		searchRow(&s.find, &s.count, "Search sites, branches, pull requests, addresses", &s.list),
		s.list.viewWith(tableOpts{name: "previews", sortable: true, onActivate: s.browse.trigger, onDelete: s.remove.trigger,
			menu: menu(s.browse, s.open, s.pullRequest, nil, s.redeploy, s.remove, nil, s.settings)},
			col("Site", 150), col("Preview", 230), col("State", 90), col("Address", 280), col("Commit", 80), col("Last push", 130)),
		hint("Pull requests closed or merged, deleted branches and previews without a push for the site's expiry delete themselves. " +
			"A preview's settings are its site's: web console → site → Previews."),
	}
}

func (s *previewsPage) actionsPane(m *manager) []Widget {
	return pane(
		"Preview deployments", s.refresh,
		"Selected preview", s.browse, s.open, s.pullRequest, s.redeploy, s.remove,
		"Settings", s.settings,
	)
}

func (s *previewsPage) selected() *previewRow {
	key := s.list.selected()
	for i := range s.rows {
		if s.rows[i].v.ID == key {
			return &s.rows[i]
		}
	}
	return nil
}

func (s *previewsPage) enable(m *manager) {
	r := s.selected()
	ok := r != nil && m.connected()
	setEnabled(ok, s.open, s.browse, s.settings)
	setEnabled(ok && r.v.Preview.PRURL != "", s.pullRequest)
	setEnabled(ok && r.v.State != model.PreviewDeleting && r.v.State != model.PreviewDeploying, s.redeploy)
	setEnabled(ok && r.v.State != model.PreviewDeleting, s.remove)
	setEnabled(m.connected(), s.refresh)
}

// reload lists the previews of every site that has some or may get some.
func (s *previewsPage) reload(m *manager) {
	s.enable(m)
	if !m.connected() || s.busy {
		return
	}
	type parent struct{ id, name string }
	var parents []parent
	seen := map[string]bool{}
	for _, st := range m.sites {
		id := st.ID
		if st.PreviewOf != "" {
			id = st.PreviewOf
		} else if !st.Deploy.Previews.Enabled {
			continue
		}
		if !seen[id] {
			seen[id] = true
			name := id
			if p := m.siteByID(id); p != nil {
				name = p.Name
			}
			parents = append(parents, parent{id, name})
		}
	}
	s.busy = true
	go func() {
		var rows []previewRow
		var err error
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		for _, p := range parents {
			var list []model.PreviewView
			if err = m.cl.Get(ctx, "/api/sites/"+url.PathEscape(p.id)+"/previews", &list); err != nil {
				break
			}
			for _, v := range list {
				rows = append(rows, previewRow{parentID: p.id, parent: p.name, v: v})
			}
		}
		cancel()
		m.mw.Synchronize(func() {
			s.busy = false
			if err != nil {
				s.bar.show(barError, "Could not load the previews: "+err.Error(), "Retry", func() { s.reload(m) })
				return
			}
			s.rows = rows
			s.redraw(m, len(parents))
		})
	}()
}

func (s *previewsPage) redraw(m *manager, parents int) {
	slices.SortStableFunc(s.rows, func(a, b previewRow) int {
		if c := strings.Compare(strings.ToLower(a.parent), strings.ToLower(b.parent)); c != 0 {
			return c
		}
		return b.v.Preview.LastPush.Compare(a.v.Preview.LastPush)
	})
	if parents == 0 {
		s.bar.show(barInfo, "No site has previews. Turn them on for a git-deployed site in the web console (site → Previews): "+
			"each pull request then gets a temporary site of its own.", "", nil)
	} else {
		s.bar.hide()
	}
	keys := make([]string, len(s.rows))
	cells := make([][]string, len(s.rows))
	for i, r := range s.rows {
		p := r.v.Preview
		what := "branch " + p.Branch
		if p.Kind == model.PreviewPR {
			what = fmt.Sprintf("#%d %s", p.Number, p.Branch)
			if p.Title != "" {
				what += " – " + p.Title
			}
		}
		if p.Fork {
			what += " (fork)"
		}
		state := r.v.State
		if state == model.PreviewFailed && r.v.LastDeployment != nil && r.v.LastDeployment.Message != "" {
			state += ": " + strings.TrimLeft(r.v.LastDeployment.Message, "— ")
		}
		keys[i] = r.v.ID
		cells[i] = []string{r.parent, what, state, p.URL, shortCommit(p.Commit), p.LastPush.Local().Format("2006-01-02 15:04")}
	}
	s.list.set(keys, cells)
	updateCount(s.count, &s.list)
	if m.cur == &s.page {
		m.updateHeader()
	}
	s.enable(m)
}

func (s *previewsPage) redeployPreview(m *manager) {
	r := s.selected()
	if r == nil {
		return
	}
	parent, id, host := r.parentID, r.v.ID, r.v.Preview.Host
	m.do("Deploying "+host+" again", func(ctx context.Context) error {
		err := m.cl.Post(ctx, "/api/sites/"+url.PathEscape(parent)+"/previews/"+url.PathEscape(id)+"/redeploy", nil, nil)
		m.mw.Synchronize(func() { s.reload(m) })
		return err
	})
}

func (s *previewsPage) deletePreview(m *manager) {
	r := s.selected()
	if r == nil {
		return
	}
	parent, id, host := r.parentID, r.v.ID, r.v.Preview.Host
	if ask(m.mw, "Delete preview", "Delete the preview "+host+"?",
		"Its site, releases, logs and automatic certificate are removed. A new push to its pull request or branch creates it again.",
		walk.TaskDialogSystemIconWarning, [2]string{"Delete the preview", ""}) != 0 {
		return
	}
	m.do("Deleting "+host, func(ctx context.Context) error {
		err := m.cl.Delete(ctx, "/api/sites/"+url.PathEscape(parent)+"/previews/"+url.PathEscape(id))
		m.mw.Synchronize(func() { s.reload(m) })
		return err
	})
}
