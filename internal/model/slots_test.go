package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func slotSite() *Site {
	s := &Site{ID: "abc", Name: "shop", Type: SiteNode,
		Bindings: []Binding{{Protocol: "http", Port: 80, Host: "www.example.com"}, {Protocol: "http", Port: 80, Host: "staging.example.com", Slot: "Staging"}},
		Node: &NodeConfig{AppRoot: `C:\apps\shop`, Script: "server.js", Instances: 3, Env: []EnvVar{
			{Name: "NODE_ENV", Value: "production"},
			{Name: "DATABASE_URL", Value: "postgres://prod", SlotSetting: true},
			{Name: "API_URL", Value: "https://api"},
		}},
		Slots:         []DeploymentSlot{{Name: " Staging ", Instances: 1, ActiveRelease: "r2", Env: []EnvVar{{Name: "API_URL", Value: "https://api-staging"}, {Name: "DATABASE_URL", Value: "postgres://staging"}, {Name: "EXTRA", Value: "1"}}}},
		ActiveRelease: "r1",
	}
	s.ApplyDefaults()
	return s
}

func TestSlotDefaults(t *testing.T) {
	s := slotSite()
	sl := s.Slots[0]
	if sl.Name != "staging" || s.Bindings[1].Slot != "staging" || s.Bindings[0].Slot != "" {
		t.Fatalf("names not normalized: %q %q", sl.Name, s.Bindings[1].Slot)
	}
	if !reflect.DeepEqual(sl.Warmup, WarmupConfig{Paths: []string{"/"}, Statuses: "200-399", TimeoutSec: 120}) {
		t.Fatalf("warm-up defaults = %+v", sl.Warmup)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	// "production" in a binding is the site itself.
	s.Bindings[0].Slot = "Production"
	s.ApplyDefaults()
	if s.Bindings[0].Slot != "" {
		t.Fatalf("production binding slot = %q", s.Bindings[0].Slot)
	}
}

func TestSlotSite(t *testing.T) {
	s := slotSite()
	d := SlotSite(s, "staging")
	if d == nil || d.Slot != "staging" || d.ID != "abc" || d.ActiveRelease != "r2" || d.Node.Instances != 1 {
		t.Fatalf("derived = %+v", d)
	}
	got := map[string]string{}
	var order []string
	for _, e := range d.Node.Env {
		got[e.Name] = e.Value
		order = append(order, e.Name)
		if e.SlotSetting {
			t.Fatalf("%s kept SlotSetting", e.Name)
		}
	}
	want := map[string]string{"NODE_ENV": "production", "API_URL": "https://api-staging", "DATABASE_URL": "postgres://staging", "EXTRA": "1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env = %v", got)
	}
	// Overrides keep production's position; the slot's own come after.
	if strings.Join(order, ",") != "NODE_ENV,API_URL,DATABASE_URL,EXTRA" {
		t.Fatalf("order = %v", order)
	}
	// Production's configuration is untouched.
	if s.Node.Instances != 3 || len(s.Node.Env) != 3 || s.Node.Env[2].Value != "https://api" || s.ActiveRelease != "r1" {
		t.Fatalf("production changed: %+v", s.Node)
	}
	if len(d.Bindings) != 2 || d.Slots != nil {
		t.Fatalf("bindings %d, slots %v", len(d.Bindings), d.Slots)
	}
	if SlotSite(s, "nope") != nil {
		t.Fatal("unknown slot derived")
	}
	// Instances 0 = production's count.
	s.Slots[0].Instances = 0
	if n := SlotSite(s, "staging").Node.Instances; n != 3 {
		t.Fatalf("instances = %d", n)
	}
}

func TestSwapSite(t *testing.T) {
	s := slotSite()
	w := SwapSite(s, "staging")
	if w.ActiveRelease != "r2" || w.Node != s.Node || w.Slot != "" || s.ActiveRelease != "r1" {
		t.Fatalf("swap site = %+v", w)
	}
	// Production's own settings, sticky ones included.
	if len(w.Node.Env) != 3 || w.Node.Instances != 3 {
		t.Fatalf("env = %+v", w.Node.Env)
	}
}

