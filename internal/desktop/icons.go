package desktop

import (
	"image"
	"image/color"
	"math"
	"slices"
	"strings"
)

// The desktop manager's icon set: line icons on a 24-unit grid, drawn with
// 2-unit round strokes and a translucent fill of the same color (a two-tone
// style that stays legible at 16 pixels). Each icon is described by signed
// distance functions, so every size, and each variant, is drawn from the
// same description: the plain icon, a disabled (grey) one, a white glyph on
// a colored tile for page headers, and site types with a status badge.
//
// mkicon renders them into the executable's icon resources, where Windows
// picks the right size for every display scaling; nothing here depends on
// Windows, so the drawings are tested (and can be previewed) on any OS.

// Icon names. The resource of an icon is ResourceName(name).
const (
	IconServer      = "server"
	IconSites       = "sites"
	IconNode        = "node"
	IconWorker      = "worker"
	IconStatic      = "static"
	IconProxy       = "proxy"
	IconRedirect    = "redirect"
	IconCertificate = "certificate"
	IconMail        = "mail"
	IconNodeVersion = "node-versions"
	IconUsers       = "users"
	IconUser        = "user"
	IconBans        = "bans"
	IconActivity    = "activity"
	IconBackups     = "backups"

	IconStart     = "start"
	IconStop      = "stop"
	IconPause     = "pause"
	IconRestart   = "restart"
	IconRecycle   = "recycle"
	IconRefresh   = "refresh"
	IconBrowse    = "browse"
	IconOpen      = "open"
	IconRemove    = "remove"
	IconAdd       = "add"
	IconEdit      = "edit"
	IconSettings  = "settings"
	IconFolder    = "folder"
	IconLog       = "log"
	IconTerminal  = "terminal"
	IconSearch    = "search"
	IconCopy      = "copy"
	IconKey       = "key"
	IconLock      = "lock"
	IconHTTPS     = "https"
	IconHTTP      = "http"
	IconUnlock    = "unlock"
	IconOK        = "ok"
	IconWarning   = "warning"
	IconError     = "error"
	IconInfo      = "info"
	IconClock     = "clock"
	IconTasks     = "tasks"
	IconRun       = "run"
	IconCancel    = "cancel"
	IconHistory   = "history"
	IconDeploy    = "deploy"
	IconPackage   = "package"
	IconStar      = "star"
	IconCPU       = "cpu"
	IconMemory    = "memory"
	IconDisk      = "disk"
	IconTag       = "tag"
	IconWindows   = "windows"
	IconPlug      = "plug"
	IconConsole   = "console"
	IconSend      = "send"
	IconCheckList = "check-list"
	IconFilter    = "filter"
	IconEye       = "eye"
	IconPower     = "power"
	IconExit      = "exit"
	IconEraser    = "eraser"
	IconSave      = "save"
	IconDownload  = "download"
	IconUpload    = "upload"
	IconImport    = "import"
	IconUp        = "up"
	IconDown      = "down"
	IconToggle    = "toggle"
	IconPhone     = "phone"
	IconIDCard    = "id-card"
	IconShield    = "shield"
	IconBan       = "ban"
	IconLink      = "link"
	IconBraces    = "braces"
	IconRoute     = "route"
	IconFileCode  = "file-code"
	IconMailQueue = "mail-queue"
	IconGlobe     = "globe"
	IconHelp      = "help"
	IconDot       = "dot"
)

// Colors of the icon set, a Fluent-like palette that reads on light and
// dark backgrounds alike.
var (
	cBlue    = color.NRGBA{0x25, 0x63, 0xeb, 0xff}
	cTeal    = color.NRGBA{0x0f, 0x76, 0x6e, 0xff}
	cGreen   = color.NRGBA{0x16, 0xa3, 0x4a, 0xff}
	cNode    = color.NRGBA{0x3c, 0x87, 0x3a, 0xff}
	cRed     = color.NRGBA{0xdc, 0x26, 0x26, 0xff}
	cAmber   = color.NRGBA{0xd9, 0x77, 0x06, 0xff}
	cGold    = color.NRGBA{0xca, 0x8a, 0x04, 0xff}
	cViolet  = color.NRGBA{0x7c, 0x3a, 0xed, 0xff}
	cIndigo  = color.NRGBA{0x4f, 0x46, 0xe5, 0xff}
	cCyan    = color.NRGBA{0x08, 0x91, 0xb2, 0xff}
	cSlate   = color.NRGBA{0x47, 0x55, 0x69, 0xff}
	cOrange  = color.NRGBA{0xea, 0x58, 0x0c, 0xff}
	cBrown   = color.NRGBA{0xb4, 0x53, 0x09, 0xff}
	cWinBlue = color.NRGBA{0x00, 0x78, 0xd4, 0xff}
	cGrey    = color.NRGBA{0x8a, 0x90, 0x99, 0xff}
)

// ---- shapes: signed distance functions on the 24-unit grid (negative
// inside). Open paths have no inside; their distance is never negative.

type sdf func(x, y float64) float64

// segDist is distToSegment that also takes a zero-length segment (as where
// a path joins an arc starting at its last point), which would divide by 0.
func segDist(px, py, ax, ay, bx, by float64) float64 {
	if ax == bx && ay == by {
		return math.Hypot(px-ax, py-ay)
	}
	return distToSegment(px, py, ax, ay, bx, by)
}

