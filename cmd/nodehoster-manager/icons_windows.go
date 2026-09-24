package main

import (
	"strconv"
	"strings"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/tailscale/walk"
)

// Icons come from the executable's resources (drawn by package desktop,
// written by mkicon, see generate.go): Windows loads each at the size the
// display's scaling needs, so they stay sharp at 100%, 150% or 200%, and
// on a window moved to another monitor. An icon made from an image at run
// time exists at one DPI only; walk cannot draw it at another, and it
// disappears from lists and trees on a scaled display.

var icons = map[string]*walk.Icon{}

// resIcon loads an icon resource at size (in 1/96 inch), or returns nil.
func resIcon(name string, size int) *walk.Icon {
	key := name + "@" + strconv.Itoa(size)
	if ic, ok := icons[key]; ok {
		return ic
	}
	ic, err := walk.NewIconFromResourceWithSize(name, walk.Size{Width: size, Height: size})
	if err != nil {
		ic = nil // built without the icon resources: go generate
	}
	icons[key] = ic
	return ic
}

// ico is an icon of the set at list size.
func ico(name string) *walk.Icon { return resIcon(desktop.ResourceName(name), 16) }

// icoOff is the grey icon of a disabled command.
func icoOff(name string) *walk.Icon {
	return resIcon(desktop.ResourceName(name, desktop.DisabledSuffix), 16)
}

// tileIcon is the 32-pixel tile of a page header.
func tileIcon(name string) *walk.Icon { return resIcon(desktop.TileResourceName(name), 32) }

// siteIcon is a site type's icon with a badge of its state.
func siteIcon(siteType string, level desktop.Level) *walk.Icon {
	return resIcon(desktop.ResourceName(desktop.SiteTypeIcon(siteType), desktop.LevelSlug(level)), 16)
}

// dotIcon is a status dot.
func dotIcon(level desktop.Level) *walk.Icon {
	return resIcon(desktop.ResourceName(desktop.IconDot, desktop.LevelSlug(level)), 16)
}

// trayIcon is the notification-area icon of a level.
func trayIcon(level desktop.Level) *walk.Icon {
	return resIcon("TRAY_"+strings.ToUpper(desktop.LevelSlug(level)), 16)
}

// appIcon is the application's own icon.
func appIcon() *walk.Icon { return resIcon("APP", 16) }

// img turns an icon into a walk.Image that is nil when the icon is: a nil
// *walk.Icon inside an interface is not nil, and walk would call it.
func img(name string) walk.Image { return asImage(ico(name)) }

func asImage(ic *walk.Icon) walk.Image {
	if ic == nil {
		return nil
	}
	return ic
}
