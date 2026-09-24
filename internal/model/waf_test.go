package model

import (
	"encoding/json"
	"testing"
)

func TestWAFSettingsDefaults(t *testing.T) {
	var s WAFSettings // saved before the firewall existed
	s.ApplyDefaults()
	if s != DefaultWAF() || s.DefaultMode != WAFDetect {
		t.Fatalf("defaults = %+v", s)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s = WAFSettings{DefaultMode: WAFBlock, EventRetentionDays: 7}
	s.ApplyDefaults()
	if s.DefaultMode != WAFBlock || s.DefaultParanoiaLevel != 1 || s.DefaultAnomalyThreshold != 5 || s.EventRetentionDays != 7 {
		t.Fatalf("partial = %+v", s)
	}
	for _, bad := range []WAFSettings{{DefaultMode: "on"}, {DefaultMode: WAFOff, DefaultParanoiaLevel: 4}, {DefaultMode: WAFOff, EventRetentionDays: 5000}} {
		bad.ApplyDefaults()
		if bad.Validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestWAFForNewSite(t *testing.T) {
	s := WAFSettings{DefaultMode: WAFBlock, DefaultParanoiaLevel: 2, DefaultAnomalyThreshold: 7}
	if c := s.ForNewSite(SiteNode); c.Mode != WAFBlock || c.ParanoiaLevel != 2 || c.AnomalyThreshold != 7 {
		t.Errorf("node = %+v", c)
	}
	for _, typ := range []SiteType{SiteWorker, SiteRedirect} {
		if c := s.ForNewSite(typ); c.Mode != WAFOff {
			t.Errorf("%s = %+v", typ, c)
		}
	}
}

// A site saved before the firewall existed has it off, and the effective
// values of an empty configuration are the defaults.
func TestWAFConfigOldSite(t *testing.T) {
	var s Site
	if err := json.Unmarshal([]byte(`{"name":"old","type":"node","routing":{"compression":true}}`), &s); err != nil {
		t.Fatal(err)
	}
	w := s.Routing.WAF
	if w.Enabled() || w.Paranoia() != 1 || w.Threshold() != 5 || w.BodyLimit() != 128<<10 {
		t.Errorf("old site firewall = %+v", w)
	}
	if err := w.Validate(SiteNode); err != nil {
		t.Error(err)
	}
}

func TestWAFSuggestExclusion(t *testing.T) {
	ev := WAFEvent{ID: "abc", Path: "/admin/posts", Matches: []WAFMatch{
		{RuleID: 941110, In: WAFInArg, Name: "content"}, {RuleID: 941100, In: WAFInArg, Name: "content"}, {RuleID: 942100, In: WAFInCookie, Name: "prefs"},
	}}
	x := ev.SuggestExclusion()
	if x.Path != "/admin/posts" || len(x.RuleIDs) != 3 || x.RuleIDs[0] != 941100 || len(x.Args) != 1 || x.Args[0] != "content" || x.Cookies[0] != "prefs" || x.Comment != "From request abc" {
		t.Errorf("named = %+v", x)
	}
	if err := x.validate("x"); err != nil {
		t.Error(err)
	}
	if got := x.String(); got != "rule 941100, rule 941110, rule 942100 for argument content, cookie prefs under /admin/posts" {
		t.Errorf("String = %q", got)
	}
	ev.Matches = append(ev.Matches, WAFMatch{RuleID: 930100, In: WAFInPath})
	if x := ev.SuggestExclusion(); len(x.Args)+len(x.Cookies) != 0 || len(x.RuleIDs) != 4 {
		t.Errorf("with a path match = %+v", x)
	}
	if ev.SiteName("shop") != "shop" || (&WAFEvent{SiteID: "s1", Slot: "staging"}).SiteName("") != "s1 [staging]" {
		t.Error("SiteName")
	}
	// A redacted token is not part of the suggested path.
	ev.Path = "/reset/" + WAFRedacted + "/confirm"
	if x := ev.SuggestExclusion(); x.Path != "/reset/" {
		t.Errorf("redacted path = %q", x.Path)
	}
	for _, tc := range []struct {
		x    WAFExclusion
		want string
	}{
		{WAFExclusion{Path: "/hooks/"}, "firewall off under /hooks/"},
		{WAFExclusion{Headers: []string{"X-T"}}, "all rules for header X-T on the whole site"},
		{WAFExclusion{Categories: []string{WAFSQLi}}, "sqli on the whole site"},
	} {
		if got := tc.x.String(); got != tc.want {
			t.Errorf("%+v: %q", tc.x, got)
		}
	}
	if (WAFMatch{In: WAFInArg, Name: "q"}).Where() != "argument q" || (WAFMatch{In: "?"}).Where() != "the request" {
		t.Error("Where")
	}
}

// TestIPBanWAFBlocksUpgrade: settings saved before the firewall existed
// get its ban rule; one turned off stays off.
func TestIPBanWAFBlocksUpgrade(t *testing.T) {
	s := IPBanSettings{Enabled: true, AuthFailures: BanRule{Threshold: 3, WindowSec: 60}}
	s.ApplyDefaults()
	if s.WAFBlocks != (BanRule{Threshold: 5, WindowSec: 60}) {
		t.Errorf("upgraded = %+v", s.WAFBlocks)
	}
	s.WAFBlocks = BanRule{Threshold: 0, WindowSec: 60}
	s.ApplyDefaults()
	if s.WAFBlocks.Threshold != 0 {
		t.Errorf("off rule = %+v", s.WAFBlocks)
	}
	if err := s.Validate(); err != nil {
		t.Error(err)
	}
}
