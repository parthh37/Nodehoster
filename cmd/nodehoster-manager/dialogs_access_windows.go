package main

import (
	"fmt"
	"slices"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- web console users' role and site access, as IIS Manager's "IIS
// Manager Permissions": a user either has a role on the whole server or is
// allowed on selected sites only.

var (
	accessRoles = []model.Role{model.RoleAdmin, model.RoleOperator, model.RoleViewer, model.RoleSites}
	accessNames = []string{
		"Administrator of the server",
		"Operator of every site",
		"Viewer of every site",
		"Selected sites only",
	}
	grantRoles     = []model.Role{model.RoleViewer, model.RoleOperator}
	grantRoleNames = []string{"Viewer (read-only)", "Operator (start, stop, deploy)"}
)

// siteAccessLabel is the users list's "Site access" column.
func siteAccessLabel(m *manager, u model.User) string {
	if u.Role != model.RoleSites {
		return "All sites"
	}
	switch len(u.Sites) {
	case 0:
		return "No sites"
	case 1:
		return m.siteName(u.Sites[0].SiteID) + " (" + string(u.Sites[0].Role) + ")"
	}
	return fmt.Sprintf("%d sites", len(u.Sites))
}

// siteName names a site by ID; a site the manager does not know (yet)
// shows as its ID.
func (m *manager) siteName(id string) string {
	if s := m.siteByID(id); s != nil {
		return s.Name
	}
	return id
}

// accessDialog edits a role and, for "Selected sites only", the per-site
// grants. It returns what to send as "role" and "sites".
func accessDialog(m *manager, title string, role model.Role, grants []model.SiteGrant) (model.Role, []model.SiteGrant, bool) {
	grants = slices.Clone(grants)
	l := newEditList(&grants, func(g model.SiteGrant) []string {
		return []string{m.siteName(g.SiteID), grantRoleNames[max(slices.Index(grantRoles, g.Role), 0)]}
	})
	l.minHeight = 160
	l.refresh()
	var dlg *walk.Dialog
	var scope *walk.ComboBox
	var box *walk.Composite
	var picked model.Role
	edit := func() {
		l.edit(func(g *model.SiteGrant) bool { return grantDialog(m, dlg, "Edit site permission", g, grants) })
	}
	onScope := func() {
		if box != nil {
			box.SetEnabled(scope.CurrentIndex() == len(accessRoles)-1)
		}
	}
	ok := runDialogAs(&dlg, m.mw, title, Size{Width: 560, Height: 400}, []Widget{
		intro(desktop.IconIDCard, "What the user may do in the web console: a role on the whole server, or permissions on selected sites."),
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Access:"},
			ComboBox{AssignTo: &scope, Model: accessNames, CurrentIndex: max(slices.Index(accessRoles, role), 0), OnCurrentIndexChanged: onScope},
		}},
		Composite{AssignTo: &box, Layout: VBox{MarginsZero: true}, Enabled: role == model.RoleSites, Children: []Widget{
			l.view(edit, []TableViewColumn{col("Site", 200), col("Permission", 200)},
				button("Add…", func() {
					g := model.SiteGrant{Role: model.RoleViewer}
					if grantDialog(m, dlg, "Add site permission", &g, grants) {
						l.add(g)
					}
				}),
				button("Edit…", edit),
				button("Remove", l.remove),
			),
		}},
		hint("Site operators can start, stop, recycle and deploy their sites and read their logs. Changing a site's settings, and everything server-wide, needs an administrator."),
	}, func(dlg *walk.Dialog) bool {
		picked = accessRoles[max(scope.CurrentIndex(), 0)]
		if picked == model.RoleSites && len(grants) == 0 {
			return invalid(dlg, "Add at least one site, or give the user a role on every site.")
		}
		return true
	})
	if !ok {
		return "", nil, false
	}
	if picked != model.RoleSites {
		return picked, nil, true
	}
	return picked, grants, true
}

// grantDialog edits one site permission; all are the dialog's other
// grants, so a site is listed once.
func grantDialog(m *manager, owner walk.Form, title string, g *model.SiteGrant, all []model.SiteGrant) bool {
	names := make([]string, len(m.sites))
	cur := -1
	for i, s := range m.sites {
		names[i] = s.Name
		if s.ID == g.SiteID {
			cur = i
		}
	}
	var site, role *walk.ComboBox
	return runDialog(owner, title, Size{Width: 400}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Site:"},
			ComboBox{AssignTo: &site, Model: names, CurrentIndex: cur},
			Label{Text: "Permission:"},
			ComboBox{AssignTo: &role, Model: grantRoleNames, CurrentIndex: max(slices.Index(grantRoles, g.Role), 0)},
		}},
	}, func(dlg *walk.Dialog) bool {
		i := site.CurrentIndex()
		if i < 0 || i >= len(m.sites) {
			return invalid(dlg, "Pick a site.")
		}
		id := m.sites[i].ID
		for _, o := range all {
			if o.SiteID == id && o.SiteID != g.SiteID {
				return invalid(dlg, m.sites[i].Name+" is already in the list. Edit that entry instead.")
			}
		}
		g.SiteID, g.Role = id, grantRoles[max(role.CurrentIndex(), 0)]
		return true
	})
}
