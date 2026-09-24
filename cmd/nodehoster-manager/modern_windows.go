package main

import (
	"syscall"
	"unsafe"

	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
	"github.com/tailscale/win"
)

// The Windows 11 touches: rounded window corners and a title bar in the
// window's own color, trees drawn like File Explorer's, dialogs that open
// where they belong, and commands drawn as rows the whole of which takes
// the click. On Windows 10 the DWM attributes are refused, harmlessly.

// ---- windows

const dwmwcpRound = 2 // DWM_WINDOW_CORNER_PREFERENCE: DWMWCP_ROUND

// styleForm gives a top-level window rounded corners and a caption the
// color of its surface, so that title bar and window read as one.
func styleForm(f walk.Form, caption walk.Color) {
	if f == nil || f.Handle() == 0 {
		return
	}
	h := f.Handle()
	corner := uint32(dwmwcpRound)
	win.DwmSetWindowAttribute(h, win.DWMWA_WINDOW_CORNER_PREFERENCE, unsafe.Pointer(&corner), uint32(unsafe.Sizeof(corner)))
	c := win.COLORREF(caption)
	win.DwmSetWindowAttribute(h, win.DWMWA_CAPTION_COLOR, unsafe.Pointer(&c), uint32(unsafe.Sizeof(c)))
	b := win.COLORREF(colorBorder)
	win.DwmSetWindowAttribute(h, win.DWMWA_BORDER_COLOR, unsafe.Pointer(&b), uint32(unsafe.Sizeof(b)))
}

// cloak hides a window from the screen without hiding it from Windows:
// IsWindowVisible stays true, so walk lays it out as shown.
func cloak(f walk.Form, on bool) {
	v := int32(0)
	if on {
		v = 1
	}
	win.DwmSetWindowAttribute(f.Handle(), win.DWMWA_CLOAK, unsafe.Pointer(&v), uint32(unsafe.Sizeof(v)))
}

// workArea is the part of w's monitor not taken by the task bar, in
// native pixels.
func workArea(w walk.Window) walk.Rectangle {
	mi := win.MONITORINFO{CbSize: uint32(unsafe.Sizeof(win.MONITORINFO{}))}
	if !win.GetMonitorInfo(win.MonitorFromWindow(w.Handle(), win.MONITOR_DEFAULTTONEAREST), &mi) {
		return w.BoundsPixels()
	}
	r := mi.RcWork
	return walk.Rectangle{X: int(r.Left), Y: int(r.Top), Width: int(r.Right - r.Left), Height: int(r.Bottom - r.Top)}
}

// runModal shows a dialog and runs it until it closes, returning its
// result, like walk's Dialog.Run but placed properly.
//
// walk sizes a dialog before showing it, when Windows reports all of its
// widgets as hidden, so it centers a dialog of almost no height on the
// owner: the dialog's top edge lands at the owner's middle and the real
// layout then grows it downwards, often past the bottom of the screen with
// its OK and Cancel buttons (and, the dialog being modal, the whole
// manager unusable until it is found). Here the dialog is shown cloaked,
// measured once its widgets are visible, sized to its content (body, at
// the width asked for) but never beyond the owner's screen, centered on
// the owner and only then revealed.
//
// size is in 1/96 inch; a zero height means as tall as the content. body
// and footer (optional) are the dialog's scrolled content and its fixed
// button bar.
func runModal(dlg *walk.Dialog, owner walk.Form, size Size, body, footer *walk.Composite) int {
	cloak(dlg, true)
	styleForm(dlg, colorCard)
	dlg.Show()
	createdHooks(false) // before measuring: what starts hidden takes no room
	placeDialog(dlg, owner, size, body, footer)
	cloak(dlg, false)
	walk.App().RunModal(dlg)
	return dlg.Result()
}

