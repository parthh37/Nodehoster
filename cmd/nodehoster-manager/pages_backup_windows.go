package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- Backups: the scheduled backups' state and history, running one now,
// and backing up to or restoring from a file (Windows Server Backup's
// "Backup Once" and "Recover", for NodeHoster's own data). Schedule and
// destinations are edited in the web console, which has the editors for
// cloud credentials and SFTP host keys.

type backupsPage struct {
	page
	history table
	status  *model.BackupStatus
	loading bool
	bar     infoBar

	state, next, last, encryption statCard

	run, schedule, refresh, details *command
}

func (s *backupsPage) init(m *manager) *page {
	s.icon = desktop.IconBackups
	s.title = func() string { return "Backups" }
	s.subtitle = func() string {
		return "The configuration, and as configured the certificates and the sites' shared folders"
	}
	s.update = func() { s.reload(m) }
	s.load = func() { s.reload(m) }
	s.run = newCommand("Back up now", desktop.IconStart, func() { s.runNow(m) })
	s.schedule = newCommand("Schedule and destinations…", desktop.IconConsole, func() { m.openConsolePath("/settings/backup") })
	s.refresh = newCommand("Refresh", desktop.IconRefresh, func() { m.refresh(true) })
	s.details = newCommand("Details…", desktop.IconEye, func() { s.showDetails(m) })
	s.history.onSelect = func() { s.enable(m) }
	s.history.color = func(row, col int) (walk.Color, bool) {
		if s.status == nil || row >= len(s.status.History) || col != 1 {
			return 0, false
		}
		return runColor(s.status.History[row].Status)
	}
	s.history.icon = func(row, col int) walk.Image {
		if s.status == nil || row >= len(s.status.History) {
			return nil
		}
		switch col {
		case 0:
			return img(desktop.IconBackups)
		case 1:
			return img(runIcon(s.status.History[row].Status))
		}
		return nil
	}
	return &s.page
}

func runColor(status string) (walk.Color, bool) {
	switch status {
	case model.BackupSuccess:
		return colorOK, true
	case model.BackupPartial:
		return colorWarning, true
	case model.BackupFailed:
		return colorError, true
	}
	return 0, false
}

func (s *backupsPage) content(m *manager) []Widget {
	return []Widget{
		s.bar.widget(),
		cards(s.state.widget(desktop.IconBackups, "State", false), s.next.widget(desktop.IconClock, "Next backup", false),
			s.last.widget(desktop.IconHistory, "Last backup", false), s.encryption.widget(desktop.IconLock, "Encryption", false)),
		heading("History"),
		s.history.viewWith(tableOpts{name: "backupHistory", sortable: true, onActivate: s.details.trigger, menu: menu(s.details)},
			col("Started", 150), col("Result", 90), col("Trigger", 80), col("Archive", 300), colR("Size", 80), col("Destinations", 300)),
		hint("Set the schedule, destinations (folders, SFTP, S3, Azure) and passphrase in the web console (Settings → Backups)."),
	}
}

func (s *backupsPage) actionsPane(m *manager) []Widget {
	return pane(
		"Backups", s.run, m.cmdBackup, m.cmdRestore, s.schedule, s.refresh,
		"Selected backup", s.details,
	)
}

// reload reads the status off the UI thread, one read at a time.
func (s *backupsPage) reload(m *manager) {
	s.enable(m)
	if s.loading || !m.connected() {
		return
	}
	s.loading = true
	go func() {
		var st model.BackupStatus
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := m.cl.Get(ctx, "/api/backups", &st)
		cancel()
		m.mw.Synchronize(func() {
			s.loading = false
			if err != nil {
				s.status = nil
				s.bar.show(barError, "The backups' state could not be read: "+err.Error(), "Retry", func() { s.reload(m) })
				s.history.set(nil, nil)
				s.enable(m)
				return
			}
			s.status = &st
			s.redraw(m)
		})
	}()
}

