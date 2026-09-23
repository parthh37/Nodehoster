// Package store persists configuration and history in SQLite (pure Go
// driver, so the Windows binary needs no cgo). Configuration objects are
// stored as JSON documents; history (audit, events, metrics) as rows.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct{ db *sql.DB }

var migrations = []string{
	`CREATE TABLE sites (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE COLLATE NOCASE,
		doc TEXT NOT NULL,
		created INTEGER NOT NULL,
		updated INTEGER NOT NULL
	);
	CREATE TABLE certificates (id TEXT PRIMARY KEY, doc TEXT NOT NULL);
	CREATE TABLE deployments (
		id TEXT PRIMARY KEY,
		site_id TEXT NOT NULL,
		started INTEGER NOT NULL,
		doc TEXT NOT NULL
	);
	CREATE INDEX deployments_site ON deployments(site_id, started DESC);
	CREATE TABLE users (
		id TEXT PRIMARY KEY,
		username TEXT NOT NULL UNIQUE COLLATE NOCASE,
		role TEXT NOT NULL,
		password_hash TEXT NOT NULL,
		totp_secret TEXT NOT NULL DEFAULT '',
		totp_enabled INTEGER NOT NULL DEFAULT 0,
		disabled INTEGER NOT NULL DEFAULT 0,
		must_change INTEGER NOT NULL DEFAULT 0,
		last_login INTEGER,
		created INTEGER NOT NULL
	);
	CREATE TABLE tokens (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		prefix TEXT NOT NULL,
		hash TEXT NOT NULL UNIQUE,
		expires INTEGER,
		last_used INTEGER,
		created INTEGER NOT NULL
	);
	CREATE TABLE sessions (
		hash TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created INTEGER NOT NULL,
		expires INTEGER NOT NULL,
		ip TEXT NOT NULL DEFAULT '',
		user_agent TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE settings (key TEXT PRIMARY KEY, doc TEXT NOT NULL);
	CREATE TABLE audit (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		time INTEGER NOT NULL,
		user TEXT NOT NULL,
		ip TEXT NOT NULL,
		action TEXT NOT NULL,
		target TEXT NOT NULL,
		detail TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		time INTEGER NOT NULL,
		level TEXT NOT NULL,
		type TEXT NOT NULL,
		site_id TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL
	);
	CREATE INDEX events_site ON events(site_id, id DESC);
	CREATE TABLE metrics (
		site_id TEXT NOT NULL,
		ts INTEGER NOT NULL,
		req INTEGER NOT NULL,
		err INTEGER NOT NULL,
		lat REAL NOT NULL,
		cpu REAL NOT NULL,
		mem INTEGER NOT NULL,
		PRIMARY KEY (site_id, ts)
	) WITHOUT ROWID;`,
	// Per-site permissions. users.sites holds the JSON grants of a
	// site-scoped user (role "sites"), NULL for everyone else; tokens.role
	// and tokens.site_ids restrict a token, '' and NULL meaning "as the
	// owner". Existing rows keep NULL/'' and so behave exactly as before.
	`ALTER TABLE users ADD COLUMN sites TEXT;
	ALTER TABLE tokens ADD COLUMN role TEXT NOT NULL DEFAULT '';
	ALTER TABLE tokens ADD COLUMN site_ids TEXT;`,
}

