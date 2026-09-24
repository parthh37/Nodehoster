package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- Updates: the installed and the newest version, installing it now,
// and whether NodeHoster installs new versions by itself (like Windows
// Update's automatic updates, with a maintenance time).

type updatesPage struct {
	page
	status  *model.UpdateStatus
	loading bool
	bar     infoBar
	props   table

	installed, newest, auto, checked statCard

	check, install, settings, notes, refresh *command
}

func (s *updatesPage) init(m *manager) *page {
	s.icon = desktop.IconDownload
	s.title = func() string { return "Updates" }
	s.subtitle = func() string { return "New versions of NodeHoster, installed by hand or automatically" }
	s.update = func() { s.enable(m) }
	s.load = func() { s.reload(m) }
	s.check = newCommand("Check now", desktop.IconRefresh, func() { s.checkNow(m) })
	s.install = newCommand("Install now…", desktop.IconDownload, func() { s.installNow(m) })
	s.settings = newCommand("Automatic updates…", desktop.IconClock, func() { s.editSettings(m) })
	s.notes = newCommand("Release notes", desktop.IconLink, func() {
		if s.status != nil && s.status.Available != nil {
			shellOpen(s.status.Available.Notes)
		}
	})
	s.refresh = newCommand("Refresh", desktop.IconRefresh, func() { s.reload(m) })
	return &s.page
}

func (s *updatesPage) content(m *manager) []Widget {
	return []Widget{
		s.bar.widget(),
		cards(s.installed.widget(desktop.IconPackage, "Installed", false), s.newest.widget(desktop.IconDownload, "Newest", false),
			s.auto.widget(desktop.IconClock, "Automatic updates", false), s.checked.widget(desktop.IconHistory, "Last check", false)),
		heading("Details"),
		properties(&s.props, "updateDetails"),
		hint("Releases are checked every few hours and verified against NodeHoster's signing key before they are installed. " +
			"Installing restarts the service: every site is offline for a few seconds."),
	}
}

func (s *updatesPage) actionsPane(m *manager) []Widget {
	return pane(
		"Updates", s.check, s.install, s.settings, s.refresh,
		"Newest version", s.notes,
	)
}

// reload reads the status off the UI thread, one read at a time. While an
// update installs, it polls: the service goes away and comes back as the
// new version.
func (s *updatesPage) reload(m *manager) {
	s.enable(m)
	if s.loading || !m.connected() {
		return
	}
	s.loading = true
	go func() {
		var st model.UpdateStatus
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := m.cl.Get(ctx, "/api/updates", &st)
		cancel()
		m.mw.Synchronize(func() { s.loaded(m, &st, err) })
	}()
}

func (s *updatesPage) loaded(m *manager, st *model.UpdateStatus, err error) {
	s.loading = false
	if err != nil {
		s.status = nil
		s.bar.show(barError, "The update state could not be read: "+err.Error(), "Retry", func() { s.reload(m) })
		s.enable(m)
		return
	}
	s.status = st
	s.redraw(m)
	if st.State != model.UpdateIdle {
		time.AfterFunc(2*time.Second, func() { m.mw.Synchronize(func() { s.reload(m) }) })
	}
}

func (s *updatesPage) redraw(m *manager) {
	st := s.status
	at := func(t *time.Time) string {
		if t == nil {
			return "—"
		}
		return t.Local().Format("2006-01-02 15:04")
	}

	s.installed.set(st.Current, "", 0)
	switch a := st.Available; {
	case !st.Supported:
		s.newest.set("—", "", colorMuted)
	case a != nil && a.Failed:
		s.newest.set(a.Version, "failed to install before", colorError)
	case a != nil:
		s.newest.set(a.Version, desktop.Bytes(uint64(a.Size)), colorWarning)
	case st.LastCheck != nil && st.LastError == "":
		s.newest.set(st.Current, "up to date", colorOK)
	default:
		s.newest.set("—", "not checked yet", colorMuted)
	}
	if st.Auto {
		s.auto.set("On", "at "+st.Time+", "+weekdayText(st.Weekdays), colorOK)
	} else {
		s.auto.set("Off", "install by hand", colorMuted)
	}
	if st.LastCheck != nil {
		detail := "ago"
		c := walk.Color(0)
		if st.LastError != "" {
			detail, c = "failed", colorWarning
		}
		s.checked.set(desktop.Uptime(*st.LastCheck, time.Now()), detail, c)
	} else {
		s.checked.set("Not yet", "", colorMuted)
	}

	// The bar says what needs attention, most important first.
	r := st.LastResult
	switch {
	case !st.Supported:
		s.bar.show(barInfo, "This server does not update itself: "+st.Reason+".", "", nil)
	case st.State == model.UpdateInstalling:
		s.bar.show(barInfo, "Installing the update: the service restarts in a moment.", "", nil)
	case st.State == model.UpdateDownloading:
		s.bar.show(barInfo, "Downloading the update…", "", nil)
	case r != nil && !r.OK && st.Available != nil && st.Available.Version == r.To:
		s.bar.show(barError, fmt.Sprintf("Updating to %s failed: %s", r.To, r.Error), "Try again…", func() { s.installNow(m) })
	case st.Available != nil && st.NextInstall != nil:
		s.bar.show(barInfo, fmt.Sprintf("NodeHoster %s will be installed automatically on %s.", st.Available.Version, at(st.NextInstall)), "Install now…", func() { s.installNow(m) })
	case st.Available != nil && st.Available.Manual && st.Auto:
		s.bar.show(barWarning, fmt.Sprintf("NodeHoster %s is not installed automatically: read its release notes, then install it.", st.Available.Version), "Install now…", func() { s.installNow(m) })
	case st.Available != nil:
		s.bar.show(barWarning, fmt.Sprintf("NodeHoster %s is available.", st.Available.Version), "Install now…", func() { s.installNow(m) })
	case st.LastError != "":
		s.bar.show(barWarning, "Could not check for updates: "+st.LastError, "Check now", func() { s.checkNow(m) })
	default:
		s.bar.hide()
	}

	last := "None"
	if r != nil {
		res := "succeeded"
		if !r.OK {
			res = "failed: " + r.Error
		}
		last = fmt.Sprintf("%s → %s %s (%s, %s)", r.From, r.To, res, r.Trigger, r.StartedAt.Local().Format("2006-01-02 15:04"))
	}
	props := []prop{
		p(desktop.IconPackage, "Installed version", st.Current),
		p(desktop.IconHistory, "Last update", last),
		p(desktop.IconClock, "Next automatic install", at(st.NextInstall)),
		p(desktop.IconRefresh, "Next check", at(st.NextCheck)),
	}
	if r != nil && r.Log != "" {
		props = append(props, p(desktop.IconLog, "Setup log", r.Log))
	}
	if st.Available != nil {
		props = append(props, p(desktop.IconLink, "Release notes", st.Available.Notes))
	}
	s.props.setProperties(props...)
	s.enable(m)
}

