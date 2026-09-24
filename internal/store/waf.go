package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// Web application firewall events: requests blocked (or, in detect mode,
// that would have been). The table is created on first use rather than
// by a numbered migration, so it needs no slot in the migrations list.
// Rule IDs and categories are kept as ",942100,941100," for filtering;
// the event itself is a JSON document.

const wafSchema = `CREATE TABLE IF NOT EXISTS waf_events (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	id TEXT NOT NULL,
	time INTEGER NOT NULL,
	site_id TEXT NOT NULL,
	action TEXT NOT NULL,
	client_ip TEXT NOT NULL,
	rules TEXT NOT NULL,
	categories TEXT NOT NULL,
	doc TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS waf_events_site ON waf_events(site_id, seq DESC);
CREATE INDEX IF NOT EXISTS waf_events_time ON waf_events(time);
CREATE INDEX IF NOT EXISTS waf_events_id ON waf_events(id);`

// InitWAF creates the firewall events table if it does not exist.
func (s *Store) InitWAF(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, wafSchema)
	return err
}

// AddWAFEvents stores a batch of events in one transaction.
func (s *Store) AddWAFEvents(ctx context.Context, events []model.WAFEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO waf_events (id, time, site_id, action, client_ip, rules, categories, doc) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i := range events {
		e := events[i]
		e.Seq = 0 // assigned by the table
		doc, err := json.Marshal(&e)
		if err != nil {
			return err
		}
		var rules, cats strings.Builder
		rules.WriteByte(',')
		cats.WriteByte(',')
		for _, m := range e.Matches {
			rules.WriteString(strconv.Itoa(m.RuleID) + ",")
			if !strings.Contains(cats.String(), ","+m.Category+",") {
				cats.WriteString(m.Category + ",")
			}
		}
		if _, err := stmt.ExecContext(ctx, e.ID, ms(e.Time), e.SiteID, e.Action, e.ClientIP, rules.String(), cats.String(), string(doc)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListWAFEvents returns events, newest first.
func (s *Store) ListWAFEvents(ctx context.Context, q model.WAFEventQuery) ([]model.WAFEvent, error) {
	out := []model.WAFEvent{}
	if q.SiteIDs != nil && len(q.SiteIDs) == 0 {
		return out, nil
	}
	var where []string
	var args []any
	if q.SiteIDs != nil {
		where = append(where, `site_id IN (?`+strings.Repeat(", ?", len(q.SiteIDs)-1)+`)`)
		for _, id := range q.SiteIDs {
			args = append(args, id)
		}
	}
	add := func(cond string, arg any) {
		where = append(where, cond)
		args = append(args, arg)
	}
	if q.Action != "" {
		add(`action = ?`, q.Action)
	}
	if q.ClientIP != "" {
		add(`client_ip = ?`, q.ClientIP)
	}
	if q.RuleID > 0 {
		add(`instr(rules, ?) > 0`, ","+strconv.Itoa(q.RuleID)+",")
	}
	if q.Category != "" {
		add(`instr(categories, ?) > 0`, ","+q.Category+",")
	}
	if q.RequestID != "" {
		add(`id = ?`, q.RequestID)
	}
	if !q.Since.IsZero() {
		add(`time >= ?`, ms(q.Since))
	}
	if q.Before > 0 {
		add(`seq < ?`, q.Before)
	}
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := `SELECT seq, doc FROM waf_events`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var doc string
		if err := rows.Scan(&seq, &doc); err != nil {
			return nil, err
		}
		var e model.WAFEvent
		if err := json.Unmarshal([]byte(doc), &e); err != nil {
			continue // a damaged row hides nothing else
		}
		e.Seq = seq
		if e.Matches == nil {
			e.Matches = []model.WAFMatch{}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneWAFEvents deletes events older than before, and all but the newest
// keep.
func (s *Store) PruneWAFEvents(ctx context.Context, before time.Time, keep int) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM waf_events WHERE time < ?`, ms(before)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM waf_events WHERE seq <= (SELECT seq FROM waf_events ORDER BY seq DESC LIMIT 1 OFFSET ?)`, keep)
	return err
}

// DeleteWAFEvents deletes a site's events (the site was deleted).
func (s *Store) DeleteWAFEvents(ctx context.Context, siteID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM waf_events WHERE site_id = ?`, siteID)
	return err
}
