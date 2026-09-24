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
// one keeps rt.waf nil and pays nothing.
func (s *Server) compileWAF(rt *siteRuntime) {
	if e := waf.Compile(rt.site.Routing.WAF); e != nil {
		rt.waf = e
		rt.wafStats = s.deps.WAF.Counters(rt.site.ID)
	}
}

// inspectWAF runs the site's firewall over a request that passed IP
// restrictions, maintenance, rate limiting and authentication, before
// URL rewriting (the firewall sees what the client sent). It reports
// whether the request may go on; when not, the 403 is written.
func (rt *siteRuntime) inspectWAF(w http.ResponseWriter, r *http.Request, clientIP string) bool {
	res := rt.waf.Inspect(r)
	if c := rt.wafStats; c != nil {
		c.Inspected.Add(1)
		c.Matched(res)
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
		"score", res.Score, "rules", strings.Join(rules, ","))
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
	// out; the ban manager itself never bans loopback, the trusted proxies
	// or the allow list.
	if bans := rt.srv.deps.Bans; bans != nil && !rt.site.Routing.Banning.Exempt {
		bans.Record(net.ParseIP(clientIP), ipban.WAFBlocked)
	}
	w.Header().Set("X-Request-Id", id)
	errorPage(w, rt.site.Routing.ErrorPages, http.StatusForbidden,
		"This request was blocked by the web application firewall. If you think it should not have been, tell the site's administrator the request ID "+id+".")
	return false
}
