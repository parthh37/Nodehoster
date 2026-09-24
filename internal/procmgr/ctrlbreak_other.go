//go:build !windows

package procmgr

import (
	"fmt"
	"os"
)

// CtrlBreakCommand is the hidden nodehoster.exe command that sends a
// console Ctrl+Break to a process on Windows (see ctrlbreak_windows.go).
const CtrlBreakCommand = "__ctrl-break"

// RunCtrlBreak is the Windows helper; elsewhere a process is signalled
// directly.
func RunCtrlBreak([]string) int {
	fmt.Fprintln(os.Stderr, "only used on Windows")
	return 2
}

// interrupt asks a runtime without an agent to stop. On Unix that is the
// SIGTERM every process gets: uvicorn, hypercorn and .NET's generic host
// shut down gracefully on it; Bun and Deno run their listeners for it.
func (p *osProc) interrupt(pid int) error { return p.signalStop(pid) }
