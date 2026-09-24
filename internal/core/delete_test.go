package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
	"github.com/parthh37/nodehoster/internal/tasks"
)

// slowTask is a task process that takes as long as the test wants to stop,
// like one using its whole shutdown timeout.
type slowTask struct {
	done    chan struct{}
	release chan struct{}
	once    sync.Once
	asked   chan struct{}
}

func (p *slowTask) PID() int              { return 4242 }
func (p *slowTask) Done() <-chan struct{} { return p.done }
func (p *slowTask) ExitCode() int         { return 0 }
func (p *slowTask) Stop(time.Duration) {
	close(p.asked)
	<-p.release
	p.Kill()
}
func (p *slowTask) Kill() { p.once.Do(func() { close(p.done) }) }

type slowRunner struct{ proc *slowTask }

func (r slowRunner) StartTask(*model.Site, model.ScheduledTask, string, io.Writer) (tasks.Process, error) {
	return r.proc, nil
}

// Deleting a site waits for its task runs to stop, which can take their
// shutdown timeout; other sites must stay editable meanwhile.
func TestDeleteSiteDoesNotBlockOtherEdits(t *testing.T) {
	boot := config.DefaultBootstrap()
	boot.Admin.Listen = "127.0.0.1:0"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A short path: the process manager's agent socket lives in it.
	root, err := os.MkdirTemp("", "nhcore")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	c, err := Open(config.NewPaths(root), boot, log)
	if err != nil {
		t.Fatal(err)
	}
	proc := &slowTask{done: make(chan struct{}), release: make(chan struct{}), asked: make(chan struct{})}
	defer c.Shutdown()
	defer close(proc.release) // before Shutdown, whatever happens
	c.Tasks = tasks.New(tasks.Options{Store: c.Store, Bus: c.Bus, Log: log, LogsDir: c.Paths.SiteLogs, Runner: slowRunner{proc}})

	ctx := context.Background()
	worker, err := c.CreateSite(ctx, &model.Site{Name: "worker", Type: model.SiteWorker,
		Node:  &model.NodeConfig{AppRoot: t.TempDir(), Script: "w.js"},
		Tasks: []model.ScheduledTask{{Name: "export", Script: "export.js", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := c.CreateSite(ctx, &model.Site{Name: "other", Type: model.SiteRedirect,
		Redirect: &model.RedirectConfig{TargetURL: "https://example.com", StatusCode: 301}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Tasks.Run(worker.ID, worker.Tasks[0].ID, "admin"); err != nil {
		t.Fatal(err)
	}

	deleted := make(chan error, 1)
	go func() { deleted <- c.DeleteSite(ctx, worker.ID, true) }()
	<-proc.asked // DeleteSite is now waiting for the run to stop

	if _, err := c.Site(worker.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the site being deleted is still found: %v", err)
	}
	edited := make(chan error, 1)
	go func() {
		o := clone(other)
		o.Redirect.StatusCode = 302
		_, err := c.UpdateSite(ctx, other.ID, o)
		edited <- err
	}()
	select {
	case err := <-edited:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("editing another site waited for the deletion")
	}

	proc.release <- struct{}{}
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store.GetSite(ctx, worker.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the site is still stored: %v", err)
	}
	// The run's record is gone with the site, not written back after.
	if runs, err := c.Store.ListTaskRuns(ctx, worker.ID, worker.Tasks[0].ID, 10); err != nil || len(runs) != 0 {
		t.Errorf("task runs of the deleted site: %+v, %v", runs, err)
	}
}
