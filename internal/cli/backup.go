package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

func init() {
	register(
		// "backup run" and "backup history" win over "backup <file>" (the
		// longest command matches): a file with one of those names needs a
		// path, such as .\run.
		&Command{Name: "backup", Args: "<file>", MinArgs: 1, MaxArgs: 1,
			Summary: "Save a backup to a file: .zip = the full archive, else the configuration as JSON (- = stdout)",
			Setup:   func(*flag.FlagSet) Runner { return backup }},
		&Command{Name: "backup run", MaxArgs: 0,
			Summary: "Run a backup to the configured destinations now and show the result",
			Setup:   func(*flag.FlagSet) Runner { return backupRun }},
		&Command{Name: "backup history", MaxArgs: 0,
			Summary: "List recent backups to the destinations (-n count)",
			Setup:   backupHistoryCmd},
		&Command{Name: "restore", Args: "<file>", MinArgs: 1, MaxArgs: 1,
			Summary: "Restore a backup (.json or .zip; asks first unless --yes; --passphrase-file)",
			Setup:   restoreCmd},
	)
}

// passphraseEnv holds an encrypted archive's passphrase for restore. A
// passphrase is never a command-line value: other users can read the
// process list.
const passphraseEnv = "NODEHOSTER_BACKUP_PASSPHRASE"

func backup(e *Env, args []string) error {
	// A .zip is the full archive the scheduled backups make (certificates,
	// shared folders as configured; encrypted when a passphrase is set);
	// anything else the configuration export.
	path, archive := "/api/backup", strings.EqualFold(filepath.Ext(args[0]), ".zip")
	if archive {
		path += "?format=zip"
	}
	if args[0] == "-" {
		_, _, err := e.Client.Download(e.Ctx, path, e.Stdout)
		return err
	}
	// Written next to the target and renamed, so that a failure never
	// leaves half a backup under the name.
	tmp, err := os.CreateTemp(filepath.Dir(args[0]), ".nodehoster-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, n, err := e.Client.Download(e.Ctx, path, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), args[0]); err != nil {
		return err
	}
	if e.JSON {
		return e.printJSON(map[string]any{"file": args[0], "bytes": n})
	}
	if !archive {
		e.printf("Configuration saved to %s (%d bytes). Its secrets stay encrypted with this server's key: restore it here, or after restoring the data folder.\n", args[0], n)
		return nil
	}
	// Whether it is encrypted decides where it can be restored.
	var st model.BackupStatus
	if _, err := e.get("/api/backups", &st); err == nil && st.Encrypted {
		e.printf("Backup archive saved to %s (%s), encrypted with the backup passphrase: it restores on any NodeHoster server with that passphrase.\n", args[0], sizeText(n))
	} else {
		e.printf("Backup archive saved to %s (%s). Without a backup passphrase its secrets stay encrypted with this server's key: restore it here, or set a passphrase in Settings → Backups for archives another server can restore.\n", args[0], sizeText(n))
	}
	return nil
}

func sizeText(n int64) string {
	if n < 0 {
		n = 0
	}
	return formatBytes(uint64(n))
}

// pollInterval is how often backup run checks whether the backup is done.
var pollInterval = time.Second

func backupRun(e *Env, _ []string) error {
	var started model.BackupStatus
	if _, err := e.post("/api/backups/run", &started); err != nil {
		var ae *localapi.Error
		if errors.As(err, &ae) && ae.Status == http.StatusConflict {
			return fmt.Errorf("the backup was not started: %s", ae.Message)
		}
		return err
	}
	// The run is added to the history when it ends; the runs listed now
	// are older.
	before := map[string]bool{}
	for _, r := range started.History {
		before[r.ID] = true
	}
	var run *model.BackupRun
	for run == nil {
		select {
		case <-e.Ctx.Done():
			return fmt.Errorf("stopped waiting; the backup goes on (nodehoster backup history shows its result): %w", e.Ctx.Err())
		case <-time.After(pollInterval):
		}
		var st model.BackupStatus
		if _, err := e.get("/api/backups", &st); err != nil {
			return err
		}
		if st.Running {
			continue
		}
		// Newest first: the oldest new manual run is this one (a
		// scheduled backup may have run right after it).
		for i := range st.History {
			if r := &st.History[i]; !before[r.ID] && r.Trigger == "manual" {
				run = r
			}
		}
		if run == nil {
			return errors.New("the backup ended without a result in the history")
		}
	}
	if e.JSON {
		if err := e.printJSON(run); err != nil {
			return err
		}
	} else {
		e.printBackupRun(run)
	}
	if run.Status != model.BackupSuccess {
		msg := run.Error
		if msg == "" {
			msg = "not every destination received the archive"
		}
		return fmt.Errorf("backup %s: %s", run.Status, msg)
	}
	return nil
}

