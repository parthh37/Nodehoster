//go:build !windows

package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestWAFCommands(t *testing.T) {
	s := newServer(t)
	site := s.createSite(redirectSite("shop", freePort(t)))
	if site.Routing.WAF.Mode != model.WAFOff {
		t.Fatalf("a redirect site starts with mode %q", site.Routing.WAF.Mode)
	}

	s.run("waf", "mode", "shop", "loud").expect(t, ExitUsage)
	s.run("waf", "mode", "shop", "block", "--paranoia", "4").expect(t, ExitUsage)
	r := s.run("waf", "mode", "shop", "block", "--paranoia", "2").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "blocking attacks (paranoia level 2, threshold 5)") {
		t.Errorf("mode: %s", r.stdout)
	}

	s.run("waf", "exclude", "shop").expect(t, ExitUsage)
	s.run("waf", "exclude", "shop", "--rule", "abc").expect(t, ExitUsage)
	r = s.run("waf", "exclude", "shop", "--rule", "942100,941100", "--arg", "content", "--path", "/admin").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "rule 942100, rule 941100 for argument content under /admin") {
		t.Errorf("exclude: %s", r.stdout)
	}
	s.run("waf", "exclude", "shop", "--rule", "123").expect(t, ExitError) // no such rule

	stored, _ := s.c.Site(site.ID)
	w := stored.Routing.WAF
	if w.Mode != model.WAFBlock || w.ParanoiaLevel != 2 || len(w.Exclusions) != 1 || w.Exclusions[0].Path != "/admin" {
		t.Errorf("stored firewall = %+v", w)
	}
	// Setting the mode again keeps the exclusions.
	s.run("waf", "mode", "shop", "detect").expect(t, ExitOK)
	if stored, _ := s.c.Site(site.ID); stored.Routing.WAF.Mode != model.WAFDetect || len(stored.Routing.WAF.Exclusions) != 1 {
		t.Errorf("after mode detect = %+v", stored.Routing.WAF)
	}

	r = s.run("waf", "list").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "shop") || !strings.Contains(r.stdout, "detect") {
		t.Errorf("list: %s", r.stdout)
	}

	if err := s.c.Store.AddWAFEvents(context.Background(), []model.WAFEvent{{
		ID: "0123456789abcdef", Time: time.Now(), SiteID: site.ID, Action: model.WAFActionBlocked, ClientIP: "203.0.113.9",
		Method: "GET", Path: "/login", Score: 5, Threshold: 5, Matches: []model.WAFMatch{{RuleID: 942100, Category: model.WAFSQLi}},
	}}); err != nil {
		t.Fatal(err)
	}
	r = s.run("waf", "events", "shop").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "203.0.113.9") || !strings.Contains(r.stdout, "0123456789abcdef") || !strings.Contains(r.stdout, "942100") {
		t.Errorf("events: %s", r.stdout)
	}
	r = s.run("--json", "waf", "events", "--rule", "941100").expect(t, ExitOK)
	var none []model.WAFEvent
	if err := json.Unmarshal([]byte(r.stdout), &none); err != nil || len(none) != 0 {
		t.Errorf("filtered events: %s %v", r.stdout, err)
	}
	s.run("waf", "events", "--action", "maybe").expect(t, ExitError)

	r = s.run("--json", "waf", "rules", "--category", "sqli").expect(t, ExitOK)
	var rules []model.WAFRuleInfo
	if err := json.Unmarshal([]byte(r.stdout), &rules); err != nil || len(rules) < 5 {
		t.Fatalf("rules: %v", err)
	}
	for _, rl := range rules {
		if rl.Category != model.WAFSQLi {
			t.Errorf("rule %d is %s", rl.ID, rl.Category)
		}
	}
}
