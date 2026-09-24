package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
)

type siteWAFResp struct {
	Config model.WAFConfig `json:"config"`
	Stats  struct {
		Inspected int64            `json:"inspected"`
		Blocked   int64            `json:"blocked"`
		Detected  int64            `json:"detected"`
		Matches   map[string]int64 `json:"matches"`
	} `json:"stats"`
}

func TestWAFEndpointsAuthorization(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	a, b, _ := threeSites(e, admin)
	e.user("viewer", model.RoleViewer, false)
	e.user("operator", model.RoleOperator, false)
	e.scoped("siteop", grant(a.ID, model.RoleOperator))
	viewer, operator, siteop := session(e.login("viewer")), session(e.login("operator")), session(e.login("siteop"))

	// One event for each of two sites, stored directly.
	now := time.Now()
	if err := e.c.Store.AddWAFEvents(context.Background(), []model.WAFEvent{
		{ID: "ev-a", Time: now, SiteID: a.ID, Action: model.WAFActionBlocked, ClientIP: "203.0.113.1", Matches: []model.WAFMatch{{RuleID: 942100, Category: model.WAFSQLi}}},
		{ID: "ev-b", Time: now, SiteID: b.ID, Action: model.WAFActionDetected, ClientIP: "203.0.113.2", Matches: []model.WAFMatch{{RuleID: 941100, Category: model.WAFXSS}}},
	}); err != nil {
		t.Fatal(err)
	}

	cfg := model.WAFConfig{Mode: model.WAFBlock, ParanoiaLevel: 2}
	ex := model.WAFExclusion{Path: "/admin", Args: []string{"content"}, RuleIDs: []int{941100}}
	for _, tc := range []struct {
		name         string
		method, path string
		body         any
		who          []opt
		want         int
	}{
		{"viewer reads rules", "GET", "/api/waf/rules", nil, viewer, http.StatusOK},
		{"site operator reads rules", "GET", "/api/waf/rules", nil, siteop, http.StatusOK},
		{"viewer reads events", "GET", "/api/waf/events", nil, viewer, http.StatusOK},
		{"viewer reads site firewall", "GET", "/api/sites/" + a.ID + "/waf", nil, viewer, http.StatusOK},
		{"site operator reads own site", "GET", "/api/sites/" + a.ID + "/waf/events", nil, siteop, http.StatusOK},
		{"site operator, other site", "GET", "/api/sites/" + b.ID + "/waf", nil, siteop, http.StatusNotFound},
		{"operator sets mode", "PUT", "/api/sites/" + a.ID + "/waf", cfg, operator, http.StatusForbidden},
		{"site operator sets mode", "PUT", "/api/sites/" + a.ID + "/waf", cfg, siteop, http.StatusForbidden},
		{"admin sets mode", "PUT", "/api/sites/" + a.ID + "/waf", cfg, admin, http.StatusOK},
		{"operator adds exclusion", "POST", "/api/sites/" + a.ID + "/waf/exclusions", ex, operator, http.StatusForbidden},
		{"admin adds exclusion", "POST", "/api/sites/" + a.ID + "/waf/exclusions", ex, admin, http.StatusCreated},
		{"same exclusion again", "POST", "/api/sites/" + a.ID + "/waf/exclusions", ex, admin, http.StatusConflict},
		{"unknown rule", "POST", "/api/sites/" + a.ID + "/waf/exclusions", model.WAFExclusion{RuleIDs: []int{1}}, admin, http.StatusUnprocessableEntity},
		{"bad mode", "PUT", "/api/sites/" + a.ID + "/waf", model.WAFConfig{Mode: "on"}, admin, http.StatusUnprocessableEntity},
		{"bad filter", "GET", "/api/waf/events?action=allowed", nil, admin, http.StatusUnprocessableEntity},
	} {
		if rec := e.do(tc.method, tc.path, tc.body, tc.who...); rec.Code != tc.want {
			t.Errorf("%s: %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body)
		}
	}
	actions := e.auditActions()
	if !contains(actions, "root:waf.update") || !contains(actions, "root:waf.exclusion.add") {
		t.Errorf("audit = %v", actions)
	}

	got := decodeJSON[siteWAFResp](t, e.do("GET", "/api/sites/"+a.ID+"/waf", nil, admin...))
	if got.Config.Mode != model.WAFBlock || got.Config.ParanoiaLevel != 2 || len(got.Config.Exclusions) != 1 || got.Config.Exclusions[0].Path != "/admin" {
		t.Errorf("site firewall = %+v", got.Config)
	}
	// The change went through the site: the rest of it is as it was.
	site := decodeJSON[siteResp](t, e.do("GET", "/api/sites/"+a.ID, nil, admin...))
	if site.Redirect == nil || site.Redirect.TargetURL != "https://example.com" || site.Routing.WAF.Mode != model.WAFBlock {
		t.Errorf("site = %+v", site.Site)
	}

	ids := func(path string, who []opt) string {
		t.Helper()
		var out []string
		for _, ev := range decodeJSON[[]model.WAFEvent](t, e.do("GET", path, nil, who...)) {
			out = append(out, ev.ID)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		path string
		who  []opt
		want string
	}{
		{"/api/waf/events", admin, "ev-b,ev-a"},
		{"/api/waf/events?siteId=" + a.ID, viewer, "ev-a"},
		{"/api/waf/events?action=detected", admin, "ev-b"},
		{"/api/waf/events?rule=942100", admin, "ev-a"},
		{"/api/waf/events?category=xss", admin, "ev-b"},
		{"/api/waf/events?ip=203.0.113.2", admin, "ev-b"},
		{"/api/waf/events?requestId=ev-a", admin, "ev-a"},
		{"/api/waf/events", siteop, "ev-a"},                    // only the sites granted
		{"/api/waf/events?siteId=" + b.ID, siteop, ""},         // not another's
		{"/api/sites/" + a.ID + "/waf/events", siteop, "ev-a"}, // the site's own
		{"/api/sites/" + b.ID + "/waf/events?limit=1", admin, "ev-b"},
	} {
		if got := ids(tc.path, tc.who); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.path, got, tc.want)
		}
	}

	rules := decodeJSON[[]model.WAFRuleInfo](t, e.do("GET", "/api/waf/rules", nil, viewer...))
	if len(rules) < 50 || rules[0].ID > rules[1].ID {
		t.Errorf("%d rules", len(rules))
	}
}

