//go:build windows

package procmgr

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
	"golang.org/x/sys/windows"
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

// TestHelperOnlyForTheInstance: the Ctrl+Break helper is only started for
// a process inside the instance's job, and, for an instance of the
// service's own account, with the service's token (a copy of the
// instance's is kept only for another account's).
func TestHelperOnlyForTheInstance(t *testing.T) {
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "ping.exe"), "-n", "30", "127.0.0.1")
	cleanup, err := prepare(cmd, model.RunAsConfig{}, "", t.TempDir())
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	osp, err := afterStart(cmd.Process.Pid, model.ProcessLimits{})
	if err != nil {
		cmd.Process.Kill()
		t.Fatal(err)
	}
	defer func() {
		osp.kill(cmd.Process.Pid)
		cmd.Wait()
		osp.release()
	}()
	if osp.token != 0 {
		t.Fatal("kept a token for a process of the service's own account")
	}
	open := func(pid int) windows.Handle {
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { windows.CloseHandle(h) })
		return h
	}
	if tok, err := osp.helperToken(open(cmd.Process.Pid)); err != nil || tok != 0 {
		t.Fatalf("the instance: %v %v", tok, err)
	}
	if _, err := osp.helperToken(open(os.Getpid())); err == nil {
		t.Fatal("a process outside the instance's job was accepted")
	}
}