// pl is an open polyline through x0,y0, x1,y1…
func pl(pts ...float64) sdf {
	return func(x, y float64) float64 {
		d := math.Inf(1)
		for i := 0; i+3 < len(pts); i += 2 {
			d = math.Min(d, segDist(x, y, pts[i], pts[i+1], pts[i+2], pts[i+3]))
		}
		return d
	}
}

func seg(x1, y1, x2, y2 float64) sdf { return pl(x1, y1, x2, y2) }

// pg is a closed polygon, signed (even-odd).
func pg(pts ...float64) sdf {
	closed := append(slices.Clone(pts), pts[0], pts[1])
	edge := pl(closed...)
	return func(x, y float64) float64 {
		d := edge(x, y)
		in := false
		for i, j := 0, len(pts)-2; i < len(pts); j, i = i, i+2 {
			xi, yi, xj, yj := pts[i], pts[i+1], pts[j], pts[j+1]
			if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
				in = !in
			}
		}
		if in {
			return -d
		}
		return d
	}
}

func circ(cx, cy, r float64) sdf {
	return func(x, y float64) float64 { return math.Hypot(x-cx, y-cy) - r }
}

// rr is a rounded rectangle.
func rr(x, y, w, h, r float64) sdf {
	cx, cy, hx, hy := x+w/2, y+h/2, w/2-r, h/2-r
	return func(px, py float64) float64 {
		qx, qy := math.Abs(px-cx)-hx, math.Abs(py-cy)-hy
		out := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
		return out + math.Min(math.Max(qx, qy), 0) - r
	}
}

// capsule is a segment thickened by r.
func capsule(x1, y1, x2, y2, r float64) sdf {
	return func(x, y float64) float64 { return segDist(x, y, x1, y1, x2, y2) - r }
}

func union(ds ...sdf) sdf {
	return func(x, y float64) float64 {
		d := math.Inf(1)
		for _, f := range ds {
			d = math.Min(d, f(x, y))
		}
		return d
	}
}

// arcPts samples an elliptical arc from angle a0 to a1 (degrees, clockwise
// on screen, 0 pointing right) into polyline points.
func arcPts(cx, cy, rx, ry, a0, a1 float64) []float64 {
	n := int(math.Max(4, math.Abs(a1-a0)/6))
	pts := make([]float64, 0, 2*(n+1))
	for i := 0; i <= n; i++ {
		a := (a0 + (a1-a0)*float64(i)/float64(n)) * math.Pi / 180
		pts = append(pts, cx+rx*math.Cos(a), cy+ry*math.Sin(a))
	}
	return pts
}

func arc(cx, cy, r, a0, a1 float64) sdf { return pl(arcPts(cx, cy, r, r, a0, a1)...) }

func ellipse(cx, cy, rx, ry float64) sdf { return pl(arcPts(cx, cy, rx, ry, 0, 360)...) }

// arcArrow is an arc with an arrowhead at its end, pointing along it.
func arcArrow(cx, cy, r, a0, a1 float64) sdf {
	a := a1 * math.Pi / 180
	ex, ey := cx+r*math.Cos(a), cy+r*math.Sin(a)
	dir := 1.0
	if a1 < a0 {
		dir = -1
	}
	tx, ty := -math.Sin(a)*dir, math.Cos(a)*dir // tangent, direction of travel
	return union(arc(cx, cy, r, a0, a1), head(ex, ey, tx, ty, 4.2))
}

// head is an arrowhead (a chevron) with its tip at x,y pointing along dx,dy.
func head(x, y, dx, dy, size float64) sdf {
	rot := func(deg float64) (float64, float64) {
		s, c := math.Sincos(deg * math.Pi / 180)
		return dx*c - dy*s, dx*s + dy*c
	}
	ax, ay := rot(150)
	bx, by := rot(-150)
	return pl(x+ax*size, y+ay*size, x, y, x+bx*size, y+by*size)
}

// ---- layers

type layerKind int

const (
	stroke layerKind = iota
	fill
	knockout // clears what is below
)

type tone int

const (
	toneAccent tone = iota // the icon's color
	toneTint               // the icon's color, translucent
	toneWhite              // white (marks inside filled shapes)
	toneFixed              // the layer's own color
)

type layer struct {
	kind  layerKind
	d     sdf
	width float64
	tone  tone
	fixed color.NRGBA
}

const strokeWidth = 2

// S strokes d in the icon's color.
func S(d sdf) layer { return layer{kind: stroke, d: d, width: strokeWidth} }

// T fills d with the translucent tint.
func T(d sdf) layer { return layer{kind: fill, d: d, tone: toneTint} }

// ST is the usual pair: tinted inside, stroked outline.
func ST(d sdf) []layer { return []layer{T(d), S(d)} }

// F fills d in the icon's color.
func F(d sdf) layer { return layer{kind: fill, d: d} }

// WS strokes d in white.
func WS(d sdf) layer { return layer{kind: stroke, d: d, width: 2.2, tone: toneWhite} }

// WF fills d in white.
func WF(d sdf) layer { return layer{kind: fill, d: d, tone: toneWhite} }

func withColor(c color.NRGBA, l layer) layer {
	l.tone, l.fixed = toneFixed, c
	return l
}

