package main

import (
	"slices"
	"strings"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// table is a list view's model: rows of text keyed by ID. Refreshing with
// the same keys updates cells in place, so a list that refreshes every few
// seconds keeps its selection and scroll position.
//
// The rows given to set are the model; the list shows those that pass the
// filters, in the order of the sorted column (when the list is sortable).
// Row numbers passed to color, icon and match are model rows, so they
// index the slices the page passed to set, whatever is shown.
type table struct {
	walk.TableModelBase
	tv    *walk.TableView
	keys  []string
	rows  [][]string
	shown []int // model row of each row shown

	search   string                                // lower-cased text a shown row contains
	match    func(row int) bool                    // optional: another filter
	color    func(row, col int) (walk.Color, bool) // optional text color per cell
	icon     func(row, col int) walk.Image         // optional image per cell
	onSelect func()                                // optional: the current row changed

	// Sorting, for lists declared sortable (see sortedTable).
	sortCol     int
	sortOrder   walk.SortOrder
	sortChanged walk.EventPublisher // the list view draws the header's arrow
	armed       bool                // header clicks sort; see arm
	sorter      *sortedTable
}

func (t *table) RowCount() int { return len(t.shown) }

func (t *table) Value(row, col int) interface{} {
	if r := t.model(row); r >= 0 && col < len(t.rows[r]) {
		return t.rows[r][col]
	}
	return ""
}

// model is the model row shown at row, or -1.
func (t *table) model(row int) int {
	if row >= 0 && row < len(t.shown) {
		return t.shown[row]
	}
	return -1
}

// rebuild computes the shown rows from the filters and the sort.
func (t *table) rebuild() {
	t.shown = t.shown[:0]
	for i, cells := range t.rows {
		if t.match != nil && !t.match(i) {
			continue
		}
		if t.search != "" && !slices.ContainsFunc(cells, func(c string) bool {
			return strings.Contains(strings.ToLower(c), t.search)
		}) {
			continue
		}
		t.shown = append(t.shown, i)
	}
	if t.sortCol >= 0 && t.sorter != nil {
		col, desc := t.sortCol, t.sortOrder == walk.SortDescending
		slices.SortStableFunc(t.shown, func(a, b int) int {
			var x, y string
			if col < len(t.rows[a]) {
				x = t.rows[a][col]
			}
			if col < len(t.rows[b]) {
				y = t.rows[b][col]
			}
			c := desktop.CompareCells(x, y)
			if desc {
				c = -c
			}
			return c
		})
	}
}

func (t *table) set(keys []string, rows [][]string) {
	sel := t.selected()
	sameKeys := slices.Equal(keys, t.keys)
	before := slices.Clone(t.shown)
	t.keys, t.rows = keys, rows
	t.rebuild()
	if sameKeys && slices.Equal(before, t.shown) {
		if len(t.shown) > 0 {
			t.PublishRowsChanged(0, len(t.shown)-1)
		}
		return
	}
	t.PublishRowsReset()
	t.reselect(sel)
}

// refilter shows the rows again after a filter changed.
func (t *table) refilter() {
	sel := t.selected()
	t.rebuild()
	t.PublishRowsReset()
	t.reselect(sel)
	if t.onSelect != nil {
		t.onSelect()
	}
}

// setSearch shows only the rows containing text (any column, any case).
func (t *table) setSearch(text string) {
	t.search = strings.ToLower(strings.TrimSpace(text))
	t.refilter()
}

func (t *table) reselect(key string) {
	if t.tv == nil || key == "" {
		return
	}
	for row, r := range t.shown {
		if t.keys[r] == key {
			t.tv.SetCurrentIndex(row)
			return
		}
	}
}

// selected is the key of the current row, or "".
func (t *table) selected() string {
	if r := t.current(); r >= 0 {
		return t.keys[r]
	}
	return ""
}

// current is the model row of the current row, or -1.
func (t *table) current() int {
	if t.tv == nil {
		return -1
	}
	if r := t.model(t.tv.CurrentIndex()); r >= 0 && r < len(t.keys) {
		return r
	}
	return -1
}

// selectModel makes model row r the current row, if it is shown.
func (t *table) selectModel(r int) {
	if t.tv == nil {
		return
	}
	if row := slices.Index(t.shown, r); row >= 0 {
		t.tv.SetCurrentIndex(row)
	}
}

// count is how many rows are shown, and of how many.
func (t *table) count() (shown, total int) { return len(t.shown), len(t.rows) }

// sortedTable is the model of a sortable list: the table with walk's
// Sorter, so that clicking a column header sorts by it (and again, the
// other way).
type sortedTable struct {
	*table
}

func (s sortedTable) ColumnSortable(col int) bool { return s.armed }
func (s sortedTable) SortedColumn() int           { return s.sortCol }
func (s sortedTable) SortOrder() walk.SortOrder   { return s.sortOrder }
func (s sortedTable) SortChanged() *walk.Event    { return s.sortChanged.Event() }

// Sort sorts by col. The list view asks for a sort as soon as it gets its
// model, and again when it restores its saved state; until the window is
// built (arm) those are ignored, so a list starts in the server's order
// (newest first, for the lists of events).
func (s sortedTable) Sort(col int, order walk.SortOrder) error {
	if !s.armed {
		col = -1
	}
	sel := s.selected()
	s.sortCol, s.sortOrder = col, order
	s.sortChanged.Publish()
	s.rebuild()
	s.PublishRowsReset()
	s.reselect(sel)
	return nil
}

// sortables are the sortable lists of the main window, armed (see Sort)
// once it is built.
var sortables []*table

func armSortables() {
	for _, t := range sortables {
		t.armed = true
	}
}

// tableOpts are a list view's options beyond its columns.
type tableOpts struct {
	name       string // persists column widths across sessions (unique)
	sortable   bool
	onActivate func()
	menu       []MenuItem // context menu
	onDelete   func()     // the Delete key
	minHeight  int
	checkboxes bool
}

// view declares the list view for t.
func (t *table) view(onActivate func(), cols ...TableViewColumn) TableView {
	return t.viewWith(tableOpts{onActivate: onActivate}, cols...)
}

func (t *table) viewWith(o tableOpts, cols ...TableViewColumn) TableView {
	t.sortCol = -1
	var model any = t
	if o.sortable {
		t.sorter = &sortedTable{t}
		model = *t.sorter
		sortables = append(sortables, t)
	}
	tv := TableView{
		AssignTo:                 &t.tv,
		Name:                     o.name,
		Persistent:               o.name != "",
		Model:                    model,
		Columns:                  cols,
		LastColumnStretched:      true,
		ColumnsOrderable:         true,
		NotSortableByHeaderClick: !o.sortable,
		OnItemActivated:          o.onActivate,
		ContextMenuItems:         o.menu,
		OnCurrentIndexChanged: func() {
			if t.onSelect != nil {
				t.onSelect()
			}
		},
		StyleCell: func(s *walk.CellStyle) {
			r := t.model(s.Row())
			if r < 0 {
				return
			}
			if t.color != nil {
				if c, ok := t.color(r, s.Col()); ok {
					s.TextColor = c
				}
			}
			if t.icon != nil {
				if im := t.icon(r, s.Col()); im != nil {
					s.Image = im
				}
			}
		},
	}
	if o.onDelete != nil {
		del := o.onDelete
		tv.OnKeyDown = func(key walk.Key) {
			if key == walk.KeyDelete {
				del()
			}
		}
	}
	if o.minHeight > 0 {
		tv.MinSize = Size{Height: o.minHeight}
	}
	return tv
}

// col is a column. Its name is its title: a list that remembers its
// column widths finds them by name, and columns without one would all
// share the last one's width.
func col(title string, width int) TableViewColumn {
	return TableViewColumn{Name: title, Title: title, Width: width}
}

// colR is a column of numbers, aligned right.
func colR(title string, width int) TableViewColumn {
	return TableViewColumn{Name: title, Title: title, Width: width, Alignment: AlignFar}
}

// ---- property lists

// prop is a row of a property list: an icon, a name and a value.
type prop struct {
	icon, name, value string
}

func p(icon, name, value string) prop { return prop{icon, name, value} }

// properties is a two-column name/value list, like a property grid.
func properties(t *table, name string) TableView {
	tv := t.viewWith(tableOpts{name: name}, col("Name", 170), col("Value", 380))
	return tv
}

// setProperties fills a property list; the icons show in the name column.
func (t *table) setProperties(props ...prop) {
	keys := make([]string, len(props))
	rows := make([][]string, len(props))
	icons := make([]string, len(props))
	for i, pr := range props {
		keys[i], rows[i], icons[i] = pr.name, []string{pr.name, pr.value}, pr.icon
	}
	t.icon = func(row, col int) walk.Image {
		if col == 0 && row < len(icons) && icons[row] != "" {
			return img(icons[row])
		}
		return nil
	}
	t.set(keys, rows)
}

// ---- small helpers

func setEnabled(on bool, cmds ...*command) {
	for _, c := range cmds {
		if c != nil {
			c.setEnabled(on)
		}
	}
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}
