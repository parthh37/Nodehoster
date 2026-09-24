package main

import (
	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
	"github.com/tailscale/win"
)

// The manager's look, in the style of Windows 11 tools and matching the
// web console (its teal accent and slate greys): a light grey window whose
// title bar is the same grey, a navigation tree like File Explorer's, white
// cards and lists, commands drawn as rows that highlight under the
// pointer, Segoe UI with semibold headings, and colored bars for what
// needs attention.

var (
	colorOK       = walk.RGB(0x16, 0x7c, 0x3a)
	colorWarning  = walk.RGB(0x9a, 0x5b, 0x00)
	colorError    = walk.RGB(0xc4, 0x2b, 0x1c)
	colorText     = walk.RGB(0x1f, 0x29, 0x37)
	colorMuted    = walk.RGB(0x5f, 0x6b, 0x7a)
	colorDisabled = walk.RGB(0xa0, 0xa7, 0xb1)
	colorAccent   = walk.RGB(0x0f, 0x76, 0x6e)
	colorSurface  = walk.RGB(0xf3, 0xf4, 0xf6) // the window
	colorCard     = walk.RGB(0xff, 0xff, 0xff)
	colorBorder   = walk.RGB(0xe1, 0xe4, 0xe8)
	colorHover    = walk.RGB(0xe5, 0xe8, 0xeb) // a row under the pointer
	colorPressed  = walk.RGB(0xd9, 0xdd, 0xe2)
	colorTrack    = walk.RGB(0xe5, 0xe7, 0xeb) // a meter's empty part
)

var (
	fontTitle   = Font{Family: "Segoe UI Semibold", PointSize: 16}
	fontPane    = Font{Family: "Segoe UI Semibold", PointSize: 11} // a pane's title
	fontHeading = Font{Family: "Segoe UI Semibold", PointSize: 9}
	fontValue   = Font{Family: "Segoe UI Semibold", PointSize: 14}
	fontMono    = Font{Family: "Consolas", PointSize: 9}
)

var brushes = map[walk.Color]*walk.SolidColorBrush{}

// brush is a solid brush of c, made once and kept for the program's life.
func brush(c walk.Color) *walk.SolidColorBrush {
	b := brushes[c]
	if b == nil {
		b, _ = walk.NewSolidColorBrush(c)
		brushes[c] = b
	}
	return b
}

// setLabel, setVisible and setColor change a widget only when the value
// differs: pages redraw on every refresh, and each change lays the window
// out or repaints it, which would flicker every few seconds.
func setLabel(l *walk.Label, text string) {
	if l != nil && l.Text() != text {
		l.SetText(text)
	}
}

// setVisible shows or hides w itself. Unlike walk's SetVisible, it works
// while an ancestor is hidden: walk compares with IsWindowVisible, which is
// false for every child of a hidden window, so hiding a child of a hidden
// bar did nothing and the child came back with the bar. Windows reports
// the change to walk (WM_WINDOWPOSCHANGED) as when walk makes it.
func setVisible(w walk.Widget, on bool) {
	if w == nil || w.Handle() == 0 {
		return
	}
	if shown := win.GetWindowLong(w.Handle(), win.GWL_STYLE)&win.WS_VISIBLE != 0; shown != on {
		cmd := int32(win.SW_HIDE)
		if on {
			cmd = win.SW_SHOWNA
		}
		win.ShowWindow(w.Handle(), cmd)
	}
}

// A widget declared Visible: false still shows: walk applies the property
// while the form is being built, when the form is hidden, so its
// SetVisible sees the widget as hidden already and does nothing. What
// starts hidden registers with whenCreated, and the form's builder runs
// createdHooks once the widgets exist (the main window after Create, a
// dialog in runModal), which hide it with setVisible.
var pendingCreated []func()

// whenCreated runs fn once the form now being declared exists.
func whenCreated(fn func()) { pendingCreated = append(pendingCreated, fn) }

// createdHooks runs what whenCreated deferred; failed is true when the
// form could not be created, and the hooks are dropped.
func createdHooks(failed bool) {
	hooks := pendingCreated
	pendingCreated = nil
	if failed {
		return
	}
	for _, fn := range hooks {
		fn()
	}
}

func setColor(l *walk.Label, c walk.Color) {
	if l != nil && l.TextColor() != c {
		l.SetTextColor(c)
	}
}

