//go:build !windows

package deploy

import (
	"os/exec"
	"strings"
)

// commandLine is what a command runs.
func commandLine(cmd *exec.Cmd) string { return strings.Join(cmd.Args[1:], " ") }