func (e *Env) printBackupRun(r *model.BackupRun) {
	if r.File != "" {
		enc := ""
		if r.Encrypted {
			enc = ", encrypted"
		}
		e.printf("Backup %s (%s%s): %s.\n", r.File, sizeText(r.Size), enc, strings.Join(r.Contents, ", "))
	}
	if len(r.Destinations) == 0 {
		return
	}
	rows := make([][]string, 0, len(r.Destinations))
	for _, d := range r.Destinations {
		result := "ok"
		if !d.OK {
			result = "failed: " + d.Error
		}
		if d.Pruned > 0 {
			result += fmt.Sprintf(" (%d old deleted)", d.Pruned)
		}
		rows = append(rows, []string{d.Name, result})
	}
	e.table([]string{"DESTINATION", "RESULT"}, rows)
}

func backupHistoryCmd(fs *flag.FlagSet) Runner {
	n := fs.Int("n", 10, "number of backups, newest first")
	return func(e *Env, _ []string) error {
		if *n < 1 {
			return usagef("-n must be 1 or more")
		}
		var st model.BackupStatus
		if _, err := e.get("/api/backups", &st); err != nil {
			return err
		}
		list := st.History
		if len(list) > *n {
			list = list[:*n]
		}
		if e.JSON {
			return e.printJSON(list)
		}
		if st.Running && st.RunningSince != nil {
			e.printf("A backup is running (%s, since %s).\n", st.RunningWhat, localTime(*st.RunningSince))
		}
		if len(list) == 0 {
			e.printf("No backups have run.\n")
			return nil
		}
		rows := make([][]string, 0, len(list))
		for _, r := range list {
			var dests []string
			for _, d := range r.Destinations {
				ok := "ok"
				if !d.OK {
					ok = "failed"
				}
				dests = append(dests, d.Name+" "+ok)
			}
			detail := strings.Join(dests, ", ")
			if r.Error != "" {
				detail = r.Error
			}
			size := "-"
			if r.File != "" {
				size = sizeText(r.Size)
			}
			rows = append(rows, []string{localTime(r.StartedAt), r.Trigger, r.Status, size, r.File, truncate(detail, 60)})
		}
		e.table([]string{"STARTED", "TRIGGER", "STATUS", "SIZE", "FILE", "DESTINATIONS"}, rows)
		return nil
	}
}

func restoreCmd(fs *flag.FlagSet) Runner {
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	passFile := fs.String("passphrase-file", "", "file holding the passphrase of an encrypted archive (or set "+passphraseEnv+")")
	return func(e *Env, args []string) error {
		passphrase, err := restorePassphrase(*passFile)
		if err != nil {
			return err
		}
		f, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer f.Close()
		if !*yes {
			if !e.Interactive {
				return usagef("restoring replaces the server settings and sites; add --yes to confirm")
			}
			fmt.Fprintf(e.Stdout, "Restore %s? The server settings, and the sites with the same IDs, are replaced. [y/N] ", args[0])
			answer, _ := bufio.NewReader(e.Stdin).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
				return errors.New("cancelled; nothing was restored")
			}
		}
		// The raw file as the body, so that the passphrase can go in a
		// header rather than the command line.
		var header http.Header
		if passphrase != "" {
			header = http.Header{"X-Backup-Passphrase": {passphrase}}
		}
		var res model.RestoreResult
		if err := e.Client.Upload(e.Ctx, "/api/restore", "application/octet-stream", f, header, &res); err != nil {
			var ae *localapi.Error
			if errors.As(err, &ae) && ae.Field == "passphrase" && passphrase == "" {
				return fmt.Errorf("%s is an encrypted backup: put its passphrase in a file and add --passphrase-file <file>, or set %s", args[0], passphraseEnv)
			}
			return err
		}
		if e.JSON {
			return e.printJSON(res)
		}
		e.printRestore(args[0], &res)
		return nil
	}
}

// restorePassphrase reads the passphrase from --passphrase-file (its
// first line), else the environment.
func restorePassphrase(file string) (string, error) {
	if file == "" {
		return os.Getenv(passphraseEnv), nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("passphrase file: %w", err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	if line = strings.TrimRight(line, "\r"); line == "" {
		return "", fmt.Errorf("passphrase file %s is empty", file)
	}
	return line, nil
}

func (e *Env) printRestore(file string, r *model.RestoreResult) {
	from := file
	if r.Format == "archive" {
		var about []string
		if r.Hostname != "" {
			about = append(about, "from "+r.Hostname)
		}
		if r.Created != nil {
			about = append(about, "made "+localTime(*r.Created))
		}
		if r.Encrypted {
			about = append(about, "encrypted")
		}
		if len(about) > 0 {
			from += " (" + strings.Join(about, ", ") + ")"
		}
	}
	e.printf("Configuration restored from %s: %s, %s restored.\n", from, plural(r.Sites, "site"), plural(r.Certificates, "certificate"))
	if len(r.SharedSites) > 0 {
		e.printf("Shared folders restored: %s.\n", strings.Join(r.SharedSites, ", "))
	}
	for _, w := range r.Warnings {
		e.printf("Warning: %s\n", w)
	}
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}