func weekdayText(days []int) string {
	if len(days) == 0 {
		return "every day"
	}
	names := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	var out []string
	for _, d := range days {
		if d >= 0 && d < 7 {
			out = append(out, names[d])
		}
	}
	return strings.Join(out, ", ")
}

func (s *updatesPage) enable(m *manager) {
	if s.check == nil {
		return
	}
	st := s.status
	idle := m.connected() && st != nil && st.Supported && st.State == model.UpdateIdle
	setEnabled(idle, s.check)
	setEnabled(idle && st.Available != nil, s.install)
	setEnabled(m.connected() && st != nil, s.settings)
	setEnabled(st != nil && st.Available != nil, s.notes)
}

func (s *updatesPage) checkNow(m *manager) {
	m.do("Checking for updates", func(ctx context.Context) error {
		var st model.UpdateStatus
		if err := m.cl.Post(ctx, "/api/updates/check", nil, &st); err != nil {
			return err
		}
		m.mw.Synchronize(func() { s.loaded(m, &st, nil) })
		return nil
	})
}

func (s *updatesPage) installNow(m *manager) {
	st := s.status
	if st == nil || st.Available == nil {
		return
	}
	content := fmt.Sprintf("This server runs %s. Setup installs %s unattended and restarts the service: every site is offline for a few seconds, "+
		"and NodeHoster Manager and the status icons close while it runs.", st.Current, st.Available.Version)
	if ask(m.mw, "Updates", "Install NodeHoster "+st.Available.Version+" now?", content,
		walk.TaskDialogSystemIconInformation, [2]string{"Install now", ""}, [2]string{"Not now", ""}) != 0 {
		return
	}
	m.do("Installing NodeHoster "+st.Available.Version, func(ctx context.Context) error {
		var out model.UpdateStatus
		if err := m.cl.Post(ctx, "/api/updates/install", nil, &out); err != nil {
			return err
		}
		m.mw.Synchronize(func() { s.loaded(m, &out, nil) })
		return nil
	})
}

// editSettings turns automatic updates on or off and sets their time.
func (s *updatesPage) editSettings(m *manager) {
	if s.status == nil {
		return
	}
	cur := s.status.UpdateSettings
	var on *walk.CheckBox
	var at *walk.LineEdit
	days := make([]*walk.CheckBox, 7)
	names := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	var dayBoxes []Widget
	for i, n := range names {
		dayBoxes = append(dayBoxes, CheckBox{AssignTo: &days[i], Text: n, Checked: slices.Contains(cur.Weekdays, i)})
	}
	runDialog(m.mw, "Automatic updates", Size{Width: 520}, []Widget{
		intro(desktop.IconDownload, "NodeHoster can install new versions by itself, at a quiet time. Installing restarts the service: every site is offline for a few seconds."),
		CheckBox{AssignTo: &on, Text: "Install updates automatically", Checked: cur.Auto},
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "At (server time):"}, LineEdit{AssignTo: &at, Text: cur.Time, CueBanner: "03:00", MaxLength: 5},
			Label{Text: "On:"}, Composite{Layout: HBox{MarginsZero: true}, Children: dayBoxes},
		}},
		hint("No day ticked = every day. New versions are announced either way; with automatic updates off, install them from here."),
	}, func(dlg *walk.Dialog) bool {
		in := model.UpdateSettings{Auto: on.Checked(), Time: strings.TrimSpace(at.Text()), Weekdays: []int{}}
		for i, d := range days {
			if d.Checked() {
				in.Weekdays = append(in.Weekdays, i)
			}
		}
		if err := in.Validate(); err != nil {
			return invalid(dlg, "Enter the time as HH:MM, 24-hour, such as 03:00.")
		}
		var out model.UpdateStatus
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.cl.Put(ctx, "/api/updates", in, &out); err != nil {
			m.errorBoxFor(dlg, "Automatic updates", err)
			return false
		}
		s.loaded(m, &out, nil)
		return true
	})
}
