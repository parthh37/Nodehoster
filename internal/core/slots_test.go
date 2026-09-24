package core

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
)

func slotTestSite() *model.Site {
	return &model.Site{Name: "shop", Type: model.SiteNode,
		Bindings: []model.Binding{
			{Protocol: "http", Port: 8081, Host: "www.example.com"},
			{Protocol: "http", Port: 8081, Host: "staging.example.com", Slot: "staging"},
		},
		Node: &model.NodeConfig{AppRoot: filepath.Join("C:", "apps", "shop"), Script: "server.js", Instances: 2,
			Env: []model.EnvVar{{Name: "DB", Value: "prod", SlotSetting: true}}},
		Slots: []model.DeploymentSlot{{Name: "staging", Env: []model.EnvVar{{Name: "TOKEN", Value: "s3cret", Secret: true}}}},
	}
}

// setSlotRelease stores a slot's release as a deployment would.
func setSlotRelease(t *testing.T, c *Core, id, slot, release string) *model.Site {
	t.Helper()
	if err := c.activateSlot(context.Background(), id, slot, release); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Site(id)
	return s
}

func TestSlotSecretsAndReleases(t *testing.T) {
	c := testCore(t)
	s, err := c.CreateSite(context.Background(), slotTestSite())
	if err != nil {
		t.Fatal(err)
	}
	tok := s.Slots[0].Env[0].Value
	if !secrets.IsSealed(tok) || c.Box.MustUnseal(tok) != "s3cret" {
		t.Fatalf("slot secret stored as %q", tok)
	}
	if m := Masked(s); m.Slots[0].Env[0].Value != secrets.Mask || s.Slots[0].Env[0].Value != tok {
		t.Fatalf("masked %q, stored %q", m.Slots[0].Env[0].Value, s.Slots[0].Env[0].Value)
	}
	s = setSlotRelease(t, c, s.ID, "staging", "r1")

	// An update from the API: the mask keeps the secret, the release is
	// NodeHoster's (a forged one is ignored, a missing one kept).
	in := Masked(s)
	in.Slots[0].ActiveRelease = "forged"
	in.Slots[0].AutoSwap = true
	got, err := c.UpdateSite(context.Background(), s.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if sl := got.Slots[0]; sl.ActiveRelease != "r1" || sl.Env[0].Value != tok || !sl.AutoSwap {
		t.Fatalf("after update: %+v", sl)
	}
	// Releases the slots run are kept by deployments' pruning.
	if !slices.Contains(c.releasesInUse(s.ID), "r1") {
		t.Fatalf("in use: %v", c.releasesInUse(s.ID))
	}
	// Renaming the slot starts it afresh.
	in = Masked(got)
	in.Slots[0].Name = "qa"
	in.Bindings[1].Slot = "qa"
	got, err = c.UpdateSite(context.Background(), s.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slots[0].ActiveRelease != "" || got.Slots[0].Env[0].Value != "" {
		t.Fatalf("renamed slot kept %+v", got.Slots[0])
	}
}

func TestRoutedViews(t *testing.T) {
	c := testCore(t)
	in := slotTestSite()
	in.Node.LoadBalancer = model.LoadBalancerConfig{Enabled: true, Servers: []model.Upstream{{URL: "http://10.0.0.2"}}}
	s, err := c.CreateSite(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	s = setSlotRelease(t, c, s.ID, "staging", "r2")
	views := c.routedViews(s)
	if len(views) != 2 {
		t.Fatalf("%d views", len(views))
	}
	prod, stg := views[0], views[1]
	if prod.ID != s.ID || len(prod.Bindings) != 1 || prod.Bindings[0].Host != "www.example.com" || !prod.Node.LoadBalancer.Enabled {
		t.Fatalf("production view: %+v", prod)
	}
	if stg.ID != s.ID+"@staging" || stg.Name != "shop [staging]" || len(stg.Bindings) != 1 || stg.Bindings[0].Host != "staging.example.com" {
		t.Fatalf("slot view: %+v", stg)
	}
	if stg.Node.LoadBalancer.Enabled || stg.ActiveRelease != "" || stg.Node.AppRoot != model.ReleaseDir(c.Paths.Sites, s.ID, "r2") {
		t.Fatalf("slot view: lb %v, release %q, root %q", stg.Node.LoadBalancer.Enabled, stg.ActiveRelease, stg.Node.AppRoot)
	}
	// The stored configuration is untouched, and the views are reused
	// until it changes (the proxy keeps the runtime, and its cache).
	if len(s.Bindings) != 2 || s.Node.AppRoot == stg.Node.AppRoot || !s.Node.LoadBalancer.Enabled {
		t.Fatal("the site was modified")
	}
	if again := c.routedViews(s); again[0] != prod || again[1] != stg {
		t.Fatal("views rebuilt for the same configuration")
	}
	// A site without slots is routed as it is.
	plain := &model.Site{ID: "x", Type: model.SiteNode}
	if v := c.routedViews(plain); len(v) != 1 || v[0] != plain {
		t.Fatal("a site without slots was copied")
	}
}

func TestSwapPreviewAndTarget(t *testing.T) {
	c := testCore(t)
	s, err := c.CreateSite(context.Background(), slotTestSite())
	if err != nil {
		t.Fatal(err)
	}
	pv, err := c.SwapPreview(s, "staging", false)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing deployed, production stopped.
	if len(pv.Blockers) != 2 {
		t.Fatalf("blockers = %v", pv.Blockers)
	}
	if _, err := c.StartSwap(s.ID, "staging", "alice", false); err == nil {
		t.Fatal("a blocked swap started")
	}
	if _, err := c.SwapPreview(s, "qa", true); err == nil {
		t.Fatal("preview of a missing slot")
	}
	if _, err := c.StartSwap(s.ID, "production", "alice", false); err == nil {
		t.Fatal("swapped production with itself")
	}

	tgt, err := c.DeployTarget(s, "staging")
	if err != nil || tgt.Slot != "staging" || tgt.Name != "shop [staging]" || tgt.ID != s.ID {
		t.Fatalf("target = %+v, %v", tgt, err)
	}
	if tgt, _ := c.DeployTarget(s, "production"); tgt != s {
		t.Fatal("production target is not the site")
	}
	if _, err := c.DeployTarget(s, "qa"); err == nil {
		t.Fatal("deploy target for a missing slot")
	}
}

func TestEnvDiffAndHost(t *testing.T) {
	c := testCore(t)
	a, _ := c.Box.Seal("same")
	b, _ := c.Box.Seal("same") // sealed twice: different ciphertexts
	d, _ := c.Box.Seal("other")
	vault := func(r string) *model.SecretRef { return &model.SecretRef{Store: "vault", Ref: r} }
	x := []model.EnvVar{{Name: "A", Value: a, Secret: true}, {Name: "B", Value: "1"}, {Name: "C", Value: d, Secret: true}, {Name: "ONLY_A", Value: "x"},
		{Name: "R", From: vault("app#R")}, {Name: "S", From: vault("app#S")}, {Name: "T", Value: d, Secret: true}}
	y := []model.EnvVar{{Name: "A", Value: b, Secret: true}, {Name: "B", Value: "2"}, {Name: "C", Value: a, Secret: true}, {Name: "ONLY_B", Value: "y"},
		{Name: "R", From: vault("app#R")}, {Name: "S", From: vault("other#S")}, {Name: "T", From: vault("app#T")}}
	// An administrator is told which secrets differ; references are
	// compared, not only values (S and T differ in their source).
	diff, n := c.envDiff(x, y, true)
	if strings.Join(diff, ",") != "B,C,ONLY_A,ONLY_B,S,T" || n != 0 {
		t.Fatalf("diff = %v, %d", diff, n)
	}
	// Anyone else only how many: which secret values differ is not
	// something the configuration shows them.
	diff, n = c.envDiff(x, y, false)
	if strings.Join(diff, ",") != "B,ONLY_A,ONLY_B,S,T" || n != 1 {
		t.Fatalf("viewer diff = %v, %d", diff, n)
	}

	for _, tc := range []struct {
		bindings    []model.Binding
		host, proto string
	}{
		{nil, "localhost", "http"},
		{[]model.Binding{{Protocol: "https", Port: 443, Host: "*.example.com"}, {Protocol: "https", Port: 443, Host: "shop.example.com"}}, "shop.example.com", "https"},
		{[]model.Binding{{Protocol: "http", Port: 8080, Host: "shop.example.com"}}, "shop.example.com:8080", "http"},
		{[]model.Binding{{Protocol: "http", Port: 80, Host: "staging.example.com", Slot: "staging"}}, "localhost", "http"},
	} {
		host, proto := productionHost(&model.Site{Bindings: tc.bindings})
		if host != tc.host || proto != tc.proto {
			t.Errorf("%+v: %s %s", tc.bindings, host, proto)
		}
	}
}
