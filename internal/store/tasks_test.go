package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestTaskRuns(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	base := msTime(time.Now().Add(-time.Hour))
	put := func(id, site, task, status string, at time.Time) *model.TaskRun {
		r := &model.TaskRun{ID: id, SiteID: site, TaskID: task, TaskName: "n-" + task, Trigger: "schedule", Status: status, StartedAt: at, LogPath: "/logs/" + id + ".log"}
		if status != model.RunRunning {
			f, code := at.Add(time.Second), 0
			r.FinishedAt, r.ExitCode = &f, &code
		}
		if err := s.PutTaskRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	for i := 0; i < 5; i++ {
		put(fmt.Sprintf("a%d", i), "s1", "t1", model.RunSucceeded, base.Add(time.Duration(i)*time.Minute))
	}
	put("skip", "s1", "t1", model.RunSkipped, base.Add(10*time.Minute))
	put("run", "s1", "t1", model.RunRunning, base.Add(11*time.Minute))
	put("b0", "s1", "t2", model.RunFailed, base)
	put("c0", "s2", "t1", model.RunSucceeded, base)

	got, err := s.GetTaskRun(ctx, "a3")
	if err != nil || got.ExitCode == nil || *got.ExitCode != 0 || got.FinishedAt == nil || !got.StartedAt.Equal(base.Add(3*time.Minute)) || got.LogPath != "/logs/a3.log" {
		t.Fatalf("GetTaskRun = %+v, %v", got, err)
	}
	if _, err := s.GetTaskRun(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("missing run: %v", err)
	}

	list, _ := s.ListTaskRuns(ctx, "s1", "t1", 100)
	if len(list) != 7 || list[0].ID != "run" || list[1].ID != "skip" || list[6].ID != "a0" {
		t.Fatalf("list order: %v", runIDs(list))
	}
	if all, _ := s.ListTaskRuns(ctx, "s1", "", 100); len(all) != 8 {
		t.Fatalf("all runs of s1: %v", runIDs(all))
	}

	last, err := s.LastTaskRuns(ctx, "s1")
	if err != nil || last["t1"].ID != "run" || last["t2"].ID != "b0" || len(last) != 2 {
		t.Fatalf("last runs: %+v %v", last, err)
	}

	// Keep 3: the running run is never pruned and does not count.
	logs, err := s.PruneTaskRuns(ctx, "s1", "t1", 3)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(logs)
	if !slices.Equal(logs, []string{"/logs/a0.log", "/logs/a1.log", "/logs/a2.log"}) {
		t.Fatalf("pruned logs: %v", logs)
	}
	list, _ = s.ListTaskRuns(ctx, "s1", "t1", 100)
	if !slices.Equal(runIDs(list), []string{"run", "skip", "a4", "a3"}) {
		t.Fatalf("after prune: %v", runIDs(list))
	}

	// Finishing the running run updates it in place.
	r, _ := s.GetTaskRun(ctx, "run")
	f, code := time.Now(), 3
	r.Status, r.FinishedAt, r.ExitCode, r.Error = model.RunFailed, &f, &code, "exit code 3"
	if err := s.PutTaskRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r2, _ := s.GetTaskRun(ctx, "run"); r2.Status != model.RunFailed || *r2.ExitCode != 3 || r2.Error != "exit code 3" {
		t.Fatalf("update: %+v", r2)
	}

	put("run2", "s1", "t2", model.RunRunning, base)
	if n, err := s.FailInterruptedTaskRuns(ctx, time.Now(), "restarted"); n != 1 || err != nil {
		t.Fatalf("FailInterruptedTaskRuns = %d, %v", n, err)
	}
	if r, _ := s.GetTaskRun(ctx, "run2"); r.Status != model.RunFailed || r.Error != "restarted" || r.FinishedAt == nil {
		t.Fatalf("interrupted run: %+v", r)
	}

	if logs, _ := s.DeleteTaskRuns(ctx, "s1", "t2"); len(logs) != 2 {
		t.Fatalf("DeleteTaskRuns logs: %v", logs)
	}
	if err := s.DeleteSite(ctx, "s2"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.ListTaskRuns(ctx, "s2", "", 10); len(l) != 0 {
		t.Fatalf("runs of a deleted site remain: %v", runIDs(l))
	}
}

func runIDs(list []*model.TaskRun) []string {
	var out []string
	for _, r := range list {
		out = append(out, r.ID)
	}
	return out
}
