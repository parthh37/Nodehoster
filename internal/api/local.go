package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
)

type localKey struct{}

// WithLocalUser marks a connection of the local admin pipe as coming from
// the named Windows account. Only the pipe server calls it, from its
// ConnContext hook: requests on the network listener never carry the value,
// so it cannot be forged by a client.
func WithLocalUser(ctx context.Context, account string) context.Context {
	return context.WithValue(ctx, localKey{}, account)
}

// LocalHandler serves the API to the desktop manager over the local admin
// pipe. The operating system has already authenticated the caller: the
// pipe only admits SYSTEM and elevated Administrators, the same people who
// could stop the service or read the database. So there is no login, no
// session and no CSRF check, and the caller has the admin role. This is
// what keeps the server manageable when the web console is not (its port
// is taken, its certificate is broken, or every admin is locked out).
//
// Account endpoints (password, 2FA, API tokens) are not served: a Windows
// administrator is not a NodeHoster user.
func LocalHandler(c *core.Core) http.Handler {
	a := &API{c: c, log: c.Log}
	r := chi.NewRouter()
	r.Use(a.recoverer)
	r.Route("/api", func(r chi.Router) {
		r.Use(noCache)
		r.Use(a.localIdentity)
		r.Get("/local/whoami", func(w http.ResponseWriter, r *http.Request) {
			account, _ := r.Context().Value(localKey{}).(string)
			writeJSON(w, http.StatusOK, map[string]string{"account": account})
		})
		a.routes(r)
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeErr(w, http.StatusNotFound, "no such endpoint")
		})
	})
	return r
}

func (a *API) localIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account, _ := r.Context().Value(localKey{}).(string)
		if account == "" {
			writeErr(w, http.StatusUnauthorized, "not a local administrator connection")
			return
		}
		u := &store.UserRecord{User: model.User{ID: "local:" + account, Username: account + " (desktop)", Role: model.RoleAdmin}}
		// The audit log shows where a change came from; a pipe has no
		// client address.
		r.RemoteAddr = "local"
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUser, u)))
	})
}
