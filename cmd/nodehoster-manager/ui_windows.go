package main

import (
	"slices"

	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// table is a list view's model: rows of text keyed by ID. Refreshing with
// the same keys updates cells in place, so a list that refreshes every few
// seconds keeps its selection and scroll position.
type table struct {
	walk.TableModelBase
	tv       *walk.TableView
	keys     []string
	rows     [][]string
	color    func(row, col int) (walk.Color, bool) // optional text color per cell
	onSelect func()                                // optional: the current row changed
}

func (t *table) RowCount() int { return len(t.rows) }

func (t *table) Value(row, col int) interface{} {
	if row < len(t.rows) && col < len(t.rows[row]) {
		return t.rows[row][col]
	}
	return ""
}

func (t *table) set(keys []string, rows [][]string) {
	if slices.Equal(keys, t.keys) {
		t.rows = rows
		if len(rows) > 0 {
			t.PublishRowsChanged(0, len(rows)-1)
		}
		return
	}
	sel := t.selected()
	t.keys, t.rows = keys, rows
	t.PublishRowsReset()
	if i := slices.Index(keys, sel); i >= 0 && t.tv != nil {
		t.tv.SetCurrentIndex(i)
	}
}

// selected is the key of the current row, or "".
func (t *table) selected() string {
	if t.tv == nil {
		return ""
	}
	if i := t.tv.CurrentIndex(); i >= 0 && i < len(t.keys) {
		return t.keys[i]
	}
	return ""
}

// view declares the list view for t.
func (t *table) view(onActivate func(), cols ...TableViewColumn) TableView {
	return TableView{
		AssignTo:                 &t.tv,
		Model:                    t,
		Columns:                  cols,
		AlternatingRowBG:         true,
		LastColumnStretched:      true,
		NotSortableByHeaderClick: true,
		OnItemActivated:          onActivate,
		OnCurrentIndexChanged: func() {
			if t.onSelect != nil {
				t.onSelect()
			}
		},
		StyleCell: func(s *walk.CellStyle) {
			if t.color != nil {
				if c, ok := t.color(s.Row(), s.Col()); ok {
					s.TextColor = c
				}
			}
		},
	}
}

func col(title string, width int) TableViewColumn {
	return TableViewColumn{Title: title, Width: width}
}

// properties is a two-column name/value list, like a property grid.
func properties(t *table) TableView {
	return t.view(nil, col("Name", 160), col("Value", 360))
}

func (t *table) setProperties(pairs ...[2]string) {
	keys := make([]string, len(pairs))
	rows := make([][]string, len(pairs))
	for i, p := range pairs {
		keys[i], rows[i] = p[0], []string{p[0], p[1]}
	}
	t.set(keys, rows)
}

// Colors for states in lists.
var (
	colorOK      = walk.RGB(0x1a, 0x7f, 0x37)
	colorWarning = walk.RGB(0x9a, 0x67, 0x00)
	colorError   = walk.RGB(0xcf, 0x22, 0x2e)
	colorMuted   = walk.RGB(0x6e, 0x77, 0x81)
)

// ---- the actions pane

// link is an entry of the actions pane, as in IIS Manager: a hyperlink
// that runs fn.
func link(assign **walk.LinkLabel, text string, fn func()) LinkLabel {
	return LinkLabel{
		AssignTo:        assign,
		Text:            `<a>` + text + `</a>`,
		OnLinkActivated: func(*walk.LinkLabelLink) { fn() },
	}
}

func heading(text string) Label {
	return Label{Text: text, Font: Font{PointSize: 9, Bold: true}}
}

func setEnabled(on bool, links ...*walk.LinkLabel) {
	for _, l := range links {
		if l != nil {
			l.SetEnabled(on)
		}
	}
}
