//go:build !windows

package deploy

import (
	"context"
	"os/exec"
)

// shellCommand runs a command line through sh.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command)
}

func hideWindow(*exec.Cmd) {}
