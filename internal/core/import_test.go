package core

import (
	"context"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// An import adding a task to an existing site must not undo an edit of
// that site saved while it was waiting for its turn.
func TestAddTaskKeepsConcurrentEdit(t *testing.T) {
	c := testCore(t)
	ctx := context.Background()
	site, err := c.CreateSite(ctx, &model.Site{Name: "worker", Type: model.SiteWorker,
		Node: &model.NodeConfig{AppRoot: t.TempDir(), Script: "w.js"}})
	if err != nil {
		t.Fatal(err)
	}

	// Another edit holds the lock while the import comes in.
	c.sitesMu.Lock()
	added := make(chan error, 1)
	go func() {
		_, err := c.addTask(ctx, site.ID, model.ScheduledTask{Name: "cleanup", Script: "cleanup.js", Enabled: true})
		added <- err
	}()
	time.Sleep(100 * time.Millisecond) // the import is waiting now
	edit := Masked(site)
	edit.Node.Env = []model.EnvVar{{Name: "EDITED", Value: "1"}}
	_, err = c.updateSite(ctx, site.ID, edit)
	c.sitesMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-added; err != nil {
		t.Fatal(err)
	}

	got, err := c.Site(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].Name != "cleanup" {
		t.Errorf("tasks %+v", got.Tasks)
	}
	if len(got.Node.Env) != 1 || got.Node.Env[0].Name != "EDITED" {
		t.Errorf("the concurrent edit was lost: env %+v", got.Node.Env)
	}
}