func placeDialog(dlg *walk.Dialog, owner walk.Form, size Size, body, footer *walk.Composite) {
	dpi := dlg.DPI()
	px := func(v int) int { return walk.IntFrom96DPI(v, dpi) }

	ref := walk.Window(dlg)
	if owner != nil && owner.Handle() != 0 && !win.IsIconic(owner.Handle()) {
		ref = owner
	}
	work := workArea(ref)
	margin := px(12)
	maxW, maxH := work.Width-2*margin, work.Height-2*margin

	b, cb := dlg.BoundsPixels(), dlg.ClientBoundsPixels()
	ncW, ncH := b.Width-cb.Width, b.Height-cb.Height

	// walk has grown the dialog to what its layout needs at the least
	// (for a scrolled body, nothing: that is measured here).
	w := max(b.Width, px(size.Width)+ncW)
	if body != nil {
		w = max(w, minSize(body).Width+ncW)
	}
	w = min(w, maxW)
	h := b.Height
	if body != nil {
		clientW := w - ncW
		h = heightFor(body, clientW) + ncH + 1 // a pixel spare: no scroll bar for rounding
		if footer != nil {
			h += heightFor(footer, clientW) + px(1) // and the divider above it
		}
	}
	if size.Height > 0 {
		h = max(h, px(size.Height)+ncH)
	}
	h = min(h, maxH)

	area := work
	if ref == walk.Window(owner) {
		area = owner.BoundsPixels()
	}
	x := area.X + (area.Width-w)/2
	y := area.Y + (area.Height-h)/2
	x = max(work.X+margin, min(x, work.X+work.Width-margin-w))
	y = max(work.Y+margin, min(y, work.Y+work.Height-margin-h))
	dlg.SetBoundsPixels(walk.Rectangle{X: x, Y: y, Width: w, Height: h})
}

// centerOnOwner centers a task dialog over its owner when it is created
// (Windows centers it on the screen, away from the window that asked).
func centerOnOwner(td walk.TaskDialog, owner walk.Form) {
	if owner == nil || owner.Handle() == 0 || win.IsIconic(owner.Handle()) {
		return
	}
	td.Created().Attach(func(w walk.Win32Window) {
		var r win.RECT
		if !win.GetWindowRect(w.Handle(), &r) {
			return
		}
		width, height := int(r.Right-r.Left), int(r.Bottom-r.Top)
		ob, work := owner.BoundsPixels(), workArea(owner)
		x := max(work.X, min(ob.X+(ob.Width-width)/2, work.X+work.Width-width))
		y := max(work.Y, min(ob.Y+(ob.Height-height)/2, work.Y+work.Height-height))
		win.SetWindowPos(w.Handle(), 0, int32(x), int32(y), 0, 0, win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_NOACTIVATE)
	})
}

// heightFor is the height c's layout needs at width, in native pixels.
func heightFor(c *walk.Composite, width int) int {
	li := walk.CreateLayoutItemsForContainer(c)
	return li.MinSizeForSize(walk.Size{Width: width}).Height
}

// minSize is the least size c's layout needs, in native pixels.
func minSize(c *walk.Composite) walk.Size {
	return walk.CreateLayoutItemsForContainer(c).MinSizeForSize(walk.Size{})
}

// divider is a one-pixel line across its container.
func divider(color walk.Color) CustomWidget {
	var cw *walk.CustomWidget
	return CustomWidget{
		AssignTo:            &cw,
		MinSize:             Size{Height: 1},
		MaxSize:             Size{Height: 1},
		InvalidatesOnResize: true,
		PaintPixels: func(c *walk.Canvas, _ walk.Rectangle) error {
			return c.FillRectanglePixels(brush(color), cw.ClientBoundsPixels())
		},
	}
}

// ---- trees

// treeRowHeight is the height of a navigation tree's rows, in 1/96 inch.
const treeRowHeight = 28

// modernizeTree draws a tree view like File Explorer's navigation pane:
// chevrons that fade in, a rounded highlight across the whole row, taller
// rows and the pane's own background instead of white.
func modernizeTree(tv *walk.TreeView, bg walk.Color) {
	h := tv.Handle()
	win.SetWindowTheme(h, syscall.StringToUTF16Ptr("Explorer"), nil)
	style := win.GetWindowLong(h, win.GWL_STYLE)
	win.SetWindowLong(h, win.GWL_STYLE, style&^win.TVS_HASLINES|win.TVS_FULLROWSELECT)
	// Not TVS_EX_AUTOHSCROLL: it slides a long label sideways under the
	// pointer, and the pane with it.
	ex := uintptr(win.TVS_EX_FADEINOUTEXPANDOS)
	tv.SendMessage(win.TVM_SETEXTENDEDSTYLE, ex, ex)
	tv.SendMessage(win.TVM_SETBKCOLOR, 0, uintptr(bg))
	tv.SetItemHeight(walk.IntFrom96DPI(treeRowHeight, tv.DPI()))
	// The border walk draws around it would box the pane in.
	ex2 := win.GetWindowLong(h, win.GWL_EXSTYLE)
	win.SetWindowLong(h, win.GWL_EXSTYLE, ex2&^win.WS_EX_CLIENTEDGE)
	win.SetWindowPos(h, 0, 0, 0, 0, 0, win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_FRAMECHANGED)
}