// heading is a section title, in the actions pane and above lists.
func heading(text string) Label {
	return Label{Text: text, Font: fontHeading, TextColor: colorMuted}
}

// hint is a line of explanation under a list; it wraps.
func hint(text string) TextLabel {
	return TextLabel{Text: text, TextColor: colorMuted}
}

// iconView shows a 16-pixel icon.
func iconView(assign **walk.ImageView, name string) ImageView {
	return ImageView{AssignTo: assign, Image: img(name), MinSize: Size{Width: 16, Height: 16}, MaxSize: Size{Width: 16, Height: 16}}
}

// card is a white panel with a thin border.
func card(margins Margins, layout Layout, children ...Widget) Composite {
	return Composite{
		Background: SolidColorBrush{Color: colorBorder},
		Layout:     VBox{Margins: Margins{Left: 1, Top: 1, Right: 1, Bottom: 1}},
		Children: []Widget{
			Composite{Background: SolidColorBrush{Color: colorCard}, Layout: layout, Children: children},
		},
	}
}

// ---- commands

// command is something the user can do, shown as a link with its icon in
// the actions pane and as an item of menus (context menus of lists, the
// menu bar, the tool bar). Enabling, disabling, renaming or hiding it
// updates every place it shows, and a disabled command shows its icon in
// grey.
type command struct {
	text     string
	icon     string
	run      func()
	enabled  bool
	visible  bool
	shortcut Shortcut // shown in the menu bar
	short    string   // the tool bar's text, if shorter (for commands never renamed)

	views   []*commandView // rows in actions panes
	actions []**walk.Action
}

// commandView is a command in a pane (a row) or as a button.
type commandView struct {
	row    *actionRow
	button *walk.PushButton
}

// commands are all the commands made, for syncCommands.
var commands []*command

func newCommand(text, icon string, run func()) *command {
	c := &command{text: text, icon: icon, run: run, enabled: true, visible: true}
	commands = append(commands, c)
	return c
}

// syncCommands shows every command's state in every place it shows. While
// the window is built, some commands change (a list's filter disables its
// commands) before all their places exist: a context menu built later
// would still show the state the command was declared with.
func syncCommands() {
	for _, c := range commands {
		for _, a := range c.actions {
			if *a != nil {
				(*a).SetEnabled(c.enabled)
				(*a).SetVisible(c.visible)
				(*a).SetImage(c.currentIcon())
			}
		}
		for _, v := range c.views {
			if v.button != nil {
				v.button.SetEnabled(c.enabled)
				v.button.SetImage(c.currentIcon())
				setVisible(v.button, c.visible)
			}
			if v.row != nil && v.row.cw != nil {
				v.row.init()
				v.row.update()
				setVisible(v.row.cw, c.visible)
			}
		}
	}
}

// withShortcut sets the command's keyboard shortcut (its menu bar item
// carries it).
func (c *command) withShortcut(mod walk.Modifiers, key walk.Key) *command {
	c.shortcut = Shortcut{Modifiers: mod, Key: key}
	return c
}

func (c *command) trigger() {
	if c.enabled && c.run != nil {
		c.run()
	}
}

func (c *command) currentIcon() walk.Image {
	if c.enabled {
		return img(c.icon)
	}
	return asImage(icoOff(c.icon))
}

func linkText(text string) string { return "<a>" + text + "</a>" }

// paneRow declares the command as a row of an actions pane.
func (c *command) paneRow() Widget {
	v := &commandView{row: &actionRow{cmd: c, bg: colorSurface}}
	c.views = append(c.views, v)
	return v.row.widget()
}

// menuItem declares the command as an item of a menu or tool bar.
func (c *command) menuItem() Action {
	a := new(*walk.Action)
	c.actions = append(c.actions, a)
	return Action{AssignTo: a, Text: c.text, Image: c.currentIcon(), Enabled: c.enabled, Visible: c.visible, OnTriggered: c.trigger}
}

// barItem is menuItem with the command's shortcut, for the menu bar.
func (c *command) barItem() Action {
	a := c.menuItem()
	a.Shortcut = c.shortcut
	return a
}

// toolItem is menuItem with the short text, for the tool bar.
func (c *command) toolItem() Action {
	a := c.menuItem()
	if c.short != "" {
		a.Text = c.short
	}
	return a
}

