package auth

import (
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestWithPreviews(t *testing.T) {
	previews := func(id string) []string {
		return map[string][]string{"shop": {"shop-pr-1", "shop-pr-2"}, "blog": {"blog-pr-9"}}[id]
	}
	u := &model.User{Role: model.RoleSites, Sites: []model.SiteGrant{
		{SiteID: "shop", Role: model.RoleOperator},
		{SiteID: "blog", Role: model.RoleViewer},
		{SiteID: "blog-pr-9", Role: model.RoleOperator}, // granted directly: the better role wins
	}}
	acc := UserAccess(u).WithPreviews(previews)
	for id, want := range map[string]model.Role{
		"shop": model.RoleOperator, "shop-pr-1": model.RoleOperator, "shop-pr-2": model.RoleOperator,
		"blog": model.RoleViewer, "blog-pr-9": model.RoleOperator, "other": "",
	} {
		if got := acc.SiteRole(id); got != want {
			t.Errorf("SiteRole(%s) = %q, want %q", id, got, want)
		}
	}
	// A token restricted to one site reaches that site's previews only,
	// with the token's role.
	tok := &model.APIToken{SiteIDs: []string{"blog"}}
	acc = UserAccess(u).Restrict(tok).WithPreviews(previews)
	if acc.CanSee("shop-pr-1") || !acc.CanSee("blog-pr-9") || acc.OnSite("blog-pr-9", model.RoleOperator) {
		t.Errorf("restricted token access = %+v", acc)
	}
	// Server-wide access is unchanged: it covers every site already.
	admin := Access{Role: model.RoleAdmin}
	if got := admin.WithPreviews(previews); got.Role != model.RoleAdmin || got.Sites != nil {
		t.Errorf("server-wide = %+v", got)
	}
	// The receiver is not modified.
	orig := UserAccess(u)
	orig.WithPreviews(previews)
	if len(orig.Sites) != 3 {
		t.Errorf("WithPreviews changed its receiver: %+v", orig.Sites)
	}
}
