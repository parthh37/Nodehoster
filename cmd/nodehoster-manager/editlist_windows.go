package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// editList is the list-and-buttons pattern of the Site Bindings dialog
// over a slice that a dialog edits: a table beside a column of buttons.
// Rows are keyed by position, so an edit keeps the selection in place.
type editList[T any] struct {
	items     *[]T
	row       func(v T) []string
	t         table
	minHeight int
	numbered  bool // the first column is the row's position, "1", "2"…
}

func newEditList[T any](items *[]T, row func(v T) []string) *editList[T] {
	return &editList[T]{items: items, row: row}
}

func (l *editList[T]) refresh() {
	n := len(*l.items)
	keys := make([]string, n)
	rows := make([][]string, n)
	for i, v := range *l.items {
		keys[i] = strconv.Itoa(i)
		rows[i] = l.row(v)
		if l.numbered {
			rows[i] = append([]string{strconv.Itoa(i + 1)}, rows[i]...)
		}
	}
	l.t.set(keys, rows)
}

// index is the selected row, or -1.
func (l *editList[T]) index() int {
	if l.t.tv == nil {
		return -1
	}
	if i := l.t.tv.CurrentIndex(); i >= 0 && i < len(*l.items) {
		return i
	}
	return -1
}

func (l *editList[T]) current() *T {
	if i := l.index(); i >= 0 {
		return &(*l.items)[i]
	}
	return nil
}

func (l *editList[T]) selectRow(i int) {
	if l.t.tv != nil && i >= 0 && i < len(*l.items) {
		l.t.tv.SetCurrentIndex(i)
	}
}

func (l *editList[T]) add(v ...T) {
	*l.items = append(*l.items, v...)
	l.refresh()
	l.selectRow(len(*l.items) - 1)
}

// edit runs fn on a copy of the selected item and keeps the copy if fn
// accepts it.
func (l *editList[T]) edit(fn func(v *T) bool) {
	i := l.index()
	if i < 0 {
		return
	}
	v := (*l.items)[i]
	if fn(&v) {
		(*l.items)[i] = v
		l.refresh()
	}
}

func (l *editList[T]) remove() {
	i := l.index()
	if i < 0 {
		return
	}
	*l.items = slices.Delete(*l.items, i, i+1)
	l.refresh()
	l.selectRow(min(i, len(*l.items)-1))
}

func (l *editList[T]) move(delta int) {
	i := l.index()
	j := i + delta
	if i < 0 || j < 0 || j >= len(*l.items) {
		return
	}
	s := *l.items
	s[i], s[j] = s[j], s[i]
	l.refresh()
	l.selectRow(j)
}

// view declares the table with the buttons to its right.
func (l *editList[T]) view(onActivate func(), cols []TableViewColumn, buttons ...Widget) Composite {
	tv := l.t.view(onActivate, cols...)
	if l.minHeight > 0 {
		tv.MinSize = Size{Height: l.minHeight}
	}
	return Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
		tv,
		Composite{Layout: VBox{MarginsZero: true}, Children: append(buttons, VSpacer{})},
	}}
}

func button(text string, fn func()) PushButton {
	return PushButton{Text: text, OnClicked: fn}
}

// ---- multi-line text fields

// lines splits a TextEdit's text into its non-empty, trimmed lines.
func lines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// joinLines is the text of a TextEdit holding one item per line.
func joinLines(items []string) string { return strings.Join(items, "\r\n") }

// ---- saving from a dialog

// The settings document is read fresh, one section replaced and the whole
// document written back, so the other sections (with their masked secrets,
// which the server keeps) go back as the server sent them.

func (m *manager) getSettingsSection(key string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var doc map[string]json.RawMessage
	if err := m.cl.Get(ctx, "/api/settings", &doc); err != nil {
		return err
	}
	raw, ok := doc[key]
	if !ok {
		return errors.New("the server does not have " + key + " settings; update NodeHoster")
	}
	return json.Unmarshal(raw, out)
}

// putSettingsSection replaces one section of the settings and decodes the
// saved section, as the server returns it, into saved (unless nil).
func (m *manager) putSettingsSection(key string, v, saved any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var doc map[string]json.RawMessage
	if err := m.cl.Get(ctx, "/api/settings", &doc); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	doc[key] = data
	var out map[string]json.RawMessage
	if err := m.cl.Put(ctx, "/api/settings", doc, &out); err != nil {
		return err
	}
	if saved != nil && out[key] != nil {
		return json.Unmarshal(out[key], saved)
	}
	return nil
}

// fetchSite reads a site fresh from the server, for a dialog to edit.
func (m *manager) fetchSite(id string) (*model.Site, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := m.cl.Site(ctx, id)
	if err != nil {
		return nil, err
	}
	if st.Site == nil {
		return nil, errors.New("the site was not found")
	}
	return st.Site, nil
}

// updateSiteNow applies change to a fresh copy of the site and saves it
// while the dialog is still open, so a validation error from the server
// keeps the dialog, and the user's input, on screen.
func (m *manager) updateSiteNow(owner walk.Form, id, what string, change func(s *model.Site)) bool {
	s, err := m.fetchSite(id)
	if err == nil {
		change(s)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err = m.cl.UpdateSite(ctx, s)
		cancel()
	}
	if err != nil {
		m.errorBoxFor(owner, what, err)
		return false
	}
	m.refresh(true)
	return true
}
