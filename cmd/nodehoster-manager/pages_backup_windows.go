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
	props, history table
	status         *model.BackupStatus
	loading        bool

	run, toFile, restore, details *walk.LinkLabel
}

func (s *backupsPage) init(m *manager) *page {
	s.title = func() string { return "Backups" }
	s.update = func() { s.reload(m) }
	s.load = func() { s.reload(m) }
	s.history.onSelect = func() { s.enable(m) }
	s.props.color = func(row, col int) (walk.Color, bool) {
		if col != 1 || s.status == nil {
			return 0, false
		}
		switch {
		case row == 0 && s.status.Running:
			return colorOK, true
		case row == 2 && len(s.status.History) > 0:
			return runColor(s.status.History[0].Status)
		case row == 3 && !s.status.Encrypted:
			return colorWarning, true
		}
		return 0, false
	}
	s.history.color = func(row, col int) (walk.Color, bool) {
		if s.status == nil || row >= len(s.status.History) || col != 1 {
			return 0, false
		}
		return runColor(s.status.History[row].Status)
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
	props := properties(&s.props)
	props.MaxSize = Size{Height: 140}
	props.MinSize = Size{Height: 120}
	hist := s.history.view(func() { s.showDetails(m) },
		col("Started", 130), col("Result", 70), col("Trigger", 70), col("Archive", 300), col("Size", 70), col("Destinations", 300))
	hist.StretchFactor = 3
	return []Widget{
		props,
		Label{Text: "History", Font: Font{Bold: true}},
		hist,
		Label{Text: "Archives hold the configuration, and as configured the certificates and the sites' shared folders. Set the schedule, destinations and passphrase in the web console (Settings → Backups).", TextColor: colorMuted},
	}
}

func (s *backupsPage) actionsPane(m *manager) []Widget {
	return []Widget{
		heading("Backups"),
		link(&s.run, "Back up now", func() { s.runNow(m) }),
		link(&s.toFile, "Back up to a file…", m.backupConfig),
		link(&s.restore, "Restore from a file…", func() { restoreFromFile(m) }),
		link(nil, "Schedule and destinations…", func() { m.openConsolePath("/settings/backup") }),
		link(nil, "Refresh", func() { m.refresh(true) }),
		heading("Selected backup"),
		link(&s.details, "Details…", func() { s.showDetails(m) }),
	}
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
				s.props.setProperties([2]string{"State", "Unknown: " + err.Error()})
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
	state := "Not scheduled"
	switch {
	case st.Running && st.RunningWhat == "restore":
		state = "Restoring since " + st.RunningSince.Local().Format("15:04:05")
	case st.Running:
		state = "Backing up since " + st.RunningSince.Local().Format("15:04:05")
	case st.Enabled:
		state = "Scheduled"
	}
	next := "—"
	if st.NextRun != nil {
		next = st.NextRun.Local().Format("2006-01-02 15:04") + " (in " + desktop.Uptime(time.Now(), *st.NextRun) + ")"
	}
	last := "Never"
	if len(st.History) > 0 {
		r := st.History[0]
		last = r.Status + ", " + r.StartedAt.Local().Format("2006-01-02 15:04")
		if r.Error != "" {
			last += ": " + r.Error
		} else if r.Status != model.BackupSuccess {
			last += ": " + failedDestinations(r)
		}
	}
	enc := "Passphrase set: archives restore on any server"
	if !st.Encrypted {
		enc = "No passphrase: archives only restore on this server"
	}
	s.props.setProperties(
		[2]string{"State", state},
		[2]string{"Next backup", next},
		[2]string{"Last backup", last},
		[2]string{"Encryption", enc},
	)
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
	setEnabled(idle, s.run, s.toFile, s.restore)
	setEnabled(s.history.selected() != "", s.details)
}

func (s *backupsPage) selectedRun() *model.BackupRun {
	if s.status == nil {
		return nil
	}
	id := s.history.selected()
	for i := range s.status.History {
		if s.status.History[i].ID == id {
			return &s.status.History[i]
		}
	}
	return nil
}

func (s *backupsPage) showDetails(m *manager) {
	r := s.selectedRun()
	if r == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Started: %s (%s)\r\nFinished: %s\r\nResult: %s\r\n", r.StartedAt.Local().Format("2006-01-02 15:04:05"), r.Trigger,
		r.FinishedAt.Local().Format("2006-01-02 15:04:05"), r.Status)
	if r.Error != "" {
		fmt.Fprintf(&b, "Error: %s\r\n", r.Error)
	}
	if r.File != "" {
		enc := "not encrypted"
		if r.Encrypted {
			enc = "encrypted"
		}
		fmt.Fprintf(&b, "Archive: %s (%s, %s)\r\nContents: %s\r\n", r.File, desktop.Bytes(uint64(r.Size)), enc, strings.Join(r.Contents, ", "))
	}
	for _, d := range r.Destinations {
		fmt.Fprintf(&b, "\r\n%s: ", d.Name)
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
	icon := walk.MsgBoxIconInformation
	if r.Status != model.BackupSuccess {
		icon = walk.MsgBoxIconWarning
	}
	walk.MsgBox(m.mw, "Backup", b.String(), icon)
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
	if !m.confirm("Restore from backup", "Restore "+filepath.Base(path)+"?\r\n\r\nThe server settings are replaced and sites with the same ID as in the backup are overwritten; other sites are kept. Sites whose shared folder is restored are stopped meanwhile.") {
		return
	}
	var attempt func(passphrase string)
	attempt = func(passphrase string) {
		m.setActivity("Restoring " + filepath.Base(path) + "…")
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
				m.setActivity("")
				var apiErr *localapi.Error
				if errors.As(err, &apiErr) && apiErr.Field == "passphrase" {
					prompt := "The backup is encrypted. Passphrase:"
					if passphrase != "" {
						prompt = apiErr.Message + ". Passphrase:"
					}
					if p, ok := inputDialog(m.mw, "Restore from backup", prompt, "", true); ok && p != "" {
						attempt(p)
					}
					return
				}
				if err != nil {
					m.errorBox("Restore from backup", err)
					return
				}
				walk.MsgBox(m.mw, "Restore from backup", restoreSummary(res), walk.MsgBoxIconInformation)
				m.refresh(true)
			})
		}()
	}
	attempt("")
}

