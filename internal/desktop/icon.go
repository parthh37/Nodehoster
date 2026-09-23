package desktop

import (
	"image"
	"image/color"
	"math"
)

// The NodeHoster mark (web/public/favicon.svg): a teal rounded square with
// a white "N", on a 32-unit grid.
var (
	brandTeal = color.NRGBA{0x0f, 0x76, 0x6e, 0xff}
	white     = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	nStroke   = [][2]float64{{9, 22}, {9, 10}, {23, 22}, {23, 10}}
)

// Badge colors, readable on both light and dark taskbars.
var levelColors = map[Level]color.NRGBA{
	LevelOK:           {0x2e, 0xa0, 0x43, 0xff},
	LevelWarning:      {0xe3, 0xa0, 0x08, 0xff},
	LevelDown:         {0xda, 0x36, 0x33, 0xff},
	LevelNotInstalled: {0x8b, 0x94, 0x9e, 0xff},
}

// AppIcon draws the mark at size×size pixels.
func AppIcon(size int) *image.NRGBA {
	return render(size, func(x, y float64) color.NRGBA { return mark(x, y) })
}

// StatusIcon draws the mark with a status badge in its lower right corner,
// separated from the mark by a transparent gap so it reads at 16 pixels.
func StatusIcon(level Level, size int) *image.NRGBA {
	badge := levelColors[level]
	const cx, cy, r, gap = 24.5, 24.5, 7.0, 2.0
	return render(size, func(x, y float64) color.NRGBA {
		d := math.Hypot(x-cx, y-cy)
		switch {
		case d <= r:
			return badge
		case d <= r+gap:
			return color.NRGBA{}
		}
		return mark(x, y)
	})
}

func mark(x, y float64) color.NRGBA {
	if !inRoundedRect(x, y, 0, 0, 32, 32, 7) {
		return color.NRGBA{}
	}
	for i := 0; i+1 < len(nStroke); i++ {
		a, b := nStroke[i], nStroke[i+1]
		if distToSegment(x, y, a[0], a[1], b[0], b[1]) <= 1.5 {
			return white
		}
	}
	return brandTeal
}

// render samples shade on a 32-unit grid, 4×4 times per pixel, and
// averages the samples (premultiplied) for smooth edges.
func render(size int, shade func(x, y float64) color.NRGBA) *image.NRGBA {
	const ss = 4
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	unit := 32 / float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					c := shade((float64(px)+(float64(sx)+0.5)/ss)*unit, (float64(py)+(float64(sy)+0.5)/ss)*unit)
					fa := float64(c.A) / 255
					r += float64(c.R) * fa
					g += float64(c.G) * fa
					b += float64(c.B) * fa
					a += fa
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(math.Round(r / a)), G: uint8(math.Round(g / a)), B: uint8(math.Round(b / a)),
				A: uint8(math.Round(a / (ss * ss) * 255)),
			})
		}
	}
	return img
}

func inRoundedRect(x, y, x0, y0, x1, y1, rad float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := math.Max(x0+rad, math.Min(x, x1-rad))
	cy := math.Max(y0+rad, math.Min(y, y1-rad))
	return math.Hypot(x-cx, y-cy) <= rad
}

func distToSegment(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := ((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// StatusDot draws a plain status dot, for lists and trees.
func StatusDot(level Level, size int) *image.NRGBA {
	c := levelColors[level]
	return render(size, func(x, y float64) color.NRGBA {
		if math.Hypot(x-16, y-16) <= 9 {
			return c
		}
		return color.NRGBA{}
	})
}
