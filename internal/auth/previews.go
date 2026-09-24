package auth

import (
	"slices"

	"github.com/parthh37/nodehoster/internal/model"
)

// WithPreviews extends site-scoped access to the previews of the sites it
// covers, with the same role on each: a preview belongs to its parent the
// way a deployment does, so whoever may deploy a site may manage its
// previews. previews lists a site's previews. Server-wide access already
// covers every site.
func (a Access) WithPreviews(previews func(siteID string) []string) Access {
	if !a.SiteScoped() || previews == nil {
		return a
	}
	out := Access{Role: a.Role, Sites: slices.Clone(a.Sites)}
	for _, g := range a.Sites {
		for _, id := range previews(g.SiteID) {
			out.Sites = append(out.Sites, model.SiteGrant{SiteID: id, Role: g.Role})
		}
	}
	return out
}
