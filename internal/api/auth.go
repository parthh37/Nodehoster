package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/auth"
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
	u, token, err := a.c.Auth.Login(r.Context(), in.Username, in.Password, in.TOTP, clientIP(r), r.UserAgent())
	switch {
	case errors.Is(err, auth.ErrTOTPRequired):
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error(), "totpRequired": true})
		return
	case errors.Is(err, auth.ErrLocked):
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	case err != nil:
		a.c.Store.AddAudit(r.Context(), model.AuditEntry{Time: time.Now(), User: in.Username, IP: clientIP(r), Action: "login.failed", Target: in.Username})
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error(), "totpRequired": errors.Is(err, auth.ErrTOTPInvalid)})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: auth.SessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: int(auth.SessionTTL.Seconds()),
	})
	a.c.Store.AddAudit(r.Context(), model.AuditEntry{Time: time.Now(), User: u.Username, IP: clientIP(r), Action: "login", Target: u.Username})
	writeJSON(w, http.StatusOK, map[string]any{"user": u.User, "mustChangePassword": u.MustChange})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(auth.SessionCookie); err == nil {
		a.c.Auth.Logout(r.Context(), ck.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	writeJSON(w, http.StatusOK, map[string]any{"user": u.User, "mustChangePassword": u.MustChange})
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
		Name        string `json:"name"`
		ExpiresDays int    `json:"expiresDays"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	raw, t, err := a.c.Auth.CreateToken(r.Context(), user(r).ID, in.Name, in.ExpiresDays)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "token.create", in.Name, "")
	writeJSON(w, http.StatusCreated, map[string]any{"token": raw, "info": t})
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

func validRole(r model.Role) bool {
	return r == model.RoleAdmin || r == model.RoleOperator || r == model.RoleViewer
}

func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string     `json:"username"`
		Password string     `json:"password"`
		Role     model.Role `json:"role"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	if len(in.Username) < 2 || len(in.Username) > 64 {
		a.fail(w, &model.ValidationError{Field: "username", Message: "2-64 characters"})
		return
	}
	if !validRole(in.Role) {
		a.fail(w, &model.ValidationError{Field: "role", Message: "admin, operator or viewer"})
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		a.fail(w, &model.ValidationError{Field: "password", Message: err.Error()})
		return
	}
	u := &store.UserRecord{User: model.User{ID: uuid.NewString(), Username: in.Username, Role: in.Role, PasswordHash: hash, CreatedAt: time.Now()}, MustChange: true}
	if err := a.c.Store.PutUser(r.Context(), u); err != nil {
		a.fail(w, &model.ValidationError{Field: "username", Message: err.Error()})
		return
	}
	a.audit(r, "user.create", in.Username, string(in.Role))
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
		Role     *model.Role `json:"role"`
		Disabled *bool       `json:"disabled"`
		Password *string     `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	if in.Role != nil {
		if !validRole(*in.Role) {
			a.fail(w, &model.ValidationError{Field: "role", Message: "admin, operator or viewer"})
			return
		}
		if *in.Role != model.RoleAdmin && u.Role == model.RoleAdmin && a.lastAdmin(r, id) {
			a.fail(w, errors.New("this is the last administrator"))
			return
		}
		u.Role = *in.Role
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
	}
	if err := a.c.Store.PutUser(r.Context(), u); err != nil {
		a.fail(w, err)
		return
	}
	if u.Disabled || (in.Password != nil && *in.Password != "") {
		a.c.Store.DeleteUserSessions(r.Context(), u.ID, "")
	}
	a.audit(r, "user.update", u.Username, "")
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
