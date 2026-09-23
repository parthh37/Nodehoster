//go:build windows

package deploy

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// runShellWindows runs a command line through cmd.exe. The raw command line
// is passed as-is: Go's argument quoting is designed for C runtimes and
// mangles cmd.exe syntax.
func runShellWindows(ctx context.Context, out io.Writer, dir string, env []string, command string) error {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	}
	cmd := exec.CommandContext(ctx, comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `/d /s /c "` + command + `"`,
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, out, out
	cmd.WaitDelay = 10 * time.Second
	return cmd.Run()
}

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
