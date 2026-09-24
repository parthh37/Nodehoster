package desktop

import (
	"bytes"
	"strings"
	"testing"
)

// Every layer of every glyph must draw something: a shape that covers no
// pixel is a broken drawing (a NaN distance, a path off the grid).
func TestEveryLayerDraws(t *testing.T) {
	for _, name := range IconNames() {
		g := glyphs[name]
		for i, l := range g.layers {
			hit := false
			for y := 0.0; y < 24 && !hit; y += 0.25 {
				for x := 0.0; x < 24 && !hit; x += 0.25 {
					hit = l.covers(x, y)
				}
			}
			if !hit {
				t.Errorf("%s: layer %d covers nothing", name, i)
			}
		}
	}
}

func TestIconsRender(t *testing.T) {
	for _, name := range IconNames() {
		for _, size := range []int{16, 20, 32} {
			img := Icon(name, size)
			if b := img.Bounds(); b.Dx() != size || b.Dy() != size {
				t.Fatalf("%s: %v, want %d×%d", name, b, size, size)
			}
			opaque, clear := 0, 0
			for y := 0; y < size; y++ {
				for x := 0; x < size; x++ {
					switch a := img.NRGBAAt(x, y).A; {
					case a > 0x80:
						opaque++
					case a == 0:
						clear++
					}
				}
			}
			if opaque == 0 {
				t.Errorf("%s at %d px: nothing drawn", name, size)
			}
			if clear == 0 {
				t.Errorf("%s at %d px: no transparent pixel", name, size)
			}
		}
	}
}

// The disabled variant keeps the shape but has no color of its own.
func TestIconDisabledIsGrey(t *testing.T) {
	for _, name := range []string{IconStart, IconRemove, IconOK, IconServer} {
		img := IconDisabled(name, 24)
		for y := 0; y < 24; y++ {
			for x := 0; x < 24; x++ {
				if c := img.NRGBAAt(x, y); c.A > 0 && (absDiff(c.R, c.G) > 24 || absDiff(c.G, c.B) > 24) {
					t.Fatalf("%s: pixel %d,%d is colored: %v", name, x, y, c)
				}
			}
		}
	}
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func TestIconTileIsOpaqueInside(t *testing.T) {
	img := IconTile(IconSites, 32)
	if c := img.NRGBAAt(16, 2); c.A != 0xff {
		t.Errorf("tile edge %v, want opaque", c)
	}
	if c := img.NRGBAAt(0, 0); c.A != 0 {
		t.Errorf("tile corner %v, want transparent (rounded)", c)
	}
}

func TestIconBadged(t *testing.T) {
	img := IconBadged(IconNode, LevelDown, 32)
	want := levelColors[LevelDown]
	if c := img.NRGBAAt(24, 24); c != want {
		t.Errorf("badge %v, want %v", c, want)
	}
}

func TestUnknownIconFallsBack(t *testing.T) {
	if !bytes.Equal(Icon("no-such-icon", 16).Pix, Icon(IconDot, 16).Pix) {
		t.Error("an unknown icon should draw the dot")
	}
}

func TestResourceNames(t *testing.T) {
	if got := ResourceName(IconNodeVersion); got != "UI_NODE_VERSIONS" {
		t.Errorf("ResourceName = %q", got)
	}
	if got := ResourceName(IconNode, LevelSlug(LevelOK)); got != "UI_NODE_OK" {
		t.Errorf("ResourceName with suffix = %q", got)
	}
	if got := TileResourceName(IconMail); got != "TILE_MAIL" {
		t.Errorf("TileResourceName = %q", got)
	}
	seen := map[string]bool{}
	for _, r := range IconResources() {
		if seen[r.Name] {
			t.Errorf("duplicate resource %s", r.Name)
		}
		seen[r.Name] = true
		// The executable's own icon is the first group, "APP".
		if strings.ToUpper(r.Name) <= "APP" {
			t.Errorf("resource %s sorts before APP", r.Name)
		}
		if len(r.Sizes) == 0 || r.Draw(r.Sizes[0]) == nil {
			t.Errorf("resource %s draws nothing", r.Name)
		}
	}
	for _, n := range TileIcons {
		if _, ok := glyphs[n]; !ok {
			t.Errorf("tile of unknown icon %q", n)
		}
	}
}

func TestIconsDeterministic(t *testing.T) {
	a, b := Icon(IconRecycle, 24), Icon(IconRecycle, 24)
	if !bytes.Equal(a.Pix, b.Pix) {
		t.Error("the same icon drew differently twice")
	}
}
