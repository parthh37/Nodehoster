package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/procmgr"
)

// slotHarness is a harness whose node sites can deploy (no install step
// runs: the releases have no package.json) and whose slot activations
// are recorded like core's activateSlot does them.
type slotHarness struct {
	*harness
	mu       sync.Mutex
	slotHits []string // "slot=release"
	finished chan *model.Deployment
}

func newSlotHarness(t *testing.T) *slotHarness {
	h := &slotHarness{harness: newHarness(t), finished: make(chan *model.Deployment, 10)}
	h.d.opts.ResolveNode = func(string) (procmgr.NodeRuntime, error) {
		return procmgr.NodeRuntime{Version: "test", Exe: "/nonexistent/node"}, nil
	}
	h.d.opts.ActivateSlot = func(ctx context.Context, siteID, slot, release string) error {
		s, err := h.st.GetSite(ctx, siteID)
		if err != nil {
			return err
		}
		s.FindSlot(slot).ActiveRelease = release
		h.mu.Lock()
		h.slotHits = append(h.slotHits, slot+"="+release)
		h.mu.Unlock()
		return h.st.PutSite(ctx, s)
	}
	h.d.opts.OnFinish = func(site *model.Site, dep *model.Deployment) {
		// The site is free again by now: a swap could reserve it.
		release, err := h.d.Reserve(site.ID)
		if err == nil {
			release()
		}
		h.finished <- dep
	}
	return h
}

func (h *slotHarness) nodeSite(t *testing.T, id string) *model.Site {
	t.Helper()
	s := &model.Site{ID: id, Name: "app-" + id, Type: model.SiteNode, ActiveRelease: "prod-0",
		Node: &model.NodeConfig{Script: "server.js", Env: []model.EnvVar{
			{Name: "API_URL", Value: "https://api"}, {Name: "DB", Value: "prod", SlotSetting: true},
		}},
		Slots: []model.DeploymentSlot{{Name: "staging", Env: []model.EnvVar{{Name: "API_URL", Value: "https://api-staging"}}}},
	}
	s.ApplyDefaults()
	s.Deploy.KeepReleases = 1
	if err := h.st.PutSite(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (h *slotHarness) deploySlot(t *testing.T, site *model.Site) *model.Deployment {
	t.Helper()
	zp := writeZip(t, t.TempDir(), "app.zip", []zipEntry{{name: "server.js", body: "//"}})
	dep, err := h.d.DeployZip(context.Background(), model.SlotSite(site, "staging"), zp, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if dep.Slot != "staging" {
		t.Fatalf("started deployment slot = %q", dep.Slot)
	}
	got := h.wait(t, dep)
	if got.Status != "succeeded" {
		log, _ := h.d.Log(site.ID, dep.ID)
		t.Fatalf("deployment %s: %s\n%s", got.Status, got.Message, log)
	}
	if fin := <-h.finished; fin.ID != dep.ID || fin.Status != "succeeded" || fin.Slot != "staging" {
		t.Fatalf("OnFinish got %+v", fin)
	}
	return got
}

// TestDeployToSlot: a deployment made with a slot's configuration is
// recorded for the slot, activated in the slot (production is left
// alone) and built with the slot's variables.
func TestDeployToSlot(t *testing.T) {
	h := newSlotHarness(t)
	site := h.nodeSite(t, "s1")
	dep := h.deploySlot(t, site)
	if dep.Slot != "staging" {
		t.Fatalf("stored slot = %q", dep.Slot)
	}
	if calls := h.act.calls(); len(calls) != 0 {
		t.Fatalf("production activated: %v", calls)
	}
	cur, _ := h.st.GetSite(context.Background(), "s1")
	if cur.ActiveRelease != "prod-0" || cur.FindSlot("staging").ActiveRelease != dep.ID {
		t.Fatalf("releases: production %s, staging %s", cur.ActiveRelease, cur.FindSlot("staging").ActiveRelease)
	}

	env, err := h.d.commandEnv(model.SlotSite(site, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "API_URL=https://api-staging") || strings.Contains(joined, "DB=prod") {
		t.Fatalf("build environment of the slot:\n%s", joined)
	}

	// Rolling a slot back activates the release in the slot.
	cur2, _ := h.st.GetSite(context.Background(), "s1")
	if _, err := h.d.Activate(context.Background(), model.SlotSite(cur2, "staging"), dep.ID); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	hits := slices.Clone(h.slotHits)
	h.mu.Unlock()
	if len(hits) != 2 || hits[1] != "staging="+dep.ID {
		t.Fatalf("slot activations = %v", hits)
	}
}

// TestSlotDeployKeepsPreviousSlotRelease: pruning after a deployment to a
// slot keeps the slot's previous release (its instances are draining),
// not production's.
func TestSlotDeployKeepsPreviousSlotRelease(t *testing.T) {
	h := newSlotHarness(t)
	site := h.nodeSite(t, "s1")
	first := h.deploySlot(t, site)
	cur, _ := h.st.GetSite(context.Background(), "s1")
	second := h.deploySlot(t, cur)
	got, _ := h.st.GetDeployment(context.Background(), first.ID)
	if got.ReleaseDir == "" || !exists(got.ReleaseDir) {
		t.Fatal("the slot's previous release was pruned while it drains")
	}
	cur, _ = h.st.GetSite(context.Background(), "s1")
	third := h.deploySlot(t, cur)
	if got, _ := h.st.GetDeployment(context.Background(), first.ID); got.ReleaseDir != "" {
		t.Fatal("an old slot release was kept")
	}
	for _, d := range []*model.Deployment{second, third} {
		if got, _ := h.st.GetDeployment(context.Background(), d.ID); got.ReleaseDir == "" {
			t.Fatalf("%s pruned", d.ID)
		}
	}
}

// TestSwapReservation: a swap and a deployment exclude each other, with
// errors that say which one is running.
func TestSwapReservation(t *testing.T) {
	h := newSlotHarness(t)
	site := h.nodeSite(t, "s1")
	release, err := h.d.Reserve("s1")
	if err != nil {
		t.Fatal(err)
	}
	zp := writeZip(t, t.TempDir(), "app.zip", []zipEntry{{name: "server.js", body: "//"}})
	_, err = h.d.DeployZip(context.Background(), site, zp, "alice")
	if !errors.Is(err, ErrSwapping) || !errors.Is(err, ErrBusy) {
		t.Fatalf("deploy during a swap: %v", err)
	}
	if _, err := h.d.Reserve("s1"); !errors.Is(err, ErrSwapping) {
		t.Fatalf("second swap: %v", err)
	}
	release()
	release() // idempotent

	h.act.gate = make(chan struct{})
	dep, err := h.d.DeployZip(context.Background(), site, writeZip(t, t.TempDir(), "b.zip", []zipEntry{{name: "server.js", body: "//"}}), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.Reserve("s1"); err == nil || errors.Is(err, ErrSwapping) || !errors.Is(err, ErrBusy) {
		t.Fatalf("swap during a deployment: %v", err)
	}
	close(h.act.gate)
	h.wait(t, dep)
	<-h.finished
}
