package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// ---- resource alert history
//
// An alert is stored when it fires and updated as it changes (silenced,
// resolved); pending alerts live only in memory. The document is the
// model.Alert; the columns are what queries filter and sort on.

// PutAlert inserts or replaces an alert.
func (s *Store) PutAlert(ctx context.Context, a *model.Alert) error {
	doc, err := json.Marshal(a)
	if err != nil {
		return err
	}
	var fired int64
	if a.FiredAt != nil {
		fired = ms(*a.FiredAt)
	} else {
		fired = ms(a.Since)
	}
	var resolved any
	if a.ResolvedAt != nil {
		resolved = ms(*a.ResolvedAt)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO alerts (id, site_id, rule_id, state, fired, resolved, doc) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET state = excluded.state, resolved = excluded.resolved, doc = excluded.doc`,
		a.ID, a.SiteID, a.RuleID, a.State, fired, resolved, string(doc))
	return err
}

func (s *Store) GetAlert(ctx context.Context, id string) (*model.Alert, error) {
	var doc string
	err := s.db.QueryRowContext(ctx, `SELECT doc FROM alerts WHERE id = ?`, id).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var a model.Alert
	if err := json.Unmarshal([]byte(doc), &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// FiringAlerts returns the alerts that were firing, oldest first (the
// service stopped while they fired).
func (s *Store) FiringAlerts(ctx context.Context) ([]model.Alert, error) {
	return s.queryAlerts(ctx, `SELECT doc FROM alerts WHERE state = ? ORDER BY fired`, model.AlertFiring)
}

// AlertFilter selects alert history.
type AlertFilter struct {
	// SiteIDs limits the history to these sites; nil = every alert,
	// server alerts included; empty (non-nil) = none.
	SiteIDs []string
	// Server includes server alerts (site_id '') alongside SiteIDs.
	Server bool
	Since  time.Time // fired at or after; zero = any time
	Limit  int
}

// ListAlerts returns alert history, most recently fired first.
func (s *Store) ListAlerts(ctx context.Context, f AlertFilter) ([]model.Alert, error) {
	q := `SELECT doc FROM alerts WHERE fired >= ?`
	args := []any{ms(f.Since)}
	if f.SiteIDs != nil {
		ids := f.SiteIDs
		if f.Server {
			ids = append(append([]string{}, ids...), "")
		}
		if len(ids) == 0 {
			return []model.Alert{}, nil
		}
		q += ` AND site_id IN (?` + strings.Repeat(", ?", len(ids)-1) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q += ` ORDER BY fired DESC, rowid DESC LIMIT ?`
	args = append(args, limit)
	return s.queryAlerts(ctx, q, args...)
}

func (s *Store) queryAlerts(ctx context.Context, q string, args ...any) ([]model.Alert, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Alert{}
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var a model.Alert
		if err := json.Unmarshal([]byte(doc), &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PruneAlerts deletes resolved alerts that resolved before cutoff, and
// the oldest resolved ones beyond keep. Firing alerts are never pruned.
func (s *Store) PruneAlerts(ctx context.Context, cutoff time.Time, keep int) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM alerts WHERE state = ? AND resolved < ?`, model.AlertResolved, ms(cutoff)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM alerts WHERE state = ? AND id NOT IN (
		SELECT id FROM alerts WHERE state = ? ORDER BY fired DESC, rowid DESC LIMIT ?)`, model.AlertResolved, model.AlertResolved, keep)
	return err
}
