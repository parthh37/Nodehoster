//go:build windows

package runtimes

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideWindow keeps a detection command (python --version, dotnet
// --list-runtimes) from opening a console window when NodeHoster runs in
// a desktop session.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