// ---- tool bars

// roomyToolBar gives the tool bar's buttons the padding of current
// Windows tools, so that they are easy targets, on one row.
//
// walk makes the tool bar wrap its buttons. With the padding, a window
// narrower than the buttons wrapped them onto a second row whenever it was
// resized, the tool bar grew over the top of the panes, which walk had
// laid out under a one-row bar, and it drew clipped or overlapping. The
// buttons that do not fit are cut off instead; each is in the menu bar.
func roomyToolBar(tb *walk.ToolBar) {
	if tb == nil {
		return
	}
	h := tb.Handle()
	if style := win.GetWindowLong(h, win.GWL_STYLE); style&win.TBSTYLE_WRAPABLE != 0 {
		win.SetWindowLong(h, win.GWL_STYLE, style&^win.TBSTYLE_WRAPABLE)
	}
	dpi := tb.DPI()
	cx, cy := walk.IntFrom96DPI(14, dpi), walk.IntFrom96DPI(10, dpi)
	tb.SendMessage(win.TB_SETPADDING, 0, uintptr(cx|cy<<16))
	tb.SendMessage(win.TB_AUTOSIZE, 0, 0)
	tb.RequestLayout()
}

// ---- action rows

// actionRow is a command drawn as a row of an actions pane: an icon and
// its text, highlighted under the pointer. The whole row takes the click,
// where the link it replaces only took it on the letters of its text.
// Every command of a pane is also in the menu bar or a list's context
// menu, which is how the keyboard reaches it.
type actionRow struct {
	cmd     *command
	cw      *walk.CustomWidget
	bg      walk.Color
	hover   bool
	pressed bool
	hooked  bool
}

func (r *actionRow) widget() Widget {
	return CustomWidget{
		AssignTo:            &r.cw,
		Visible:             r.cmd.visible,
		Enabled:             r.cmd.enabled,
		MinSize:             Size{Width: 140, Height: 32},
		MaxSize:             Size{Height: 32},
		Style:               win.WS_TABSTOP,
		PaintMode:           PaintBuffered,
		InvalidatesOnResize: true,
		PaintPixels:         r.paint,
		// walk captures the mouse while a button is down, so the row
		// hears of moves and the release outside it too.
		OnMouseMove: func(x, y int, _ walk.MouseButton) {
			r.init() // in case syncCommands has not
			r.setHover(r.inside(x, y))
		},
		OnMouseDown: func(x, y int, b walk.MouseButton) {
			if b == walk.LeftButton {
				r.pressed = true
				r.cw.Invalidate()
			}
		},
		OnMouseUp: func(x, y int, b walk.MouseButton) {
			if b != walk.LeftButton || !r.pressed {
				return
			}
			r.pressed = false
			r.cw.Invalidate()
			if r.inside(x, y) { // not when dragged off and let go
				r.cmd.trigger()
			}
		},
		OnKeyDown: func(key walk.Key) {
			if key == walk.KeyReturn || key == walk.KeySpace {
				r.cmd.trigger()
			}
		},
	}
}

// init gives the row a hand cursor and lets it know when the pointer
// leaves, once it exists (syncCommands, after the window is built).
func (r *actionRow) init() {
	if r.hooked || r.cw == nil {
		return
	}
	r.hooked = true
	r.cw.SetCursor(walk.CursorHand())
	r.cw.FocusedChanged().Attach(func() { r.cw.Invalidate() })
	onMouseLeave(r.cw, func() {
		r.pressed = false
		r.setHover(false)
	})
}

