package api

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/waf"
)

// Web application firewall. A site's configuration is part of the site
// (routing.waf, saved with PUT /sites/{id}); these endpoints add what the
// console, NodeHoster Manager and the command line need around it: the
// rule catalog, the events, the counters, and changing just the firewall
// or adding one exclusion without sending the whole site.

// siteWAF is GET/PUT /sites/{id}/waf.
type siteWAF struct {
	Config model.WAFConfig     `json:"config"`
	Stats  waf.CounterSnapshot `json:"stats"`
}

func (a *API) siteWAFView(s *model.Site) siteWAF {
	st, ok := a.c.WAF.Snapshot()[s.ID]
	if !ok {
		st = waf.CounterSnapshot{Matches: map[string]int64{}}
	}
	cfg := s.Routing.WAF
	if cfg.Mode == "" {
		cfg.Mode = model.WAFOff
	}
	return siteWAF{Config: cfg, Stats: st}
}

// wafRules is the rule catalog, the same for everyone.
func (a *API) wafRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, waf.Rules())
}

// wafQuery reads the events filters: action, ip, rule, category,
// requestId, since (RFC 3339), before (a seq, for the next page), limit.
func wafQuery(r *http.Request) (model.WAFEventQuery, error) {
	p := r.URL.Query()
	q := model.WAFEventQuery{
		Action: p.Get("action"), ClientIP: strings.TrimSpace(p.Get("ip")), Category: p.Get("category"),
		RequestID: strings.TrimSpace(p.Get("requestId")), Limit: intParam(r, "limit", 100, 1000),
	}
	if q.Action != "" && q.Action != model.WAFActionBlocked && q.Action != model.WAFActionDetected {
		return q, &model.ValidationError{Field: "action", Message: "must be blocked or detected"}
	}
	if q.ClientIP != "" && net.ParseIP(q.ClientIP) == nil {
		return q, &model.ValidationError{Field: "ip", Message: "not an IP address"}
	}
	if q.Category != "" && !slices.Contains(model.WAFCategories, q.Category) {
		return q, &model.ValidationError{Field: "category", Message: "unknown category"}
	}
	if v := p.Get("rule"); v != "" {
		id, err := strconv.Atoi(v)
		if err != nil || id <= 0 {
			return q, &model.ValidationError{Field: "rule", Message: "not a rule ID"}
		}
		q.RuleID = id
	}
	if v := p.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return q, &model.ValidationError{Field: "since", Message: "use an RFC 3339 time"}
		}
		q.Since = t
	}
	if v := p.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return q, &model.ValidationError{Field: "before", Message: "not an event sequence number"}
		}
		q.Before = n
	}
	return q, nil
}

// listWAFEvents is GET /waf/events: every site's events for server-wide
// callers, the sites they may see for site-scoped ones (siteId narrows).
func (a *API) listWAFEvents(w http.ResponseWriter, r *http.Request) {
	q, err := wafQuery(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	acc := access(r)
	switch siteID := r.URL.Query().Get("siteId"); {
	case siteID != "" && !acc.CanSee(siteID):
		q.SiteIDs = []string{}
	case siteID != "":
		q.SiteIDs = []string{siteID}
	case acc.SiteScoped():
		q.SiteIDs = acc.SiteIDs()
	}
	list, err := a.c.WAFEvents(r.Context(), q)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// siteWAFEvents is GET /sites/{id}/waf/events.
func (a *API) siteWAFEvents(w http.ResponseWriter, r *http.Request) {
	q, err := wafQuery(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	q.SiteIDs = []string{chi.URLParam(r, "id")}
	list, err := a.c.WAFEvents(r.Context(), q)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) getSiteWAF(w http.ResponseWriter, r *http.Request) {
	if s := a.site(w, r); s != nil {
		writeJSON(w, http.StatusOK, a.siteWAFView(s))
	}
}

// putSiteWAF replaces a site's firewall configuration.
func (a *API) putSiteWAF(w http.ResponseWriter, r *http.Request) {
	var in model.WAFConfig
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	s, err := a.c.SetSiteWAF(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		a.fail(w, err)
		return
	}
	cfg := a.siteWAFView(s).Config
	a.audit(r, "waf.update", s.Name, fmt.Sprintf("mode %s, paranoia level %d, threshold %d, %d exclusions",
		cfg.Mode, cfg.Paranoia(), cfg.Threshold(), len(cfg.Exclusions)))
	writeJSON(w, http.StatusOK, a.siteWAFView(s))
}

// addWAFExclusion adds one exclusion, as "exclude" on an event does.
func (a *API) addWAFExclusion(w http.ResponseWriter, r *http.Request) {
	var in model.WAFExclusion
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	s, err := a.c.AddWAFExclusion(r.Context(), chi.URLParam(r, "id"), in)
	if errors.Is(err, core.ErrExclusionExists) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "waf.exclusion.add", s.Name, in.String())
	writeJSON(w, http.StatusCreated, a.siteWAFView(s))
}

// wafMetrics adds the firewall's counters to /metrics.
func (a *API) wafMetrics(b *strings.Builder, sites []*model.Site, siteScoped bool) {
	snap := a.c.WAF.Snapshot()
	metric := func(name, help, typ string) {
		fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	label := func(s *model.Site) string { return fmt.Sprintf(`site=%q,type=%q`, s.Name, s.Type) }
	var on []*model.Site
	for _, s := range sites {
		if s.Routing.WAF.Enabled() {
			on = append(on, s)
		}
	}
	metric("nodehoster_waf_inspected_total", "Requests the web application firewall inspected", "counter")
	for _, s := range on {
		fmt.Fprintf(b, "nodehoster_waf_inspected_total{%s} %d\n", label(s), snap[s.ID].Inspected)
	}
	metric("nodehoster_waf_requests_total", "Requests the web application firewall blocked, or would have blocked (detect mode)", "counter")
	for _, s := range on {
		fmt.Fprintf(b, "nodehoster_waf_requests_total{%s,action=\"blocked\"} %d\n", label(s), snap[s.ID].Blocked)
		fmt.Fprintf(b, "nodehoster_waf_requests_total{%s,action=\"detected\"} %d\n", label(s), snap[s.ID].Detected)
	}
	metric("nodehoster_waf_rule_matches_total", "Web application firewall rule matches, by category", "counter")
	for _, s := range on {
		for _, c := range model.WAFCategories {
			fmt.Fprintf(b, "nodehoster_waf_rule_matches_total{%s,category=%q} %d\n", label(s), c, snap[s.ID].Matches[c])
		}
	}
	if !siteScoped {
		metric("nodehoster_waf_events_dropped_total", "Web application firewall events not saved (too many, too fast)", "counter")
		fmt.Fprintf(b, "nodehoster_waf_events_dropped_total %d\n", a.c.WAF.Dropped())
	}
}
