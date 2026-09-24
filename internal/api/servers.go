package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/auth"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/model"
)

// Server connections: other NodeHoster servers this one manages (see
// model.ServerConnection). Administrators add, change and remove them;
// server-wide users whose role is at least a connection's MinRole see it
// and use it through the proxy (servers_proxy.go). Site-scoped callers
// never do: a connection is the whole of another server.

// mayUseServer reports whether the caller may see and use a connection.
func mayUseServer(acc auth.Access, s model.ServerConnection) bool {
	return acc.Server(s.MinRole)
}

// usableServer finds a connection the caller may use; one they may not
// answers 404, as if it did not exist.
func (a *API) usableServer(w http.ResponseWriter, r *http.Request) (model.ServerConnection, bool) {
	s, err := a.c.ServerConnection(chi.URLParam(r, "id"))
	if err != nil || !mayUseServer(access(r), s) {
		writeErr(w, http.StatusNotFound, "no such server connection")
		return s, false
	}
	return s, true
}

func (a *API) listServers(w http.ResponseWriter, r *http.Request) {
	acc := access(r)
	writeJSON(w, http.StatusOK, a.c.ServerViews(func(s model.ServerConnection) bool { return mayUseServer(acc, s) }))
}

func (a *API) getServer(w http.ResponseWriter, r *http.Request) {
	s, ok := a.usableServer(w, r)
	if !ok {
		return
	}
	v, err := a.c.ServerView(s.ID)
	if err != nil {
		a.serverFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// checkServer checks a connection now (the Servers page's "Check now").
func (a *API) checkServer(w http.ResponseWriter, r *http.Request) {
	s, ok := a.usableServer(w, r)
	if !ok {
		return
	}
	if _, err := a.c.CheckServer(r.Context(), s.ID); err != nil && r.Context().Err() == nil {
		a.serverFail(w, err)
		return
	}
	v, err := a.c.ServerView(s.ID)
	if err != nil {
		a.serverFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (a *API) createServer(w http.ResponseWriter, r *http.Request) {
	var in model.ServerConnection
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	v, err := a.c.CreateServer(r.Context(), in)
	if err != nil {
		a.serverFail(w, err)
		return
	}
	a.audit(r, "server.add", v.Name, v.URL)
	writeJSON(w, http.StatusCreated, v)
}

func (a *API) updateServer(w http.ResponseWriter, r *http.Request) {
	var in model.ServerConnection
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	v, err := a.c.UpdateServer(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		a.serverFail(w, err)
		return
	}
	a.audit(r, "server.update", v.Name, v.URL)
	writeJSON(w, http.StatusOK, v)
}

func (a *API) deleteServer(w http.ResponseWriter, r *http.Request) {
	s, err := a.c.DeleteServer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.serverFail(w, err)
		return
	}
	a.audit(r, "server.remove", s.Name, s.URL)
	w.WriteHeader(http.StatusNoContent)
}

// testServer tries a connection being set up: the certificate the server
// presents (to pin it on first use) and, if it can be trusted, the token.
// The token is only sent once the TLS connection is trusted.
func (a *API) testServer(w http.ResponseWriter, r *http.Request) {
	var in model.ServerTest
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	res, err := a.c.TestServer(r.Context(), in)
	if err != nil {
		a.serverFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *API) serverFail(w http.ResponseWriter, err error) {
	if errors.Is(err, core.ErrServerNotFound) {
		writeErr(w, http.StatusNotFound, "no such server connection")
		return
	}
	a.fail(w, err)
}
