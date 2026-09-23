package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/service"
	"github.com/tailscale/walk"
)

// tray is the notification-area icon. It runs unelevated in each
// interactive session, reads the service state from the SCM and the rest
// from the read-only status pipe, and hands every change to the manager
// (or to an elevated copy of itself, for service control).
type tray struct {
	ni    *walk.NotifyIcon
	icons map[desktop.Level]*walk.Icon

	// Written on the UI thread only.
	service   string
	sum       *localapi.Summary
	health    desktop.Health
	lastLevel desktop.Level
	lastNote  time.Time
	noteSite  string // the site of the last notification, opened on click
}

func runTray(autostart bool) {
	if autostart && trayAutostartDisabled() {
		return
	}
	if !singleInstance("NodeHosterStatusIcon") {
		return
	}
	app, err := walk.InitApp()
	if err != nil {
		log.Fatal(err)
	}
	ni, err := walk.NewNotifyIcon()
	if err != nil {
		log.Fatal(err)
	}
	defer ni.Dispose()

	t := &tray{ni: ni, icons: map[desktop.Level]*walk.Icon{}, lastLevel: -1}
	for _, l := range []desktop.Level{desktop.LevelOK, desktop.LevelWarning, desktop.LevelDown, desktop.LevelNotInstalled} {
		// 32 pixels: the icon cache scales it down for 100% displays
		// and it stays sharp at 200%.
		if ic, err := walk.NewIconFromImage(desktop.StatusIcon(l, 32)); err == nil {
			t.icons[l] = ic
		}
	}
	ni.ShowingContextMenu().Attach(func() bool {
		t.buildMenu()
		return true
	})
	ni.MessageClicked().Attach(func() {
		if t.noteSite != "" {
			startManager("--site", t.noteSite)
		} else {
			startManager()
		}
	})
	t.apply("", nil)
	ni.SetVisible(true)

	go t.watch()
	app.Run()
}

// watch follows the service: while it runs, the status stream delivers a
// summary every few seconds and notices as they happen; otherwise the SCM
// is polled.
func (t *tray) watch() {
	cl := localapi.Connect(localapi.Status, config.DefaultDataDir())
	failures := 0
	for {
		svc, err := service.Status()
		if err != nil {
			svc = "unknown"
		}
		if svc != "running" {
			failures = 0
			t.sync(func() { t.apply(svc, nil) })
			time.Sleep(3 * time.Second)
			continue
		}
		cl.Stream(context.Background(), "/status/stream", func(event string, data []byte) {
			failures = 0
			switch event {
			case "summary":
				var s localapi.Summary
				if json.Unmarshal(data, &s) == nil {
					t.sync(func() { t.apply("running", &s) })
				}
			case "notice":
				var n localapi.Notice
				if json.Unmarshal(data, &n) == nil {
					t.sync(func() { t.notify(n) })
				}
			}
		})
		// A service that has just started opens its pipe a moment later;
		// only call it unresponsive when that persists.
		if failures++; failures >= 3 {
			t.sync(func() { t.apply("running", nil) })
		}
		time.Sleep(2 * time.Second)
	}
}

func (t *tray) sync(fn func()) { walk.App().Synchronize(fn) }

// apply updates the icon and tooltip; it runs on the UI thread.
func (t *tray) apply(svc string, sum *localapi.Summary) {
	prev := t.service
	t.service, t.sum = svc, sum
	if svc == "" || svc == "unknown" {
		t.health = desktop.Health{Level: desktop.LevelDown, Summary: "Checking the service…"}
	} else {
		t.health = desktop.Overall(svc, sum)
	}
	if t.health.Level != t.lastLevel {
		if ic := t.icons[t.health.Level]; ic != nil {
			t.ni.SetIcon(ic)
		}
		t.lastLevel = t.health.Level
	}
	tip := "NodeHoster\n" + t.health.Summary
	if len([]rune(tip)) > 127 { // the shell's limit
		tip = string([]rune(tip)[:126]) + "…"
	}
	t.ni.SetToolTip(tip)

	// The service going away is worth a notification; the first reading
	// after start-up is not.
	switch {
	case prev == "running" && (svc == "stopped" || svc == "stopping"):
		t.balloon(desktop.LevelDown, "NodeHoster service stopped", "Sites hosted by NodeHoster are offline.", "")
	case (prev == "stopped" || prev == "starting") && svc == "running":
		t.balloon(desktop.LevelOK, "NodeHoster service running", t.health.Summary, "")
	}
}

