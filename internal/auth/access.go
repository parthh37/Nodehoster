package auth

import (
	"fmt"
	"slices"

	"github.com/parthh37/nodehoster/internal/model"
)

// Access is what one request may do: its user's own permissions, narrowed
// by the API token's restriction when it carries one. It is either
// server-wide (Role admin, operator or viewer, which applies to the server
// and to every site) or site-scoped (Role "sites": no server-wide rights,
// and the role of each grant on that site only), as IIS Manager separates
// server administrators from users permitted on individual sites.
//
// The zero value allows nothing.
type Access struct {
	Role  model.Role        `json:"role"`
	Sites []model.SiteGrant `json:"sites,omitempty"`
}

func rank(r model.Role) int {
	switch r {
	case model.RoleViewer:
		return 1
	case model.RoleOperator:
		return 2
	case model.RoleAdmin:
		return 3
	}
	return 0 // RoleSites, "" and anything unknown rank below viewer
}

// lesser returns the weaker of two roles.
func lesser(a, b model.Role) model.Role {
	if rank(a) <= rank(b) {
		return a
	}
	return b
}

// UserAccess is a user's own access. Grants are capped at operator even if
// the stored data says otherwise: there is no site-level administrator.
func UserAccess(u *model.User) Access {
	if u.Role != model.RoleSites {
		return Access{Role: u.Role}
	}
	grants := make([]model.SiteGrant, 0, len(u.Sites))
	for _, g := range u.Sites {
		grants = append(grants, model.SiteGrant{SiteID: g.SiteID, Role: lesser(g.Role, model.RoleOperator)})
	}
	return Access{Role: model.RoleSites, Sites: grants}
}

// Restrict narrows the access to what a token allows (nil: no token). The
// result never exceeds either side, however the two were configured, so a
// token loses whatever its owner loses later.
func (a Access) Restrict(t *model.APIToken) Access {
	if t == nil || !t.Restricted() {
		return a
	}
	limit := func(r model.Role) model.Role {
		if t.Role != "" {
			return lesser(r, t.Role)
		}
		return r
	}
	if !a.SiteScoped() && t.SiteIDs == nil {
		return Access{Role: limit(a.Role)}
	}
	grants := []model.SiteGrant{}
	if a.SiteScoped() {
		for _, g := range a.Sites {
			if t.SiteIDs == nil || slices.Contains(t.SiteIDs, g.SiteID) {
				grants = append(grants, model.SiteGrant{SiteID: g.SiteID, Role: limit(g.Role)})
			}
		}
	} else {
		// A server-wide user's token restricted to sites becomes
		// site-scoped, with the owner's role on each (at most operator).
		for _, id := range t.SiteIDs {
			grants = append(grants, model.SiteGrant{SiteID: id, Role: limit(lesser(a.Role, model.RoleOperator))})
		}
	}
	return Access{Role: model.RoleSites, Sites: grants}
}

// SiteScoped reports whether the access is limited to some sites.
func (a Access) SiteScoped() bool { return a.Role == model.RoleSites }

// Server reports whether a server-wide action needing role is allowed.
// It never is for site-scoped access.
func (a Access) Server(need model.Role) bool {
	return !a.SiteScoped() && Allowed(a.Role, need)
}

// SiteRole is the role on one site; "" when the site is not accessible.
// It does not check that the site exists.
func (a Access) SiteRole(siteID string) model.Role {
	if !a.SiteScoped() {
		return a.Role
	}
	var best model.Role
	for _, g := range a.Sites {
		if g.SiteID == siteID && rank(g.Role) > rank(best) {
			best = g.Role
		}
	}
	return best
}

// OnSite reports whether an action needing role is allowed on a site.
func (a Access) OnSite(siteID string, need model.Role) bool {
	return rank(need) > 0 && rank(a.SiteRole(siteID)) >= rank(need)
}

// CanSee reports whether the site is visible at all.
func (a Access) CanSee(siteID string) bool { return a.OnSite(siteID, model.RoleViewer) }

// SiteIDs lists the sites of site-scoped access (nil for server-wide).
func (a Access) SiteIDs() []string {
	if !a.SiteScoped() {
		return nil
	}
	ids := make([]string, 0, len(a.Sites))
	for _, g := range a.Sites {
		if rank(g.Role) > 0 {
			ids = append(ids, g.SiteID)
		}
	}
	return ids
}

// Highest is the strongest role the access has anywhere: the server-wide
// role, or the best grant of site-scoped access. A token's maximum role
// may not exceed it.
func (a Access) Highest() model.Role {
	if !a.SiteScoped() {
		return a.Role
	}
	var best model.Role
	for _, g := range a.Sites {
		if rank(g.Role) > rank(best) {
			best = g.Role
		}
	}
	return best
}

// Weaker reports whether role a ranks below role b.
func Weaker(a, b model.Role) bool { return rank(a) < rank(b) }

// ValidRole reports whether r is a role a user can have.
func ValidRole(r model.Role) bool {
	return r == model.RoleAdmin || r == model.RoleOperator || r == model.RoleViewer || r == model.RoleSites
}

// CheckUserAccess validates a user's role and site grants together, so that
// no combination is ambiguous or over-privileged: grants only with role
// "sites", at least one of them, each on an existing site, at most once,
// as viewer or operator.
func CheckUserAccess(role model.Role, grants []model.SiteGrant, siteExists func(id string) bool) error {
	if !ValidRole(role) {
		return &model.ValidationError{Field: "role", Message: "admin, operator, viewer or sites"}
	}
	if role != model.RoleSites {
		if len(grants) > 0 {
			return &model.ValidationError{Field: "sites", Message: `site grants need role "sites"; a server-wide role already covers every site`}
		}
		return nil
	}
	if len(grants) == 0 {
		return &model.ValidationError{Field: "sites", Message: "select at least one site"}
	}
	seen := map[string]bool{}
	for i, g := range grants {
		field := fmt.Sprintf("sites[%d]", i)
		if g.Role != model.RoleViewer && g.Role != model.RoleOperator {
			return &model.ValidationError{Field: field + ".role", Message: "viewer or operator (changing a site's configuration needs a server administrator)"}
		}
		if g.SiteID == "" || !siteExists(g.SiteID) {
			return &model.ValidationError{Field: field + ".siteId", Message: "no such site"}
		}
		if seen[g.SiteID] {
			return &model.ValidationError{Field: field + ".siteId", Message: "the site is listed twice"}
		}
		seen[g.SiteID] = true
	}
	return nil
}
