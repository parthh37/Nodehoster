package auth

import (
	"errors"
	"reflect"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func grants(pairs ...string) []model.SiteGrant {
	var out []model.SiteGrant
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, model.SiteGrant{SiteID: pairs[i], Role: model.Role(pairs[i+1])})
	}
	return out
}

func TestAccessOnSite(t *testing.T) {
	t.Parallel()
	scoped := UserAccess(&model.User{Role: model.RoleSites, Sites: grants("a", "operator", "b", "viewer")})
	for _, tc := range []struct {
		name string
		acc  Access
		site string
		need model.Role
		want bool
	}{
		{"admin views any site", Access{Role: model.RoleAdmin}, "x", model.RoleViewer, true},
		{"admin configures any site", Access{Role: model.RoleAdmin}, "x", model.RoleAdmin, true},
		{"operator operates any site", Access{Role: model.RoleOperator}, "x", model.RoleOperator, true},
		{"operator cannot configure", Access{Role: model.RoleOperator}, "x", model.RoleAdmin, false},
		{"viewer views", Access{Role: model.RoleViewer}, "x", model.RoleViewer, true},
		{"viewer cannot operate", Access{Role: model.RoleViewer}, "x", model.RoleOperator, false},
		{"granted operator operates", scoped, "a", model.RoleOperator, true},
		{"granted operator cannot configure", scoped, "a", model.RoleAdmin, false},
		{"granted viewer views", scoped, "b", model.RoleViewer, true},
		{"granted viewer cannot operate", scoped, "b", model.RoleOperator, false},
		{"ungranted site is invisible", scoped, "c", model.RoleViewer, false},
		{"empty site ID", scoped, "", model.RoleViewer, false},
		{"zero access allows nothing", Access{}, "a", model.RoleViewer, false},
		{"unknown role allows nothing", Access{Role: "root"}, "a", model.RoleViewer, false},
		{"empty need is never met", Access{Role: model.RoleAdmin}, "a", "", false},
	} {
		if got := tc.acc.OnSite(tc.site, tc.need); got != tc.want {
			t.Errorf("%s: OnSite(%q, %s) = %v, want %v", tc.name, tc.site, tc.need, got, tc.want)
		}
	}
}

func TestAccessServer(t *testing.T) {
	t.Parallel()
	scoped := Access{Role: model.RoleSites, Sites: grants("a", "operator")}
	for _, need := range []model.Role{model.RoleViewer, model.RoleOperator, model.RoleAdmin} {
		if scoped.Server(need) {
			t.Errorf("site-scoped access allowed server-wide %s", need)
		}
		if Allowed(model.RoleSites, need) {
			t.Errorf("Allowed(sites, %s) = true", need)
		}
	}
	if !(Access{Role: model.RoleAdmin}).Server(model.RoleAdmin) || (Access{Role: model.RoleOperator}).Server(model.RoleAdmin) {
		t.Error("server-wide roles misranked")
	}
}

func TestUserAccessCapsGrantsAtOperator(t *testing.T) {
	t.Parallel()
	// Stored data never has admin grants (CheckUserAccess refuses them),
	// but the enforcement must not depend on it.
	acc := UserAccess(&model.User{Role: model.RoleSites, Sites: grants("a", "admin")})
	if acc.OnSite("a", model.RoleAdmin) || !acc.OnSite("a", model.RoleOperator) {
		t.Errorf("admin grant not capped: %+v", acc)
	}
	// Grants on a server-wide user are ignored.
	acc = UserAccess(&model.User{Role: model.RoleViewer, Sites: grants("a", "operator")})
	if acc.SiteScoped() || acc.OnSite("a", model.RoleOperator) {
		t.Errorf("viewer with stray grants = %+v", acc)
	}
}

