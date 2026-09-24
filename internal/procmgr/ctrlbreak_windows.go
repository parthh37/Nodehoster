//go:build windows

package procmgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// Windows has no SIGTERM. What console programs treat as "please stop" is
// a console control event: Ctrl+C or Ctrl+Break. .NET's generic host
// (ConsoleLifetime), uvicorn and hypercorn shut down gracefully on
// Ctrl+Break (SIGBREAK); Bun and Deno run their SIGBREAK listeners, and a
// program with no handler for it exits. Ctrl+C cannot be used: a process
// started in a new process group, as every instance is, ignores it.
//
// An instance runs with CREATE_NO_WINDOW, which gives it a console of its
// own without a window, and CREATE_NEW_PROCESS_GROUP, which makes its PID
// the ID of a process group holding it and its children. The event is
// sent to that group on that console. Only a process attached to the
// console can send it, and attaching means leaving your own console, which
// the service cannot do safely while it runs (nor `nodehoster run` in a
// terminal without losing its output). So a short-lived helper does it:
// nodehoster.exe started detached (no console), which attaches to the
// instance's console, sends Ctrl+Break to the group and exits.

const stillActive = 259 // STILL_ACTIVE, GetExitCodeProcess of a running process

// CtrlBreakCommand is the hidden nodehoster.exe command that sends a
// console Ctrl+Break to a process group: nodehoster __ctrl-break <pid>.
const CtrlBreakCommand = "__ctrl-break"

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole         = kernel32.NewProc("AttachConsole")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
	procSetConsoleCtrlHandler = kernel32.NewProc("SetConsoleCtrlHandler")
)

// ctrlBreakHelper returns the program that runs CtrlBreakCommand: this
// executable, when it is nodehoster.exe. Any other program (a test binary
// of a package that runs sites) would not understand the command, so
// those processes are not interrupted but killed after the timeout, as
// before this existed.
var ctrlBreakHelper = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(filepath.Base(exe), "nodehoster.exe") {
		return "", errors.New("the console Ctrl+Break helper is nodehoster.exe")
	}
	return exe, nil
}

// interrupt sends Ctrl+Break to the process group of an instance or task
// that has no agent. The helper normally takes a few milliseconds; it is
// given ten seconds before the stop falls back to killing the tree.
func (p *osProc) interrupt(pid int) error {
	exe, err := ctrlBreakHelper()
	if err != nil {
		return err
	}
	// A handle keeps the PID from being reused while the helper runs, so a
	// process that just exited cannot have its number taken by another
	// console program that would receive the event instead.
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil || code != stillActive {
		return errors.New("the process has exited")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, CtrlBreakCommand, strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// No console of its own, so it can attach to the instance's; a
		// group of its own, so the event it sends cannot reach it.
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// RunCtrlBreak is the helper's side: attach to the console of the process
// whose PID is args[0] and send Ctrl+Break to the process group it leads.
// It returns the exit code: 0 sent, 1 failed, 2 wrong usage.
func RunCtrlBreak(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: nodehoster "+CtrlBreakCommand+" <pid>")
		return 2
	}
	pid, err := strconv.ParseUint(args[0], 10, 32)
	if err != nil || pid == 0 {
		fmt.Fprintln(os.Stderr, "not a process ID:", args[0])
		return 2
	}
	procFreeConsole.Call() // started detached, but be sure
	if r, _, err := procAttachConsole.Call(uintptr(pid)); r == 0 {
		fmt.Fprintf(os.Stderr, "attach to the console of process %d: %v\n", pid, err)
		return 1
	}
	defer procFreeConsole.Call()
	// Not in the target group, but ignore Ctrl+C all the same.
	procSetConsoleCtrlHandler.Call(0, 1)
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid)); err != nil {
		fmt.Fprintf(os.Stderr, "send Ctrl+Break to process group %d: %v\n", pid, err)
		return 1
	}
	return 0
}