func restoreSummary(r model.RestoreResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Restored %d site(s)", r.Sites)
	if r.Hostname != "" {
		fmt.Fprintf(&b, " from %s", r.Hostname)
	}
	if r.Created != nil {
		fmt.Fprintf(&b, ", backed up %s", r.Created.Local().Format("2006-01-02 15:04"))
	}
	b.WriteString(".")
	if r.Certificates > 0 {
		fmt.Fprintf(&b, "\r\n%d certificate(s) restored with their keys.", r.Certificates)
	}
	if len(r.SharedSites) > 0 {
		fmt.Fprintf(&b, "\r\nShared folders restored: %s.", strings.Join(r.SharedSites, ", "))
	}
	for _, w := range r.Warnings {
		b.WriteString("\r\n\r\n" + w)
	}
	return b.String()
}

// long is do for transfers that may take much longer than a request.
func (m *manager) long(what string, fn func(ctx context.Context) error) {
	m.setActivity(what + "…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		err := fn(ctx)
		cancel()
		m.mw.Synchronize(func() {
			m.setActivity("")
			if err != nil {
				m.errorBox(what, err)
			}
			m.refresh(true)
		})
	}()
}

// openConsolePath opens a page of the web console.
func (m *manager) openConsolePath(p string) {
	if m.info == nil || m.info.AdminURL == "" {
		m.openConsole() // explains why it is unavailable
		return
	}
	shellOpen(strings.TrimRight(desktop.ConsoleURL(m.info.AdminURL), "/") + p)
}
