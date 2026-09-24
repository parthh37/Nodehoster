// Command mkicon writes the manager's icons, drawn by package desktop: the
// application icon as the PNG files go-winres packs into the executable's
// icon resource, every icon of the user interface as an .ico file under
// DIR/icons, listed in DIR/winres.json, and optionally the .ico file the
// installer uses for setup and uninstall.
//
//	go run ./internal/mkicon winres [../../installer/nodehoster.ico]
//	go run ./internal/mkicon -sheet icons.png   (a contact sheet, for review)
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/parthh37/nodehoster/internal/desktop"
)

var sizes = []int{256, 48, 32, 16}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "-sheet" {
		if err := os.WriteFile(os.Args[2], sheet(), 0o644); err != nil {
			fail(err)
		}
		return
	}
	if len(os.Args) != 2 && len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mkicon DIR [ICO] | mkicon -sheet PNG")
		os.Exit(2)
	}
	dir := os.Args[1]
	for _, size := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, desktop.AppIcon(size)); err != nil {
			fail(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("icon%d.png", size)), buf.Bytes(), 0o644); err != nil {
			fail(err)
		}
	}
	if err := writeUIIcons(dir); err != nil {
		fail(err)
	}
	if len(os.Args) == 3 {
		if err := os.WriteFile(os.Args[2], ico(), 0o644); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// writeUIIcons writes one .ico per icon resource into DIR/icons (removing
// icons that no longer exist) and lists them in the RT_GROUP_ICON section of
// DIR/winres.json, after the application icon. The other sections of the
// file are kept as they are.
func writeUIIcons(dir string) error {
	iconDir := filepath.Join(dir, "icons")
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		return err
	}
	res := desktop.IconResources()
	keep := map[string]bool{}
	groups := map[string]any{}
	for _, r := range res {
		file := strings.ToLower(r.Name) + ".ico"
		keep[file] = true
		var imgs []*image.NRGBA
		for _, s := range r.Sizes {
			imgs = append(imgs, r.Draw(s))
		}
		data, err := pngICO(imgs)
		if err != nil {
			return err
		}
		if err := writeIfChanged(filepath.Join(iconDir, file), data); err != nil {
			return err
		}
		groups[r.Name] = map[string]string{"0000": "icons/" + file}
	}
	old, _ := filepath.Glob(filepath.Join(iconDir, "*.ico"))
	for _, f := range old {
		if !keep[filepath.Base(f)] {
			os.Remove(f)
		}
	}

	path := filepath.Join(dir, "winres.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	var current map[string]any
	if err := json.Unmarshal(doc["RT_GROUP_ICON"], &current); err != nil {
		return err
	}
	// The application icon stays: "APP" sorts before every "TILE_",
	// "TRAY_" and "UI_" name, and Windows shows the first icon group as the
	// executable's icon.
	groups["APP"] = current["APP"]
	for _, n := range []string{"TILE_", "TRAY_", "UI_"} {
		if n < "APP" {
			return fmt.Errorf("icon prefix %s sorts before APP", n)
		}
	}
	data, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	doc["RT_GROUP_ICON"] = data
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeIfChanged(path, append(out, '\n'))
}

func writeIfChanged(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	return os.WriteFile(path, data, 0o644)
}

// pngICO encodes images as an icon file whose entries are PNG images, which
// every Windows version the manager supports reads at any size; they are
// much smaller than bitmaps.
func pngICO(imgs []*image.NRGBA) ([]byte, error) {
	var images [][]byte
	for _, img := range imgs {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
		images = append(images, buf.Bytes())
	}
	var out bytes.Buffer
	le := func(v any) { binary.Write(&out, binary.LittleEndian, v) }
	le([3]uint16{0, 1, uint16(len(imgs))})
	offset := 6 + 16*len(imgs)
	for i, img := range imgs {
		dim := uint8(img.Bounds().Dx()) // 0 means 256
		le([4]uint8{dim, dim, 0, 0})
		le([2]uint16{1, 32})
		le([2]uint32{uint32(len(images[i])), uint32(offset)})
		offset += len(images[i])
	}
	for _, b := range images {
		out.Write(b)
	}
	return out.Bytes(), nil
}

// ico encodes every size into one icon file. The 256-pixel image is stored
// as PNG (as Windows expects for that size); the small ones as 32-bit
// bitmaps, which every consumer of .ico files reads, including the resource
// updater Inno Setup uses for the setup program's icon.
func ico() []byte {
	var images [][]byte
	for _, size := range sizes {
		img := desktop.AppIcon(size)
		if size == 256 {
			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err != nil {
				fail(err)
			}
			images = append(images, buf.Bytes())
		} else {
			images = append(images, dib(img))
		}
	}

	var out bytes.Buffer
	le := func(v any) { binary.Write(&out, binary.LittleEndian, v) }
	le([3]uint16{0, 1, uint16(len(sizes))}) // reserved, type 1 = icon, count
	offset := 6 + 16*len(sizes)
	for i, size := range sizes {
		dim := uint8(size) // 0 means 256
		le([4]uint8{dim, dim, 0, 0})
		le([2]uint16{1, 32}) // planes, bits per pixel
		le([2]uint32{uint32(len(images[i])), uint32(offset)})
		offset += len(images[i])
	}
	for _, b := range images {
		out.Write(b)
	}
	return out.Bytes()
}

// dib is an icon bitmap: a BITMAPINFOHEADER whose height counts both the
// color image and the mask, the BGRA rows bottom-up, then a 1-bit AND mask
// (set where the pixel is fully transparent) with rows padded to 32 bits.
func dib(img *image.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, struct {
		Size          uint32
		Width, Height int32
		Planes, Bits  uint16
		Compression   uint32
		ImageSize     uint32
		XPPM, YPPM    int32
		Used, Import  uint32
	}{Size: 40, Width: int32(w), Height: int32(2 * h), Planes: 1, Bits: 32})
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(x, y)
			out.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	stride := (w + 31) / 32 * 4
	for y := h - 1; y >= 0; y-- {
		row := make([]byte, stride)
		for x := 0; x < w; x++ {
			if img.NRGBAAt(x, y).A == 0 {
				row[x/8] |= 0x80 >> (x % 8)
			}
		}
		out.Write(row)
	}
	return out.Bytes()
}