func Open(path string) (*Store, error) {
	q := url.Values{}
	for _, p := range []string{"busy_timeout(10000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// SQLite has one writer; a small pool avoids SQLITE_BUSY storms while
	// still letting readers run alongside the writer in WAL mode.
	db.SetMaxOpenConns(4)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func ms(t time.Time) int64     { return t.UnixMilli() }
func fromMS(v int64) time.Time { return time.UnixMilli(v) }

// ---- sites

func (s *Store) ListSites(ctx context.Context) ([]*model.Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT doc FROM sites ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Site
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var site model.Site
		if err := json.Unmarshal([]byte(doc), &site); err != nil {
			return nil, err
		}
		out = append(out, &site)
	}
	return out, rows.Err()
}

func (s *Store) GetSite(ctx context.Context, id string) (*model.Site, error) {
	var doc string
	err := s.db.QueryRowContext(ctx, `SELECT doc FROM sites WHERE id = ?`, id).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var site model.Site
	return &site, json.Unmarshal([]byte(doc), &site)
}

// ErrDuplicateName is returned when a site name is already taken.
var ErrDuplicateName = errors.New("a site with this name already exists")

func (s *Store) PutSite(ctx context.Context, site *model.Site) error {
	doc, err := json.Marshal(site)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sites (id, name, doc, created, updated) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, doc = excluded.doc, updated = excluded.updated`,
		site.ID, site.Name, string(doc), ms(site.CreatedAt), ms(site.UpdatedAt))
	if err != nil && isUniqueViolation(err) {
		return ErrDuplicateName
	}
	return err
}

func (s *Store) DeleteSite(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM sites WHERE id = ?1`,
		`DELETE FROM deployments WHERE site_id = ?1`,
		`DELETE FROM metrics WHERE site_id = ?1`,
		// Grants and token restrictions naming the site go with it. A
		// token restricted to only this site is left restricted to no
		// site ('[]'), never widened to NULL ("every site").
		`UPDATE users SET sites = (SELECT json_group_array(json(value)) FROM json_each(users.sites) WHERE json_extract(value, '$.siteId') IS NOT ?1)
			WHERE sites IS NOT NULL AND EXISTS (SELECT 1 FROM json_each(users.sites) WHERE json_extract(value, '$.siteId') = ?1)`,
		`UPDATE tokens SET site_ids = (SELECT json_group_array(value) FROM json_each(tokens.site_ids) WHERE value IS NOT ?1)
			WHERE site_ids IS NOT NULL AND EXISTS (SELECT 1 FROM json_each(tokens.site_ids) WHERE value = ?1)`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- certificates

func (s *Store) ListCertificates(ctx context.Context) ([]*model.Certificate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT doc FROM certificates`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Certificate
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var c model.Certificate
		if err := json.Unmarshal([]byte(doc), &c); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (s *Store) GetCertificate(ctx context.Context, id string) (*model.Certificate, error) {
	var doc string
	err := s.db.QueryRowContext(ctx, `SELECT doc FROM certificates WHERE id = ?`, id).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var c model.Certificate
	return &c, json.Unmarshal([]byte(doc), &c)
}

func (s *Store) PutCertificate(ctx context.Context, c *model.Certificate) error {
	doc, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO certificates (id, doc) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET doc = excluded.doc`, c.ID, string(doc))
	return err
}

func (s *Store) DeleteCertificate(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM certificates WHERE id = ?`, id)
	return err
}

// ---- deployments

func (s *Store) ListDeployments(ctx context.Context, siteID string, limit int) ([]*model.Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT doc FROM deployments WHERE site_id = ? ORDER BY started DESC LIMIT ?`, siteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Deployment
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var d model.Deployment
		if err := json.Unmarshal([]byte(doc), &d); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

func (s *Store) GetDeployment(ctx context.Context, id string) (*model.Deployment, error) {
	var doc string
	err := s.db.QueryRowContext(ctx, `SELECT doc FROM deployments WHERE id = ?`, id).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var d model.Deployment
	return &d, json.Unmarshal([]byte(doc), &d)
}

func (s *Store) PutDeployment(ctx context.Context, d *model.Deployment) error {
	doc, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO deployments (id, site_id, started, doc) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET doc = excluded.doc`, d.ID, d.SiteID, ms(d.StartedAt), string(doc))
	return err
}

func (s *Store) DeleteDeployment(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deployments WHERE id = ?`, id)
	return err
}

// ---- settings

func (s *Store) GetDoc(ctx context.Context, key string, v any) error {
	var doc string
	err := s.db.QueryRowContext(ctx, `SELECT doc FROM settings WHERE key = ?`, key).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(doc), v)
}

