package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

type UserRecord struct {
	model.User
	MustChange bool
}

const userCols = `id, username, role, password_hash, totp_secret, totp_enabled, disabled, must_change, last_login, created, sites`

func scanUser(sc interface{ Scan(...any) error }) (*UserRecord, error) {
	var u UserRecord
	var role string
	var lastLogin sql.NullInt64
	var created int64
	var sites sql.NullString
	err := sc.Scan(&u.ID, &u.Username, &role, &u.PasswordHash, &u.TOTPSecret, &u.TOTPEnabled, &u.Disabled, &u.MustChange, &lastLogin, &created, &sites)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.Role = model.Role(role)
	if u.Role == model.RoleSites {
		u.Sites = []model.SiteGrant{}
		if sites.Valid {
			if err := json.Unmarshal([]byte(sites.String), &u.Sites); err != nil {
				return nil, err
			}
		}
	}
	u.CreatedAt = fromMS(created)
	if lastLogin.Valid {
		t := fromMS(lastLogin.Int64)
		u.LastLogin = &t
	}
	return &u, nil
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	return n, s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
}

func (s *Store) ListUsers(ctx context.Context) ([]*UserRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*UserRecord
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) GetUser(ctx context.Context, id string) (*UserRecord, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *Store) GetUserByName(ctx context.Context, name string) (*UserRecord, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, name))
}

func (s *Store) PutUser(ctx context.Context, u *UserRecord) error {
	var lastLogin any
	if u.LastLogin != nil {
		lastLogin = ms(*u.LastLogin)
	}
	// Grants are stored only for a site-scoped user, so a role change away
	// from "sites" cannot leave grants behind to resurface later.
	var sites any
	if u.Role == model.RoleSites {
		grants := u.Sites
		if grants == nil {
			grants = []model.SiteGrant{}
		}
		data, err := json.Marshal(grants)
		if err != nil {
			return err
		}
		sites = string(data)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (`+userCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET username = excluded.username, role = excluded.role,
		password_hash = excluded.password_hash, totp_secret = excluded.totp_secret,
		totp_enabled = excluded.totp_enabled, disabled = excluded.disabled,
		must_change = excluded.must_change, last_login = excluded.last_login, sites = excluded.sites`,
		u.ID, u.Username, string(u.Role), u.PasswordHash, u.TOTPSecret, u.TOTPEnabled, u.Disabled, u.MustChange, lastLogin, ms(u.CreatedAt), sites)
	if isUniqueViolation(err) {
		return errors.New("username already exists")
	}
	return err
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

// ---- sessions (keyed by SHA-256 of the cookie value)

func (s *Store) CreateSession(ctx context.Context, hash, userID, ip, ua string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (hash, user_id, created, expires, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?)`,
		hash, userID, ms(time.Now()), ms(expires), ip, ua)
	return err
}

// SessionUser returns the user owning a live session.
func (s *Store) SessionUser(ctx context.Context, hash string) (*UserRecord, time.Time, error) {
	var userID string
	var exp int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id, expires FROM sessions WHERE hash = ?`, hash).Scan(&userID, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, ErrNotFound
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	if time.Now().UnixMilli() > exp {
		s.DeleteSession(ctx, hash)
		return nil, time.Time{}, ErrNotFound
	}
	u, err := s.GetUser(ctx, userID)
	return u, fromMS(exp), err
}

func (s *Store) ExtendSession(ctx context.Context, hash string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET expires = ? WHERE hash = ?`, ms(expires), hash)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE hash = ?`, hash)
	return err
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID, exceptHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND hash != ?`, userID, exceptHash)
	return err
}

// ---- API tokens

func (s *Store) CreateToken(ctx context.Context, t *model.APIToken) error {
	var exp any
	if t.ExpiresAt != nil {
		exp = ms(*t.ExpiresAt)
	}
	var siteIDs any // NULL: not restricted to sites
	if t.SiteIDs != nil {
		data, err := json.Marshal(t.SiteIDs)
		if err != nil {
			return err
		}
		siteIDs = string(data)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens (id, user_id, name, prefix, hash, expires, created, role, site_ids) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.Name, t.Prefix, t.Hash, exp, ms(t.CreatedAt), string(t.Role), siteIDs)
	return err
}

func scanToken(sc interface{ Scan(...any) error }) (*model.APIToken, error) {
	var t model.APIToken
	var exp, used sql.NullInt64
	var created int64
	var role string
	var siteIDs sql.NullString
	if err := sc.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.Hash, &exp, &used, &created, &role, &siteIDs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t.CreatedAt = fromMS(created)
	t.Role = model.Role(role)
	if siteIDs.Valid {
		// Never nil once restricted: nil means "every site".
		t.SiteIDs = []string{}
		if err := json.Unmarshal([]byte(siteIDs.String), &t.SiteIDs); err != nil {
			return nil, err
		}
		if t.SiteIDs == nil {
			t.SiteIDs = []string{}
		}
	}
	if exp.Valid {
		v := fromMS(exp.Int64)
		t.ExpiresAt = &v
	}
	if used.Valid {
		v := fromMS(used.Int64)
		t.LastUsed = &v
	}
	return &t, nil
}

const tokenCols = `id, user_id, name, prefix, hash, expires, last_used, created, role, site_ids`

func (s *Store) ListTokens(ctx context.Context, userID string) ([]*model.APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE user_id = ? ORDER BY created DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.APIToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) TokenByHash(ctx context.Context, hash string) (*model.APIToken, error) {
	return scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE hash = ?`, hash))
}

func (s *Store) TouchToken(ctx context.Context, id string) {
	s.db.ExecContext(ctx, `UPDATE tokens SET last_used = ? WHERE id = ?`, ms(time.Now()), id)
}

func (s *Store) DeleteToken(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
