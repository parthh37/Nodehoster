package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- import from IIS: the migration path from iisnode

// importList is the checklist of the import dialog.
type importList struct {
	walk.TableModelBase
	items   []model.ImportItem
	checked []bool
}

func (l *importList) RowCount() int { return len(l.items) }

func (l *importList) Value(row, col int) interface{} {
	it := l.items[row]
	opt := it.Options[it.Choice]
	switch col {
	case 0:
		if opt.Site != nil {
			return opt.Site.Name
		}
		return opt.Task.Name
	case 1:
		return opt.Label
	case 2:
		return desktop.ImportDetail(opt)
	case 3:
		return strings.Join(it.Conflicts, "; ")
	}
	return ""
}

func (l *importList) Checked(row int) bool { return l.checked[row] }

func (l *importList) SetChecked(row int, checked bool) error {
	l.checked[row] = checked
	return nil
}

// importIISDialog reads this server's IIS configuration, shows the sites
// it found with what converts and what does not, and creates the checked
// ones. Nothing in IIS is changed.
func importIISDialog(m *manager) {
	var pv model.ImportPreview
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err := m.cl.Post(ctx, "/api/import/preview", map[string]string{"source": model.ImportLocalIIS}, &pv)
	cancel()
	if err != nil {
		m.errorBox("Import from IIS", err)
		return
	}
	if len(pv.Items) == 0 {
		notify(m.mw, "Import from IIS", "IIS has no sites to import.", "", "", walk.TaskDialogSystemIconInformation)
		return
	}
	list := &importList{items: pv.Items, checked: make([]bool, len(pv.Items))}
	for i, it := range pv.Items {
		list.checked[i] = it.Selected
	}
	var tv *walk.TableView
	var details *walk.TextEdit
	var start *walk.CheckBox
	showNotes := func() {
		i := tv.CurrentIndex()
		if i < 0 || i >= len(list.items) {
			details.SetText("")
			return
		}
		it := list.items[i]
		var b strings.Builder
		b.WriteString(it.Source + "\r\n")
		for _, c := range it.Conflicts {
			b.WriteString("  ✗ " + c + "\r\n")
		}
		for _, n := range it.Notes {
			mark := map[string]string{model.NoteConverted: "✓", model.NoteApproximated: "≈", model.NoteSkipped: "–"}[n.Level]
			b.WriteString("  " + mark + " " + n.Text + "\r\n")
		}
		details.SetText(b.String())
	}
	introText := "These sites were found in this server's IIS configuration. The checked ones are created in NodeHoster, " +
		"stopped unless you choose otherwise; IIS is not changed. Select a site to see what was converted. " +
		"To change more than this (names, bindings, another way of importing), use Import sites in the web console."
	for _, w := range pv.Warnings {
		introText += "\r\n\r\n" + w
	}

	var result model.ImportApplyResult
	ok := runDialog(m.mw, "Import from IIS", Size{Width: 860, Height: 560}, []Widget{
		intro(desktop.IconImport, introText),
		TableView{
			AssignTo: &tv, Model: list, CheckBoxes: true, AlternatingRowBG: true, LastColumnStretched: true,
			MinSize: Size{Height: 220},
			Columns: []TableViewColumn{
				{Title: "Site", Width: 180}, {Title: "Import as", Width: 140}, {Title: "Details", Width: 300}, {Title: "Conflicts", Width: 200},
			},
			OnCurrentIndexChanged: func() { showNotes() },
		},
		TextEdit{AssignTo: &details, ReadOnly: true, VScroll: true, MinSize: Size{Height: 140}},
		CheckBox{AssignTo: &start, Text: "Start the imported sites (stop them in IIS first: both cannot listen on the same port)"},
	}, func(dlg *walk.Dialog) bool {
		req := model.ImportApplyRequest{Source: pv.Source, Start: start.Checked()}
		for i, it := range list.items {
			if !list.checked[i] {
				continue
			}
			opt := it.Options[it.Choice]
			req.Items = append(req.Items, model.ImportApplyItem{Key: it.Key, Kind: opt.Kind, Site: opt.Site, Task: opt.Task, TaskSite: opt.TaskSite})
		}
		if len(req.Items) == 0 {
			return invalid(dlg, "Check the sites to import.")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := m.cl.Post(ctx, "/api/import/apply", req, &result); err != nil {
			m.errorBoxFor(dlg, "Import from IIS", err)
			return false
		}
		return true
	})
	if !ok {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d imported.", len(result.Created))
	for _, c := range result.Created {
		if c.Warning != "" {
			fmt.Fprintf(&b, "\n%s: %s", c.Name, c.Warning)
		}
	}
	if len(result.Failed) > 0 {
		fmt.Fprintf(&b, "\n\nNot imported:")
		for _, f := range result.Failed {
			fmt.Fprintf(&b, "\n%s: %s", f.Name, f.Error)
		}
	}
	icon := walk.TaskDialogSystemIconInformation
	if len(result.Failed) > 0 {
		icon = walk.TaskDialogSystemIconWarning
	}
	notify(m.mw, "Import from IIS", plural(len(result.Created), "site")+" imported", strings.TrimPrefix(b.String(), fmt.Sprintf("%d imported.", len(result.Created))), "", icon)
	m.refresh(true)
}
