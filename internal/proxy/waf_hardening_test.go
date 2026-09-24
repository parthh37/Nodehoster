package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/waf"
)

const sqliQuery = "/search?q=1%27+UNION+SELECT+password+FROM+users--"

// TestWAFSlotTrafficIsTheSite's: a deployment slot's runtime has the slot
// key as its ID; its events and counters are the site's, naming the slot.
func TestWAFSlotTrafficIsTheSites(t *testing.T) {
	e := newWAFEnv(t, model.WAFConfig{Mode: model.WAFBlock}, func(s *model.Site) { s.ID = model.SlotKey("w1", "staging") })
	if rec := e.do("GET", sqliQuery, "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("slot: %d", rec.Code)
	}
	ev := e.waitEvents(t, 1)[0]
	if ev.SiteID != "w1" || ev.Slot != "staging" {
		t.Errorf("event site %q slot %q", ev.SiteID, ev.Slot)
	}
	snap := e.recorder.Snapshot()
	if st, ok := snap["w1"]; !ok || st.Blocked != 1 {
		t.Errorf("counters %+v", snap)
	}
	if _, ok := snap[model.SlotKey("w1", "staging")]; ok {
		t.Error("counters kept under the slot key")
	}
}

// TestWAFCrossSiteBlocksDoNotBan: a page elsewhere embedding attack URLs
// must not get its visitors banned; the requests are still blocked.
func TestWAFCrossSiteBlocksDoNotBan(t *testing.T) {
	e := newWAFEnv(t, model.WAFConfig{Mode: model.WAFBlock})
	ip := net.ParseIP("203.0.113.50")
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", sqliQuery, nil)
		req.RemoteAddr = "203.0.113.50:40000"
		req.Header.Set("User-Agent", "Mozilla/5.0 test")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Sec-Fetch-Mode", "no-cors")
		req.Header.Set("Sec-Fetch-Dest", "image")
		rec := httptest.NewRecorder()
		e.rt.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("cross-site attack not blocked: %d", rec.Code)
		}
	}
	if e.bans.Banned(ip) {
		t.Fatal("cross-site blocks banned the visitor")
	}
	// Same-site or direct requests still count.
	e.do("GET", sqliQuery, "", "")
	e.do("GET", sqliQuery, "", "")
	if !e.bans.Banned(ip) {
		t.Error("direct blocks did not ban")
	}
}

// TestWAFFailsClosed: a request the firewall cannot inspect completely is
// refused in block mode whatever the threshold, and let through but
// logged in detect mode.
func TestWAFFailsClosed(t *testing.T) {
	pad := strings.Repeat("a=1&", 2100)
	e := newWAFEnv(t, model.WAFConfig{Mode: model.WAFBlock, AnomalyThreshold: 1000})
	if rec := e.do("POST", "/form", pad+"q=hello", "application/x-www-form-urlencoded"); rec.Code != http.StatusForbidden || e.hits.Load() != 0 {
		t.Errorf("block mode: %d, upstream hits %d", rec.Code, e.hits.Load())
	}
	e = newWAFEnv(t, model.WAFConfig{Mode: model.WAFDetect})
	if rec := e.do("POST", "/form", pad+"q=hello", "application/x-www-form-urlencoded"); rec.Code != 200 {
		t.Errorf("detect mode: %d", rec.Code)
	}
	if ev := e.waitEvents(t, 1)[0]; ev.Action != model.WAFActionDetected || len(ev.Matches) == 0 || ev.Matches[0].RuleID != 920210 {
		t.Errorf("detect mode event %+v", ev)
	}
}

// TestWAFBusy: with the server-wide buffering budget spent, block mode
// answers 503 (not an attack: no event, no strike) and detect mode passes
// the body on uninspected.
func TestWAFBusy(t *testing.T) {
	prev := waf.SetMaxBufferedBytes(0)
	defer waf.SetMaxBufferedBytes(prev)
	body := `{"q":"hello"}`
	e := newWAFEnv(t, model.WAFConfig{Mode: model.WAFBlock})
	rec := e.do("POST", "/api", body, "application/json")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" || e.hits.Load() != 0 {
		t.Errorf("block mode: %d", rec.Code)
	}
	if e.bans.Banned(net.ParseIP("203.0.113.50")) {
		t.Error("busy counted towards a ban")
	}
	e = newWAFEnv(t, model.WAFConfig{Mode: model.WAFDetect})
	rec = e.do("POST", "/api", body, "application/json")
	e.mu.Lock()
	n := e.bodyLen
	e.mu.Unlock()
	if rec.Code != 200 || n != len(body) {
		t.Errorf("detect mode: %d, %d bytes upstream", rec.Code, n)
	}
}

// TestWAFMountedSite: a site mounted under another (a location of kind
// site) has its own firewall applied, even when the site it is mounted in
// has none.
func TestWAFMountedSite(t *testing.T) {
	e := newWAFEnv(t, model.WAFConfig{Mode: model.WAFBlock})
	srv := e.rt.srv
	srv.table.Load().sites[e.rt.site.ID] = e.rt
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(other.Close)
	parent := &model.Site{ID: "p1", Name: "portal", Type: model.SiteProxy, Proxy: &model.ProxyConfig{Upstreams: []model.Upstream{{URL: other.URL}}}}
	parent.Routing.Locations = []model.Location{{Path: "/shop", Kind: "site", SiteID: e.rt.site.ID, StripPrefix: true}}
	prt := compileTest(t, srv, parent)

	do := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		req.RemoteAddr = "203.0.113.60:40000"
		req.Header.Set("User-Agent", "Mozilla/5.0 test")
		rec := httptest.NewRecorder()
		prt.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("/shop" + sqliQuery); rec.Code != http.StatusForbidden || e.hits.Load() != 0 {
		t.Fatalf("attack on the mounted site: %d, upstream hits %d", rec.Code, e.hits.Load())
	}
	if rec := do("/shop/search?q=" + url.QueryEscape("running shoes")); rec.Code != 200 || e.hits.Load() != 1 {
		t.Fatalf("ordinary request: %d", rec.Code)
	}
	if ev := e.waitEvents(t, 1)[0]; ev.SiteID != e.rt.site.ID || ev.Path != "/search" {
		t.Errorf("event %+v", ev)
	}
}
