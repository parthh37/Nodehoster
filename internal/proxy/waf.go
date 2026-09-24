package proxy

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/ipban"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/waf"
)

// compileWAF sets up a site's web application firewall; a site without
// one keeps rt.waf nil and pays nothing. A deployment slot's counters are
// its site's: the Firewall tab and /metrics show a site's traffic whatever
// slot served it.
func (s *Server) compileWAF(rt *siteRuntime) {
	if e := waf.Compile(rt.site.Routing.WAF); e != nil {
		siteID, _ := model.SplitSlotKey(rt.site.ID)
		rt.waf = e
		rt.wafStats = s.deps.WAF.Counters(siteID)
	}
}

// inspectWAF runs the site's firewall over a request that passed IP
// restrictions, maintenance, rate limiting and authentication, before
// URL rewriting (the firewall sees what the client sent). It reports
// whether the request may go on; when not, the response is written: 403,
// or 503 when the server has no room left to buffer the body for
// inspection (block mode).
func (rt *siteRuntime) inspectWAF(w http.ResponseWriter, r *http.Request, clientIP string) bool {
	res := rt.waf.Inspect(r)
	if c := rt.wafStats; c != nil {
		c.Inspected.Add(1)
		c.Matched(res)
	}
	if res.Busy {
		// Not an attack: no event, no strike towards a ban.
		w.Header().Set("Retry-After", "1")
		errorPage(w, rt.site.Routing.ErrorPages, http.StatusServiceUnavailable, "The server is busy. Please try again shortly.")
		return false
	}
	if !res.Exceeded() {
		return true
	}
	block := rt.waf.Mode() == model.WAFBlock
	action := model.WAFActionDetected
	if block {
		action = model.WAFActionBlocked
	}
	id := waf.NewRequestID()
	ev := waf.NewEvent(r, id, rt.site.ID, clientIP, action, res, time.Now())
	rt.srv.deps.WAF.Record(ev)
	rules := make([]string, len(res.Matches))
	for i, m := range res.Matches {
		rules[i] = strconv.Itoa(m.RuleID)
	}
	// Rule IDs only: matched text can be a secret, and is in the event.
	rt.srv.deps.Log.Info("web application firewall "+action, "site", rt.site.Name, "request", id, "client", clientIP,
		"score", res.Score, "incomplete", res.Incomplete, "rules", strings.Join(rules, ","))
	if !block {
		if c := rt.wafStats; c != nil {
			c.Detected.Add(1)
		}
		return true
	}
	if c := rt.wafStats; c != nil {
		c.Blocked.Add(1)
	}
	// Blocks count towards automatic IP banning, unless the site opted
	// out or a browser made the request for another site's page (an image
	// or a link there whose URL carries an attack string would otherwise
	// get its visitors banned); the ban manager itself never bans
	// loopback, the trusted proxies or the allow list.
	if bans := rt.srv.deps.Bans; bans != nil && !rt.site.Routing.Banning.Exempt && !crossSite(r) {
		bans.Record(net.ParseIP(clientIP), ipban.WAFBlocked)
	}
	w.Header().Set("X-Request-Id", id)
	errorPage(w, rt.site.Routing.ErrorPages, http.StatusForbidden,
		"This request was blocked by the web application firewall. If you think it should not have been, tell the site's administrator the request ID "+id+".")
	return false
}

// crossSite reports whether a browser sent the request for a page of
// another site (Fetch Metadata). Anyone can send the header, which only
// spares the sender a ban: the request is blocked all the same.
func crossSite(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site")
}
