package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	register(
		&Command{Name: "backup", Args: "<file>", MinArgs: 1, MaxArgs: 1,
			Summary: "Save the configuration to a file (- for stdout)",
			Setup:   func(*flag.FlagSet) Runner { return backup }},
		&Command{Name: "restore", Args: "<file>", MinArgs: 1, MaxArgs: 1,
			Summary: "Restore a configuration backup (asks first unless --yes)",
			Setup:   restoreCmd},
	)
}

func backup(e *Env, args []string) error {
	if args[0] == "-" {
		_, _, err := e.Client.Download(e.Ctx, "/api/backup", e.Stdout)
		return err
	}
	// Written next to the target and renamed, so that a failure never
	// leaves half a backup under the name.
	tmp, err := os.CreateTemp(filepath.Dir(args[0]), ".nodehoster-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, n, err := e.Client.Download(e.Ctx, "/api/backup", tmp)
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
	e.printf("Configuration saved to %s (%d bytes). Its secrets stay encrypted with this server's key: restore it here, or after restoring the data folder.\n", args[0], n)
	return nil
}

func restoreCmd(fs *flag.FlagSet) Runner {
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	return func(e *Env, args []string) error {
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
		if err := e.Client.UploadFile(e.Ctx, "/api/restore", "file", filepath.Base(args[0]), f, nil); err != nil {
			return err
		}
		if e.JSON {
			return e.printJSON(map[string]any{"restored": args[0]})
		}
		e.printf("Configuration restored from %s.\n", args[0])
		return nil
	}
}
