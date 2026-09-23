package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func openAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	// Close before t.TempDir is removed (cleanups run LIFO); an open
	// database file cannot be deleted on Windows. Double Close is harmless.
	t.Cleanup(func() { s.Close() })
	return s
}

// newStore opens a fresh database in a temp dir.
func newStore(t *testing.T) *Store {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "nodehoster.db"))
}

// msTime returns a time with millisecond precision, matching storage.
func msTime(t time.Time) time.Time { return time.UnixMilli(t.UnixMilli()) }

func TestOpenMigratesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "db.sqlite")

	s := openAt(t, path)
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != len(migrations) {
		t.Fatalf("user_version = %d, want %d", v, len(migrations))
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v; want 1", fk, err)
	}
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v; want wal", mode, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Re-opening must not re-run the migration (which would fail on
	// CREATE TABLE of existing tables).
	s2 := openAt(t, path)
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("after reopen user_version = %d, %v", v, err)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	now := msTime(time.Now())

	s := openAt(t, path)
	u := &UserRecord{User: model.User{ID: "u1", Username: "alice", Role: model.RoleOperator, PasswordHash: "h", TOTPSecret: "enc:v1:x", TOTPEnabled: true, CreatedAt: now}, MustChange: true}
	if err := s.PutUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	site := &model.Site{ID: "s1", Name: "blog", Description: "d", CreatedAt: now, UpdatedAt: now}
	if err := s.PutSite(ctx, site); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDoc(ctx, "general", map[string]int{"port": 8443}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "sess", "u1", "1.2.3.4", "ua", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := openAt(t, path)
	got, err := s2.GetUser(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "alice" || got.Role != model.RoleOperator || !got.TOTPEnabled || !got.MustChange || got.TOTPSecret != "enc:v1:x" || !got.CreatedAt.Equal(now) {
		t.Fatalf("user after reopen = %+v", got)
	}
	gs, err := s2.GetSite(ctx, "s1")
	if err != nil || gs.Name != "blog" || gs.Description != "d" {
		t.Fatalf("site after reopen = %+v, %v", gs, err)
	}
	var doc map[string]int
	if err := s2.GetDoc(ctx, "general", &doc); err != nil || doc["port"] != 8443 {
		t.Fatalf("doc after reopen = %v, %v", doc, err)
	}
	if su, _, err := s2.SessionUser(ctx, "sess"); err != nil || su.ID != "u1" {
		t.Fatalf("session after reopen = %v, %v", su, err)
	}
}

func TestOpenCorruptFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	junk := []byte("this is definitely not an SQLite database file, just some junk bytes....")
	for len(junk) < 4096 {
		junk = append(junk, junk...)
	}
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err == nil {
		s.Close()
		t.Fatal("Open succeeded on a corrupt database file")
	}
}

func TestOpenMissingDirectory(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "nope", "db.sqlite"))
	if err == nil {
		s.Close()
		t.Fatal("Open succeeded in a non-existent directory")
	}
}

// ---- users

func TestUserCRUD(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	if n, err := s.CountUsers(ctx); err != nil || n != 0 {
		t.Fatalf("CountUsers on empty = %d, %v", n, err)
	}
	if _, err := s.GetUser(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUser(missing) err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetUserByName(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUserByName(missing) err = %v, want ErrNotFound", err)
	}

	created := msTime(time.Now().Add(-time.Hour))
	u := &UserRecord{User: model.User{ID: "id-b", Username: "bob", Role: model.RoleViewer, PasswordHash: "hash1", CreatedAt: created}}
	if err := s.PutUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	a := &UserRecord{User: model.User{ID: "id-a", Username: "alice", Role: model.RoleAdmin, PasswordHash: "hash2", CreatedAt: created}}
	if err := s.PutUser(ctx, a); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetUser(ctx, "id-b")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastLogin != nil {
		t.Fatalf("LastLogin = %v, want nil", got.LastLogin)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, created)
	}

	// Username lookup is case-insensitive (COLLATE NOCASE).
	byName, err := s.GetUserByName(ctx, "BoB")
	if err != nil || byName.ID != "id-b" {
		t.Fatalf("GetUserByName(BoB) = %v, %v", byName, err)
	}

	// Update via upsert; CreatedAt must not change on update.
	login := msTime(time.Now())
	u.Role, u.PasswordHash, u.Disabled, u.MustChange, u.LastLogin = model.RoleOperator, "hash3", true, true, &login
	u.CreatedAt = time.Now().Add(24 * time.Hour)
	if err := s.PutUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetUser(ctx, "id-b")
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != model.RoleOperator || got.PasswordHash != "hash3" || !got.Disabled || !got.MustChange {
		t.Fatalf("after update = %+v", got)
	}
	if got.LastLogin == nil || !got.LastLogin.Equal(login) {
		t.Fatalf("LastLogin = %v, want %v", got.LastLogin, login)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("upsert changed CreatedAt to %v", got.CreatedAt)
	}

	list, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Username != "alice" || list[1].Username != "bob" {
		t.Fatalf("ListUsers not ordered by username: %v", list)
	}
	if n, _ := s.CountUsers(ctx); n != 2 {
		t.Fatalf("CountUsers = %d, want 2", n)
	}

	if err := s.DeleteUser(ctx, "id-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUser(ctx, "id-b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted user still found: %v", err)
	}
	if err := s.DeleteUser(ctx, "id-b"); err != nil {
		t.Fatalf("deleting a missing user should be a no-op, got %v", err)
	}
}