// inside reports whether a point of the row's client area (in native
// pixels, as walk's mouse events give them) is on the row.
func (r *actionRow) inside(x, y int) bool {
	b := r.cw.ClientBoundsPixels()
	return x >= b.X && y >= b.Y && x < b.X+b.Width && y < b.Y+b.Height
}

func (r *actionRow) setHover(on bool) {
	if r.hover != on {
		r.hover = on
		r.cw.Invalidate()
	}
}

// update shows the command's current text, icon and state.
func (r *actionRow) update() {
	if r.cw == nil {
		return
	}
	if r.cw.Enabled() != r.cmd.enabled {
		r.cw.SetEnabled(r.cmd.enabled)
		r.hover, r.pressed = false, false
	}
	r.cw.Invalidate()
}

func (r *actionRow) paint(c *walk.Canvas, _ walk.Rectangle) error {
	b := r.cw.ClientBoundsPixels()
	px := func(v int) int { return walk.IntFrom96DPI(v, c.DPI()) }
	c.FillRectanglePixels(brush(r.bg), b)

	enabled := r.cmd.enabled
	radius := walk.Size{Width: px(8), Height: px(8)}
	switch {
	case enabled && r.pressed:
		c.FillRoundedRectanglePixels(brush(colorPressed), b, radius)
	case enabled && r.hover:
		c.FillRoundedRectanglePixels(brush(colorHover), b, radius)
	}
	if r.cw.Focused() {
		if pen := accentPen(); pen != nil {
			inner := walk.Rectangle{X: b.X + 1, Y: b.Y + 1, Width: b.Width - 2, Height: b.Height - 2}
			c.DrawRoundedRectanglePixels(pen, inner, radius)
		}
	}

	icon := px(16)
	x := b.X + px(10)
	if im := r.cmd.currentIcon(); im != nil {
		c.DrawImageStretchedPixels(im, walk.Rectangle{X: x, Y: b.Y + (b.Height-icon)/2, Width: icon, Height: icon})
	}
	x += icon + px(10)
	color := colorText
	if !enabled {
		color = colorDisabled
	}
	text := walk.Rectangle{X: x, Y: b.Y, Width: b.X + b.Width - x - px(8), Height: b.Height}
	return c.DrawTextPixels(r.cmd.text, r.cw.Font(), color, text,
		walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis|walk.TextNoPrefix)
}

var focusPen *walk.CosmeticPen

func accentPen() walk.Pen {
	if focusPen == nil {
		focusPen, _ = walk.NewCosmeticPen(walk.PenSolid, colorAccent)
	}
	if focusPen == nil {
		return nil
	}
	return focusPen
}

// ---- mouse leave

// walk tells a widget when the mouse moves over it but not when it
// leaves; onMouseLeave subclasses the window to ask Windows for that
// (TrackMouseEvent) and report it.

type leaveHook struct {
	orig     uintptr
	tracking bool
	fn       func()
}

var (
	leaveHooks = map[win.HWND]*leaveHook{}
	leaveProc  = syscall.NewCallback(leaveWndProc)
)

func onMouseLeave(w walk.Window, fn func()) {
	h := w.Handle()
	if _, ok := leaveHooks[h]; ok || h == 0 {
		return
	}
	hook := &leaveHook{fn: fn}
	leaveHooks[h] = hook
	hook.orig = win.SetWindowLongPtr(h, win.GWLP_WNDPROC, leaveProc)
}

func leaveWndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	hook := leaveHooks[hwnd]
	if hook == nil {
		return win.DefWindowProc(hwnd, msg, wParam, lParam)
	}
	orig := hook.orig
	switch msg {
	case win.WM_MOUSEMOVE:
		if !hook.tracking {
			tme := win.TRACKMOUSEEVENT{DwFlags: win.TME_LEAVE, HwndTrack: hwnd}
			tme.CbSize = uint32(unsafe.Sizeof(tme))
			hook.tracking = win.TrackMouseEvent(&tme)
		}
	case win.WM_MOUSELEAVE:
		hook.tracking = false
		hook.fn()
	case win.WM_NCDESTROY:
		win.SetWindowLongPtr(hwnd, win.GWLP_WNDPROC, orig)
		delete(leaveHooks, hwnd)
	}
	return win.CallWindowProc(orig, hwnd, msg, wParam, lParam)
}
