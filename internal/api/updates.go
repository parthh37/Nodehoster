package api

import (
	"errors"
	"net/http"

	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/model"
)

// ---- automatic updates (admin: installing runs setup as SYSTEM)

func (a *API) updateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.c.UpdateStatus())
}

// putUpdates changes the updates settings alone: NodeHoster Manager, the
// command line and setup use it instead of sending all the settings.
func (a *API) putUpdates(w http.ResponseWriter, r *http.Request) {
	var in model.UpdateSettings
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.c.SetUpdateSettings(r.Context(), in); err != nil {
		a.fail(w, err)
		return
	}
	detail := "off"
	if in.Auto {
		detail = "on at " + in.Time
	}
	a.audit(r, "settings.updates", "server", detail)
	writeJSON(w, http.StatusOK, a.c.UpdateStatus())
}

func (a *API) checkUpdate(w http.ResponseWriter, r *http.Request) {
	if err := a.c.CheckUpdate(r.Context()); err != nil {
		a.updateFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.c.UpdateStatus())
}

func (a *API) installUpdate(w http.ResponseWriter, r *http.Request) {
	if err := a.c.InstallUpdate(); err != nil {
		a.updateFail(w, err)
		return
	}
	st := a.c.UpdateStatus()
	target := ""
	if st.Available != nil {
		target = st.Available.Version
	}
	a.audit(r, "update.install", "server", target)
	writeJSON(w, http.StatusAccepted, st)
}

func (a *API) updateFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrUpdateBusy), errors.Is(err, core.ErrNoUpdate), errors.Is(err, core.ErrUpdateUnsupported):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		// The feed could not be read or verified.
		writeErr(w, http.StatusBadGateway, err.Error())
	}
}