func TestPutUserDuplicateUsername(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	if err := s.PutUser(ctx, &UserRecord{User: model.User{ID: "1", Username: "admin", Role: model.RoleAdmin, PasswordHash: "h"}}); err != nil {
		t.Fatal(err)
	}
	// Different ID, same name differing only in case: must be rejected so
	// that "Admin" cannot shadow "admin" at login.
	err := s.PutUser(ctx, &UserRecord{User: model.User{ID: "2", Username: "ADMIN", Role: model.RoleViewer, PasswordHash: "h"}})
	if err == nil || err.Error() != "username already exists" {
		t.Fatalf("duplicate username err = %v", err)
	}
	// Renaming an existing user onto a taken name must fail too.
	if err := s.PutUser(ctx, &UserRecord{User: model.User{ID: "3", Username: "other", Role: model.RoleViewer, PasswordHash: "h"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUser(ctx, &UserRecord{User: model.User{ID: "3", Username: "Admin", Role: model.RoleViewer, PasswordHash: "h"}}); err == nil {
		t.Fatal("rename onto an existing username succeeded")
	}
	if n, _ := s.CountUsers(ctx); n != 2 {
		t.Fatalf("CountUsers = %d, want 2", n)
	}
}

// ---- sessions

func putUser(t *testing.T, s *Store, id, name string) {
	t.Helper()
	if err := s.PutUser(context.Background(), &UserRecord{User: model.User{ID: id, Username: name, Role: model.RoleViewer, PasswordHash: "h", CreatedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
}

func TestSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	putUser(t, s, "u1", "alice")

	exp := msTime(time.Now().Add(time.Hour))
	if err := s.CreateSession(ctx, "h1", "u1", "10.0.0.1", "curl", exp); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "h1", "u1", "", "", exp); err == nil {
		t.Fatal("duplicate session hash accepted")
	}
	if err := s.CreateSession(ctx, "orphan", "no-such-user", "", "", exp); err == nil {
		t.Fatal("session for non-existent user accepted (foreign key not enforced)")
	}

	u, gotExp, err := s.SessionUser(ctx, "h1")
	if err != nil || u.ID != "u1" {
		t.Fatalf("SessionUser = %v, %v", u, err)
	}
	if !gotExp.Equal(exp) {
		t.Fatalf("expiry = %v, want %v", gotExp, exp)
	}
	if _, _, err := s.SessionUser(ctx, "unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session err = %v", err)
	}

	newExp := msTime(time.Now().Add(5 * time.Hour))
	if err := s.ExtendSession(ctx, "h1", newExp); err != nil {
		t.Fatal(err)
	}
	if _, gotExp, _ = s.SessionUser(ctx, "h1"); !gotExp.Equal(newExp) {
		t.Fatalf("after extend expiry = %v, want %v", gotExp, newExp)
	}

	if err := s.DeleteSession(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SessionUser(ctx, "h1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session err = %v", err)
	}
}

func TestExpiredSessionIsRejectedAndRemoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	putUser(t, s, "u1", "alice")

	if err := s.CreateSession(ctx, "old", "u1", "", "", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SessionUser(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session err = %v, want ErrNotFound", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE hash = 'old'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expired session row count = %d, %v; want 0", n, err)
	}
	// Extending an already-deleted expired session must not resurrect it.
	s.ExtendSession(ctx, "old", time.Now().Add(time.Hour))
	if _, _, err := s.SessionUser(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session resurrected: %v", err)
	}
}

func TestDeleteUserSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	putUser(t, s, "u1", "alice")
	putUser(t, s, "u2", "bob")
	exp := time.Now().Add(time.Hour)
	for _, h := range []string{"a1", "a2", "a3"} {
		if err := s.CreateSession(ctx, h, "u1", "", "", exp); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateSession(ctx, "b1", "u2", "", "", exp); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteUserSessions(ctx, "u1", "a2"); err != nil {
		t.Fatal(err)
	}
	for h, want := range map[string]bool{"a1": false, "a2": true, "a3": false, "b1": true} {
		_, _, err := s.SessionUser(ctx, h)
		if got := err == nil; got != want {
			t.Errorf("session %s alive = %v, want %v (err %v)", h, got, want, err)
		}
	}

	// Empty "except" deletes all of the user's sessions.
	if err := s.DeleteUserSessions(ctx, "u1", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SessionUser(ctx, "a2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a2 survived: %v", err)
	}
	if _, _, err := s.SessionUser(ctx, "b1"); err != nil {
		t.Fatalf("other user's session removed: %v", err)
	}
}

func TestDeleteUserCascades(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	putUser(t, s, "u1", "alice")
	if err := s.CreateSession(ctx, "sess", "u1", "", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(ctx, &model.APIToken{ID: "t1", UserID: "u1", Name: "ci", Prefix: "nh_abc", Hash: "th", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SessionUser(ctx, "sess"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session survived user deletion: %v", err)
	}
	if _, err := s.TokenByHash(ctx, "th"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("token survived user deletion: %v", err)
	}
}

// ---- tokens

func TestTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	putUser(t, s, "u1", "alice")
	putUser(t, s, "u2", "bob")

	base := msTime(time.Now().Add(-time.Hour))
	exp := msTime(time.Now().Add(24 * time.Hour))
	t1 := &model.APIToken{ID: "t1", UserID: "u1", Name: "old", Prefix: "nh_111", Hash: "hash1", CreatedAt: base}
	t2 := &model.APIToken{ID: "t2", UserID: "u1", Name: "new", Prefix: "nh_222", Hash: "hash2", CreatedAt: base.Add(time.Minute), ExpiresAt: &exp}
	for _, tk := range []*model.APIToken{t1, t2} {
		if err := s.CreateToken(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateToken(ctx, &model.APIToken{ID: "t3", UserID: "u2", Name: "dup", Hash: "hash1", CreatedAt: base}); err == nil {
		t.Fatal("duplicate token hash accepted")
	}
	if err := s.CreateToken(ctx, &model.APIToken{ID: "t4", UserID: "ghost", Name: "x", Hash: "hash4", CreatedAt: base}); err == nil {
		t.Fatal("token for non-existent user accepted")
	}

	got, err := s.TokenByHash(ctx, "hash2")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "t2" || got.Name != "new" || got.Prefix != "nh_222" || got.UserID != "u1" {
		t.Fatalf("TokenByHash = %+v", got)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Fatalf("ExpiresAt = %v, want %v", got.ExpiresAt, exp)
	}
	if got.LastUsed != nil {
		t.Fatalf("LastUsed = %v, want nil", got.LastUsed)
	}
	if _, err := s.TokenByHash(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TokenByHash(nope) err = %v", err)
	}

	list, err := s.ListTokens(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "t2" || list[1].ID != "t1" {
		t.Fatalf("ListTokens not newest-first: %+v", list)
	}
	if list[1].ExpiresAt != nil {
		t.Fatal("token without expiry got one")
	}
	empty, err := s.ListTokens(ctx, "u2")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("ListTokens(u2) = %#v, %v; want empty non-nil slice", empty, err)
	}

	before := time.Now().Add(-time.Second)
	s.TouchToken(ctx, "t1")
	got, _ = s.TokenByHash(ctx, "hash1")
	if got.LastUsed == nil || got.LastUsed.Before(before) {
		t.Fatalf("LastUsed after touch = %v", got.LastUsed)
	}

	// A user must not be able to delete another user's token.
	if err := s.DeleteToken(ctx, "u2", "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user DeleteToken err = %v, want ErrNotFound", err)
	}
	if _, err := s.TokenByHash(ctx, "hash1"); err != nil {
		t.Fatalf("token removed by another user: %v", err)
	}
	if err := s.DeleteToken(ctx, "u1", "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TokenByHash(ctx, "hash1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted token still resolves: %v", err)
	}
	if err := s.DeleteToken(ctx, "u1", "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

// ---- sites, certificates, deployments

func TestSites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	if _, err := s.GetSite(ctx, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSite(missing) err = %v", err)
	}
	now := msTime(time.Now())
	b := &model.Site{ID: "2", Name: "beta", Bindings: []model.Binding{{ID: "b", Protocol: "http", Port: 80, Host: "beta.example"}}, CreatedAt: now, UpdatedAt: now}
	a := &model.Site{ID: "1", Name: "alpha", CreatedAt: now, UpdatedAt: now}
	for _, site := range []*model.Site{b, a} {
		if err := s.PutSite(ctx, site); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetSite(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "beta" || len(got.Bindings) != 1 || got.Bindings[0].Host != "beta.example" || !got.CreatedAt.Equal(now) {
		t.Fatalf("GetSite = %+v", got)
	}

	if err := s.PutSite(ctx, &model.Site{ID: "3", Name: "ALPHA"}); !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("duplicate site name err = %v, want ErrDuplicateName", err)
	}

	b.Name, b.Description = "gamma", "renamed"
	if err := s.PutSite(ctx, b); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "alpha" || list[1].Name != "gamma" || list[1].Description != "renamed" {
		t.Fatalf("ListSites = %+v", list)
	}
	// The old name must be free again after the rename.
	if err := s.PutSite(ctx, &model.Site{ID: "4", Name: "beta"}); err != nil {
		t.Fatalf("reusing a released name: %v", err)
	}
}

func TestDeleteSiteRemovesHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	now := time.Now()

	for _, id := range []string{"keep", "drop"} {
		if err := s.PutSite(ctx, &model.Site{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
		if err := s.PutDeployment(ctx, &model.Deployment{ID: "d-" + id, SiteID: id, StartedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddMetrics(ctx, id, model.MetricPoint{Time: now, Requests: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteSite(ctx, "drop"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSite(ctx, "drop"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("site survived: %v", err)
	}
	if _, err := s.GetDeployment(ctx, "d-drop"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deployment survived: %v", err)
	}
	if m, _ := s.ListMetrics(ctx, "drop", time.Time{}); len(m) != 0 {
		t.Fatalf("metrics survived: %v", m)
	}
	if _, err := s.GetSite(ctx, "keep"); err != nil {
		t.Fatal("unrelated site removed")
	}
	if _, err := s.GetDeployment(ctx, "d-keep"); err != nil {
		t.Fatal("unrelated deployment removed")
	}
	if m, _ := s.ListMetrics(ctx, "keep", time.Time{}); len(m) != 1 {
		t.Fatal("unrelated metrics removed")
	}
}

func TestCertificates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	if _, err := s.GetCertificate(ctx, "c"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCertificate(missing) err = %v", err)
	}
	c := &model.Certificate{ID: "c1", Name: "main", Domains: []string{"a.example", "b.example"}, Status: "pending"}
	if err := s.PutCertificate(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.Status = "valid"
	if err := s.PutCertificate(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCertificate(ctx, "c1")
	if err != nil || got.Status != "valid" || len(got.Domains) != 2 {
		t.Fatalf("GetCertificate = %+v, %v", got, err)
	}
	list, err := s.ListCertificates(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListCertificates = %v, %v", list, err)
	}
	if err := s.DeleteCertificate(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCertificate(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted certificate still found: %v", err)
	}
}

func TestDeployments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	if _, err := s.GetDeployment(ctx, "d"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDeployment(missing) err = %v", err)
	}
	base := time.Now().Add(-time.Hour)
	for i := range 5 {
		d := &model.Deployment{ID: fmt.Sprintf("d%d", i), SiteID: "s1", Status: "running", StartedAt: base.Add(time.Duration(i) * time.Minute)}
		if err := s.PutDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutDeployment(ctx, &model.Deployment{ID: "other", SiteID: "s2", StartedAt: base}); err != nil {
		t.Fatal(err)
	}

	fin := msTime(time.Now())
	if err := s.PutDeployment(ctx, &model.Deployment{ID: "d2", SiteID: "s1", Status: "succeeded", StartedAt: base.Add(2 * time.Minute), FinishedAt: &fin}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDeployment(ctx, "d2")
	if err != nil || got.Status != "succeeded" || got.FinishedAt == nil || !got.FinishedAt.Equal(fin) {
		t.Fatalf("updated deployment = %+v, %v", got, err)
	}

	list, err := s.ListDeployments(ctx, "s1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].ID != "d4" || list[1].ID != "d3" || list[2].ID != "d2" {
		t.Fatalf("ListDeployments newest-first limit 3 = %v", ids(list))
	}
	if err := s.DeleteDeployment(ctx, "d4"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListDeployments(ctx, "s1", 100); len(list) != 4 {
		t.Fatalf("after delete: %v", ids(list))
	}
}

func ids(ds []*model.Deployment) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return out
}

// ---- settings, audit, events, metrics

func TestDocs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	type cfg struct {
		A string
		B []int
	}
	var v cfg
	if err := s.GetDoc(ctx, "k", &v); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDoc(missing) err = %v", err)
	}
	if err := s.PutDoc(ctx, "k", cfg{A: "x", B: []int{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDoc(ctx, "k", cfg{A: "y"}); err != nil {
		t.Fatal(err)
	}
	if err := s.GetDoc(ctx, "k", &v); err != nil || v.A != "y" || len(v.B) != 0 {
		t.Fatalf("GetDoc = %+v, %v", v, err)
	}
	if err := s.PutDoc(ctx, "bad", func() {}); err == nil {
		t.Fatal("PutDoc of unmarshalable value succeeded")
	}
	// Stored doc of the wrong shape surfaces a decode error, not a panic.
	if err := s.PutDoc(ctx, "str", "text"); err != nil {
		t.Fatal(err)
	}
	if err := s.GetDoc(ctx, "str", &v); err == nil {
		t.Fatal("GetDoc decoded a string into a struct")
	}
}

func TestAudit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	for i := range 5 {
		if err := s.AddAudit(ctx, model.AuditEntry{Time: time.Now(), User: "admin", IP: "::1", Action: fmt.Sprintf("a%d", i), Target: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListAudit(ctx, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Action != "a3" || page[1].Action != "a2" {
		t.Fatalf("ListAudit(2,1) = %+v", page)
	}
	if page[0].ID <= page[1].ID {
		t.Fatal("audit IDs not descending")
	}
	empty, err := s.ListAudit(ctx, 10, 100)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("ListAudit past end = %#v, %v", empty, err)
	}
}

func TestEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	var last int64
	for i, site := range []string{"s1", "s2", "s1", ""} {
		e := &model.Event{Time: time.Now(), Level: "info", Type: "x", SiteID: site, Message: fmt.Sprint(i)}
		if err := s.AddEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		if e.ID <= last {
			t.Fatalf("AddEvent ID = %d, want > %d", e.ID, last)
		}
		last = e.ID
	}
	all, err := s.ListEvents(ctx, "", 10)
	if err != nil || len(all) != 4 || all[0].Message != "3" {
		t.Fatalf("ListEvents(all) = %+v, %v", all, err)
	}
	s1, err := s.ListEvents(ctx, "s1", 10)
	if err != nil || len(s1) != 2 || s1[0].Message != "2" || s1[1].Message != "0" {
		t.Fatalf("ListEvents(s1) = %+v, %v", s1, err)
	}
	if lim, _ := s.ListEvents(ctx, "", 1); len(lim) != 1 {
		t.Fatalf("limit ignored: %d", len(lim))
	}
}

func TestMetrics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	base := msTime(time.Now().Add(-10 * time.Minute))
	for i := range 5 {
		p := model.MetricPoint{Time: base.Add(time.Duration(i) * time.Minute), Requests: int64(i), Errors: 1, AvgLatency: 1.5, CPUPercent: 12.5, MemoryBytes: 1 << 40}
		if err := s.AddMetrics(ctx, "s1", p); err != nil {
			t.Fatal(err)
		}
	}
	// Same (site, ts) replaces the earlier point.
	if err := s.AddMetrics(ctx, "s1", model.MetricPoint{Time: base, Requests: 99}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListMetrics(ctx, "s1", base)
	if err != nil || len(all) != 5 {
		t.Fatalf("ListMetrics = %v, %v", all, err)
	}
	if all[0].Requests != 99 || !all[0].Time.Equal(base) {
		t.Fatalf("upsert not applied: %+v", all[0])
	}
	if all[1].MemoryBytes != 1<<40 || all[1].CPUPercent != 12.5 || all[1].AvgLatency != 1.5 {
		t.Fatalf("point fields lost: %+v", all[1])
	}
	for i := 1; i < len(all); i++ {
		if !all[i].Time.After(all[i-1].Time) {
			t.Fatal("metrics not in ascending time order")
		}
	}
	since, _ := s.ListMetrics(ctx, "s1", base.Add(3*time.Minute))
	if len(since) != 2 {
		t.Fatalf("since filter returned %d points, want 2", len(since))
	}
	if none, err := s.ListMetrics(ctx, "other", time.Time{}); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("other site = %#v, %v", none, err)
	}
}

func TestPrune(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	putUser(t, s, "u1", "alice")

	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-time.Minute)
	for _, ts := range []time.Time{old, recent} {
		if err := s.AddMetrics(ctx, "s1", model.MetricPoint{Time: ts}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddEvent(ctx, &model.Event{Time: ts, Level: "info", Type: "x", Message: ts.String()}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddAudit(ctx, model.AuditEntry{Time: ts, Action: ts.String()}); err != nil {
			t.Fatal(err)
		}
	}
	// Sessions are pruned by their own expiry, not the retention window.
	if err := s.CreateSession(ctx, "expired", "u1", "", "", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "live", "u1", "", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := s.Prune(ctx, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.ListMetrics(ctx, "s1", time.Time{}); len(m) != 1 {
		t.Errorf("metrics after prune = %d, want 1", len(m))
	}
	if e, _ := s.ListEvents(ctx, "", 10); len(e) != 1 {
		t.Errorf("events after prune = %d, want 1", len(e))
	}
	if a, _ := s.ListAudit(ctx, 10, 0); len(a) != 1 {
		t.Errorf("audit after prune = %d, want 1", len(a))
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
	if n != 1 {
		t.Errorf("sessions after prune = %d, want 1", n)
	}
	if _, _, err := s.SessionUser(ctx, "live"); err != nil {
		t.Errorf("live session pruned: %v", err)
	}
}

// ---- concurrency

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	putUser(t, s, "u1", "alice")

	const workers, perWorker = 8, 20
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker*4)
	for w := range workers {
		wg.Go(func() {
			for i := range perWorker {
				key := fmt.Sprintf("w%d-%d", w, i)
				if err := s.PutDoc(ctx, key, i); err != nil {
					errs <- fmt.Errorf("PutDoc: %w", err)
				}
				var got int
				if err := s.GetDoc(ctx, key, &got); err != nil || got != i {
					errs <- fmt.Errorf("GetDoc(%s) = %d, %v", key, got, err)
				}
				if err := s.AddEvent(ctx, &model.Event{Time: time.Now(), Level: "info", Type: "t", Message: key}); err != nil {
					errs <- fmt.Errorf("AddEvent: %w", err)
				}
				if err := s.CreateSession(ctx, key, "u1", "", "", time.Now().Add(time.Hour)); err != nil {
					errs <- fmt.Errorf("CreateSession: %w", err)
				}
				if _, err := s.ListEvents(ctx, "", 5); err != nil {
					errs <- fmt.Errorf("ListEvents: %w", err)
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	all, err := s.ListEvents(ctx, "", workers*perWorker*2)
	if err != nil || len(all) != workers*perWorker {
		t.Fatalf("events = %d, %v; want %d", len(all), err, workers*perWorker)
	}
	seen := map[int64]bool{}
	for _, e := range all {
		if seen[e.ID] {
			t.Fatalf("duplicate event ID %d", e.ID)
		}
		seen[e.ID] = true
	}
}

// Two Store handles on the same file (e.g. the service and a CLI command)
// must see each other's writes.
func TestTwoHandlesSameFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	a := openAt(t, path)
	b := openAt(t, path)

	putUser(t, a, "u1", "alice")
	got, err := b.GetUserByName(ctx, "alice")
	if err != nil || got.ID != "u1" {
		t.Fatalf("second handle GetUserByName = %v, %v", got, err)
	}
	got.PasswordHash = "changed"
	if err := b.PutUser(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, err := a.GetUser(ctx, "u1")
	if err != nil || again.PasswordHash != "changed" {
		t.Fatalf("first handle sees %v, %v", again, err)
	}
}