func (l layer) covers(x, y float64) bool {
	switch l.kind {
	case stroke:
		return math.Abs(l.d(x, y)) <= l.width/2
	default:
		return l.d(x, y) <= 0
	}
}

type glyph struct {
	accent color.NRGBA
	layers []layer
}

func icon(accent color.NRGBA, parts ...any) glyph {
	g := glyph{accent: accent}
	for _, p := range parts {
		switch v := p.(type) {
		case layer:
			g.layers = append(g.layers, v)
		case []layer:
			g.layers = append(g.layers, v...)
		}
	}
	return g
}

// ---- the icons

func filePath() sdf { return pg(6, 2.5, 14, 2.5, 19.5, 8, 19.5, 21.5, 6, 21.5) }
func fileFold() sdf { return pl(14, 2.5, 14, 8, 19.5, 8) }

func shieldPath() sdf {
	return pg(12, 2.5, 19.5, 5.5, 19.5, 11, 18.3, 15.2, 15.5, 18.6, 12, 21.5, 8.5, 18.6, 5.7, 15.2, 4.5, 11, 4.5, 5.5)
}

func hexagon(cx, cy, r float64) sdf {
	var pts []float64
	for i := 0; i < 6; i++ {
		a := (-90 + 60*float64(i)) * math.Pi / 180
		pts = append(pts, cx+r*math.Cos(a), cy+r*math.Sin(a))
	}
	return pg(pts...)
}

// rbox is a rectangle of half-sizes hx, hy centered at cx,cy and turned by
// angle a (radians), with corners rounded by r.
func rbox(cx, cy, hx, hy, a, r float64) sdf {
	s, c := math.Sincos(a)
	box := rr(-hx, -hy, 2*hx, 2*hy, r)
	return func(x, y float64) float64 {
		dx, dy := x-cx, y-cy
		return box(dx*c+dy*s, -dx*s+dy*c)
	}
}

func gear() sdf {
	parts := []sdf{circ(12, 12, 6.8)}
	for i := 0; i < 8; i++ {
		a := float64(i) * math.Pi / 4
		parts = append(parts, rbox(12+7.6*math.Cos(a), 12+7.6*math.Sin(a), 1.7, 1.6, a, 0.4))
	}
	return union(parts...)
}

func star(cx, cy, ro, ri float64) sdf {
	var pts []float64
	for i := 0; i < 10; i++ {
		r := ro
		if i%2 == 1 {
			r = ri
		}
		a := (-90 + 36*float64(i)) * math.Pi / 180
		pts = append(pts, cx+r*math.Cos(a), cy+r*math.Sin(a))
	}
	return pg(pts...)
}

func tray() sdf { return pl(3.5, 15.5, 3.5, 20.5, 20.5, 20.5, 20.5, 15.5) }

func lockBody() sdf { return rr(4, 10.5, 16, 11, 2.5) }

