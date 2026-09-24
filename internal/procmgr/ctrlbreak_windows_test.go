//go:build windows

package procmgr

import (
	"os"
	"testing"
)

// TestMain lets the test binary double as nodehoster.exe's Ctrl+Break
// helper, so the tests that stop Python processes exercise the console
// stop for real (the app prints "graceful stop" from its SIGBREAK handler).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == CtrlBreakCommand {
		os.Exit(RunCtrlBreak(os.Args[2:]))
	}
	ctrlBreakHelper = os.Executable
	os.Exit(m.Run())
}