func (s *backupsPage) redraw(m *manager) {
	st := s.status
	switch {
	case st.Running && st.RunningWhat == "restore":
		s.state.set("Restoring", "since "+st.RunningSince.Local().Format("15:04:05"), colorWarning)
	case st.Running:
		s.state.set("Backing up", "since "+st.RunningSince.Local().Format("15:04:05"), colorWarning)
	case st.Enabled:
		s.state.set("Scheduled", "", colorOK)
	default:
		s.state.set("Not scheduled", "back up by hand, or set a schedule", colorMuted)
	}
	if st.NextRun != nil {
		s.next.set(st.NextRun.Local().Format("Mon 15:04"), "in "+desktop.Uptime(time.Now(), *st.NextRun), 0)
	} else {
		s.next.set("—", "", 0)
	}
	// The bar is shown or hidden once: this runs on every refresh.
	showBar := false
	if len(st.History) > 0 {
		r := st.History[0]
		c, _ := runColor(r.Status)
		s.last.set(desktop.StateText(model.SiteState(r.Status)), r.StartedAt.Local().Format("2006-01-02 15:04"), c)
		switch {
		case r.Error != "":
			s.bar.show(barError, "The last backup failed: "+r.Error, "Details…", func() { s.showRun(m, r) })
			showBar = true
		case r.Status != model.BackupSuccess:
			s.bar.show(barWarning, "The last backup did not reach every destination: "+failedDestinations(r), "Details…", func() { s.showRun(m, r) })
			showBar = true
		}
	} else {
		s.last.set("Never", "", colorMuted)
	}
	if !showBar {
		s.bar.hide()
	}
	if st.Encrypted {
		s.encryption.set("Passphrase set", "archives restore on any server", colorOK)
		s.encryption.setIcon(desktop.IconLock)
	} else {
		s.encryption.set("No passphrase", "archives only restore on this server", colorWarning)
		s.encryption.setIcon(desktop.IconUnlock)
	}

	keys := make([]string, len(st.History))
	rows := make([][]string, len(st.History))
	for i, r := range st.History {
		var dests []string
		for _, d := range r.Destinations {
			mark := "ok"
			if !d.OK {
				mark = "failed"
			}
			dests = append(dests, d.Name+" ("+mark+")")
		}
		size := ""
		if r.File != "" {
			size = desktop.Bytes(uint64(r.Size))
		}
		keys[i] = r.ID
		rows[i] = []string{r.StartedAt.Local().Format("2006-01-02 15:04:05"), r.Status, r.Trigger, r.File, size, strings.Join(dests, ", ")}
	}
	s.history.set(keys, rows)
	s.enable(m)
}

func failedDestinations(r model.BackupRun) string {
	var out []string
	for _, d := range r.Destinations {
		if !d.OK {
			out = append(out, d.Name+": "+d.Error)
		}
	}
	return strings.Join(out, "; ")
}

func (s *backupsPage) enable(m *manager) {
	if s.run == nil {
		return
	}
	idle := m.connected() && s.status != nil && !s.status.Running
	setEnabled(idle, s.run)
	setEnabled(s.selectedRun() != nil, s.details)
}

func (s *backupsPage) selectedRun() *model.BackupRun {
	if s.status == nil {
		return nil
	}
	if i := s.history.current(); i >= 0 && i < len(s.status.History) {
		return &s.status.History[i]
	}
	return nil
}

func (s *backupsPage) showDetails(m *manager) {
	if r := s.selectedRun(); r != nil {
		s.showRun(m, *r)
	}
}

func (s *backupsPage) showRun(m *manager, r model.BackupRun) {
	var b strings.Builder
	finished := "—"
	if !r.FinishedAt.IsZero() {
		finished = r.FinishedAt.Local().Format("2006-01-02 15:04:05")
	}
	fmt.Fprintf(&b, "Started: %s (%s)\nFinished: %s\n", r.StartedAt.Local().Format("2006-01-02 15:04:05"), r.Trigger, finished)
	if r.File != "" {
		enc := "not encrypted"
		if r.Encrypted {
			enc = "encrypted"
		}
		fmt.Fprintf(&b, "Archive: %s (%s, %s)\nContents: %s\n", r.File, desktop.Bytes(uint64(r.Size)), enc, strings.Join(r.Contents, ", "))
	}
	for _, d := range r.Destinations {
		fmt.Fprintf(&b, "\n%s: ", d.Name)
		if d.OK {
			b.WriteString("copied")
			if d.Pruned > 0 {
				fmt.Fprintf(&b, ", %d old archive(s) deleted", d.Pruned)
			}
		} else {
			b.WriteString("failed")
		}
		if d.Error != "" {
			b.WriteString(" — " + d.Error)
		}
	}
	icon := walk.TaskDialogSystemIconInformation
	instruction := "The backup succeeded"
	switch r.Status {
	case model.BackupFailed:
		icon, instruction = walk.TaskDialogSystemIconError, "The backup failed"
	case model.BackupPartial:
		icon, instruction = walk.TaskDialogSystemIconWarning, "The backup did not reach every destination"
	}
	content := r.Error
	if content == "" {
		content = "Started " + r.StartedAt.Local().Format("2006-01-02 15:04") + "."
	}
	notify(m.mw, "Backup", instruction, content, b.String(), icon)
}

