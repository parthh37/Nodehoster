package api

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/auth"
	"github.com/parthh37/nodehoster/internal/ipban"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
)

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TOTP     string `json:"totp"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	if sso := a.c.Settings().SSO; !sso.PasswordAllowed() {
		writeErr(w, http.StatusForbidden, "password sign-in is turned off; sign in with single sign-on")
		return
	}
	u, token, err := a.c.Auth.Login(r.Context(), in.Username, in.Password, in.TOTP, clientIP(r), r.UserAgent())
	switch {
	case errors.Is(err, auth.ErrTOTPRequired):
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error(), "totpRequired": true})
		return
	case errors.Is(err, auth.ErrLocked):
		a.c.Bans.Record(net.ParseIP(clientIP(r)), ipban.AuthFailure)
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	case err != nil:
		// A wrong password (or code) counts towards banning the address,
		// as a failed basic authentication on a site does.
		a.c.Bans.Record(net.ParseIP(clientIP(r)), ipban.AuthFailure)
		a.c.AddAudit(r.Context(), model.AuditEntry{Time: time.Now(), User: in.Username, IP: clientIP(r), Action: "login.failed", Target: in.Username})
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error(), "totpRequired": errors.Is(err, auth.ErrTOTPInvalid)})
		return
	}
	setSessionCookie(w, r, token)
	a.c.AddAudit(r.Context(), model.AuditEntry{Time: time.Now(), User: u.Username, IP: clientIP(r), Action: "login", Target: u.Username})
	writeJSON(w, http.StatusOK, map[string]any{"user": u.User, "mustChangePassword": u.MustChange})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(auth.SessionCookie); err == nil {
		a.c.Auth.Logout(r.Context(), ck.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// me also returns the effective access (the user's, narrowed by the
// token if one is used), which is what the console adapts its pages to.
func (a *API) me(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	writeJSON(w, http.StatusOK, map[string]any{"user": u.User, "mustChangePassword": u.MustChange, "access": access(r)})
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	keep := ""
	if ck, err := r.Cookie(auth.SessionCookie); err == nil {
		keep = ck.Value
	}
	if err := a.c.Auth.ChangePassword(r.Context(), user(r), in.Current, in.New, keep); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "password.change", user(r).Username, "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) totpSetup(w http.ResponseWriter, r *http.Request) {
	secret, url, err := a.c.Auth.TOTPSetup(r.Context(), user(r))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "url": url})
}

func (a *API) totpEnable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.c.Auth.TOTPEnable(r.Context(), user(r), in.Code); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "totp.enable", user(r).Username, "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) totpDisable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.c.Auth.TOTPDisable(r.Context(), user(r), in.Code); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "totp.disable", user(r).Username, "")
	w.WriteHeader(http.StatusNoContent)
}

// ---- tokens

func (a *API) listTokens(w http.ResponseWriter, r *http.Request) {
	list, err := a.c.Store.ListTokens(r.Context(), user(r).ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) createToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string     `json:"name"`
		ExpiresDays int        `json:"expiresDays"`
		Role        model.Role `json:"role"`    // optional maximum role
		SiteIDs     []string   `json:"siteIds"` // optional; empty = not restricted to sites
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	// Token requests skip the forced password change (so automation keeps
	// working after an admin reset); minting one must not be a way around it.
	if user(r).MustChange {
		writeErr(w, http.StatusForbidden, "change your password first")
		return
	}
	role, siteIDs, err := a.tokenRestriction(r, in.Role, in.SiteIDs)
	if err != nil {
		a.fail(w, err)
		return
	}
	raw, t, err := a.c.Auth.CreateRestrictedToken(r.Context(), user(r).ID, in.Name, in.ExpiresDays, role, siteIDs)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "token.create", in.Name, a.describeTokenRestriction(t))
	writeJSON(w, http.StatusCreated, map[string]any{"token": raw, "info": t})
}

// tokenRestriction validates a token's optional restriction against what
// the caller may do: the role no higher than theirs, the sites among
// theirs. A role equal to the caller's is kept: it still caps the token if
// the user is promoted later.
func (a *API) tokenRestriction(r *http.Request, role model.Role, siteIDs []string) (model.Role, []string, error) {
	acc := access(r)
	switch role {
	case "", model.RoleViewer, model.RoleOperator, model.RoleAdmin:
	default:
		return "", nil, &model.ValidationError{Field: "role", Message: "viewer, operator or admin (or empty for your own role)"}
	}
	if role != "" && auth.Weaker(acc.Highest(), role) {
		return "", nil, &model.ValidationError{Field: "role", Message: "a token cannot have more rights than you have"}
	}
	if len(siteIDs) == 0 {
		return role, nil, nil
	}
	if role == model.RoleAdmin {
		return "", nil, &model.ValidationError{Field: "role", Message: "a token restricted to sites is at most operator: site configuration needs a server administrator"}
	}
	ids := make([]string, 0, len(siteIDs))
	for i, id := range siteIDs {
		if _, err := a.c.Site(id); err != nil || !acc.CanSee(id) {
			return "", nil, &model.ValidationError{Field: fmt.Sprintf("siteIds[%d]", i), Message: "no such site"}
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return role, ids, nil
}

// describeTokenRestriction is the audit detail of a new token.
func (a *API) describeTokenRestriction(t *model.APIToken) string {
	var parts []string
	if t.Role != "" {
		parts = append(parts, "role "+string(t.Role))
	}
	if t.SiteIDs != nil {
		parts = append(parts, "sites "+strings.Join(a.siteNames(t.SiteIDs), ", "))
	}
	return strings.Join(parts, "; ")
}

// siteNames names sites by ID for audit details; a deleted site shows as
// its ID.
func (a *API) siteNames(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if s, err := a.c.Site(id); err == nil {
			out = append(out, s.Name)
		} else {
			out = append(out, id)
		}
	}
	return out
}

func (a *API) deleteToken(w http.ResponseWriter, r *http.Request) {
	if err := a.c.Store.DeleteToken(r.Context(), user(r).ID, chi.URLParam(r, "id")); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "token.delete", chi.URLParam(r, "id"), "")
	w.WriteHeader(http.StatusNoContent)
}

// ---- users

func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	list, err := a.c.Store.ListUsers(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]model.User, 0, len(list))
	for _, u := range list {
		out = append(out, u.User)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) siteExists(id string) bool {
	_, err := a.c.Site(id)
	return err == nil
}

// describeAccess is the audit detail of a user's access, naming the sites
// of a site-scoped user and the role on each.
func (a *API) describeAccess(u *model.User) string {
	if u.Role != model.RoleSites {
		return string(u.Role) + " (all sites)"
	}
	parts := make([]string, 0, len(u.Sites))
	for _, g := range u.Sites {
		parts = append(parts, a.siteNames([]string{g.SiteID})[0]+": "+string(g.Role))
	}
	return "sites: " + strings.Join(parts, ", ")
}

func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string            `json:"username"`
		Password string            `json:"password"`
		Role     model.Role        `json:"role"`
		Sites    []model.SiteGrant `json:"sites"`
		// SSO creates a user who signs in only with single sign-on: no
		// password, so none to hand over or to be forced to change.
		SSO bool `json:"sso"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	if len(in.Username) < 2 || len(in.Username) > 64 {
		a.fail(w, &model.ValidationError{Field: "username", Message: "2-64 characters"})
		return
	}
	if err := auth.CheckUserAccess(in.Role, in.Sites, a.siteExists); err != nil {
		a.fail(w, err)
		return
	}
	u := &store.UserRecord{User: model.User{ID: uuid.NewString(), Username: in.Username, Role: in.Role, Sites: in.Sites, CreatedAt: time.Now()}, MustChange: true}
	if in.SSO {
		u.SSO, u.MustChange = true, false
	} else {
		hash, err := auth.HashPassword(in.Password)
		if err != nil {
			a.fail(w, &model.ValidationError{Field: "password", Message: err.Error()})
			return
		}
		u.PasswordHash = hash
	}
	if err := a.c.Store.PutUser(r.Context(), u); err != nil {
		a.fail(w, &model.ValidationError{Field: "username", Message: err.Error()})
		return
	}
	detail := a.describeAccess(&u.User)
	if u.SSO {
		detail += "; single sign-on only"
	}
	a.audit(r, "user.create", in.Username, detail)
	writeJSON(w, http.StatusCreated, u.User)
}

