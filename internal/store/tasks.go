package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// ---- scheduled task runs

const taskRunCols = `id, site_id, task_id, task_name, trigger, user, status, started, finished, exit_code, log_path, error`

func scanTaskRun(sc interface{ Scan(...any) error }) (*model.TaskRun, error) {
	var r model.TaskRun
	var started int64
	var finished, code sql.NullInt64
	if err := sc.Scan(&r.ID, &r.SiteID, &r.TaskID, &r.TaskName, &r.Trigger, &r.User, &r.Status, &started, &finished, &code, &r.LogPath, &r.Error); err != nil {
		return nil, err
	}
	r.StartedAt = fromMS(started)
	if finished.Valid {
		t := fromMS(finished.Int64)
		r.FinishedAt = &t
	}
	if code.Valid {
		c := int(code.Int64)
		r.ExitCode = &c
	}
	return &r, nil
}

func (s *Store) PutTaskRun(ctx context.Context, r *model.TaskRun) error {
	var finished, code any
	if r.FinishedAt != nil {
		finished = ms(*r.FinishedAt)
	}
	if r.ExitCode != nil {
		code = *r.ExitCode
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_runs (`+taskRunCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET status = excluded.status, finished = excluded.finished,
			exit_code = excluded.exit_code, log_path = excluded.log_path, error = excluded.error`,
		r.ID, r.SiteID, r.TaskID, r.TaskName, r.Trigger, r.User, r.Status, ms(r.StartedAt), finished, code, r.LogPath, r.Error)
	return err
}

func (s *Store) GetTaskRun(ctx context.Context, id string) (*model.TaskRun, error) {
	r, err := scanTaskRun(s.db.QueryRowContext(ctx, `SELECT `+taskRunCols+` FROM task_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ListTaskRuns returns a site's runs newest first, of one task or of all
// when taskID is empty.
func (s *Store) ListTaskRuns(ctx context.Context, siteID, taskID string, limit int) ([]*model.TaskRun, error) {
	q := `SELECT ` + taskRunCols + ` FROM task_runs WHERE site_id = ?`
	args := []any{siteID}
	if taskID != "" {
		q += ` AND task_id = ?`
		args = append(args, taskID)
	}
	q += ` ORDER BY started DESC, rowid DESC LIMIT ?`
	args = append(args, limit)
	return s.queryTaskRuns(ctx, q, args...)
}

// LastTaskRuns returns the most recent run of each of a site's tasks that
// actually ran (skipped runs are left out), keyed by task id.
func (s *Store) LastTaskRuns(ctx context.Context, siteID string) (map[string]*model.TaskRun, error) {
	list, err := s.queryTaskRuns(ctx, `SELECT `+taskRunCols+` FROM task_runs t
		WHERE site_id = ?1 AND status != 'skipped' AND rowid = (
			SELECT rowid FROM task_runs WHERE site_id = ?1 AND task_id = t.task_id AND status != 'skipped'
			ORDER BY started DESC, rowid DESC LIMIT 1)`, siteID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*model.TaskRun, len(list))
	for _, r := range list {
		out[r.TaskID] = r
	}
	return out, nil
}

func (s *Store) queryTaskRuns(ctx context.Context, q string, args ...any) ([]*model.TaskRun, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.TaskRun{}
	for rows.Next() {
		r, err := scanTaskRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneTaskRuns keeps the newest keep finished runs of a task and deletes
// the rest, returning their log files for the caller to remove.
func (s *Store) PruneTaskRuns(ctx context.Context, siteID, taskID string, keep int) ([]string, error) {
	return s.deleteTaskRuns(ctx, `SELECT id, log_path FROM task_runs WHERE site_id = ? AND task_id = ? AND status != 'running'
		ORDER BY started DESC, rowid DESC LIMIT -1 OFFSET ?`, siteID, taskID, keep)
}

// DeleteTaskRuns removes every finished run of a task (the task was
// deleted), returning their log files.
func (s *Store) DeleteTaskRuns(ctx context.Context, siteID, taskID string) ([]string, error) {
	return s.deleteTaskRuns(ctx, `SELECT id, log_path FROM task_runs WHERE site_id = ? AND task_id = ? AND status != 'running'`, siteID, taskID)
}

func (s *Store) deleteTaskRuns(ctx context.Context, sel string, args ...any) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, sel, args...)
	if err != nil {
		return nil, err
	}
	var ids, logs []string
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		if path != "" {
			logs = append(logs, path)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_runs WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	return logs, tx.Commit()
}

// FailInterruptedTaskRuns marks runs still recorded as running as failed:
// at startup that can only mean the service stopped while they ran (the
// Job Object killed the processes with it).
func (s *Store) FailInterruptedTaskRuns(ctx context.Context, at time.Time, msg string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE task_runs SET status = 'failed', finished = ?, error = ? WHERE status = 'running'`, ms(at), msg)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
