package main

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- MIME types, as IIS Manager's "MIME Types" feature

// mimeList declares the list of mappings with Add/Edit/Remove.
func mimeList(dlg **walk.Dialog, types *[]model.MimeMap) Composite {
	l := newEditList(types, func(v model.MimeMap) []string { return []string{v.Extension, v.Type} })
	l.refresh()
	edit := func() {
		l.edit(func(v *model.MimeMap) bool { return mimeEditDialog(*dlg, "Edit MIME type", v, *types) })
	}
	return l.view(edit, []TableViewColumn{col("Extension", 130), col("MIME type", 300)},
		button("Add…", func() {
			var v model.MimeMap
			if mimeEditDialog(*dlg, "Add MIME type", &v, *types) {
				l.add(v)
			}
		}),
		button("Edit…", edit),
		button("Remove", l.remove),
	)
}

func mimeEditDialog(owner walk.Form, title string, v *model.MimeMap, all []model.MimeMap) bool {
	var ext, typ *walk.LineEdit
	return runDialog(owner, title, Size{Width: 420}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "File name extension:"}, LineEdit{AssignTo: &ext, Text: v.Extension, CueBanner: ".webmanifest"},
			Label{Text: "MIME type:"}, LineEdit{AssignTo: &typ, Text: v.Type, CueBanner: "application/manifest+json"},
		}},
	}, func(dlg *walk.Dialog) bool {
		e := strings.ToLower(strings.TrimSpace(ext.Text()))
		if e != "" && !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		t := strings.TrimSpace(typ.Text())
		// "." alone maps files without an extension, as in IIS.
		if e == "" || strings.ContainsAny(e, " /\\") {
			return invalid(dlg, "Enter a file name extension, such as .webmanifest (\".\" for files without one).")
		}
		if !strings.Contains(t, "/") {
			return invalid(dlg, "Enter a MIME type, such as application/json.")
		}
		for _, o := range all {
			if strings.EqualFold(o.Extension, e) && !strings.EqualFold(v.Extension, e) {
				return invalid(dlg, e+" is already in the list. Edit that entry instead.")
			}
		}
		v.Extension, v.Type = e, t
		return true
	})
}

var (
	unknownServer      = []string{model.UnknownMimeServe, model.UnknownMimeDeny}
	unknownServerNames = []string{"Serve as application/octet-stream", "Refuse (404 Not Found)"}
)

// serverMimeDialog edits settings.mime: the server's MIME types on top of
// the built-in table, which it shows read-only.
func serverMimeDialog(m *manager) {
	var ms model.MimeSettings
	if err := m.getSettingsSection("mime", &ms); err != nil {
		m.errorBox("MIME types", err)
		return
	}
	types := slices.Clone(ms.Types)

	var builtin table
	var defaults []model.MimeMap
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defaultsErr := m.cl.Get(ctx, "/api/mime/defaults", &defaults)
	cancel()
	slices.SortFunc(defaults, func(a, b model.MimeMap) int { return strings.Compare(a.Extension, b.Extension) })
	keys := make([]string, len(defaults))
	rows := make([][]string, len(defaults))
	for i, d := range defaults {
		keys[i], rows[i] = d.Extension, []string{d.Extension, d.Type}
	}
	builtin.set(keys, rows)
	builtinNote := "Files with these extensions are served with these types unless a mapping on the Custom tab overrides them."
	if defaultsErr != nil {
		builtinNote = "The built-in types could not be read: " + defaultsErr.Error()
	}

	var dlg *walk.Dialog
	var unknown *walk.ComboBox
	runDialogAs(&dlg, m.mw, "MIME types", Size{Width: 640, Height: 480}, []Widget{
		intro(desktop.IconFileCode, "The content type files are served with, by extension. The built-in table never consults the Windows registry, so .js is never served as text/plain."),
		TabWidget{Pages: []TabPage{
			{Title: "Custom", Image: img(desktop.IconEdit), Layout: VBox{}, Children: []Widget{
				Label{Text: "Mappings added to, or overriding, the built-in types for every site:"},
				mimeList(&dlg, &types),
			}},
			{Title: "Built-in", Image: img(desktop.IconFileCode), Layout: VBox{}, Children: []Widget{
				builtin.view(nil, col("Extension", 130), col("MIME type", 300)),
				hint(builtinNote),
			}},
		}},
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Unknown extensions:"},
			ComboBox{AssignTo: &unknown, Model: unknownServerNames, CurrentIndex: max(slices.Index(unknownServer, ms.UnknownTypes), 0)},
		}},
		hint("Applies to files served by static sites and static-folder locations. Sites can add their own types."),
	}, func(dlg *walk.Dialog) bool {
		in := model.MimeSettings{Types: types, UnknownTypes: unknownServer[max(unknown.CurrentIndex(), 0)]}
		if in.Types == nil {
			in.Types = []model.MimeMap{}
		}
		if err := m.putSettingsSection("mime", in, nil); err != nil {
			m.errorBoxFor(dlg, "MIME types", err)
			return false
		}
		return true
	})
}

// siteMimeDialog edits a site's own MIME types.
func siteMimeDialog(m *manager, siteID string) {
	s, err := m.fetchSite(siteID)
	if err != nil {
		m.errorBox("MIME types", err)
		return
	}
	types := slices.Clone(s.Routing.MimeTypes)
	modes := []string{"", model.UnknownMimeServe, model.UnknownMimeDeny}
	modeNames := []string{"Inherit from the server", unknownServerNames[0], unknownServerNames[1]}

	var dlg *walk.Dialog
	var unknown *walk.ComboBox
	runDialogAs(&dlg, m.mw, "MIME types — "+s.Name, Size{Width: 560, Height: 380}, []Widget{
		intro(desktop.IconFileCode, "Mappings for this site, on top of the server's MIME types."),
		mimeList(&dlg, &types),
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Unknown extensions:"},
			ComboBox{AssignTo: &unknown, Model: modeNames, CurrentIndex: max(slices.Index(modes, s.Routing.UnknownMimeTypes), 0)},
		}},
		hint("Applies to files the site serves itself: a static site, or static-folder locations."),
	}, func(dlg *walk.Dialog) bool {
		mode := modes[max(unknown.CurrentIndex(), 0)]
		return m.updateSiteNow(dlg, siteID, "MIME types", func(s *model.Site) {
			s.Routing.MimeTypes, s.Routing.UnknownMimeTypes = types, mode
		})
	})
}