func TestSlotKeys(t *testing.T) {
	if SlotKey("abc", "") != "abc" || SlotKey("abc", "production") != "abc" || SlotKey("abc", "staging") != "abc@staging" {
		t.Fatal("SlotKey")
	}
	if id, slot := SplitSlotKey("abc@staging"); id != "abc" || slot != "staging" {
		t.Fatal("SplitSlotKey")
	}
	if id, slot := SplitSlotKey("abc"); id != "abc" || slot != "" {
		t.Fatal("SplitSlotKey production")
	}
	s := slotSite()
	if s.ReleaseIn("") != "r1" || s.ReleaseIn("staging") != "r2" || s.ReleaseIn("x") != "" {
		t.Fatal("ReleaseIn")
	}
	if b := s.SlotBindings("staging"); len(b) != 1 || b[0].Host != "staging.example.com" {
		t.Fatalf("slot bindings = %+v", b)
	}
	if !s.HasSlotBindings() || !reflect.DeepEqual(s.SlotReleases(), []string{"r2"}) {
		t.Fatal("HasSlotBindings/SlotReleases")
	}
}

func TestSlotValidation(t *testing.T) {
	cases := map[string]func(*Site){
		"bindings[1].slot":           func(s *Site) { s.Bindings[1].Slot = "qa" },
		"node.portMode":              func(s *Site) { s.Node.PortMode, s.Node.FixedPort, s.Node.Instances = "fixed", 3000, 1 },
		"slots[1].name":              func(s *Site) { s.Slots = append(s.Slots, DeploymentSlot{Name: "staging"}) },
		"slots[0].env[0].name":       func(s *Site) { s.Slots[0].Env[0].Name = "1BAD" },
		"slots[0].env[1].name":       func(s *Site) { s.Slots[0].Env[1].Name = "API_URL" },
		"slots[0].instances":         func(s *Site) { s.Slots[0].Instances = 65 },
		"slots[0].warmup.paths[0]":   func(s *Site) { s.Slots[0].Warmup.Paths = []string{"health"} },
		"slots[0].warmup.statuses":   func(s *Site) { s.Slots[0].Warmup.Statuses = "200-99" },
		"slots[0].warmup.timeoutSec": func(s *Site) { s.Slots[0].Warmup.TimeoutSec = 4000 },
		"slots": func(s *Site) {
			s.Bindings = s.Bindings[:1]
			s.Slots = []DeploymentSlot{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}, {Name: "e"}}
		},
		"slots[0].name":         func(s *Site) { s.Bindings, s.Slots[0].Name = s.Bindings[:1], "production" },
		"slots[0].warmup.paths": func(s *Site) { s.Slots[0].Warmup.Paths = strings.Split("/a,/b,/c,/d,/e,/f,/g,/h,/i,/j,/k", ",") },
	}
	for field, mutate := range cases {
		s := slotSite()
		mutate(s)
		s.ApplyDefaults()
		err := s.Validate()
		ve, ok := err.(*ValidationError)
		if !ok || ve.Field != field {
			t.Errorf("%s: got %v", field, err)
		}
	}
	// Other site types cannot have slots.
	s := &Site{Name: "files", Type: SiteStatic, Static: &StaticConfig{Root: "/srv"}, Slots: []DeploymentSlot{{Name: "staging"}}}
	s.ApplyDefaults()
	if ve, ok := s.Validate().(*ValidationError); !ok || ve.Field != "slots" {
		t.Fatalf("static site with slots: %v", s.Validate())
	}
	// A worker may have slots (no bindings).
	w := &Site{Name: "queue", Type: SiteWorker, Node: &NodeConfig{AppRoot: "/srv", Script: "w.js"}, Slots: []DeploymentSlot{{Name: "staging"}}}
	w.ApplyDefaults()
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStatusRanges(t *testing.T) {
	r, err := ParseStatusRanges(" 200-299, 401 ,404")
	if err != nil {
		t.Fatal(err)
	}
	for code, want := range map[int]bool{200: true, 299: true, 300: false, 401: true, 404: true, 500: false} {
		if StatusAccepted(code, r) != want {
			t.Errorf("%d: want %v", code, want)
		}
	}
	for _, bad := range []string{"", ",", "abc", "99", "600", "300-200", "200-"} {
		if _, err := ParseStatusRanges(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Old documents have no slots, and a derived configuration's Slot never
// reaches storage or the API.
func TestSlotJSON(t *testing.T) {
	var s Site
	if err := json.Unmarshal([]byte(`{"id":"x","type":"node","node":{"env":[{"name":"A","value":"1"}]}}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Slots != nil || s.Node.Env[0].SlotSetting {
		t.Fatal("old document decoded with slots")
	}
	d := SlotSite(slotSite(), "staging")
	b, _ := json.Marshal(d)
	var top map[string]any
	json.Unmarshal(b, &top)
	if _, ok := top["slot"]; ok {
		t.Fatalf("derived slot marshalled: %s", b)
	}
}
