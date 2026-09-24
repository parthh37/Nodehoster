package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/alerts"
	"github.com/parthh37/nodehoster/internal/auth"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
)

// Resource alerts. Rules are configuration (Settings.alerts, Site.alerts);
// these endpoints are the alerts themselves. Everyone sees the alerts of
// the sites they can see; server alerts (machine CPU, memory, disk) only
// server-wide users. Silencing needs operator on the alert's site, or on
// the server for a server alert.

// alertVisible reports whether the caller may see an alert.
func alertVisible(acc auth.Access, a *model.Alert) bool {
	if a.SiteID == "" {
		return !acc.SiteScoped()
	}
	return acc.CanSee(a.SiteID)
}

// alertSiteParam narrows alerts to ?siteId= when given (404 if the caller
// cannot see that site, as for /sites/{id}).
func alertSiteParam(w http.ResponseWriter, r *http.Request) (siteID string, ok bool) {
	siteID = strings.TrimSpace(r.URL.Query().Get("siteId"))
	if siteID != "" && !canSeeSite(r, siteID) {
		writeErr(w, http.StatusNotFound, "not found")
		return "", false
	}
	return siteID, true
}

// listAlerts returns the alerts in progress (firing and pending).
func (a *API) listAlerts(w http.ResponseWriter, r *http.Request) {
	siteID, ok := alertSiteParam(w, r)
	if !ok {
		return
	}
	acc := access(r)
	l := a.c.Alerts.List()
	l.Enabled = a.c.Settings().Alerts.Enabled
	keep := func(list []model.Alert) []model.Alert {
		out := make([]model.Alert, 0, len(list))
		for i := range list {
			if alertVisible(acc, &list[i]) && (siteID == "" || list[i].SiteID == siteID) {
				out = append(out, list[i])
			}
		}
		return out
	}
	l.Firing, l.Pending = keep(l.Firing), keep(l.Pending)
	writeJSON(w, http.StatusOK, l)
}

// alertHistory returns stored alerts (firing and resolved), newest first.
func (a *API) alertHistory(w http.ResponseWriter, r *http.Request) {
	siteID, ok := alertSiteParam(w, r)
	if !ok {
		return
	}
	acc := access(r)
	f := store.AlertFilter{Limit: intParam(r, "limit", 100, 1000)}
	switch {
	case siteID != "":
		f.SiteIDs = []string{siteID}
	case r.URL.Query().Get("server") == "1":
		if acc.SiteScoped() {
			writeErr(w, http.StatusForbidden, "your account only has access to some sites")
			return
		}
		f.SiteIDs, f.Server = []string{}, true
	case acc.SiteScoped():
		f.SiteIDs = acc.SiteIDs()
	}
	list, err := a.c.Alerts.History(r.Context(), f)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// alertForChange finds an alert in progress the caller may silence. An
// alert the caller cannot see answers 404, like one that does not exist.
func (a *API) alertForChange(w http.ResponseWriter, r *http.Request) (model.Alert, bool) {
	id := chi.URLParam(r, "alert")
	al, ok := a.c.Alerts.Get(id)
	acc := access(r)
	if !ok || !alertVisible(acc, &al) {
		// A resolved alert can no longer be silenced; say so to whoever
		// could see it.
		if stored, err := a.c.Store.GetAlert(r.Context(), id); err == nil && alertVisible(acc, stored) {
			writeErr(w, http.StatusConflict, "the alert has resolved")
			return model.Alert{}, false
		}
		writeErr(w, http.StatusNotFound, "not found")
		return model.Alert{}, false
	}
	allowed := acc.Server(model.RoleOperator)
	if al.SiteID != "" {
		allowed = acc.OnSite(al.SiteID, model.RoleOperator)
	}
	if !allowed {
		writeErr(w, http.StatusForbidden, "your role does not allow this")
		return model.Alert{}, false
	}
	return al, true
}

func alertTarget(al model.Alert) string {
	name := al.SiteName
	if name == "" {
		name = "server"
	}
	return fmt.Sprintf("%s: %s (%s)", name, al.RuleID, al.ID)
}

// silenceAlert silences an alert in progress for {minutes} (0: until it
// resolves, which acknowledges it).
func (a *API) silenceAlert(w http.ResponseWriter, r *http.Request) {
	al, ok := a.alertForChange(w, r)
	if !ok {
		return
	}
	var in model.AlertSilenceRequest
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	by := "anonymous"
	if u := user(r); u != nil {
		by = u.Username
	}
	out, err := a.c.Alerts.Silence(r.Context(), al.ID, in.Minutes, by, in.Note, time.Now())
	if err != nil {
		a.alertFail(w, err)
		return
	}
	detail := "until it resolves"
	if in.Minutes > 0 {
		detail = fmt.Sprintf("for %d minutes", in.Minutes)
	}
	if n := strings.TrimSpace(in.Note); n != "" {
		detail += ": " + n
	}
	a.audit(r, "alert.silence", alertTarget(out), detail)
	writeJSON(w, http.StatusOK, out)
}

func (a *API) unsilenceAlert(w http.ResponseWriter, r *http.Request) {
	al, ok := a.alertForChange(w, r)
	if !ok {
		return
	}
	out, err := a.c.Alerts.Unsilence(r.Context(), al.ID, time.Now())
	if err != nil {
		a.alertFail(w, err)
		return
	}
	a.audit(r, "alert.unsilence", alertTarget(out), "")
	writeJSON(w, http.StatusOK, out)
}

func (a *API) alertFail(w http.ResponseWriter, err error) {
	if errors.Is(err, alerts.ErrNotFound) {
		writeErr(w, http.StatusConflict, "the alert has resolved")
		return
	}
	a.fail(w, err)
}

// siteAlertRules is what a site's Alerts tab needs of the server-wide
// configuration: whether alerts are on and the rules every site gets
// (the settings themselves are for administrators only).
type siteAlertRules struct {
	Enabled         bool              `json:"enabled"`
	Defaults        []model.AlertRule `json:"defaults"`
	RecoveryMinutes int               `json:"recoveryMinutes"`
}

func (a *API) siteAlertRules(w http.ResponseWriter, r *http.Request) {
	s := a.c.Settings().Alerts
	out := siteAlertRules{Enabled: s.Enabled, Defaults: s.SiteRules, RecoveryMinutes: s.RecoveryMinutes}
	if out.Defaults == nil {
		out.Defaults = []model.AlertRule{}
	}
	writeJSON(w, http.StatusOK, out)
}

// prometheusAlerts writes nodehoster_alert_firing: one series per alert
// firing (like Prometheus's own ALERTS), for the caller's sites.
func (a *API) prometheusAlerts(b *strings.Builder, acc auth.Access) {
	fmt.Fprintf(b, "# HELP nodehoster_alert_firing 1 while a resource alert fires\n# TYPE nodehoster_alert_firing gauge\n")
	for _, al := range a.c.Alerts.List().Firing {
		if !alertVisible(acc, &al) {
			continue
		}
		fmt.Fprintf(b, "nodehoster_alert_firing{rule=%q,metric=%q,severity=%q,site=%q,silenced=\"%t\"} 1\n",
			al.RuleID, al.Metric, al.Severity, al.SiteName, al.Silence != nil)
	}
}
