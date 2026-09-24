package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/preview"
)

// Preview deployments. The settings are part of the parent site
// (deploy.previews, PUT /sites/{id}); these endpoints list a site's
// previews and create, redeploy or delete one. Previews are sites too
// (GET /sites/{previewId} works), and whoever has a role on the parent
// has it on its previews (auth.Access.WithPreviews).

func (a *API) listPreviews(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	out := []model.PreviewView{}
	for _, p := range a.c.Previews(s.ID) {
		out = append(out, a.c.PreviewView(r.Context(), p))
	}
	writeJSON(w, http.StatusOK, out)
}

// preview is the {preview} of the {id} site, or nil after answering 404.
func (a *API) preview(w http.ResponseWriter, r *http.Request, parent *model.Site) *model.Site {
	p, err := a.c.Site(chi.URLParam(r, "preview"))
	if err != nil || p.PreviewOf != parent.ID || p.Preview == nil {
		writeErr(w, http.StatusNotFound, "no such preview")
		return nil
	}
	return p
}

// createPreview deploys a branch as a preview on request (the branch
// patterns do not apply; the production branch is refused).
func (a *API) createPreview(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	var in struct {
		Branch string `json:"branch"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	d, err := a.c.DeployBranchPreview(s, in.Branch, user(r).Username)
	a.audit(r, "preview.create", s.Name, "branch "+in.Branch+suffix(err))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

func (a *API) redeployPreview(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	p := a.preview(w, r, s)
	if p == nil {
		return
	}
	err := a.c.RedeployPreview(p, user(r).Username)
	a.audit(r, "preview.redeploy", s.Name, p.Preview.URL+suffix(err))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, a.c.PreviewView(r.Context(), p))
}

func (a *API) deletePreview(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	p := a.preview(w, r, s)
	if p == nil {
		return
	}
	err := a.c.RemovePreview(p, user(r).Username)
	a.audit(r, "preview.delete", s.Name, p.Preview.URL+suffix(err))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, a.c.PreviewView(r.Context(), p))
}

// previewWebhook answers the push webhook's deliveries that concern
// previews: pull request (merge request) events, and pushes to or
// deletions of previewed branches. It reports false for the others, which
// the site's own push handling answers. The signature has been verified.
func (a *API) previewWebhook(w http.ResponseWriter, r *http.Request, s *model.Site, body []byte) bool {
	if s.IsPreview() {
		return false
	}
	ev, err := preview.Parse(r.Header, body)
	if err != nil {
		if preview.IsPush(r.Header) {
			return false // say, a branch name previews refuse: the push handling decides
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": err.Error()})
		return true
	}
	d, err := a.c.PreviewWebhook(s, ev)
	if !d.Handled {
		return false
	}
	if err != nil { // the server is shutting down: the host retries later
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return true
	}
	if d.Action == preview.ActionIgnore {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": d.Reason})
		return true
	}
	a.c.AddAudit(r.Context(), model.AuditEntry{Time: time.Now(), User: "webhook", IP: clientIP(r),
		Action: "preview." + d.Action, Target: s.Name, Detail: d.Key + " (" + d.Reason + ")"})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "action": d.Action, "preview": d.Key, "reason": d.Reason})
	return true
}
