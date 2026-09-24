//go:build windows

package update

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/parthh37/nodehoster/internal/service"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Setup's uninstall key (AppId in installer\nodehoster.iss) and the
// status icon's sign-in entry.
const (
	uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\{6F1B3C2A-9D4E-4E7B-A1C5-2B7D9E0F4A11}_is1`
	runKey       = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue     = "NodeHosterStatus"
	setupTimeout = 30 * time.Minute
)

// installed reads setup's record of the installation: its folder and
// version.
func installed() (dir, version string, err error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", "", err
	}
	defer k.Close()
	dir, _, err = k.GetStringValue("InstallLocation")
	if err != nil {
		return "", "", err
	}
	version, _, _ = k.GetStringValue("DisplayVersion")
	return filepath.Clean(dir), version, nil
}

// Supported reports whether this installation can update itself: it must
// be the copy setup installed (a portable nodehoster.exe is updated by
// hand).
func Supported() (bool, string) {
	dir, _, err := installed()
	if err != nil {
		return false, "NodeHoster was not installed with setup (a portable copy is updated by hand)"
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err.Error()
	}
	if !strings.EqualFold(filepath.Dir(exe), dir) {
		return false, "this nodehoster.exe is not the installed one (" + dir + ")"
	}
	return true, ""
}

// Launch starts the updater, which runs setup (in dir, verified) and
// outlives this service: setup stops the service and replaces its program.
// pending.json must already be written. The updater is a copy of this
// program in dir, detached from the service's console and job.
func Launch(dir, setup string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	helper := filepath.Join(dir, HelperName)
	if err := copyFile(exe, helper); err != nil {
		return fmt.Errorf("copy the updater (is an update already running?): %w", err)
	}
	start := func(flags uint32) error {
		cmd := exec.Command(helper, "update-apply", "--dir", dir, "--setup", setup)
		cmd.Dir = dir
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	const detached = windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP
	// Breaking away from a job fails when the service runs in one that
	// forbids it; then the updater stays in it.
	if err := start(detached | windows.CREATE_BREAKAWAY_FROM_JOB); err != nil {
		return start(detached)
	}
	return nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// RunUpdater is `nodehoster update-apply --dir DIR --setup FILE`, run by
// Launch as SYSTEM: it runs setup unattended, records the result, makes
// sure the service runs again (the old version, if setup failed after
// stopping it) and restarts the status icons setup closed.
func RunUpdater(args []string) int {
	fs := flag.NewFlagSet("update-apply", flag.ContinueOnError)
	dir := fs.String("dir", "", "updates folder")
	setup := fs.String("setup", "", "verified setup to run")
	if fs.Parse(args) != nil || *dir == "" || *setup == "" {
		return 2
	}
	res, err := ReadPending(*dir)
	if err != nil || res == nil {
		return 2
	}

	// Setup closes every status icon and manager (it replaces their
	// program); the icons are started again in these sessions.
	sessions := managerSessions()

	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	cmd := exec.CommandContext(ctx, *setup, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-", "/LOG="+res.Log)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	runErr := cmd.Run()
	cancel()
	res.FinishedAt = time.Now().UTC()
	res.ExitCode = cmd.ProcessState.ExitCode()

	// Setup's exit codes overlap (1 is also "failed to initialize"); the
	// version it recorded is what counts.
	if _, v, err := installed(); err == nil && v == res.To {
		res.OK = true
	} else {
		res.Error = setupError(res.ExitCode, runErr)
	}
	if st, _ := service.Status(); st != "running" {
		if err := service.Start(); err != nil && res.OK {
			res.Error = "installed, but the service did not start: " + err.Error()
		}
	}
	// Written before the service can read it only on failure, which is
	// when it matters: a successful update is recognised by its version.
	_ = WriteResult(*dir, res)

	if len(sessions) > 0 && statusIconEnabled() {
		if appDir, _, err := installed(); err == nil {
			for id := range sessions {
				startTray(id, filepath.Join(appDir, "nodehoster-manager.exe"))
			}
		}
	}
	if res.OK {
		os.Remove(*setup)
		return 0
	}
	return 1
}

func setupError(code int, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "setup did not finish within 30 minutes"
	case code == 7:
		return "setup refused to install: a newer version is installed"
	case code == 2 || code == 5:
		return "setup was cancelled"
	case code > 0:
		return fmt.Sprintf("setup failed with exit code %d; see its log", code)
	case err != nil:
		return "setup could not run: " + err.Error()
	}
	return "setup finished, but the new version is not installed; see its log"
}

// managerSessions are the sessions running nodehoster-manager.exe.
func managerSessions() map[uint32]bool {
	out := map[uint32]bool{}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return out
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if !strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), "nodehoster-manager.exe") {
			continue
		}
		var id uint32
		if windows.ProcessIdToSessionId(e.ProcessID, &id) == nil && id != 0 {
			out[id] = true
		}
	}
	return out
}

// statusIconEnabled: setup's "status icon at sign-in" task is selected.
func statusIconEnabled() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, runKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

// startTray starts the status icon in a signed-in session, as its user,
// the way sign-in does (--autostart honors the user's opt-out). The
// session's token is the user's filtered one, so the icon runs unelevated.
func startTray(session uint32, exe string) {
	var tok windows.Token
	if windows.WTSQueryUserToken(session, &tok) != nil {
		return // signed out meanwhile
	}
	defer tok.Close()
	var env *uint16
	if windows.CreateEnvironmentBlock(&env, tok, false) != nil {
		return
	}
	defer windows.DestroyEnvironmentBlock(env)
	cmdline, err := windows.UTF16PtrFromString(`"` + exe + `" --tray --autostart`)
	if err != nil {
		return
	}
	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)
	si := &windows.StartupInfo{Desktop: desktop}
	si.Cb = uint32(unsafe.Sizeof(*si))
	var pi windows.ProcessInformation
	if windows.CreateProcessAsUser(tok, nil, cmdline, nil, nil, false, windows.CREATE_UNICODE_ENVIRONMENT, env, nil, si, &pi) == nil {
		windows.CloseHandle(pi.Process)
		windows.CloseHandle(pi.Thread)
	}
}
