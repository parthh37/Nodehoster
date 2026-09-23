// Command mkicon writes the manager's icon, drawn by package desktop, as
// the PNG files go-winres packs into the executable's icon resource.
//
//	go run ./internal/mkicon winres
package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"

	"github.com/parthh37/nodehoster/internal/desktop"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mkicon DIR")
		os.Exit(2)
	}
	for _, size := range []int{256, 48, 32, 16} {
		path := filepath.Join(os.Args[1], fmt.Sprintf("icon%d.png", size))
		f, err := os.Create(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := png.Encode(f, desktop.AppIcon(size)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		f.Close()
	}
}
