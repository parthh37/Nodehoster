package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/auth"
	"github.com/parthh37/nodehoster/internal/model"
)

// Authorization. Every request carries an auth.Access, set by authenticate
// (user narrowed by token) or localIdentity (the desktop manager: admin).
// Routes are guarded by one of three middlewares (see routes()):
//
//	require(role)      server-wide endpoints; never site-scoped callers
//	requireSite(role)  /sites/{id}/... endpoints; the role on that site
//	requireFiltered    lists that the handler filters with canSeeSite
//
// Handlers that return several sites' data filter it with canSeeSite (or
// access(r).SiteIDs() for a query), so a site-scoped caller only ever sees
// the sites they were granted.

type accessKey struct{}

func withAccess(ctx context.Context, acc auth.Access) context.Context {
	return context.WithValue(ctx, accessKey{}, acc)
}

// access is the caller's effective access. A request that somehow has
// none gets the zero Access, which allows nothing.
func access(r *http.Request) auth.Access {
	acc, _ := r.Context().Value(accessKey{}).(auth.Access)
	return acc
}

// canSeeSite reports whether the caller may see a site at all (viewer or
// more on it). Use it to filter anything listing several sites.
func canSeeSite(r *http.Request, siteID string) bool {
	return access(r).CanSee(siteID)
}

// canOnSite reports whether the caller has at least role on a site, for a
// handler that must decide per site (requireSite already did for {id}).
func canOnSite(r *http.Request, siteID string, role model.Role) bool {
	return access(r).OnSite(siteID, role)
}

// passwordPending refuses everything to a user who must change their
// password first, unless the request is authenticated by a token (so that
// automation keeps working after an administrator resets the password;
// minting a token is refused in that state, see createToken).
func passwordPending(w http.ResponseWriter, r *http.Request) bool {
	if u := user(r); u != nil && u.MustChange && r.Context().Value(ctxToken) == nil {
		writeErr(w, http.StatusForbidden, "change your password first")
		return true
	}
	return false
}

// require guards a server-wide endpoint: the caller needs role on the whole
// server. Site-scoped callers are refused whatever their grants, so a new
// server-wide route is closed to them by default.
func (a *API) require(role model.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			acc := access(r)
			if acc.SiteScoped() {
				writeErr(w, http.StatusForbidden, "your account only has access to some sites")
				return
			}
			if !acc.Server(role) {
				writeErr(w, http.StatusForbidden, "your role does not allow this")
				return
			}
			if passwordPending(w, r) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireSite guards an endpoint under /sites/{id}: the caller needs role
// on that site. A site the caller cannot see answers 404, the same as a
// site that does not exist, so a site-scoped user cannot probe for others.
func (a *API) requireSite(role model.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			acc, id := access(r), chi.URLParam(r, "id")
			if !acc.OnSite(id, role) {
				if acc.SiteScoped() && !acc.CanSee(id) {
					writeErr(w, http.StatusNotFound, "not found")
					return
				}
				writeErr(w, http.StatusForbidden, "your role does not allow this")
				return
			}
			if passwordPending(w, r) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireFiltered admits server-wide viewers and up, and site-scoped
// callers with or without grants. The handler must return only data of
// sites canSeeSite allows, or data that is neither about a site nor about
// the server (a static catalog).
func (a *API) requireFiltered(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acc := access(r)
		if !acc.SiteScoped() && !acc.Server(model.RoleViewer) {
			writeErr(w, http.StatusForbidden, "your role does not allow this")
			return
		}
		if passwordPending(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// unrestrictedToken refuses requests made with a restricted API token.
func unrestrictedToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if restrictedToken(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// restrictedToken refuses (403) a request made with a restricted API token,
// for the account endpoints and for a change to the token's own user
// through /users/{id}.
func restrictedToken(w http.ResponseWriter, r *http.Request) bool {
	if t, _ := r.Context().Value(ctxToken).(*model.APIToken); t != nil && t.Restricted() {
		writeErr(w, http.StatusForbidden, "a restricted API token cannot manage the account")
		return true
	}
	return false
}

// currentAccess works the caller's access out again from the store, for a
// long-lived stream that must notice a revoked grant or a disabled account
// (ok=false: end the stream).
func (a *API) currentAccess(r *http.Request) (acc auth.Access, ok bool) {
	u := user(r)
	if u == nil || r.Context().Value(localKey{}) != nil {
		return access(r), true // the desktop manager's identity is not stored
	}
	fresh, err := a.c.Store.GetUser(r.Context(), u.ID)
	if err != nil || fresh.Disabled {
		return auth.Access{}, false
	}
	tok, _ := r.Context().Value(ctxToken).(*model.APIToken)
	return auth.UserAccess(&fresh.User).Restrict(tok), true
}

// eventVisible reports whether a live or stored event may be shown to the
// caller: all of them server-wide, only their sites' otherwise (server
// events such as certificate renewals concern the whole server).
func eventVisible(acc auth.Access, e model.Event) bool {
	if !acc.SiteScoped() {
		return true
	}
	return e.SiteID != "" && acc.CanSee(e.SiteID)
}

// visibleSites filters sites to those the access can see.
func visibleSites(acc auth.Access, sites []*model.Site) []*model.Site {
	if !acc.SiteScoped() {
		return sites
	}
	out := make([]*model.Site, 0, len(sites))
	for _, s := range sites {
		if acc.CanSee(s.ID) {
			out = append(out, s)
		}
	}
	return out
}

// siteStreamContext is the context of a long-lived stream of one site's
// output (logs, a deployment's or a task run's log): it ends with the
// request, or within two seconds of the caller losing role on the site (a
// revoked grant, a disabled account), as /api/stream does.
func (a *API) siteStreamContext(r *http.Request, siteID string, role model.Role) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(r.Context())
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if acc, ok := a.currentAccess(r); !ok || !acc.OnSite(siteID, role) {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}