// withShort sets a shorter text for the tool bar.
func (c *command) withShort(text string) *command {
	c.short = text
	return c
}

func (c *command) setEnabled(on bool) {
	if c.enabled == on {
		return
	}
	c.enabled = on
	for _, v := range c.views {
		if v.row != nil {
			v.row.update()
		}
		if v.button != nil {
			v.button.SetEnabled(on)
			v.button.SetImage(c.currentIcon())
		}
	}
	for _, a := range c.actions {
		if *a != nil {
			(*a).SetEnabled(on)
			(*a).SetImage(c.currentIcon())
		}
	}
}

func (c *command) setText(text string) {
	if c.text == text {
		return
	}
	c.text = text
	for _, v := range c.views {
		if v.row != nil {
			v.row.update()
		}
		if v.button != nil {
			v.button.SetText(text)
		}
	}
	for _, a := range c.actions {
		if *a != nil {
			(*a).SetText(text)
		}
	}
}

func (c *command) setIcon(name string) {
	if c.icon == name {
		return
	}
	c.icon = name
	for _, v := range c.views {
		if v.row != nil {
			v.row.update()
		}
		if v.button != nil {
			v.button.SetImage(c.currentIcon())
		}
	}
	for _, a := range c.actions {
		if *a != nil {
			(*a).SetImage(c.currentIcon())
		}
	}
}

func (c *command) setVisible(on bool) {
	if c.visible == on {
		return
	}
	c.visible = on
	for _, v := range c.views {
		if v.row != nil && v.row.cw != nil {
			setVisible(v.row.cw, on)
		}
		if v.button != nil {
			setVisible(v.button, on)
		}
	}
	for _, a := range c.actions {
		if *a != nil {
			(*a).SetVisible(on)
		}
	}
}

// pane lays out an actions pane: a section is a heading (a string)
// followed by its commands.
func pane(items ...any) []Widget {
	var w []Widget
	for _, it := range items {
		switch v := it.(type) {
		case string:
			if len(w) > 0 {
				w = append(w, VSpacer{Size: 10})
			}
			w = append(w, Composite{Layout: HBox{Margins: Margins{Left: 10}}, Children: []Widget{heading(v), HSpacer{}}})
		case *command:
			w = append(w, v.paneRow())
		case []*command:
			for _, c := range v {
				w = append(w, c.paneRow())
			}
		}
	}
	return w
}

// menu declares a context menu of commands; nil is a separator.
func menu(cmds ...*command) []MenuItem {
	var items []MenuItem
	for _, c := range cmds {
		if c == nil {
			items = append(items, Separator{})
		} else {
			items = append(items, c.menuItem())
		}
	}
	return items
}

// ---- info bars

type barKind int

const (
	barInfo barKind = iota
	barOK
	barWarning
	barError
)

var barColors = map[barKind]walk.Color{
	barInfo:    walk.RGB(0xe8, 0xf1, 0xfe),
	barOK:      walk.RGB(0xe7, 0xf6, 0xec),
	barWarning: walk.RGB(0xff, 0xf4, 0xde),
	barError:   walk.RGB(0xfd, 0xe9, 0xe9),
}

// barStripes color the edge of a bar, like Windows 11's InfoBar.
var barStripes = map[barKind]walk.Color{
	barInfo:    walk.RGB(0x25, 0x63, 0xeb),
	barOK:      colorOK,
	barWarning: walk.RGB(0xd9, 0x77, 0x06),
	barError:   colorError,
}

var barIcons = map[barKind]string{
	barInfo:    desktop.IconInfo,
	barOK:      desktop.IconOK,
	barWarning: desktop.IconWarning,
	barError:   desktop.IconError,
}

// infoBar is a colored message across a page, with an icon and an
// optional link that fixes the problem ("Start the service").
type infoBar struct {
	box    *walk.Composite
	stripe *walk.Composite
	image  *walk.ImageView
	text   *walk.TextLabel
	link   *walk.LinkLabel
	act    func()
	kind   barKind
}

