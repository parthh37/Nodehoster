package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

func init() {
	register(
		&Command{Name: "task list", Args: "<site>", MinArgs: 1, MaxArgs: 1,
			Summary: "List a site's scheduled tasks, their next run and last result",
			Setup:   func(*flag.FlagSet) Runner { return taskList }},
		&Command{Name: "task run", Args: "<site> <task>", MinArgs: 2, MaxArgs: 2,
			Summary: "Run a task now, showing its output until it ends (--no-wait)",
			Setup:   taskRunCmd},
		&Command{Name: "task runs", Args: "<site> [<task>]", MinArgs: 1, MaxArgs: 2,
			Summary: "List recent runs of a site's tasks (-n count)",
			Setup:   taskRunsCmd},
		&Command{Name: "task cancel", Args: "<site> <run-id>", MinArgs: 2, MaxArgs: 2,
			Summary: "Stop a run in progress",
			Setup:   func(*flag.FlagSet) Runner { return taskCancel }},
	)
}

func (e *Env) tasks(s *localapi.Site) (json.RawMessage, []model.TaskView, error) {
	var list []model.TaskView
	raw, err := e.get(sitePath(s)+"/tasks", &list)
	return raw, list, err
}

// resolveTask finds a site's task by ID, or by name ignoring case (task
// names are unique in a site regardless of case), as the API does.
func (e *Env) resolveTask(s *localapi.Site, ref string) (*model.TaskView, error) {
	_, list, err := e.tasks(s)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == ref {
			return &list[i], nil
		}
	}
	for i := range list {
		if strings.EqualFold(list[i].Name, ref) {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("%s has no task named %q (nodehoster task list %s shows them)", s.Name, ref, s.Name)
}

func taskList(e *Env, args []string) error {
	s, err := e.resolveSite(args[0])
	if err != nil {
		return err
	}
	raw, list, err := e.tasks(s)
	if err != nil || e.JSON {
		if err == nil {
			err = e.printJSON(raw)
		}
		return err
	}
	if len(list) == 0 {
		e.printf("%s has no scheduled tasks.\n", s.Name)
		return nil
	}
	rows := make([][]string, 0, len(list))
	for _, t := range list {
		schedule := t.Schedule
		if schedule == "" {
			schedule = "on demand"
		}
		next := "-"
		if t.NextRunAt != nil {
			next = localTime(*t.NextRunAt)
		}
		last := "-"
		if t.LastRun != nil {
			last = t.LastRun.Status + " " + localTime(t.LastRun.StartedAt)
		}
		if len(t.Running) > 0 {
			last = fmt.Sprintf("%d running", len(t.Running))
		}
		if t.Queued {
			last += ", 1 queued"
		}
		rows = append(rows, []string{t.Name, schedule, yesNo(t.Enabled), next, last, t.ID})
	}
	e.table([]string{"NAME", "SCHEDULE", "ENABLED", "NEXT RUN", "LAST RUN", "ID"}, rows)
	return nil
}

// runStarted is the answer to starting a run: the run, or null when it
// waits for the one in progress (overlap "queue").
type runStarted struct {
	Run    *model.TaskRun `json:"run"`
	Queued bool           `json:"queued"`
}

func taskRunCmd(fs *flag.FlagSet) Runner {
	noWait := fs.Bool("no-wait", false, "return once the run has started")
	return func(e *Env, args []string) error {
		s, err := e.resolveSite(args[0])
		if err != nil {
			return err
		}
		t, err := e.resolveTask(s, args[1])
		if err != nil {
			return err
		}
		var res runStarted
		raw, err := e.post(sitePath(s)+"/tasks/"+url.PathEscape(t.ID)+"/run", &res)
		if err != nil {
			var ae *localapi.Error
			if errors.As(err, &ae) && ae.Status == http.StatusConflict {
				// The overlap policy refused it: a run is going (skip), one
				// already waits (queue) or too many are going (allow).
				return fmt.Errorf("task %s of %s was not started: %s", t.Name, s.Name, ae.Message)
			}
			return err
		}
		if res.Run == nil {
			if e.JSON {
				return e.printJSON(raw)
			}
			e.printf("Task %s of %s is queued: it runs when the current run ends. Follow it with: nodehoster task runs %s %s\n", t.Name, s.Name, s.Name, t.Name)
			return nil
		}
		if *noWait {
			if e.JSON {
				return e.printJSON(raw)
			}
			e.printf("Run %s of task %s of %s started. Follow it with: nodehoster task runs %s %s\n", res.Run.ID, t.Name, s.Name, s.Name, t.Name)
			return nil
		}
		final, err := e.followRun(s, res.Run.ID)
		if err != nil {
			return err
		}
		if e.JSON {
			if err := e.printJSON(final); err != nil {
				return err
			}
		}
		if final.Status != model.RunSucceeded {
			return fmt.Errorf("run %s of task %s of %s %s", final.ID, t.Name, s.Name, runResult(final))
		}
		e.printf("Run %s of task %s of %s succeeded.\n", final.ID, t.Name, s.Name)
		return nil
	}
}

// followRun prints a task run's output as it is written (to stderr with
// --json, which keeps stdout for the result) and returns the run once it
// has ended.
func (e *Env) followRun(s *localapi.Site, runID string) (*model.TaskRun, error) {
	out := e.Stdout
	if e.JSON {
		out = e.Stderr
	}
	var final *model.TaskRun
	err := e.Client.Stream(e.Ctx, sitePath(s)+"/runs/"+url.PathEscape(runID)+"/log/stream", func(event string, data []byte) {
		switch event {
		case "log":
			var chunk string
			if json.Unmarshal(data, &chunk) == nil {
				io.WriteString(out, chunk)
			}
		case "done":
			var r model.TaskRun
			if json.Unmarshal(data, &r) == nil {
				final = &r
			}
		}
	})
	if final != nil {
		return final, nil
	}
	if err == nil || errors.Is(err, io.ErrUnexpectedEOF) {
		err = errors.New("the log stream ended before the run did")
	}
	return nil, fmt.Errorf("following run %s: %w (nodehoster task runs %s shows its result)", runID, err, s.Name)
}

// runResult says how a run ended: "failed: exit code 3".
func runResult(r *model.TaskRun) string {
	switch {
	case r.Error != "":
		return r.Status + ": " + r.Error
	case r.ExitCode != nil:
		return r.Status + ": exit code " + strconv.Itoa(*r.ExitCode)
	}
	return r.Status
}

func taskRunsCmd(fs *flag.FlagSet) Runner {
	n := fs.Int("n", 20, "number of runs, newest first")
	return func(e *Env, args []string) error {
		if *n < 1 || *n > 500 {
			return usagef("-n must be between 1 and 500")
		}
		s, err := e.resolveSite(args[0])
		if err != nil {
			return err
		}
		q := url.Values{"limit": {strconv.Itoa(*n)}}
		if len(args) > 1 {
			t, err := e.resolveTask(s, args[1])
			if err != nil {
				return err
			}
			q.Set("task", t.ID)
		}
		var list []model.TaskRun
		raw, err := e.get(sitePath(s)+"/runs?"+q.Encode(), &list)
		if err != nil || e.JSON {
			if err == nil {
				err = e.printJSON(raw)
			}
			return err
		}
		if len(list) == 0 {
			e.printf("No runs.\n")
			return nil
		}
		rows := make([][]string, 0, len(list))
		for _, r := range list {
			took := "-"
			if r.FinishedAt != nil {
				took = r.FinishedAt.Sub(r.StartedAt).Round(time.Second).String()
			}
			detail := r.Error
			if detail == "" && r.ExitCode != nil {
				detail = "exit code " + strconv.Itoa(*r.ExitCode)
			}
			who := r.Trigger
			if r.User != "" {
				who += " (" + r.User + ")"
			}
			rows = append(rows, []string{r.ID, r.TaskName, r.Status, localTime(r.StartedAt), took, who, truncate(firstLine(detail), 60)})
		}
		e.table([]string{"RUN", "TASK", "STATUS", "STARTED", "TOOK", "TRIGGER", "DETAILS"}, rows)
		return nil
	}
}

func taskCancel(e *Env, args []string) error {
	s, err := e.resolveSite(args[0])
	if err != nil {
		return err
	}
	var r model.TaskRun
	raw, err := e.post(sitePath(s)+"/runs/"+url.PathEscape(args[1])+"/cancel", &r)
	if err != nil {
		var ae *localapi.Error
		if errors.As(err, &ae) && ae.Status == http.StatusConflict {
			return fmt.Errorf("run %s of %s is not in progress", args[1], s.Name)
		}
		return err
	}
	if e.JSON {
		return e.printJSON(raw)
	}
	e.printf("Run %s of task %s of %s is being stopped (nodehoster task runs %s shows when it has).\n", r.ID, r.TaskName, s.Name, s.Name)
	return nil
}