// sheet draws every icon at 16, 24 and 48 pixels, the disabled variant at
// 16, the tiles and the badged site types on light and dark backgrounds:
// a page to review the set at a glance.
func sheet() []byte {
	names := desktop.IconNames()
	const cell = 64
	cols := 8
	rows := (len(names) + cols - 1) / cols
	extra := 3 // tiles, badges, dark row
	W, H := cols*cell*2, (rows+extra*2)*cell
	img := image.NewNRGBA(image.Rect(0, 0, W, H))
	light := color.NRGBA{0xff, 0xff, 0xff, 0xff}
	dark := color.NRGBA{0x20, 0x20, 0x20, 0xff}
	draw.Draw(img, img.Bounds(), &image.Uniform{light}, image.Point{}, draw.Src)
	put := func(src *image.NRGBA, x, y int) {
		draw.Draw(img, src.Bounds().Add(image.Pt(x, y)), src, image.Point{}, draw.Over)
	}
	for i, n := range names {
		x, y := (i%cols)*cell*2, (i/cols)*cell
		put(desktop.Icon(n, 16), x+4, y+4)
		put(desktop.IconDisabled(n, 16), x+4, y+26)
		put(desktop.Icon(n, 24), x+24, y+4)
		put(desktop.Icon(n, 48), x+52, y+4)
		// 3× zoom of the 16-pixel icon, to judge the pixels.
		small := desktop.Icon(n, 16)
		for py := 0; py < 16; py++ {
			for px := 0; px < 16; px++ {
				c := small.NRGBAAt(px, py)
				for dy := 0; dy < 3; dy++ {
					for dx := 0; dx < 3; dx++ {
						x0, y0 := x+102+px*3+dx-24, y+14+py*3+dy
						if x0 >= x+100 && x0 < x+cell*2 {
							img.Set(x0, y0, over(c, img.NRGBAAt(x0, y0)))
						}
					}
				}
			}
		}
	}
	y := rows * cell
	draw.Draw(img, image.Rect(0, y+2*cell, W, H), &image.Uniform{dark}, image.Point{}, draw.Src)
	for pass, top := range []int{y, y + 2*cell + 4} {
		x := 4
		for _, n := range desktop.TileIcons {
			put(desktop.IconTile(n, 32), x, top+4)
			x += 38
		}
		x = 4
		for _, t := range desktop.SiteTypes {
			for _, l := range desktop.Levels {
				put(desktop.IconBadged(desktop.SiteTypeIcon(t), l, 16), x, top+48)
				put(desktop.IconBadged(desktop.SiteTypeIcon(t), l, 32), x+20, top+48)
				x += 58
			}
		}
		x = 4
		for _, l := range desktop.Levels {
			put(desktop.StatusIcon(l, 32), x, top+88)
			put(desktop.StatusDot(l, 16), x+36, top+96)
			x += 60
		}
		if pass == 1 {
			for i, n := range names {
				put(desktop.Icon(n, 16), 300+(i%40)*20, top+88+(i/40)*20)
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func over(src, dst color.NRGBA) color.NRGBA {
	a := float64(src.A) / 255
	f := func(s, d uint8) uint8 { return uint8(float64(s)*a + float64(d)*(1-a)) }
	return color.NRGBA{f(src.R, dst.R), f(src.G, dst.G), f(src.B, dst.B), 0xff}
}
