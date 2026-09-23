package model

import (
	"strings"
	"testing"
)

func nodeSite() *Site {
	s := &Site{ID: "a", Name: "app", Type: SiteNode,
		Bindings: []Binding{{Protocol: "http", Host: "example.com"}},
		Node:     &NodeConfig{AppRoot: `C:\apps\app`, Script: "server.js"}}
	s.ApplyDefaults()
	return s
}

func TestDefaults(t *testing.T) {
	s := nodeSite()
	if s.Bindings[0].Port != 80 || s.Node.Instances != 1 || s.Node.PortMode != "auto" {
		t.Fatalf("defaults not applied: %+v", s.Node)
	}
	if s.Node.RapidFailAction != "recover" || s.Node.RecoverAfterSec != 300 {
		t.Fatalf("rapid-fail recovery defaults not applied: %q, %d", s.Node.RapidFailAction, s.Node.RecoverAfterSec)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestMaxRestartsUnlimited: 0 is documented (and offered in the console) as
// "unlimited", so defaults must not replace it.
func TestMaxRestartsUnlimited(t *testing.T) {
	s := nodeSite()
	if s.Node.MaxRestarts != 0 {
		t.Fatalf("MaxRestarts 0 became %d", s.Node.MaxRestarts)
	}
	s.Node.MaxRestarts = -5
	s.ApplyDefaults()
	if s.Node.MaxRestarts != 0 {
		t.Fatalf("negative MaxRestarts became %d, want 0", s.Node.MaxRestarts)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]func(*Site){
		"name":                 func(s *Site) { s.Name = "" },
		"bindings[0].host":     func(s *Site) { s.Bindings[0].Host = "bad host" },
		"bindings[0].port":     func(s *Site) { s.Bindings[0].Port = 70000 },
		"node.script":          func(s *Site) { s.Node.Script = "" },
		"node.instances":       func(s *Site) { s.Node.PortMode = "fixed"; s.Node.FixedPort = 3000; s.Node.Instances = 2 },
		"node.rapidFailAction": func(s *Site) { s.Node.RapidFailAction = "explode" },
		"node.recoverAfterSec": func(s *Site) { s.Node.RecoverAfterSec = 5 },
		"routing.rewrites[0].match": func(s *Site) {
			s.Routing.Rewrites = []RewriteRule{{Match: "(", Action: "rewrite"}}
		},
		"bindings[0].certMode": func(s *Site) {
			s.Bindings[0] = Binding{Protocol: "https", Port: 443, Host: "*.example.com", CertMode: CertModeAuto}
		},
	}
	for field, mutate := range cases {
		s := nodeSite()
		mutate(s)
		err := s.Validate()
		ve, ok := err.(*ValidationError)
		if !ok || !strings.HasPrefix(ve.Field, field) {
			t.Errorf("%s: got %v", field, err)
		}
	}
}

func TestBindingConflicts(t *testing.T) {
	a := nodeSite()
	b := nodeSite()
	b.ID = "b"
	if err := ValidateBindings(b, []*Site{a}); err == nil {
		t.Fatal("duplicate binding accepted")
	}
	b.Bindings[0].Host = "other.com"
	if err := ValidateBindings(b, []*Site{a}); err != nil {
		t.Fatalf("different host rejected: %v", err)
	}
	b.Bindings[0] = Binding{Protocol: "https", Port: 80, Host: "other.com"}
	if err := ValidateBindings(b, []*Site{a}); err == nil {
		t.Fatal("http and https on one port accepted")
	}
}

func TestResolveRoot(t *testing.T) {
	s := nodeSite()
	if got := s.ResolveRoot("/data/sites", "dist"); got != "dist" {
		t.Fatalf("without release: %s", got)
	}
	s.ActiveRelease = "r1"
	if got := s.ResolveRoot("/data/sites", "dist"); !strings.HasSuffix(got, "r1/dist") && !strings.HasSuffix(got, `r1\dist`) {
		t.Fatalf("relative in release: %s", got)
	}
	if got := s.ResolveRoot("/data/sites", `C:\apps\app`); !strings.HasSuffix(got, "r1") {
		t.Fatalf("absolute path should use release root: %s", got)
	}
}