var glyphs = map[string]glyph{
	IconServer: icon(cBlue,
		ST(rr(3, 3, 18, 7.5, 2)), ST(rr(3, 13.5, 18, 7.5, 2)),
		withColor(cGreen, F(circ(7, 6.75, 1.4))), withColor(cGreen, F(circ(7, 17.25, 1.4))),
		S(seg(11.5, 6.75, 17, 6.75)), S(seg(11.5, 17.25, 17, 17.25))),
	IconSites: icon(cTeal, ST(circ(12, 12, 9)), S(seg(3, 12, 21, 12)), S(ellipse(12, 12, 4, 9))),
	IconGlobe: icon(cBlue, ST(circ(12, 12, 9)), S(seg(3, 12, 21, 12)), S(ellipse(12, 12, 4, 9))),
	IconNode: icon(cNode, ST(hexagon(12, 12, 9.5)),
		S(pl(9.5, 15.5, 9.5, 9, 14.5, 15, 14.5, 8.5))),
	IconWorker: icon(cOrange, ST(gear()), S(circ(12, 12, 2.6))),
	IconStatic: icon(cIndigo, ST(filePath()), S(fileFold()),
		S(pl(10.5, 12.5, 8.5, 14.75, 10.5, 17)), S(pl(13.5, 12.5, 15.5, 14.75, 13.5, 17))),
	IconProxy: icon(cViolet,
		S(seg(4, 8, 19, 8)), S(pl(15, 4, 19, 8, 15, 12)),
		S(seg(20, 16, 5, 16)), S(pl(9, 12, 5, 16, 9, 20))),
	IconRedirect: icon(cCyan,
		S(pl(append(append([]float64{4, 20.5, 4, 13}, arcPts(8.5, 13, 4.5, 4.5, 180, 270)...), 20, 8.5)...)),
		S(pl(15.5, 4, 20, 8.5, 15.5, 13))),
	IconCertificate: icon(cGold, ST(circ(12, 9, 6)),
		S(pl(8.3, 13.8, 7, 21.5, 12, 19, 17, 21.5, 15.7, 13.8))),
	IconMail:      icon(cBlue, ST(rr(2.5, 5, 19, 14, 2.5)), S(pl(3.5, 7, 12, 13, 20.5, 7))),
	IconMailQueue: icon(cBlue, ST(rr(2.5, 5, 19, 14, 2.5)), S(pl(3.5, 7, 12, 13, 20.5, 7)), withColor(cAmber, F(circ(19, 17, 4.5))), WS(pl(19, 15, 19, 17.2, 20.4, 18))),
	IconNodeVersion: icon(cNode, ST(pg(12, 3, 21.5, 8, 12, 13, 2.5, 8)),
		S(pl(2.5, 12.5, 12, 17.5, 21.5, 12.5)), S(pl(2.5, 17, 12, 22, 21.5, 17))),
	IconUsers: icon(cViolet, ST(circ(9, 7.5, 3.7)), S(arc(9, 21.5, 6.5, 180, 360)),
		S(arc(15.5, 7.5, 3.7, -75, 75)), S(arc(16.5, 21.5, 5.5, 270, 360))),
	IconUser:     icon(cViolet, ST(circ(12, 8, 4.2)), S(arc(12, 21.5, 7.5, 180, 360))),
	IconBans:     icon(cRed, ST(circ(12, 12, 9)), S(seg(5.6, 5.6, 18.4, 18.4))),
	IconBan:      icon(cRed, ST(circ(12, 12, 9)), S(seg(5.6, 5.6, 18.4, 18.4))),
	IconActivity: icon(cIndigo, S(pl(2.5, 12, 6.5, 12, 9.5, 4, 14.5, 20, 17.5, 12, 21.5, 12))),
	IconBackups: icon(cCyan, ST(rr(2.5, 3.5, 19, 5, 1.5)), ST(rr(4, 8.5, 16, 12, 2)),
		S(seg(10, 12.5, 14, 12.5))),

	IconStart:   icon(cGreen, F(pg(7.5, 4.5, 19, 12, 7.5, 19.5)), S(pg(7.5, 4.5, 19, 12, 7.5, 19.5))),
	IconStop:    icon(cRed, F(rr(5.5, 5.5, 13, 13, 2.5))),
	IconPause:   icon(cAmber, F(rr(6, 5, 4.2, 14, 1.4)), F(rr(13.8, 5, 4.2, 14, 1.4))),
	IconRestart: icon(cOrange, S(arcArrow(12, 12, 8.5, 20, 305))),
	IconRecycle: icon(cTeal, S(arcArrow(12, 12, 8, 200, 325)), S(arcArrow(12, 12, 8, 20, 145)), F(circ(12, 12, 1.8))),
	IconRefresh: icon(cBlue, S(arcArrow(12, 12, 8, 200, 325)), S(arcArrow(12, 12, 8, 20, 145))),
	IconBrowse: icon(cBlue, S(pl(15, 3, 21, 3, 21, 9)), S(seg(10.5, 13.5, 21, 3)),
		S(pl(18, 13.5, 18, 19.5, 16.5, 21, 4.5, 21, 3, 19.5, 3, 7.5, 4.5, 6, 10.5, 6))),
	IconOpen: icon(cBlue, ST(circ(12, 12, 9)), S(seg(7.5, 12, 16, 12)), S(pl(12.5, 8.5, 16, 12, 12.5, 15.5))),
	IconRemove: icon(cRed, S(seg(3.5, 6.5, 20.5, 6.5)), S(pl(9, 6.5, 9, 3.5, 15, 3.5, 15, 6.5)),
		T(pg(5.8, 6.5, 7, 20.5, 17, 20.5, 18.2, 6.5)), S(pl(5.8, 6.5, 7, 20.5, 17, 20.5, 18.2, 6.5)),
		S(seg(10, 10.5, 10, 16.5)), S(seg(14, 10.5, 14, 16.5))),
	IconAdd:  icon(cGreen, layer{kind: stroke, d: seg(12, 4.5, 12, 19.5), width: 2.4}, layer{kind: stroke, d: seg(4.5, 12, 19.5, 12), width: 2.4}),
	IconEdit: icon(cSlate, ST(pg(3.5, 20.5, 4.5, 16, 16, 4.5, 19.5, 8, 8, 19.5)), S(seg(13.8, 6.7, 17.3, 10.2))),
	IconSettings: icon(cSlate, S(seg(3, 7, 21, 7)), S(seg(3, 17, 21, 17)),
		WF(circ(15, 7, 2.7)), S(circ(15, 7, 2.7)), WF(circ(8.5, 17, 2.7)), S(circ(8.5, 17, 2.7))),
	IconFolder:   icon(cAmber, ST(pg(2.5, 5, 9, 5, 11, 7.5, 21.5, 7.5, 21.5, 19.5, 2.5, 19.5))),
	IconLog:      icon(cSlate, ST(filePath()), S(fileFold()), S(seg(9, 13, 15.5, 13)), S(seg(9, 17, 13.5, 17))),
	IconTerminal: icon(cSlate, ST(rr(2.5, 4, 19, 16, 2.5)), S(pl(7, 9.5, 10, 12.5, 7, 15.5)), S(seg(12.5, 15.5, 17, 15.5))),
	IconSearch:   icon(cSlate, ST(circ(10.5, 10.5, 6.5)), S(seg(15.5, 15.5, 20.5, 20.5))),
	IconCopy: icon(cSlate, ST(rr(8.5, 8.5, 12.5, 12.5, 2)),
		S(pl(5, 15.5, 4, 15.2, 3.3, 14.5, 3, 13.5, 3, 4.5, 3.5, 3.5, 4.5, 3, 13.5, 3, 14.5, 3.3, 15.2, 4, 15.5, 5))),
	IconKey:     icon(cGold, ST(circ(7.5, 15.5, 4.5)), S(seg(10.7, 12.3, 20.5, 2.5)), S(seg(15.5, 7.5, 18.5, 10.5)), S(seg(18.2, 4.8, 20.6, 7.2))),
	IconLock:    icon(cGold, ST(lockBody()), S(pl(append(append([]float64{7.5, 10.5}, arcPts(12, 7.5, 4.5, 4.5, 180, 360)...), 16.5, 10.5)...))),
	IconHTTPS:   icon(cGreen, ST(lockBody()), S(pl(append(append([]float64{7.5, 10.5}, arcPts(12, 7.5, 4.5, 4.5, 180, 360)...), 16.5, 10.5)...))),
	IconHTTP:    icon(cSlate, ST(circ(12, 12, 9)), S(seg(3, 12, 21, 12)), S(ellipse(12, 12, 4, 9))),
	IconUnlock:  icon(cGreen, ST(lockBody()), S(pl(append([]float64{7.5, 10.5}, arcPts(12, 7.5, 4.5, 4.5, 180, 325)...)...))),
	IconOK:      icon(cGreen, F(circ(12, 12, 10)), WS(pl(7.5, 12.5, 10.5, 15.5, 16.5, 9))),
	IconWarning: icon(cAmber, F(pg(12, 3, 21.5, 20, 2.5, 20)), S(pg(12, 3, 21.5, 20, 2.5, 20)), WS(seg(12, 9, 12, 13.5)), WF(circ(12, 16.8, 1.25))),
	IconError:   icon(cRed, F(circ(12, 12, 10)), WS(seg(8.5, 8.5, 15.5, 15.5)), WS(seg(15.5, 8.5, 8.5, 15.5))),
	IconInfo:    icon(cBlue, F(circ(12, 12, 10)), WS(seg(12, 11, 12, 16.5)), WF(circ(12, 7.7, 1.35))),
	IconHelp: icon(cBlue, F(circ(12, 12, 10)),
		WS(pl(append(arcPts(12, 9.5, 3, 3, 190, 400), 12, 14)...)), WF(circ(12, 17.2, 1.3))),
	IconClock: icon(cIndigo, ST(circ(12, 12, 9)), S(pl(12, 7, 12, 12, 15.5, 14))),
	IconTasks: icon(cIndigo, ST(rr(3, 4.5, 18, 16.5, 2.5)), S(seg(3, 9.5, 21, 9.5)), S(seg(8, 2.5, 8, 6.5)), S(seg(16, 2.5, 16, 6.5)),
		F(circ(8, 14, 1.3)), F(circ(12, 14, 1.3)), F(circ(16, 14, 1.3)), F(circ(8, 17.5, 1.3))),
	IconRun:    icon(cGreen, ST(circ(12, 12, 9)), F(pg(10, 8.3, 15.8, 12, 10, 15.7)), S(pg(10, 8.3, 15.8, 12, 10, 15.7))),
	IconCancel: icon(cRed, ST(circ(12, 12, 9)), S(seg(9, 9, 15, 15)), S(seg(15, 9, 9, 15))),
	IconHistory: icon(cViolet, S(arcArrow(12, 12, 8.5, 540, 215)),
		S(pl(12, 7.5, 12, 12, 15, 14))),
	IconDeploy: icon(cOrange, ST(pg(12, 2.5, 15.3, 6.3, 15.6, 15, 8.4, 15, 8.7, 6.3)), S(circ(12, 9, 1.6)),
		S(pl(8.5, 11.5, 5.5, 14.5, 5.5, 17.5, 8.4, 15.5)), S(pl(15.5, 11.5, 18.5, 14.5, 18.5, 17.5, 15.6, 15.5)),
		S(seg(12, 18.5, 12, 21.5))),
	IconPackage: icon(cBrown, ST(pg(12, 2.5, 20.5, 7, 20.5, 17, 12, 21.5, 3.5, 17, 3.5, 7)),
		S(pl(3.5, 7, 12, 11.5, 20.5, 7)), S(seg(12, 11.5, 12, 21.5)), S(seg(7.8, 4.7, 16.3, 9.3))),
	IconStar: icon(cGold, F(star(12, 12.6, 9.6, 4.1)), S(star(12, 12.6, 9.6, 4.1))),
	IconCPU: icon(cSlate, ST(rr(5.5, 5.5, 13, 13, 2)), F(rr(9.3, 9.3, 5.4, 5.4, 1)),
		S(seg(9.5, 2, 9.5, 5.5)), S(seg(14.5, 2, 14.5, 5.5)), S(seg(9.5, 18.5, 9.5, 22)), S(seg(14.5, 18.5, 14.5, 22)),
		S(seg(2, 9.5, 5.5, 9.5)), S(seg(2, 14.5, 5.5, 14.5)), S(seg(18.5, 9.5, 22, 9.5)), S(seg(18.5, 14.5, 22, 14.5))),
	IconMemory: icon(cSlate, ST(rr(2.5, 6, 19, 10.5, 2)),
		F(rr(6, 9, 3, 4.5, .6)), F(rr(10.5, 9, 3, 4.5, .6)), F(rr(15, 9, 3, 4.5, .6)),
		S(seg(6.5, 16.5, 6.5, 19.5)), S(seg(12, 16.5, 12, 19.5)), S(seg(17.5, 16.5, 17.5, 19.5))),
	IconDisk: icon(cSlate, ST(rr(2.5, 12.5, 19, 8, 2)), S(pl(2.8, 13.5, 6, 5, 18, 5, 21.2, 13.5)),
		F(circ(6.5, 16.5, 1.2)), F(circ(10, 16.5, 1.2))),
	IconTag:     icon(cSlate, ST(pg(3, 3, 11.5, 3, 21, 12.5, 12.5, 21, 3, 11.5)), F(circ(7.5, 7.5, 1.6))),
	IconWindows: icon(cWinBlue, F(rr(3, 3, 8.4, 8.4, 1)), F(rr(12.6, 3, 8.4, 8.4, 1)), F(rr(3, 12.6, 8.4, 8.4, 1)), F(rr(12.6, 12.6, 8.4, 8.4, 1))),
	IconPlug: icon(cTeal, S(seg(9, 2.5, 9, 7)), S(seg(15, 2.5, 15, 7)),
		ST(pg(5.5, 7, 18.5, 7, 18.5, 11, 16.5, 14.8, 12, 17, 7.5, 14.8, 5.5, 11)), S(seg(12, 17, 12, 21.5))),
	IconConsole: icon(cBlue, ST(rr(2.5, 4, 19, 16, 2.5)), S(seg(2.5, 9, 21.5, 9)),
		F(circ(6, 6.5, 1)), F(circ(9, 6.5, 1)), S(seg(6.5, 13.5, 13, 13.5)), S(seg(6.5, 16.5, 10.5, 16.5))),
	IconSend: icon(cBlue, ST(pg(21.5, 2.5, 14.5, 21.5, 10.5, 13.5, 2.5, 9.5)), S(seg(10.5, 13.5, 21.5, 2.5))),
	IconCheckList: icon(cGreen, ST(rr(4.5, 4, 15, 17.5, 2)), WF(rr(8.5, 2.5, 7, 4, 1.2)), S(rr(8.5, 2.5, 7, 4, 1.2)),
		S(pl(8.5, 13.5, 11, 16, 15.5, 11))),
	IconFilter: icon(cSlate, ST(pg(3, 4, 21, 4, 14, 12.5, 14, 19.5, 10, 21.5, 10, 12.5))),
	IconEye:    icon(cSlate, T(ellipseFill(12, 12, 10, 6.5)), S(ellipse(12, 12, 10, 6.5)), S(circ(12, 12, 3)), F(circ(12, 12, 1.2))),
	IconPower:  icon(cRed, S(arc(12, 13, 8, 300, 600)), S(seg(12, 2.5, 12, 11))),
	IconExit: icon(cSlate, S(pl(9, 21, 5, 21, 3.5, 19.5, 3.5, 4.5, 5, 3, 9, 3)), S(seg(10, 12, 21, 12)),
		S(pl(16, 7, 21, 12, 16, 17))),
	IconEraser: icon(cOrange, ST(pg(3.5, 15.5, 13, 6, 20, 13, 12.5, 20.5, 8.5, 20.5)), S(seg(8.5, 10.5, 15.5, 17.5)),
		S(seg(12.5, 20.5, 21, 20.5))),
	IconSave: icon(cBlue, ST(pg(5, 3, 16, 3, 21, 8, 21, 19, 19, 21, 5, 21, 3, 19, 3, 5)),
		S(pl(7.5, 3, 7.5, 8, 15, 8)), S(pl(7, 21, 7, 14, 17, 14, 17, 21))),
	IconDownload: icon(cBlue, S(seg(12, 3, 12, 15)), S(pl(7, 10, 12, 15, 17, 10)), S(tray())),
	IconUpload:   icon(cBlue, S(seg(12, 15, 12, 3.5)), S(pl(7, 8.5, 12, 3.5, 17, 8.5)), S(tray())),
	IconImport: icon(cTeal, S(pl(15, 3, 19, 3, 21, 5, 21, 19, 19, 21, 15, 21)), S(seg(3, 12, 15, 12)),
		S(pl(10, 7, 15, 12, 10, 17))),
	IconUp:     icon(cSlate, S(seg(12, 19.5, 12, 4.5)), S(pl(5.5, 11, 12, 4.5, 18.5, 11))),
	IconDown:   icon(cSlate, S(seg(12, 4.5, 12, 19.5)), S(pl(5.5, 13, 12, 19.5, 18.5, 13))),
	IconToggle: icon(cTeal, ST(rr(2, 6.5, 20, 11, 5.5)), F(circ(16.5, 12, 3.2))),
	IconPhone:  icon(cSlate, ST(rr(6, 2.5, 12, 19, 2.5)), F(circ(12, 18, 1.2))),
	IconIDCard: icon(cViolet, ST(rr(2.5, 5, 19, 14, 2)), S(circ(8.5, 10.5, 2.3)), S(arc(8.5, 17.5, 3.5, 200, 340)),
		S(seg(14, 10, 18.5, 10)), S(seg(14, 14, 18.5, 14))),
	IconShield: icon(cGreen, ST(shieldPath()), S(pl(8.5, 12, 11, 14.5, 15.5, 9.5))),
	IconLink: icon(cBlue, S(ring(capsule(5.6, 18.4, 10.4, 13.6, 3.4))), S(ring(capsule(13.6, 10.4, 18.4, 5.6, 3.4))),
		S(seg(9.5, 14.5, 14.5, 9.5))),
	IconBraces: icon(cIndigo,
		S(pl(9.5, 3.5, 8, 3.5, 6.8, 4.7, 6.8, 10, 4.5, 12, 6.8, 14, 6.8, 19.3, 8, 20.5, 9.5, 20.5)),
		S(pl(14.5, 3.5, 16, 3.5, 17.2, 4.7, 17.2, 10, 19.5, 12, 17.2, 14, 17.2, 19.3, 16, 20.5, 14.5, 20.5))),
	IconRoute: icon(cViolet, ST(circ(5.5, 19, 2.6)), ST(circ(18.5, 5, 2.6)),
		S(pl(append(append(append(append([]float64{8.5, 19}, arcPts(17, 15.5, 3.5, 3.5, 90, -90)...), 7, 12), arcPts(7, 8.5, 3.5, 3.5, 90, 270)...), 15.5, 5)...))),
	IconFileCode: icon(cIndigo, ST(filePath()), S(fileFold()),
		S(pl(10.5, 12.5, 8.5, 14.75, 10.5, 17)), S(pl(13.5, 12.5, 15.5, 14.75, 13.5, 17))),
	IconDot: icon(cSlate, F(circ(12, 12, 5))),
}

