package core

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/parthh37/nodehoster/internal/config"
)

// openTestCore opens a core over a private data directory. A short path:
// on Unix the process manager's agent socket lives in the data directory
// and socket paths are limited to ~104 bytes.
func openTestCore(t *testing.T) *Core {
	t.Helper()
	root, err := os.MkdirTemp("", "nhcore")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	boot := config.DefaultBootstrap()
	boot.Admin.Listen = "127.0.0.1:0"
	c, err := Open(config.NewPaths(root), boot, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestStartShutdown runs the service lifecycle the way main does: every
// background job Start launches must be accounted for when Shutdown waits,
// including a manual backup still running at that point.
func TestStartShutdown(t *testing.T) {
	c := openTestCore(t)
	c.Start()
	if err := c.StartBackup(); err != nil {
		t.Fatalf("StartBackup: %v", err)
	}
	c.Shutdown()

	// Once Shutdown started, no backup may join the WaitGroup it waits on.
	if err := c.StartBackup(); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("StartBackup after Shutdown: %v, want ErrShuttingDown", err)
	}
}
