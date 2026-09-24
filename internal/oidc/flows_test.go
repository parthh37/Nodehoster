package oidc

import (
	"errors"
	"testing"
	"time"
)

func TestFlows(t *testing.T) {
	f := NewFlows()
	now := time.Now()
	f.now = func() time.Time { return now }

	fl, binding, err := f.Begin("https://idp", "app", "https://console/cb", "/sites")
	if err != nil {
		t.Fatal(err)
	}
	if fl.State == "" || fl.Nonce == "" || len(fl.Verifier) < 43 || binding == "" || fl.State == fl.Nonce {
		t.Fatalf("flow = %+v", fl)
	}
	if _, err := f.Take(fl.State, "other"); !errors.Is(err, ErrBinding) {
		t.Fatalf("wrong binding: %v", err)
	}
	if _, err := f.Take(fl.State, ""); !errors.Is(err, ErrBinding) {
		t.Fatalf("no binding: %v", err)
	}
	got, err := f.Take(fl.State, binding)
	if err != nil || got != fl {
		t.Fatalf("take: %v", err)
	}
	if _, err := f.Take(fl.State, binding); !errors.Is(err, ErrState) {
		t.Fatalf("replay: %v", err)
	}

	fl, binding, _ = f.Begin("https://idp", "app", "https://console/cb", "/")
	now = now.Add(FlowTTL + time.Second)
	if _, err := f.Take(fl.State, binding); !errors.Is(err, ErrState) {
		t.Fatalf("expired: %v", err)
	}
	if len(f.m) != 0 {
		t.Fatalf("%d flows left", len(f.m))
	}
}

func TestClaimsStrings(t *testing.T) {
	c := Claims{"groups": []any{"a", 1.0, "b"}, "role": "admin", "n": 3.0}
	if g := c.Strings("groups"); len(g) != 2 || g[1] != "b" {
		t.Errorf("groups = %v", g)
	}
	if r := c.Strings("role"); len(r) != 1 || r[0] != "admin" {
		t.Errorf("role = %v", r)
	}
	if c.Strings("n") != nil || c.String("n") != "" || c.Strings("missing") != nil {
		t.Error("non-strings returned")
	}
}