// ellipseFill is a filled ellipse (approximate distance, enough for fills).
func ellipseFill(cx, cy, rx, ry float64) sdf {
	return func(x, y float64) float64 {
		return (math.Hypot((x-cx)/rx, (y-cy)/ry) - 1) * math.Min(rx, ry)
	}
}

// ring turns a filled shape into its outline, for stroking.
func ring(d sdf) sdf { return func(x, y float64) float64 { return math.Abs(d(x, y)) } }

// IconNames lists every icon, sorted.
func IconNames() []string {
	names := make([]string, 0, len(glyphs))
	for n := range glyphs {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// ResourceName is the name of an icon's resource in the executable:
// "UI_SITES" for "sites". Variants append a suffix: "UI_SITES_OFF".
func ResourceName(name string, suffix ...string) string {
	s := "UI_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	for _, x := range suffix {
		s += "_" + strings.ToUpper(x)
	}
	return s
}

// TileResourceName is the resource of an icon drawn as a page header tile.
func TileResourceName(name string) string {
	return "TILE_" + strings.TrimPrefix(ResourceName(name), "UI_")
}

// ---- rendering

// paint composites the layers of g at x,y (24-unit grid) and returns the
// color there, recolored by pal.
func (g glyph) paint(x, y float64, pal func(l layer) color.NRGBA) color.NRGBA {
	var r, gg, b, a float64 // premultiplied
	for _, l := range g.layers {
		if !l.covers(x, y) {
			continue
		}
		if l.kind == knockout {
			r, gg, b, a = 0, 0, 0, 0
			continue
		}
		c := pal(l)
		sa := float64(c.A) / 255
		r = float64(c.R)*sa + r*(1-sa)
		gg = float64(c.G)*sa + gg*(1-sa)
		b = float64(c.B)*sa + b*(1-sa)
		a = sa + a*(1-sa)
	}
	if a == 0 {
		return color.NRGBA{}
	}
	return color.NRGBA{uint8(math.Round(r / a)), uint8(math.Round(gg / a)), uint8(math.Round(b / a)), uint8(math.Round(a * 255))}
}

func alpha(c color.NRGBA, f float64) color.NRGBA {
	c.A = uint8(math.Round(float64(c.A) * f))
	return c
}

const tintAlpha = 0.16

// normalPalette colors layers as designed.
func (g glyph) normalPalette(l layer) color.NRGBA {
	switch l.tone {
	case toneTint:
		return alpha(g.accent, tintAlpha)
	case toneWhite:
		return white
	case toneFixed:
		return l.fixed
	}
	return g.accent
}

// disabledPalette greys everything out, as a disabled command shows.
func (g glyph) disabledPalette(l layer) color.NRGBA {
	switch l.tone {
	case toneWhite:
		return white
	case toneTint:
		return alpha(cGrey, tintAlpha*0.8)
	}
	return alpha(cGrey, 0.75)
}

// tilePalette is for the glyph on a tile: white marks on the tile's color.
func (g glyph) tilePalette(l layer) color.NRGBA {
	switch l.tone {
	case toneTint:
		return alpha(white, 0.22)
	case toneWhite:
		return g.accent
	case toneFixed:
		return l.fixed
	}
	return white
}

func lookup(name string) glyph {
	g, ok := glyphs[name]
	if !ok {
		return glyphs[IconDot]
	}
	return g
}

// Icon draws an icon at size×size pixels.
func Icon(name string, size int) *image.NRGBA {
	g := lookup(name)
	return render(size, func(x, y float64) color.NRGBA { return g.paint(x*0.75, y*0.75, g.normalPalette) })
}

// IconDisabled draws the grey version of an icon, for disabled commands.
func IconDisabled(name string, size int) *image.NRGBA {
	g := lookup(name)
	return render(size, func(x, y float64) color.NRGBA { return g.paint(x*0.75, y*0.75, g.disabledPalette) })
}

// IconTile draws an icon as a white glyph on a rounded tile of its color,
// lighter at the top: the page headers' icons.
func IconTile(name string, size int) *image.NRGBA {
	g := lookup(name)
	top := mix(g.accent, white, 0.18)
	return render(size, func(x, y float64) color.NRGBA {
		if !inRoundedRect(x, y, 0, 0, 32, 32, 7) {
			return color.NRGBA{}
		}
		bg := mix(top, g.accent, y/32)
		// The glyph fills 62% of the tile, centered.
		const scale = 0.62 * 32 / 24
		c := g.paint((x-16)/scale+12, (y-16)/scale+12, g.tilePalette)
		return over(c, bg)
	})
}

// IconBadged draws an icon with a status dot in its lower right corner,
// separated by a transparent gap, like the notification-area icon.
func IconBadged(name string, level Level, size int) *image.NRGBA {
	g := lookup(name)
	badge := levelColors[level]
	const cx, cy, r, gap = 24.5, 24.5, 6.5, 2.0
	return render(size, func(x, y float64) color.NRGBA {
		d := math.Hypot(x-cx, y-cy)
		switch {
		case d <= r:
			return badge
		case d <= r+gap:
			return color.NRGBA{}
		}
		return g.paint(x*0.75, y*0.75, g.normalPalette)
	})
}

// mix blends a toward b by t (0..1).
func mix(a, b color.NRGBA, t float64) color.NRGBA {
	t = math.Max(0, math.Min(1, t))
	f := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return color.NRGBA{f(a.R, b.R), f(a.G, b.G), f(a.B, b.B), f(a.A, b.A)}
}

// over composites src over an opaque dst.
func over(src, dst color.NRGBA) color.NRGBA {
	sa := float64(src.A) / 255
	f := func(s, d uint8) uint8 { return uint8(math.Round(float64(s)*sa + float64(d)*(1-sa))) }
	return color.NRGBA{f(src.R, dst.R), f(src.G, dst.G), f(src.B, dst.B), dst.A}
}

// SiteTypeIcon is the icon of a site type.
func SiteTypeIcon(t string) string {
	switch t {
	case "node":
		return IconNode
	case "worker":
		return IconWorker
	case "static":
		return IconStatic
	case "proxy":
		return IconProxy
	case "redirect":
		return IconRedirect
	}
	return IconSites
}

// SiteTypes are the site types that have a badged icon per level.
var SiteTypes = []string{"node", "worker", "static", "proxy", "redirect"}

// Levels lists every level, for drawing one icon per level.
var Levels = []Level{LevelOK, LevelWarning, LevelDown, LevelNotInstalled}

// DisabledSuffix marks the resource of an icon's disabled variant:
// ResourceName(name, DisabledSuffix).
const DisabledSuffix = "dis"

// LevelSlug names a level in resource names.
func LevelSlug(l Level) string {
	return [...]string{"ok", "warning", "down", "off"}[l]
}

// IconResource is one icon resource of the executable: its name, the
// sizes it holds and how to draw it.
type IconResource struct {
	Name  string
	Sizes []int
	Draw  func(size int) *image.NRGBA
}

var (
	smallSizes = []int{16, 20, 24, 32, 40, 48}
	offSizes   = []int{16, 20, 24, 32, 40}
	tileSizes  = []int{32, 40, 48, 64, 80}
)

// TileIcons are the icons also drawn as header tiles.
var TileIcons = []string{
	IconServer, IconSites, IconNode, IconWorker, IconStatic, IconProxy, IconRedirect, IconCertificate,
	IconMail, IconNodeVersion, IconUsers, IconBans, IconActivity, IconBackups, IconSettings, IconConsole,
	IconRoute, IconLink, IconBraces, IconFileCode, IconImport, IconDeploy, IconCheckList, IconUser, IconKey,
	IconBan, IconInfo, IconWarning, IconError, IconHistory, IconUpload, IconDownload,
	IconIDCard, IconSend, IconPackage, IconTerminal, IconLock, IconFolder, IconTag,
}

// IconResources lists every icon resource the desktop programs load, in a
// stable order.
func IconResources() []IconResource {
	var out []IconResource
	for _, n := range IconNames() {
		out = append(out,
			IconResource{ResourceName(n), smallSizes, func(s int) *image.NRGBA { return Icon(n, s) }},
			IconResource{ResourceName(n, DisabledSuffix), offSizes, func(s int) *image.NRGBA { return IconDisabled(n, s) }},
		)
	}
	for _, n := range TileIcons {
		out = append(out, IconResource{TileResourceName(n), tileSizes, func(s int) *image.NRGBA { return IconTile(n, s) }})
	}
	for _, t := range SiteTypes {
		for _, l := range Levels {
			n := SiteTypeIcon(t)
			out = append(out, IconResource{ResourceName(n, LevelSlug(l)), smallSizes, func(s int) *image.NRGBA { return IconBadged(n, l, s) }})
		}
	}
	for _, l := range Levels {
		out = append(out,
			IconResource{ResourceName(IconDot, LevelSlug(l)), smallSizes, func(s int) *image.NRGBA { return StatusDot(l, s) }},
			IconResource{"TRAY_" + strings.ToUpper(LevelSlug(l)), smallSizes, func(s int) *image.NRGBA { return StatusIcon(l, s) }},
		)
	}
	return out
}
