package api

import (
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/tasks"
)

// Scheduled tasks of node and worker sites. The definitions are part of
// the site (PUT /sites/{id}); these endpoints show their schedule and
// history and start or cancel runs.

func (a *API) taskFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tasks.ErrRunning), errors.Is(err, tasks.ErrQueued), errors.Is(err, tasks.ErrTooMany),
		errors.Is(err, tasks.ErrNotRunning), errors.Is(err, tasks.ErrStopping):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		a.fail(w, err)
	}
}

func (a *API) listTasks(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	views, err := a.c.Tasks.Views(core.Masked(s))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views)
}

type runResponse struct {
	Run    *model.TaskRun `json:"run"`    // null when queued
	Queued bool           `json:"queued"` // waiting for the current run (overlap "queue")
}

func (a *API) runTask(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	t, ok := s.Task(chi.URLParam(r, "task"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no such task")
		return
	}
	rec, queued, err := a.c.Tasks.Run(s.ID, t.ID, user(r).Username)
	detail := t.Name
	if queued {
		detail += " (queued)"
	}
	a.audit(r, "task.run", s.Name, detail+suffix(err))
	if err != nil {
		a.taskFail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, runResponse{Run: rec, Queued: queued})
}

func suffix(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}

func (a *API) cancelRun(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	run := a.siteRun(w, r, s)
	if run == nil {
		return
	}
	if err := a.c.Tasks.Cancel(s.ID, run.ID, user(r).Username); err != nil {
		a.taskFail(w, err)
		return
	}
	a.audit(r, "task.cancel", s.Name, run.TaskName)
	writeJSON(w, http.StatusAccepted, run)
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	taskID := ""
	if q := r.URL.Query().Get("task"); q != "" {
		taskID = q
		if t, ok := s.Task(q); ok {
			taskID = t.ID
		}
	}
	list, err := a.c.Store.ListTaskRuns(r.Context(), s.ID, taskID, intParam(r, "limit", 50, 500))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// siteRun loads a run and makes sure it belongs to the site in the path,
// or a user of one site could read another site's task output.
func (a *API) siteRun(w http.ResponseWriter, r *http.Request, s *model.Site) *model.TaskRun {
	run, err := a.c.Store.GetTaskRun(r.Context(), chi.URLParam(r, "run"))
	if err != nil || run.SiteID != s.ID {
		writeErr(w, http.StatusNotFound, "run not found")
		return nil
	}
	return run
}

func (a *API) runLog(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	run := a.siteRun(w, r, s)
	if run == nil {
		return
	}
	f, err := os.Open(run.LogPath)
	if run.LogPath == "" || err != nil {
		writeErr(w, http.StatusNotFound, "this run has no log")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("download") == "true" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeName(s.Name+"-"+run.TaskName)+"-"+run.StartedAt.Format("20060102-150405")+`.log"`)
	}
	http.ServeContent(w, r, "", time.Time{}, f)
}

func (a *API) runLogStream(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	run := a.siteRun(w, r, s)
	if run == nil {
		return
	}
	// What was written so far, then the live output: the backlog ends
	// exactly where the live lines begin, so none is sent twice or lost.
	backlog, lines, done, cancel, running := a.c.Tasks.Subscribe(run.ID)
	defer cancel()
	if !running && run.LogPath != "" {
		backlog, _ = os.ReadFile(run.LogPath)
	}
	stream := newSSE(w)
	if len(backlog) > 0 {
		stream.send("log", string(backlog))
	}
	finish := func() {
		if rec, err := a.c.Store.GetTaskRun(r.Context(), run.ID); err == nil {
			stream.send("done", rec)
		}
	}
	if !running {
		finish()
		return
	}
	ctx, stop := a.siteStreamContext(r, s.ID, model.RoleViewer)
	defer stop()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case l := <-lines:
			if stream.send("log", l) != nil {
				return
			}
		case <-done:
		drain:
			for {
				select {
				case l := <-lines:
					stream.send("log", l)
				default:
					break drain
				}
			}
			finish()
			return
		case <-ping.C:
			if stream.ping() != nil {
				return
			}
		}
	}
}
