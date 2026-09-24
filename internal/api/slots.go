package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/model"
)

// Deployment slots: status, swap (with a preview first) and start, stop
// and recycle of a slot. Slots themselves, their settings and auto-swap
// are site configuration (PUT /sites/{id}); deployments go to a slot
// with ?slot= on the deployment endpoints.

// querySlot reads the optional ?slot= of a request about a site: "" for
// production ("production" is accepted too). given is false when the
// parameter is absent. An unknown slot answers 404 and ok is false.
func querySlot(w http.ResponseWriter, r *http.Request, s *model.Site) (slot string, given, ok bool) {
	raw := r.URL.Query().Get("slot")
	if raw == "" {
		return "", false, true
	}
	slot = model.NormalizeSlot(raw)
	if slot != "" && s.FindSlot(slot) == nil {
		writeErr(w, http.StatusNotFound, "the site has no deployment slot "+slot)
		return "", true, false
	}
	return slot, true, true
}

// pathSlot is the {slot} of the URL; 404 when the site has no such slot.
func pathSlot(w http.ResponseWriter, r *http.Request, s *model.Site) (string, bool) {
	slot := model.NormalizeSlot(chi.URLParam(r, "slot"))
	if slot != "" && s.FindSlot(slot) == nil {
		writeErr(w, http.StatusNotFound, "the site has no deployment slot "+slot)
		return "", false
	}
	return slot, true
}

// slotLabel names a slot in audit details.
func slotLabel(slot string) string {
	if slot == "" {
		return model.ProductionSlot
	}
	return slot
}

func (a *API) listSlots(w http.ResponseWriter, r *http.Request) {
	if s := a.site(w, r); s != nil {
		writeJSON(w, http.StatusOK, a.c.Slots(s))
	}
}

func (a *API) swapPreview(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	slot, ok := pathSlot(w, r, s)
	if !ok {
		return
	}
	p, err := a.c.SwapPreview(s, slot)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// swapSlot starts a swap and answers 202 at once: it goes on in the
// background (GET /sites/{id}/slots shows its phase, then how it ended).
func (a *API) swapSlot(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	slot, ok := pathSlot(w, r, s)
	if !ok {
		return
	}
	p, err := a.c.StartSwap(s.ID, slot, user(r).Username, false)
	a.audit(r, "site.slot.swap", s.Name, slotLabel(slot)+errDetail(err))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, p)
}

func errDetail(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}

func (a *API) slotAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := a.site(w, r)
		if s == nil {
			return
		}
		slot, ok := pathSlot(w, r, s)
		if !ok {
			return
		}
		err := a.c.SlotAction(s.ID, slot, action)
		a.audit(r, "site.slot."+action, s.Name, slotLabel(slot)+errDetail(err))
		if err != nil {
			a.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a.c.Slots(s))
	}
}

// deployTarget is the configuration a deployment request goes to: the
// site, or the slot named by ?slot= (or the git body's "slot").
func (a *API) deployTarget(w http.ResponseWriter, s *model.Site, slot string) (*model.Site, bool) {
	t, err := a.c.DeployTarget(s, slot)
	if err != nil {
		a.fail(w, err)
		return nil, false
	}
	return t, true
}

// slotDetail suffixes an audit detail with the slot, for deployments to one.
func slotDetail(t *model.Site) string {
	if t.Slot == "" {
		return ""
	}
	return " to " + t.Slot
}

// matchesSlot filters by the ?slot= of a list request: everything when
// none was given.
func matchesSlot(have, want string, given bool) bool {
	return !given || have == want
}
