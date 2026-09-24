//go:build windows

package procmgr

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// TestRunAsKeepsCmdLineAndKillsTheJob: a deployment's shell command keeps
// its raw cmd.exe command line, and cancelling it kills the whole job: a
// grandchild holding the output pipe does not keep the command waiting.
// (Logging on as another account needs one; the tree is what is tested.)
func TestRunAsKeepsCmdLineAndKillsTheJob(t *testing.T) {
	dir := t.TempDir()
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	}
	var out bytes.Buffer
	cmd := exec.Command(comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `/d /s /c "echo "quoted" & ping -n 30 127.0.0.1"`}
	cmd.Stdout, cmd.Stderr = &out, &out
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	err := RunAs(ctx, cmd, model.RunAsConfig{}, "", dir)
	if err == nil {
		t.Fatal("a cancelled command succeeded")
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("cancelling did not stop the command")
	}
	if !strings.Contains(out.String(), `"quoted"`) {
		t.Errorf("the command line was not kept: %q", out.String())
	}

	cmd = exec.Command(comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `/d /s /c "exit 3"`}
	var ee *exec.ExitError
	if err := RunAs(context.Background(), cmd, model.RunAsConfig{}, "", dir); !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("exit status: %v", err)
	}
}