// TestWAFDefaultsForNewSites: new sites take the server's default mode;
// sites saved before the firewall existed keep it off.
func TestWAFDefaultsForNewSites(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	proxy := func(name string, routing map[string]any) map[string]any {
		return map[string]any{"name": name, "type": "proxy", "proxy": map[string]any{"upstreams": []map[string]any{{"url": "http://127.0.0.1:1"}}}, "routing": routing}
	}
	if s := e.createSite(admin, proxy("fresh", nil)); s.Routing.WAF.Mode != model.WAFDetect || s.Routing.WAF.ParanoiaLevel != 1 {
		t.Errorf("new site firewall = %+v", s.Routing.WAF)
	}
	if s := e.createSite(admin, redirectSite("redir", 0)); s.Routing.WAF.Mode != model.WAFOff {
		t.Errorf("redirect firewall = %+v", s.Routing.WAF)
	}
	if s := e.createSite(admin, proxy("chosen", map[string]any{"waf": map[string]any{"mode": "off"}})); s.Routing.WAF.Mode != model.WAFOff {
		t.Errorf("explicit off = %+v", s.Routing.WAF)
	}

	st := decodeJSON[model.Settings](t, e.do("GET", "/api/settings", nil, admin...))
	if st.WAF != model.DefaultWAF() {
		t.Fatalf("settings.waf = %+v", st.WAF)
	}
	st.WAF.DefaultMode, st.WAF.DefaultParanoiaLevel = model.WAFBlock, 2
	expect(t, e.do("PUT", "/api/settings", st, admin...), http.StatusOK)
	if s := e.createSite(admin, proxy("strict", nil)); s.Routing.WAF.Mode != model.WAFBlock || s.Routing.WAF.ParanoiaLevel != 2 {
		t.Errorf("after the default changed = %+v", s.Routing.WAF)
	}
	st.WAF.DefaultMode = "maybe"
	expect(t, e.do("PUT", "/api/settings", st, admin...), http.StatusUnprocessableEntity)

	// A site as an older version stored it: the firewall stays off, and a
	// worker cannot have it on.
	old := &model.Site{ID: "old-site", Name: "old", Type: model.SiteRedirect, Redirect: &model.RedirectConfig{TargetURL: "https://example.com", StatusCode: 301}}
	if err := e.c.Store.PutSite(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.c.Store.GetSite(context.Background(), "old-site"); got.Routing.WAF.Enabled() {
		t.Errorf("old site firewall = %+v", got.Routing.WAF)
	}
	worker := map[string]any{"name": "w", "type": "worker", "node": map[string]any{"appRoot": t.TempDir(), "script": "worker.js"}, "routing": map[string]any{"waf": map[string]any{"mode": "block"}}}
	rec := e.do("POST", "/api/sites", worker, admin...)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "routing.waf.mode") {
		t.Errorf("worker with a firewall: %d %s", rec.Code, rec.Body)
	}
}