// lastAdmin reports whether removing admin rights from id would leave no
// enabled administrator.
func (a *API) lastAdmin(r *http.Request, id string) bool {
	list, _ := a.c.Store.ListUsers(r.Context())
	for _, u := range list {
		if u.ID != id && u.Role == model.RoleAdmin && !u.Disabled {
			return false
		}
	}
	return true
}

func (a *API) updateUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u, err := a.c.Store.GetUser(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	var in struct {
		Role *model.Role `json:"role"`
		// Sites replaces the grants of a site-scoped user. Leaving it out
		// keeps them (when the role stays "sites").
		Sites    *[]model.SiteGrant `json:"sites"`
		Disabled *bool              `json:"disabled"`
		Password *string            `json:"password"`
		// ResetTOTP turns two-factor authentication off, for a user who
		// lost their authenticator. They can enroll again after signing in.
		ResetTOTP bool `json:"resetTotp"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	accessBefore := a.describeAccess(&u.User)
	if in.Role != nil || in.Sites != nil {
		role, sites := u.Role, u.Sites
		if in.Role != nil {
			role = *in.Role
			if role != model.RoleSites {
				sites = nil // a server-wide role replaces the grants
			}
		}
		if in.Sites != nil {
			sites = *in.Sites
		}
		if err := auth.CheckUserAccess(role, sites, a.siteExists); err != nil {
			a.fail(w, err)
			return
		}
		if role != model.RoleAdmin && u.Role == model.RoleAdmin && a.lastAdmin(r, id) {
			a.fail(w, errors.New("this is the last administrator"))
			return
		}
		u.Role, u.Sites = role, sites
	}
	if in.Disabled != nil {
		if *in.Disabled && u.Role == model.RoleAdmin && a.lastAdmin(r, id) {
			a.fail(w, errors.New("this is the last administrator"))
			return
		}
		u.Disabled = *in.Disabled
	}
	if in.Password != nil && *in.Password != "" {
		hash, err := auth.HashPassword(*in.Password)
		if err != nil {
			a.fail(w, &model.ValidationError{Field: "password", Message: err.Error()})
			return
		}
		u.PasswordHash, u.MustChange = hash, id != user(r).ID
		u.SSO = false // with a password, an SSO-created user is an ordinary one
	}
	if in.ResetTOTP {
		u.TOTPEnabled, u.TOTPSecret = false, ""
	}
	if err := a.c.Store.PutUser(r.Context(), u); err != nil {
		a.fail(w, err)
		return
	}
	if u.Disabled || in.ResetTOTP || (in.Password != nil && *in.Password != "") {
		a.c.Store.DeleteUserSessions(r.Context(), u.ID, "")
	}
	var details []string
	if after := a.describeAccess(&u.User); after != accessBefore {
		details = append(details, "access "+accessBefore+" -> "+after)
	}
	if in.ResetTOTP {
		details = append(details, "two-factor authentication reset")
	}
	a.audit(r, "user.update", u.Username, strings.Join(details, "; "))
	writeJSON(w, http.StatusOK, u.User)
}

func (a *API) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == user(r).ID {
		a.fail(w, errors.New("you cannot delete your own account"))
		return
	}
	u, err := a.c.Store.GetUser(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	if u.Role == model.RoleAdmin && a.lastAdmin(r, id) {
		a.fail(w, errors.New("this is the last administrator"))
		return
	}
	if err := a.c.Store.DeleteUser(r.Context(), id); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "user.delete", u.Username, "")
	w.WriteHeader(http.StatusNoContent)
}