func (b *infoBar) widget() Widget {
	whenCreated(func() {
		if b.box != nil && b.link != nil && b.text.Text() == "" { // not shown meanwhile
			setVisible(b.link, false)
			setVisible(b.box, false)
		}
	})
	return Composite{
		AssignTo:   &b.box,
		Visible:    false,
		Background: SolidColorBrush{Color: barColors[barInfo]},
		Layout:     HBox{MarginsZero: true, SpacingZero: true},
		Children: []Widget{
			// Empty, so it takes the bar's height without asking for any
			// (a CustomWidget would make the bar grab room from lists).
			Composite{
				AssignTo:   &b.stripe,
				Background: SolidColorBrush{Color: barStripes[barInfo]},
				MinSize:    Size{Width: 4},
				MaxSize:    Size{Width: 4},
				Layout:     HBox{MarginsZero: true},
			},
			Composite{
				Layout: HBox{Margins: Margins{Left: 12, Top: 9, Right: 12, Bottom: 9}, Spacing: 10, Alignment: AlignHNearVCenter},
				Children: []Widget{
					iconView(&b.image, barIcons[barInfo]),
					TextLabel{AssignTo: &b.text, StretchFactor: 1},
					LinkLabel{AssignTo: &b.link, Visible: false, OnLinkActivated: func(*walk.LinkLabelLink) {
						if b.act != nil {
							b.act()
						}
					}},
				},
			},
		},
	}
}

// show shows the bar; link and act are an optional fix.
func (b *infoBar) show(kind barKind, text, link string, act func()) {
	if b.box == nil {
		return
	}
	if kind != b.kind {
		b.kind = kind
		b.box.SetBackground(brush(barColors[kind]))
		b.image.SetImage(img(barIcons[kind]))
		b.stripe.SetBackground(brush(barStripes[kind]))
	}
	if b.text.Text() != text {
		b.text.SetText(text)
	}
	b.act = act
	setVisible(b.link, link != "" && act != nil)
	if link != "" && b.link.Text() != linkText(link) {
		b.link.SetText(linkText(link))
	}
	setVisible(b.box, true)
}

func (b *infoBar) hide() {
	if b.box != nil {
		setVisible(b.box, false)
	}
}

// ---- cards with a figure

// meter is a thin horizontal gauge.
type meter struct {
	cw    *walk.CustomWidget
	pct   int
	color walk.Color
}

func (m *meter) widget() CustomWidget {
	return CustomWidget{
		AssignTo:            &m.cw,
		MinSize:             Size{Width: 40, Height: 5},
		MaxSize:             Size{Height: 5},
		InvalidatesOnResize: true,
		PaintPixels: func(c *walk.Canvas, _ walk.Rectangle) error {
			b := m.cw.ClientBoundsPixels()
			c.FillRectanglePixels(brush(colorTrack), b)
			if w := b.Width * min(max(m.pct, 0), 100) / 100; w > 0 {
				c.FillRectanglePixels(brush(m.color), walk.Rectangle{X: b.X, Y: b.Y, Width: w, Height: b.Height})
			}
			return nil
		},
	}
}

// set fills the meter to pct, colored by how full it is.
func (m *meter) set(pct int) {
	col := colorAccent
	switch {
	case pct >= 90:
		col = colorError
	case pct >= 75:
		col = walk.RGB(0xd9, 0x77, 0x06)
	}
	if m.pct != pct || m.color != col {
		m.pct, m.color = pct, col
		if m.cw != nil {
			m.cw.Invalidate()
		}
	}
}

// statCard is a card with a caption, a figure, a line of detail and
// optionally a meter: "CPU / 12% / of 8 cores".
type statCard struct {
	image         *walk.ImageView
	caption       *walk.Label
	value, detail *walk.Label
	meter         *meter
}

func (s *statCard) widget(icon, caption string, withMeter bool) Widget {
	children := []Widget{
		Composite{Layout: HBox{MarginsZero: true, Spacing: 6, Alignment: AlignHNearVCenter}, Children: []Widget{
			iconView(&s.image, icon),
			Label{AssignTo: &s.caption, Text: caption, TextColor: colorMuted},
			HSpacer{},
		}},
		Label{AssignTo: &s.value, Text: "–", Font: fontValue, EllipsisMode: EllipsisEnd},
		Label{AssignTo: &s.detail, Text: " ", TextColor: colorMuted, EllipsisMode: EllipsisEnd},
	}
	if withMeter {
		s.meter = &meter{color: colorAccent}
		children = append(children, s.meter.widget())
	}
	return card(Margins{}, VBox{Margins: Margins{Left: 12, Top: 9, Right: 12, Bottom: 10}, Spacing: 3}, children...)
}

