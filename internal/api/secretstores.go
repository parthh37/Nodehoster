package api

import (
	"context"
	"net/http"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// Secret stores are configured in the settings (PUT /api/settings,
// secretStores); these endpoints report on them and test them. None ever
// returns a secret's value, not even to administrators: a reference is
// shown to resolve, and its value is only ever given to the processes.

// secretTimeout bounds a test: signing in and reading can each take up to
// the stores' request timeout.
const secretTimeout = 60 * time.Second

// listSecretStores reports the state of every store: values in memory,
// references, the last success and error.
func (a *API) listSecretStores(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.c.SecretStoreStatus())
}

// testSecretStore signs in to a store as edited and reads a reference
// with it if one is given.
func (a *API) testSecretStore(w http.ResponseWriter, r *http.Request) {
	var in model.SecretStoreTest
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), secretTimeout)
	defer cancel()
	res, err := a.c.TestSecretStore(ctx, in)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "secretstore.test", in.Store.Name, in.Ref)
	writeJSON(w, http.StatusOK, res)
}

// resolveSecretRef reads a reference from its saved store, without
// returning the value.
func (a *API) resolveSecretRef(w http.ResponseWriter, r *http.Request) {
	var in model.SecretResolveRequest
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), secretTimeout)
	defer cancel()
	res, err := a.c.ResolveSecretRef(ctx, in)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "secretstore.resolve", in.Store, in.Ref)
	writeJSON(w, http.StatusOK, res)
}

// checkSiteSecrets reads every secret store reference of a site now.
func (a *API) checkSiteSecrets(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), secretTimeout)
	defer cancel()
	res, err := a.c.CheckSiteSecrets(ctx, s.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
