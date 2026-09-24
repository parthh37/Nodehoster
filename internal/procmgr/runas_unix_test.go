//go:build !windows

package procmgr

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// gone waits for the process whose PID is in file to have exited.
func gone(t *testing.T, file string) bool {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
	}
	syscall.Kill(pid, syscall.SIGKILL)
	return false
}

// TestRunAsKillsTheTree: what a deployment command starts goes with it,
// when it is cancelled and when it exits leaving something behind. (Only
// the process tree is tested here: logging on as another account needs
// Windows, or root.)
func TestRunAsKillsTheTree(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.Command("sh", "-c", `sleep 30 >/dev/null 2>&1 & echo $! > "$1"; wait`, "sh", pidFile)
	go func() {
		for range 250 {
			if _, err := os.Stat(pidFile); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	if err := RunAs(ctx, cmd, model.RunAsConfig{}, "", dir); err == nil {
		t.Fatal("a cancelled command succeeded")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancelling did not stop the command")
	}
	if !gone(t, pidFile) {
		t.Fatal("the command's child survived the cancellation")
	}

	os.Remove(pidFile)
	cmd = exec.Command("sh", "-c", `sleep 30 >/dev/null 2>&1 & echo $! > "$1"`, "sh", pidFile)
	if err := RunAs(context.Background(), cmd, model.RunAsConfig{}, "", dir); err != nil {
		t.Fatal(err)
	}
	if !gone(t, pidFile) {
		t.Fatal("what the command left running survived it")
	}

	cmd = exec.Command("sh", "-c", "exit 3")
	var ee *exec.ExitError
	if err := RunAs(context.Background(), cmd, model.RunAsConfig{}, "", dir); !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("exit status: %v", err)
	}
}
