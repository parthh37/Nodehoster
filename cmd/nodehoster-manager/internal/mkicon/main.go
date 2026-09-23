// Command mkicon writes the manager's icon, drawn by package desktop, as
// the PNG files go-winres packs into the executable's icon resource and,
// optionally, as the .ico file the installer uses for setup and uninstall.
//
//	go run ./internal/mkicon winres [../../installer/nodehoster.ico]
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"

	"github.com/parthh37/nodehoster/internal/desktop"
)

var sizes = []int{256, 48, 32, 16}

func main() {
	if len(os.Args) != 2 && len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mkicon DIR [ICO]")
		os.Exit(2)
	}
	for _, size := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, desktop.AppIcon(size)); err != nil {
			fail(err)
		}
		if err := os.WriteFile(filepath.Join(os.Args[1], fmt.Sprintf("icon%d.png", size)), buf.Bytes(), 0o644); err != nil {
			fail(err)
		}
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