func (t *tray) notify(n localapi.Notice) {
	title := n.Site
	if title == "" {
		title = "NodeHoster"
	}
	level := desktop.LevelOK
	switch n.Level {
	case "error":
		level = desktop.LevelDown
	case "warning":
		level = desktop.LevelWarning
	}
	var siteID string
	if t.sum != nil {
		for _, s := range t.sum.Sites {
			if s.Name == n.Site {
				siteID = s.ID
			}
		}
	}
	t.balloon(level, title, n.Message, siteID)
}

// balloon shows a notification, at most one every few seconds: a site in
// a crash loop must not bury the desktop.
func (t *tray) balloon(level desktop.Level, title, msg, siteID string) {
	if time.Since(t.lastNote) < 5*time.Second {
		return
	}
	t.lastNote, t.noteSite = time.Now(), siteID
	switch level {
	case desktop.LevelDown:
		t.ni.ShowError(title, msg)
	case desktop.LevelWarning:
		t.ni.ShowWarning(title, msg)
	default:
		t.ni.ShowInfo(title, msg)
	}
}

// buildMenu rebuilds the context menu from the latest state, just before
// it opens.
func (t *tray) buildMenu() {
	acts := t.ni.ContextMenu().Actions()
	acts.Clear()
	add := func(text string, enabled bool, fn func()) *walk.Action {
		a := walk.NewAction()
		a.SetText(text)
		a.SetEnabled(enabled)
		if fn != nil {
			a.Triggered().Attach(fn)
		}
		acts.Add(a)
		return a
	}
	sep := func() { acts.Add(walk.NewSeparatorAction()) }

	head := "NodeHoster"
	if t.sum != nil {
		head += " " + t.sum.Version
	}
	add(head, false, nil)
	add(t.health.Summary, false, nil)
	for i, s := range t.health.Attention {
		if i == 5 {
			add(fmt.Sprintf("   and %d more…", len(t.health.Attention)-5), false, nil)
			break
		}
		id := s.ID
		add("   ⚠ "+s.Name+": "+desktop.StateText(s.State), true, func() { startManager("--site", id) })
	}
	if t.sum != nil && t.sum.AdminError != "" {
		add("   ⚠ Web console: "+t.sum.AdminError, false, nil)
	}
	sep()

	if t.sum != nil && len(t.sum.Sites) > 0 {
		menu, err := walk.NewMenu()
		if err == nil {
			for _, s := range t.sum.Sites {
				id := s.ID
				a := walk.NewAction()
				a.SetText(s.Name + "\t" + desktop.StateText(s.State))
				a.Triggered().Attach(func() { startManager("--site", id) })
				menu.Actions().Add(a)
			}
			sa, _ := acts.AddMenu(menu)
			sa.SetText("Sites")
		}
	}
	open := add("Open NodeHoster Manager", true, func() { startManager() })
	open.SetDefault(true)
	consoleURL := ""
	if t.sum != nil {
		consoleURL = desktop.ConsoleURL(t.sum.AdminURL)
	}
	add("Open web console", consoleURL != "" && t.sum.AdminError == "", func() { shellOpen(consoleURL) })
	sep()

	installed := t.service != "not installed" && t.service != "" && t.service != "unknown"
	add("Start service", installed && t.service == "stopped", func() { runElevated("--service", "start") })
	add("Stop service", t.service == "running", func() { runElevated("--service", "stop") })
	add("Restart service", t.service == "running", func() { runElevated("--service", "restart") })
	sep()

	auto := add("Show this icon at sign-in", true, nil)
	auto.SetCheckable(true)
	auto.SetChecked(!trayAutostartDisabled())
	auto.Triggered().Attach(func() { setTrayAutostartDisabled(!auto.Checked()) })
	add("Exit", true, func() {
		t.ni.Dispose()
		os.Exit(0)
	})
}
