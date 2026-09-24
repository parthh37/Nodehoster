package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/cron"
)

// ScheduledTask is a script a node or worker site runs on a schedule, like
// a Task Scheduler job or a cron entry but inside the site's sandbox: its
// active release, Node.js version, environment and secrets, run-as
// identity and Job Object limits.
type ScheduledTask struct {
	ID   string `json:"id"` // stable across renames; generated when empty
	Name string `json:"name"`
	// Schedule is a 5-field cron expression in server local time
	// ("*/15 * * * *", "0 3 * * mon-fri"), a shorthand (@hourly, @daily,
	// @weekly, @monthly, @yearly) or "@every 30m". Empty = run only on
	// demand.
	Schedule   string   `json:"schedule"`
	Script     string   `json:"script,omitempty"`    // relative to the application folder
	NpmScript  string   `json:"npmScript,omitempty"` // alternatively `npm run <script>`
	Args       []string `json:"args,omitempty"`
	Enabled    bool     `json:"enabled"`
	TimeoutSec int      `json:"timeoutSec"` // the whole process tree is killed after it
	Overlap    string   `json:"overlap"`    // skip | queue | allow
	Env        []EnvVar `json:"env,omitempty"`
}

// What happens when a run is due while the previous one is still going.
const (
	OverlapSkip  = "skip"  // record a skipped run
	OverlapQueue = "queue" // run once right after it (at most one waiting)
	OverlapAllow = "allow" // run concurrently
)

const (
	DefaultTaskTimeoutSec = 3600
	MaxTaskTimeoutSec     = 7 * 24 * 3600
	MaxTasksPerSite       = 50
)

var taskNameRe = siteNameRe

func (t *ScheduledTask) applyDefaults() {
	t.Name = strings.TrimSpace(t.Name)
	t.Schedule = strings.TrimSpace(t.Schedule)
	if t.TimeoutSec <= 0 {
		t.TimeoutSec = DefaultTaskTimeoutSec
	}
	if t.Overlap == "" {
		t.Overlap = OverlapSkip
	}
}

func validateTasks(tasks []ScheduledTask) error {
	if len(tasks) > MaxTasksPerSite {
		return verr("tasks", "at most %d tasks per site", MaxTasksPerSite)
	}
	names, ids := map[string]bool{}, map[string]bool{}
	for i, t := range tasks {
		f := fmt.Sprintf("tasks[%d]", i)
		if !taskNameRe.MatchString(t.Name) {
			return verr(f+".name", "must be 1-64 characters: letters, digits, space, '.', '_' or '-'")
		}
		if names[strings.ToLower(t.Name)] {
			return verr(f+".name", "another task is called %q", t.Name)
		}
		names[strings.ToLower(t.Name)] = true
		if t.ID != "" {
			if ids[t.ID] {
				return verr(f+".id", "duplicate task id")
			}
			ids[t.ID] = true
		}
		if t.Schedule != "" {
			s, err := cron.Parse(t.Schedule)
			if err != nil {
				return verr(f+".schedule", "%v", err)
			}
			if s.Next(time.Now()).IsZero() {
				return verr(f+".schedule", "this schedule never runs")
			}
		}
		if strings.TrimSpace(t.Script) == "" && strings.TrimSpace(t.NpmScript) == "" {
			return verr(f+".script", "set a script or an npm script")
		}
		switch t.Overlap {
		case OverlapSkip, OverlapQueue, OverlapAllow:
		default:
			return verr(f+".overlap", "must be skip, queue or allow")
		}
		if t.TimeoutSec > MaxTaskTimeoutSec {
			return verr(f+".timeoutSec", "at most %d seconds (7 days)", MaxTaskTimeoutSec)
		}
		for j, e := range t.Env {
			if !envNameRe.MatchString(e.Name) {
				return verr(fmt.Sprintf("%s.env[%d].name", f, j), "%q is not a valid variable name", e.Name)
			}
		}
	}
	return nil
}

// Task returns the task with the given id, or else the given name.
func (s *Site) Task(idOrName string) (ScheduledTask, bool) {
	for _, t := range s.Tasks {
		if t.ID == idOrName {
			return t, true
		}
	}
	for _, t := range s.Tasks {
		if strings.EqualFold(t.Name, idOrName) {
			return t, true
		}
	}
	return ScheduledTask{}, false
}

// Run statuses.
const (
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunTimeout   = "timeout"
	RunCancelled = "cancelled"
	RunSkipped   = "skipped"
)

// TaskRun is one run of a scheduled task (or a run that was skipped
// because the previous one was still going).
type TaskRun struct {
	ID         string     `json:"id"`
	SiteID     string     `json:"siteId"`
	TaskID     string     `json:"taskId"`
	TaskName   string     `json:"taskName"` // as it was called when it ran
	Trigger    string     `json:"trigger"`  // schedule | manual
	User       string     `json:"user,omitempty"`
	Status     string     `json:"status"` // running | succeeded | failed | timeout | cancelled | skipped
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	Error      string     `json:"error,omitempty"`
	LogPath    string     `json:"-"` // served through the API, never shown
}

// TaskView is a task definition with its live schedule state.
type TaskView struct {
	ScheduledTask
	NextRunAt *time.Time `json:"nextRunAt,omitempty"` // nil: disabled, on demand only, or never
	LastRun   *TaskRun   `json:"lastRun,omitempty"`
	Running   []string   `json:"running"` // ids of the runs in progress
	Queued    bool       `json:"queued"`  // a run waits for the current one (overlap "queue")
}
