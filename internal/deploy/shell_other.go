//go:build !windows

package deploy

import (
	"context"
	"io"
	"os/exec"
)

func runShellWindows(ctx context.Context, out io.Writer, dir string, env []string, command string) error {
	panic("unreachable")
}

func hideWindow(*exec.Cmd) {}
