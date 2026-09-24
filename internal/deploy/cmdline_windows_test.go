//go:build windows

package deploy

import (
	"os/exec"
	"strings"
)

// commandLine is what a command runs: on Windows the shell's command line
// is passed raw (shellCommand), not as arguments.
func commandLine(cmd *exec.Cmd) string {
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.CmdLine != "" {
		return cmd.SysProcAttr.CmdLine
	}
	return strings.Join(cmd.Args[1:], " ")
}
