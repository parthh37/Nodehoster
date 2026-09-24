package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/model"
)

// ---- runtimes other than Node.js: Bun, Deno (installed), Python, .NET (found)

// managedRuntime reads {runtime} for the endpoints that install: only Bun
// and Deno are installed by NodeHoster.
func managedRuntime(w http.ResponseWriter, r *http.Request) (string, bool) {
	rt := chi.URLParam(r, "runtime")
	if rt != model.RuntimeBun && rt != model.RuntimeDeno {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("%q is not a runtime NodeHoster installs (bun or deno); Python and .NET are found where they are installed", rt))
		return "", false
	}
	return rt, true
}

// runtimes is a catalog the site pages show, like /node/versions.
func (a *API) runtimes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.c.RuntimeReport())
}

// refreshRuntimes looks for interpreters again at once (an administrator
// just installed Python), instead of within a minute.
func (a *API) refreshRuntimes(w http.ResponseWriter, r *http.Request) {
	a.c.Runtimes.Refresh()
	writeJSON(w, http.StatusOK, a.c.RuntimeReport())
}

func (a *API) runtimeAvailable(w http.ResponseWriter, r *http.Request) {
	rt, ok := managedRuntime(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	list, err := a.c.Runtimes.Available(ctx, rt)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) installRuntime(w http.ResponseWriter, r *http.Request) {
	rt, ok := managedRuntime(w, r)
	if !ok {
		return
	}
	var in struct {
		Version string `json:"version"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.c.Runtimes.Install(rt, in.Version); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "runtime.install", rt+" "+strings.TrimPrefix(in.Version, "v"), "")
	w.WriteHeader(http.StatusAccepted)
}

func (a *API) removeRuntime(w http.ResponseWriter, r *http.Request) {
	rt, ok := managedRuntime(w, r)
	if !ok {
		return
	}
	v := chi.URLParam(r, "version")
	if used := a.c.RuntimeVersionInUse(rt, v); len(used) > 0 {
		writeErr(w, http.StatusConflict, fmt.Sprintf("%s %s is used by %s", model.RuntimeLabel(rt), v, strings.Join(used, ", ")))
		return
	}
	if err := a.c.Runtimes.Remove(rt, v); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "runtime.remove", rt+" "+v, "")
	w.WriteHeader(http.StatusNoContent)
}