// set shows a figure; color is its text color (0 for the default).
func (s *statCard) set(value, detail string, color walk.Color) {
	if s.value == nil {
		return
	}
	if detail == "" {
		detail = " "
	}
	setLabel(s.value, value)
	setLabel(s.detail, detail)
	setColor(s.value, color)
}

func (s *statCard) setIcon(name string) {
	if s.image != nil {
		s.image.SetImage(img(name))
	}
}

// cards lays out stat cards in a row.
func cards(cs ...Widget) Composite {
	return Composite{Layout: Grid{Columns: len(cs), MarginsZero: true, Spacing: 8}, Children: cs}
}

// ---- search boxes

// searchBox is a search field over a list: typing filters it.
func searchBox(assign **walk.LineEdit, cue string, onChange func(text string)) Composite {
	var le *walk.LineEdit
	if assign == nil {
		assign = &le
	}
	var icon *walk.ImageView
	return Composite{
		Layout: HBox{MarginsZero: true, Spacing: 6, Alignment: AlignHNearVCenter},
		Children: []Widget{
			iconView(&icon, desktop.IconSearch),
			LineEdit{AssignTo: assign, CueBanner: cue, MinSize: Size{Width: 180}, MaxSize: Size{Width: 320},
				OnTextChanged: func() { onChange((*assign).Text()) }},
		},
	}
}

// ---- messages

// ask shows a task dialog with a button per choice (a command link with a
// note when note is given) and returns the chosen index, or -1 when the
// user cancels.
func ask(owner walk.Form, title, instruction, content string, icon walk.TaskDialogSystemIcon, choices ...[2]string) int {
	td := walk.NewTaskDialog()
	centerOnOwner(td, owner)
	opts := walk.TaskDialogOpts{
		Owner:         owner,
		Title:         title,
		Instruction:   instruction,
		Content:       content,
		IconSystem:    icon,
		CommonButtons: win.TDCBF_CANCEL_BUTTON,
		DefaultButton: walk.TaskDialogDefaultButtonCancel,
	}
	links := false
	for _, c := range choices {
		opts.CustomButtons = append(opts.CustomButtons, walk.TaskDialogCustomButton{MainText: c[0], Note: c[1]})
		links = links || c[1] != ""
	}
	if links {
		opts.CommandLinkMode = walk.TaskDialogCommandLinks
	}
	chosen := -1
	for i := range opts.CustomButtons {
		opts.CustomButtons[i].Clicked().Attach(func() bool {
			chosen = i
			return false // close the dialog
		})
	}
	res, err := td.Show(opts)
	if err != nil {
		// No task dialogs (an unusual Windows build): a message box.
		if walk.MsgBox(owner, title, instruction+"\n\n"+content, walk.MsgBoxOKCancel|walk.MsgBoxIconQuestion) == walk.DlgCmdOK && len(choices) > 0 {
			return 0
		}
		return -1
	}
	if res.Canceled {
		return -1
	}
	return chosen
}

// notify shows a task dialog with a message and an OK button; details
// are behind a "Show details" expander.
func notify(owner walk.Form, title, instruction, content, details string, icon walk.TaskDialogSystemIcon) {
	td := walk.NewTaskDialog()
	centerOnOwner(td, owner)
	_, err := td.Show(walk.TaskDialogOpts{
		Owner:               owner,
		Title:               title,
		Instruction:         instruction,
		Content:             content,
		IconSystem:          icon,
		CommonButtons:       win.TDCBF_OK_BUTTON,
		ExpandedInformation: details,
		ExpandLabel:         "Show details",
		CollapseLabel:       "Hide details",
	})
	if err != nil {
		msg := instruction
		if content != "" {
			msg += "\n\n" + content
		}
		if details != "" {
			msg += "\n\n" + details
		}
		mb := walk.MsgBoxIconInformation
		switch icon {
		case walk.TaskDialogSystemIconError:
			mb = walk.MsgBoxIconError
		case walk.TaskDialogSystemIconWarning:
			mb = walk.MsgBoxIconWarning
		}
		walk.MsgBox(owner, title, msg, mb)
	}
}