func (s *Store) PutDoc(ctx context.Context, key string, v any) error {
	doc, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings (key, doc) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET doc = excluded.doc`, key, string(doc))
	return err
}

// ---- audit & events

func (s *Store) AddAudit(ctx context.Context, e model.AuditEntry) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit (time, user, ip, action, target, detail) VALUES (?, ?, ?, ?, ?, ?)`,
		ms(e.Time), e.User, e.IP, e.Action, e.Target, e.Detail)
	return err
}

func (s *Store) ListAudit(ctx context.Context, limit, offset int) ([]model.AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, time, user, ip, action, target, detail FROM audit ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuditEntry{}
	for rows.Next() {
		var e model.AuditEntry
		var t int64
		if err := rows.Scan(&e.ID, &t, &e.User, &e.IP, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		e.Time = fromMS(t)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) AddEvent(ctx context.Context, e *model.Event) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO events (time, level, type, site_id, message) VALUES (?, ?, ?, ?, ?)`,
		ms(e.Time), e.Level, e.Type, e.SiteID, e.Message)
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) ListEvents(ctx context.Context, siteID string, limit int) ([]model.Event, error) {
	q := `SELECT id, time, level, type, site_id, message FROM events`
	args := []any{}
	if siteID != "" {
		q += ` WHERE site_id = ?`
		args = append(args, siteID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		var e model.Event
		var t int64
		if err := rows.Scan(&e.ID, &t, &e.Level, &e.Type, &e.SiteID, &e.Message); err != nil {
			return nil, err
		}
		e.Time = fromMS(t)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListSiteEvents is ListEvents over several sites at once (what a
// site-scoped user may see); server-wide events are never included.
func (s *Store) ListSiteEvents(ctx context.Context, siteIDs []string, limit int) ([]model.Event, error) {
	out := []model.Event{}
	if len(siteIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(siteIDs)+1)
	for _, id := range siteIDs {
		args = append(args, id)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT id, time, level, type, site_id, message FROM events
		WHERE site_id IN (?`+strings.Repeat(", ?", len(siteIDs)-1)+`) ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e model.Event
		var t int64
		if err := rows.Scan(&e.ID, &t, &e.Level, &e.Type, &e.SiteID, &e.Message); err != nil {
			return nil, err
		}
		e.Time = fromMS(t)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- metrics

func (s *Store) AddMetrics(ctx context.Context, siteID string, p model.MetricPoint) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO metrics (site_id, ts, req, err, lat, cpu, mem) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		siteID, ms(p.Time), p.Requests, p.Errors, p.AvgLatency, p.CPUPercent, int64(p.MemoryBytes))
	return err
}

func (s *Store) ListMetrics(ctx context.Context, siteID string, since time.Time) ([]model.MetricPoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, req, err, lat, cpu, mem FROM metrics WHERE site_id = ? AND ts >= ? ORDER BY ts`, siteID, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.MetricPoint{}
	for rows.Next() {
		var p model.MetricPoint
		var t, mem int64
		if err := rows.Scan(&t, &p.Requests, &p.Errors, &p.AvgLatency, &p.CPUPercent, &mem); err != nil {
			return nil, err
		}
		p.Time, p.MemoryBytes = fromMS(t), uint64(mem)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Prune removes history older than the retention window.
func (s *Store) Prune(ctx context.Context, retention time.Duration) error {
	cutoff := ms(time.Now().Add(-retention))
	for _, q := range []string{
		`DELETE FROM metrics WHERE ts < ?`,
		`DELETE FROM events WHERE time < ?`,
		`DELETE FROM audit WHERE time < ?`,
		`DELETE FROM sessions WHERE expires < ?`,
	} {
		arg := cutoff
		if q == `DELETE FROM sessions WHERE expires < ?` {
			arg = ms(time.Now())
		}
		if _, err := s.db.ExecContext(ctx, q, arg); err != nil {
			return err
		}
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