// TestWAFEndToEnd drives a real site listener: an attack is blocked with a
// request ID, the event is saved and listed, security.waf is raised, the
// counters reach /metrics, and repeated blocks ban the client.
func TestWAFEndToEnd(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// The harness does not Start the core: run the event writer here.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.c.WAF.Run(ctx)
	admin := e.adminSession()
	s := e.c.Settings()
	s.IPBan.Enabled = true
	s.IPBan.WAFBlocks = model.BanRule{Threshold: 3, WindowSec: 60}
	s.Proxy.TrustedProxies = []string{"127.0.0.1"}
	if _, err := e.c.UpdateSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	var hits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		io.WriteString(w, "ok")
	}))
	defer up.Close()
	port := freePort(t)
	site := e.createSite(admin, map[string]any{
		"name": "shop", "type": "proxy", "autoStart": true,
		"bindings": []map[string]any{{"protocol": "http", "ip": "127.0.0.1", "port": port}},
		"proxy":    map[string]any{"upstreams": []map[string]any{{"url": up.URL}}},
		"routing":  map[string]any{"waf": map[string]any{"mode": "block"}},
	})
	client := &http.Client{Timeout: 5 * time.Second}
	send := func(q string) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(port)+"/products?q="+url.QueryEscape(q), nil)
		req.Header.Set("X-Forwarded-For", "198.51.100.23")
		req.Header.Set("User-Agent", "Mozilla/5.0")
		var res *http.Response
		var err error
		for i := 0; i < 50; i++ { // the listener may still be starting
			if res, err = client.Do(req); err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("X-Request-Id"), string(b)
	}
	if code, _, _ := send("red shoes"); code != 200 {
		t.Fatalf("ordinary request: %d", code)
	}
	code, id, body := send("<script>alert(document.cookie)</script>")
	if code != http.StatusForbidden || id == "" || !strings.Contains(body, id) || !strings.Contains(body, "web application firewall") {
		t.Fatalf("attack: %d %q %s", code, id, body)
	}

	// Saved within a second or so.
	var list []model.WAFEvent
	for i := 0; i < 100 && len(list) == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		list = decodeJSON[[]model.WAFEvent](t, e.do("GET", "/api/waf/events?requestId="+id, nil, admin...))
	}
	if len(list) != 1 || list[0].SiteID != site.ID || list[0].ClientIP != "198.51.100.23" || list[0].Matches[0].Category != model.WAFXSS {
		t.Fatalf("events = %+v", list)
	}
	evs, _ := e.c.Store.ListEvents(context.Background(), site.ID, 50)
	found := false
	for _, ev := range evs {
		found = found || (ev.Type == events.SecurityWAF && strings.Contains(ev.Message, id))
	}
	if !found {
		t.Errorf("no security.waf event in %+v", evs)
	}

	metrics := e.do("GET", "/metrics", nil, admin...).Body.String()
	for _, want := range []string{
		`nodehoster_waf_requests_total{site="shop",type="proxy",action="blocked"} 1`,
		`nodehoster_waf_inspected_total{site="shop",type="proxy"} 2`,
		`nodehoster_waf_rule_matches_total{site="shop",type="proxy",category="xss"}`,
		`nodehoster_waf_events_dropped_total 0`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics lack %s", want)
		}
	}

	// Two more blocks ban the client (3 within 60 s); the address that
	// was banned is the one the trusted proxy reported.
	send("' or 1=1--")
	send("../../etc/passwd")
	if code, _, _ := send("red shoes"); code != http.StatusForbidden || !e.c.Bans.Banned([]byte{198, 51, 100, 23}) {
		t.Errorf("after three blocks: %d, banned %v", code, e.c.Bans.Banned([]byte{198, 51, 100, 23}))
	}
	if hits != 1 {
		t.Errorf("upstream saw %d requests", hits)
	}

	// Excluding the rule under the path lets the request through.
	e.c.Bans.Unban("198.51.100.23")
	x := model.WAFExclusion{Path: "/products", Categories: []string{model.WAFXSS}}
	expect(t, e.do("POST", "/api/sites/"+site.ID+"/waf/exclusions", x, admin...), http.StatusCreated)
	if code, _, _ := send("<script>alert(document.cookie)</script>"); code != 200 {
		t.Errorf("after the exclusion: %d", code)
	}
}
