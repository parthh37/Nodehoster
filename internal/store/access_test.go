package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestUserSiteGrantsRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	grants := []model.SiteGrant{{SiteID: "a", Role: model.RoleOperator}, {SiteID: "b", Role: model.RoleViewer}}
	u := &UserRecord{User: model.User{ID: "u1", Username: "agency", Role: model.RoleSites, Sites: grants, PasswordHash: "h", CreatedAt: time.Now()}}
	if err := s.PutUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUser(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != model.RoleSites || !reflect.DeepEqual(got.Sites, grants) {
		t.Fatalf("GetUser = %+v", got.User)
	}

	// A site-scoped user without grants reads back as an empty, non-nil
	// list: still site-scoped, never mistaken for "everything".
	u.Sites = nil
	if err := s.PutUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUser(ctx, "u1"); got.Role != model.RoleSites || got.Sites == nil || len(got.Sites) != 0 {
		t.Fatalf("scoped user without grants = %+v", got.User)
	}

	// Moving to a server-wide role drops the grants for good.
	u.Role, u.Sites = model.RoleViewer, grants
	if err := s.PutUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	u.Role, u.Sites = model.RoleSites, nil
	if err := s.PutUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUser(ctx, "u1"); len(got.Sites) != 0 {
		t.Fatalf("grants resurfaced: %+v", got.Sites)
	}
}

func TestTokenRestrictionRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	putUser(t, s, "u1", "alice")

	for _, tc := range []struct {
		id    string
		role  model.Role
		sites []string
	}{
		{"plain", "", nil},
		{"role", model.RoleViewer, nil},
		{"sites", model.RoleOperator, []string{"a", "b"}},
		{"none", "", []string{}},
	} {
		tk := &model.APIToken{ID: tc.id, UserID: "u1", Name: tc.id, Hash: "h-" + tc.id, CreatedAt: time.Now(), Role: tc.role, SiteIDs: tc.sites}
		if err := s.CreateToken(ctx, tk); err != nil {
			t.Fatal(err)
		}
		got, err := s.TokenByHash(ctx, "h-"+tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Role != tc.role || !reflect.DeepEqual(got.SiteIDs, tc.sites) || (got.SiteIDs == nil) != (tc.sites == nil) {
			t.Errorf("%s: role %q sites %#v, want %q %#v", tc.id, got.Role, got.SiteIDs, tc.role, tc.sites)
		}
		if got.Restricted() != (tc.role != "" || tc.sites != nil) {
			t.Errorf("%s: Restricted = %v", tc.id, got.Restricted())
		}
	}
}

func TestDeleteSiteRemovesGrants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	for _, id := range []string{"keep", "drop"} {
		if err := s.PutSite(ctx, &model.Site{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	both := &UserRecord{User: model.User{ID: "u1", Username: "both", Role: model.RoleSites, PasswordHash: "h", CreatedAt: time.Now(),
		Sites: []model.SiteGrant{{SiteID: "drop", Role: model.RoleOperator}, {SiteID: "keep", Role: model.RoleViewer}}}}
	only := &UserRecord{User: model.User{ID: "u2", Username: "only", Role: model.RoleSites, PasswordHash: "h", CreatedAt: time.Now(),
		Sites: []model.SiteGrant{{SiteID: "drop", Role: model.RoleViewer}}}}
	admin := &UserRecord{User: model.User{ID: "u3", Username: "admin", Role: model.RoleAdmin, PasswordHash: "h", CreatedAt: time.Now()}}
	for _, u := range []*UserRecord{both, only, admin} {
		if err := s.PutUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	for _, tk := range []*model.APIToken{
		{ID: "t1", UserID: "u3", Name: "two", Hash: "h1", SiteIDs: []string{"drop", "keep"}},
		{ID: "t2", UserID: "u3", Name: "one", Hash: "h2", SiteIDs: []string{"drop"}},
		{ID: "t3", UserID: "u3", Name: "all", Hash: "h3"},
	} {
		tk.CreatedAt = time.Now()
		if err := s.CreateToken(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.DeleteSite(ctx, "drop"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUser(ctx, "u1"); !reflect.DeepEqual(got.Sites, []model.SiteGrant{{SiteID: "keep", Role: model.RoleViewer}}) {
		t.Errorf("u1 grants = %+v", got.Sites)
	}
	if got, _ := s.GetUser(ctx, "u2"); got.Role != model.RoleSites || got.Sites == nil || len(got.Sites) != 0 {
		t.Errorf("u2 = %+v, want site-scoped with no grants", got.User)
	}
	if got, _ := s.GetUser(ctx, "u3"); got.Role != model.RoleAdmin || got.Sites != nil {
		t.Errorf("admin changed: %+v", got.User)
	}
	for hash, want := range map[string][]string{"h1": {"keep"}, "h2": {}, "h3": nil} {
		got, err := s.TokenByHash(ctx, hash)
		if err != nil {
			t.Fatal(err)
		}
		// A token restricted to only the deleted site must stay
		// restricted (to nothing), never widen to every site.
		if !reflect.DeepEqual(got.SiteIDs, want) || (got.SiteIDs == nil) != (want == nil) {
			t.Errorf("token %s sites = %#v, want %#v", got.Name, got.SiteIDs, want)
		}
	}
}

func TestListSiteEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	for i, site := range []string{"a", "b", "c", "", "a"} {
		if err := s.AddEvent(ctx, &model.Event{Time: time.Now(), Level: "info", Type: "x", SiteID: site, Message: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListSiteEvents(ctx, []string{"a", "c"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, e := range got {
		msgs = append(msgs, e.Message)
	}
	if !reflect.DeepEqual(msgs, []string{"4", "2", "0"}) {
		t.Errorf("ListSiteEvents = %v", msgs)
	}
	if got, _ := s.ListSiteEvents(ctx, []string{"a", "c"}, 1); len(got) != 1 || got[0].Message != "4" {
		t.Errorf("limit: %+v", got)
	}
	if got, err := s.ListSiteEvents(ctx, nil, 10); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no sites = %#v, %v; want empty (server events are never included)", got, err)
	}
}

// TestMigrationKeepsExistingUsersAndTokens opens a database created before
// per-site permissions and checks existing accounts keep their access.
func TestMigrationKeepsExistingUsersAndTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`PRAGMA user_version = 1`,
		`INSERT INTO users (id, username, role, password_hash, created) VALUES ('u1', 'olga', 'operator', 'h', 0)`,
		`INSERT INTO tokens (id, user_id, name, prefix, hash, created) VALUES ('t1', 'u1', 'ci', 'nh_x', 'th', 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s := openAt(t, path)
	u, err := s.GetUser(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != model.RoleOperator || u.Sites != nil {
		t.Errorf("migrated user = %+v", u.User)
	}
	tk, err := s.TokenByHash(ctx, "th")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Role != "" || tk.SiteIDs != nil || tk.Restricted() {
		t.Errorf("migrated token = %+v", tk)
	}
}