func TestAccessRestrictIsAnIntersection(t *testing.T) {
	t.Parallel()
	admin := Access{Role: model.RoleAdmin}
	operator := Access{Role: model.RoleOperator}
	viewer := Access{Role: model.RoleViewer}
	scoped := Access{Role: model.RoleSites, Sites: grants("a", "operator", "b", "viewer")}
	tok := func(role model.Role, sites ...string) *model.APIToken {
		t := &model.APIToken{Role: role}
		if sites != nil {
			t.SiteIDs = sites
		}
		return t
	}
	for _, tc := range []struct {
		name string
		acc  Access
		tok  *model.APIToken
		want Access
	}{
		{"no token", operator, nil, operator},
		{"unrestricted token", scoped, tok(""), scoped},
		{"role cap below user", admin, tok(model.RoleViewer), viewer},
		{"role cap above user (user downgraded later)", viewer, tok(model.RoleAdmin), viewer},
		{"admin token to one site becomes operator there", admin, tok("", "x"), Access{Role: model.RoleSites, Sites: grants("x", "operator")}},
		{"viewer token to one site", viewer, tok("", "x"), Access{Role: model.RoleSites, Sites: grants("x", "viewer")}},
		{"operator user, viewer token, two sites", operator, tok(model.RoleViewer, "x", "y"), Access{Role: model.RoleSites, Sites: grants("x", "viewer", "y", "viewer")}},
		{"scoped user, role cap", scoped, tok(model.RoleViewer), Access{Role: model.RoleSites, Sites: grants("a", "viewer", "b", "viewer")}},
		{"scoped user, site subset", scoped, tok("", "b"), Access{Role: model.RoleSites, Sites: grants("b", "viewer")}},
		{"scoped user, token site revoked from user", scoped, tok("", "c"), Access{Role: model.RoleSites, Sites: []model.SiteGrant{}}},
		{"token restricted to no site", admin, &model.APIToken{SiteIDs: []string{}}, Access{Role: model.RoleSites, Sites: []model.SiteGrant{}}},
	} {
		got := tc.acc.Restrict(tc.tok)
		if got.Sites == nil && tc.want.Sites != nil {
			got.Sites = []model.SiteGrant{}
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Restrict = %+v, want %+v", tc.name, got, tc.want)
		}
	}
	// A token restricted to no site sees nothing, anywhere.
	none := admin.Restrict(&model.APIToken{SiteIDs: []string{}})
	if none.CanSee("a") || none.Server(model.RoleViewer) {
		t.Errorf("empty site restriction grants access: %+v", none)
	}
}

func TestAccessHelpers(t *testing.T) {
	t.Parallel()
	scoped := Access{Role: model.RoleSites, Sites: grants("a", "viewer", "b", "operator")}
	if got := scoped.SiteIDs(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("SiteIDs = %v", got)
	}
	if (Access{Role: model.RoleAdmin}).SiteIDs() != nil {
		t.Error("server-wide access lists sites")
	}
	if scoped.Highest() != model.RoleOperator || (Access{Role: model.RoleViewer}).Highest() != model.RoleViewer {
		t.Error("Highest")
	}
	if (Access{Role: model.RoleSites}).Highest() != "" {
		t.Error("Highest of no grants")
	}
	if !Weaker(model.RoleViewer, model.RoleOperator) || Weaker(model.RoleAdmin, model.RoleOperator) || !Weaker(model.RoleSites, model.RoleViewer) {
		t.Error("Weaker")
	}
}

func TestCheckUserAccess(t *testing.T) {
	t.Parallel()
	exists := func(id string) bool { return id == "a" || id == "b" }
	for _, tc := range []struct {
		name   string
		role   model.Role
		grants []model.SiteGrant
		field  string // "" = valid
	}{
		{"admin", model.RoleAdmin, nil, ""},
		{"viewer", model.RoleViewer, nil, ""},
		{"scoped", model.RoleSites, grants("a", "operator", "b", "viewer"), ""},
		{"unknown role", "root", nil, "role"},
		{"empty role", "", nil, "role"},
		{"server role with grants", model.RoleOperator, grants("a", "viewer"), "sites"},
		{"scoped without grants", model.RoleSites, nil, "sites"},
		{"scoped with empty grants", model.RoleSites, []model.SiteGrant{}, "sites"},
		{"admin grant", model.RoleSites, grants("a", "admin"), "sites[0].role"},
		{"sites grant", model.RoleSites, grants("a", "sites"), "sites[0].role"},
		{"missing site", model.RoleSites, grants("a", "viewer", "zzz", "viewer"), "sites[1].siteId"},
		{"blank site", model.RoleSites, grants("", "viewer"), "sites[0].siteId"},
		{"duplicate site", model.RoleSites, grants("a", "viewer", "a", "operator"), "sites[1].siteId"},
	} {
		err := CheckUserAccess(tc.role, tc.grants, exists)
		var ve *model.ValidationError
		switch {
		case tc.field == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.field != "" && (!errors.As(err, &ve) || ve.Field != tc.field):
			t.Errorf("%s: err = %v, want a validation error on %s", tc.name, err, tc.field)
		}
	}
}
