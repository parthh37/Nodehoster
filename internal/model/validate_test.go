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
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]func(*Site){
		"name":             func(s *Site) { s.Name = "" },
		"bindings[0].host": func(s *Site) { s.Bindings[0].Host = "bad host" },
		"bindings[0].port": func(s *Site) { s.Bindings[0].Port = 70000 },
		"node.script":      func(s *Site) { s.Node.Script = "" },
		"node.instances":   func(s *Site) { s.Node.PortMode = "fixed"; s.Node.FixedPort = 3000; s.Node.Instances = 2 },
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