func (s *backupsPage) runNow(m *manager) {
	m.do("Starting a backup", func(ctx context.Context) error {
		return m.cl.Post(ctx, "/api/backups/run", nil, nil)
	})
}

// backupConfig saves a backup to a file: an archive as the backup settings
// make them (contents, passphrase), or the configuration export alone.
func (m *manager) backupConfig() {
	host, _ := os.Hostname()
	dlg := walk.FileDialog{
		Title:    "Back up NodeHoster to a file",
		Filter:   "Backup archive (*.zip)|*.zip|Configuration only (*.json)|*.json",
		FilePath: "nodehoster-backup-" + host + "-" + time.Now().UTC().Format("20060102-150405") + ".zip",
	}
	if ok, _ := dlg.ShowSave(m.mw); !ok {
		return
	}
	path := dlg.FilePath
	archive := dlg.FilterIndex != 2
	if ext := strings.ToLower(filepath.Ext(path)); ext == "" {
		if archive {
			path += ".zip"
		} else {
			path += ".json"
		}
	} else {
		archive = ext != ".json"
	}
	endpoint := "/api/backup"
	if archive {
		endpoint += "?format=zip"
	}
	m.long("Backing up to "+filepath.Base(path), func(ctx context.Context) error {
		tmp := path + ".partial"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		_, _, err = m.cl.Download(ctx, endpoint, f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp, path)
		}
		if err != nil {
			os.Remove(tmp)
		}
		return err
	})
}

// restoreFromFile restores an archive or a configuration export, asking
// for the passphrase when the archive is encrypted.
func restoreFromFile(m *manager) {
	dlg := walk.FileDialog{Title: "Restore NodeHoster from a backup", Filter: "Backups (*.zip;*.json)|*.zip;*.json|All files (*.*)|*.*"}
	if ok, _ := dlg.ShowOpen(m.mw); !ok {
		return
	}
	path := dlg.FilePath
	if ask(m.mw, "Restore from backup", "Restore "+filepath.Base(path)+"?",
		"The server settings are replaced and sites with the same ID as in the backup are overwritten; other sites are kept. "+
			"Sites whose shared folder is restored are stopped meanwhile.",
		walk.TaskDialogSystemIconWarning, [2]string{"Restore", ""}) != 0 {
		return
	}
	var attempt func(passphrase string)
	attempt = func(passphrase string) {
		done := m.begin("Restoring " + filepath.Base(path))
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
			defer cancel()
			var res model.RestoreResult
			err := func() error {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				h := http.Header{}
				if passphrase != "" {
					h.Set("X-Backup-Passphrase", passphrase)
				}
				return m.cl.Upload(ctx, "/api/restore", "application/octet-stream", f, h, &res)
			}()
			m.mw.Synchronize(func() {
				done()
				var apiErr *localapi.Error
				if errors.As(err, &apiErr) && apiErr.Field == "passphrase" {
					prompt := "The backup is encrypted. Its passphrase:"
					if passphrase != "" {
						prompt = apiErr.Message + ". Passphrase:"
					}
					if p, ok := inputDialog(m.mw, "Restore from backup", desktop.IconKey, prompt, "", true); ok && p != "" {
						attempt(p)
					}
					return
				}
				if err != nil {
					m.flashStatus("The restore failed", true)
					m.errorBox("Restore from backup", err)
					return
				}
				m.flashStatus("Restored "+filepath.Base(path), false)
				icon := walk.TaskDialogSystemIconInformation
				if len(res.Warnings) > 0 {
					icon = walk.TaskDialogSystemIconWarning
				}
				notify(m.mw, "Restore from backup", "The backup is restored", restoreSummary(res), "", icon)
				m.refresh(true)
			})
		}()
	}
	attempt("")
}

func restoreSummary(r model.RestoreResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Restored %s", plural(r.Sites, "site"))
	if r.Hostname != "" {
		fmt.Fprintf(&b, " from %s", r.Hostname)
	}
	if r.Created != nil {
		fmt.Fprintf(&b, ", backed up %s", r.Created.Local().Format("2006-01-02 15:04"))
	}
	b.WriteString(".")
	if r.Certificates > 0 {
		fmt.Fprintf(&b, "\n%s restored with their keys.", plural(r.Certificates, "certificate"))
	}
	if len(r.SharedSites) > 0 {
		fmt.Fprintf(&b, "\nShared folders restored: %s.", strings.Join(r.SharedSites, ", "))
	}
	for _, w := range r.Warnings {
		b.WriteString("\n\n" + w)
	}
	return b.String()
}
