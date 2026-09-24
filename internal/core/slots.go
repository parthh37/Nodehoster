package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/deploy"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/store"
)

// Deployment slots (see model.DeploymentSlot). The process manager runs
// each slot's instances and the proxy routes each slot's bindings; this
// file keeps them in step with the configuration, and runs swaps:
//
//  1. prepare: the slot's instances restart with production's settings
//     (model.SwapSite), like Azure applying the target slot's settings to
//     the source slot, and scale to production's instance count
//  2. warm up: the warm-up paths are requested on every instance until
//     they answer an accepted status
//  3. swap: the releases change places in the stored configuration, then
//     production's traffic moves onto the slot's instances in one step;
//     the old production instances become the slot and are recycled onto
//     the slot's settings. Swapping again is the rollback.
//
// A failure before step 3 changes nothing: the slot goes back to its own
// settings. A swap holds the site against deployments (deploy.Reserve),
// configuration changes, rollbacks and start/stop/recycle, and a site has
// at most one swap at a time.

type slotState struct {
	mu     sync.Mutex
	active map[string]*model.SwapProgress // by site ID
	last   map[string]*model.SwapResult
	views  map[string]slotViews // what the proxy routes, per site
}

// slotViews are the configurations a site with slots is routed as,
// computed once per stored configuration so that the proxy keeps a
// site's runtime (and response cache) across unrelated reloads.
type slotViews struct {
	src   *model.Site
	views []*model.Site
}

// swapBusy refuses what cannot happen during a swap of the site.
func (c *Core) swapBusy(id string) error {
	c.slots.mu.Lock()
	defer c.slots.mu.Unlock()
	if c.slots.active[id] != nil {
		return deploy.ErrSwapping
	}
	return nil
}

// routedViews is what the proxy routes for a site: the site itself, or,
// with slots, production with its own bindings plus one configuration
// per slot, keyed model.SlotKey so that it finds the slot's instances,
// traffic statistics and cache. A slot is only ever served by this
// server's instances (never load balanced to other servers).
func (c *Core) routedViews(s *model.Site) []*model.Site {
	if len(s.Slots) == 0 && !s.HasSlotBindings() {
		return []*model.Site{s}
	}
	c.slots.mu.Lock()
	defer c.slots.mu.Unlock()
	if v, ok := c.slots.views[s.ID]; ok && v.src == s {
		return v.views
	}
	prod := *s
	prod.Bindings = s.SlotBindings("")
	views := []*model.Site{&prod}
	for _, sl := range s.Slots {
		v := model.SlotSite(s, sl.Name)
		v.Node.AppRoot = v.ResolveRoot(c.Paths.Sites, v.Node.AppRoot) // before the ID changes
		v.ActiveRelease = ""
		v.ID = model.SlotKey(s.ID, sl.Name)
		v.Name = s.Name + " [" + sl.Name + "]"
		v.Bindings = s.SlotBindings(sl.Name)
		v.Node.LoadBalancer = model.LoadBalancerConfig{}
		views = append(views, v)
	}
	if c.slots.views == nil {
		c.slots.views = map[string]slotViews{}
	}
	c.slots.views[s.ID] = slotViews{src: s, views: views}
	return views
}

// forgetSlots drops what is kept about a deleted site's slots.
func (c *Core) forgetSlots(s *model.Site) {
	c.slots.mu.Lock()
	delete(c.slots.views, s.ID)
	delete(c.slots.last, s.ID)
	c.slots.mu.Unlock()
	for _, sl := range s.Slots {
		c.Proxy.ForgetSite(model.SlotKey(s.ID, sl.Name))
	}
}

// prepareSlots keeps each slot's release (NodeHoster's to manage, like
// Site.ActiveRelease; matched by name) and seals the slots' secret
// variables, keeping the stored value of a masked one.
func (c *Core) prepareSlots(in, existing *model.Site) error {
	for i := range in.Slots {
		sl := &in.Slots[i]
		sl.ActiveRelease = ""
		var oldEnv []model.EnvVar
		if existing != nil {
			if old := existing.FindSlot(sl.Name); old != nil {
				sl.ActiveRelease, oldEnv = old.ActiveRelease, old.Env
			}
		}
		for j := range sl.Env {
			e := &sl.Env[j]
			if !e.Secret {
				if e.Value == secrets.Mask {
					e.Value = ""
				}
				continue
			}
			if e.Value == secrets.Mask {
				e.Value = ""
				for _, o := range oldEnv {
					if o.Name == e.Name && o.Secret {
						e.Value = o.Value
					}
				}
				continue
			}
			sealed, err := c.Box.Seal(e.Value)
			if err != nil {
				return err
			}
			e.Value = sealed
		}
	}
	return nil
}

// maskSlots hides the slots' secret variables (Masked).
func maskSlots(m *model.Site) {
	for i := range m.Slots {
		for j := range m.Slots[i].Env {
			if e := &m.Slots[i].Env[j]; e.Secret && e.Value != "" {
				e.Value = secrets.Mask
			}
		}
	}
}

// releasesInUse are the releases a deployment must not prune besides
// production's and the one it replaced: those task runs use and those
// the site's slots run.
func (c *Core) releasesInUse(id string) []string {
	out := c.Tasks.Releases(id)
	if s, err := c.Site(id); err == nil {
		out = append(out, s.SlotReleases()...)
	}
	return out
}

// DeployTarget is the configuration a deployment to a slot is made with
// ("" or "production" = the site itself). Its name carries the slot for
// the deployment's events.
func (c *Core) DeployTarget(s *model.Site, slot string) (*model.Site, error) {
	slot = model.NormalizeSlot(slot)
	if slot == "" {
		return s, nil
	}
	t := model.SlotSite(s, slot)
	if t == nil {
		return nil, &model.ValidationError{Field: "slot", Message: fmt.Sprintf("the site has no deployment slot %q", slot)}
	}
	t.Name = s.Name + " [" + slot + "]"
	return t, nil
}

// activateSlot is called by the deployer to switch a slot to a release: a
// deployment to the slot, or a rollback in it. The slot is started when
// it is not running: it is there to be tried.
func (c *Core) activateSlot(ctx context.Context, id, slot, release string) error {
	if err := c.swapBusy(id); err != nil {
		return err
	}
	c.sitesMu.Lock()
	defer c.sitesMu.Unlock()
	existing, err := c.Site(id)
	if err != nil {
		return err
	}
	s := clone(existing)
	sl := s.FindSlot(slot)
	if sl == nil {
		return fmt.Errorf("the site has no deployment slot %q any more", slot)
	}
	sl.ActiveRelease = release
	s.UpdatedAt = time.Now()
	if err := c.Store.PutSite(ctx, s); err != nil {
		return err
	}
	c.cacheMu.Lock()
	c.sites[id] = s
	c.cacheMu.Unlock()
	c.Procs.Apply(s) // a running slot recycles onto the new release
	if !c.Procs.Running(model.SlotKey(id, slot)) {
		if err := c.Procs.StartSlot(id, slot); err != nil {
			c.Bus.Error(events.SiteFailed, id, "%s [%s] could not start: %v", s.Name, slot, err)
		}
	}
	c.Proxy.PurgeCache(model.SlotKey(id, slot), "")
	c.reload()
	return nil
}

// deployFinished starts the auto-swap of a slot after a successful
// deployment to it.
func (c *Core) deployFinished(site *model.Site, dep *model.Deployment) {
	if dep.Status != "succeeded" || site.Slot == "" {
		return
	}
	cur, err := c.Site(site.ID)
	if err != nil {
		return
	}
	sl := cur.FindSlot(site.Slot)
	if sl == nil || !sl.AutoSwap || sl.ActiveRelease != dep.ID {
		return
	}
	c.AddAudit(context.Background(), model.AuditEntry{Time: time.Now(), User: "auto-swap", Action: "site.slot.swap", Target: cur.Name, Detail: site.Slot})
	if _, err := c.StartSwap(site.ID, site.Slot, "auto-swap", true); err != nil {
		c.Bus.Error(events.SlotSwapFailed, site.ID, "%s: auto-swap of %s into production did not start: %v", cur.Name, site.Slot, err)
		now := time.Now()
		c.setLastSwap(site.ID, &model.SwapResult{Slot: site.Slot, Message: err.Error(), User: "auto-swap", Auto: true, StartedAt: now, FinishedAt: now,
			ProductionRelease: cur.ActiveRelease, SlotRelease: sl.ActiveRelease})
	}
}

// startSlots starts the slots that have a release, of the sites that start
// with the service.
func (c *Core) startSlots(sites []*model.Site) {
	for _, s := range sites {
		if !s.RunsNode() {
			continue
		}
		for _, sl := range s.Slots {
			if sl.ActiveRelease == "" {
				continue
			}
			if err := c.Procs.StartSlot(s.ID, sl.Name); err != nil {
				c.Bus.Error(events.SiteFailed, s.ID, "%s [%s] could not start: %v", s.Name, sl.Name, err)
			}
		}
	}
}

// recordSlotMetrics stores a minute's metrics point for each slot, under
// the slot's key.
func (c *Core) recordSlotMetrics(ctx context.Context, s *model.Site, ts time.Time) {
	for _, sl := range s.Slots {
		key := model.SlotKey(s.ID, sl.Name)
		req, errs, lat := c.Proxy.TakeMinute(key)
		cpu, mem := c.Procs.Usage(key)
		c.Store.AddMetrics(ctx, key, model.MetricPoint{Time: ts, Requests: req, Errors: errs, AvgLatency: lat, CPUPercent: cpu, MemoryBytes: mem})
	}
}

// ---- API

// Slots is every slot of a site, production first, with the swap in
// progress or the last one.
func (c *Core) Slots(s *model.Site) model.SlotsView {
	out := model.SlotsView{Slots: []model.SlotStatus{{
		Name: model.ProductionSlot, Release: s.ActiveRelease, Status: c.Status(s), Bindings: s.SlotBindings(""),
	}}}
	for _, sl := range s.Slots {
		key := model.SlotKey(s.ID, sl.Name)
		st, ok := c.Procs.Status(key)
		if !ok {
			st = model.SiteStatus{State: model.StateStopped}
		}
		if st.Instances == nil {
			st.Instances = []model.InstanceStatus{}
		}
		st.SiteID = s.ID
		st.Traffic = c.Proxy.Traffic(key)
		st.Cache = c.Proxy.CacheStats(key)
		out.Slots = append(out.Slots, model.SlotStatus{Name: sl.Name, Release: sl.ActiveRelease, Status: st, Bindings: s.SlotBindings(sl.Name), AutoSwap: sl.AutoSwap})
	}
	c.slots.mu.Lock()
	if p := c.slots.active[s.ID]; p != nil {
		cp := *p
		out.Swap = &cp
	}
	if r := c.slots.last[s.ID]; r != nil {
		cp := *r
		out.LastSwap = &cp
	}
	c.slots.mu.Unlock()
	return out
}

// SlotAction starts, stops or recycles a slot's instances ("" or
// "production": the site, like StartSite and friends).
func (c *Core) SlotAction(id, slot, action string) error {
	slot = model.NormalizeSlot(slot)
	if slot == "" {
		switch action {
		case "start":
			return c.StartSite(id)
		case "stop":
			return c.StopSite(id)
		case "recycle":
			return c.RecycleSite(id)
		}
		return fmt.Errorf("unknown action %q", action)
	}
	if err := c.swapBusy(id); err != nil {
		return err
	}
	s, err := c.Site(id)
	if err != nil {
		return err
	}
	sl := s.FindSlot(slot)
	if sl == nil {
		return store.ErrNotFound
	}
	switch action {
	case "start":
		err = c.Procs.StartSlot(id, slot)
	case "stop":
		err = c.Procs.StopSlot(id, slot)
	case "recycle":
		err = c.Procs.RecycleSlot(id, slot, "requested")
		c.Proxy.PurgeCache(model.SlotKey(id, slot), "")
	default:
		return fmt.Errorf("unknown action %q", action)
	}
	c.reload()
	return err
}

// SwapPreview says what swapping a slot into production would do.
// revealSecrets (an administrator asks) names the secret variables whose
// values differ between the slot and production; otherwise they are only
// counted.
func (c *Core) SwapPreview(s *model.Site, slot string, revealSecrets bool) (model.SwapPreview, error) {
	slot = model.NormalizeSlot(slot)
	sl := s.FindSlot(slot)
	if sl == nil {
		return model.SwapPreview{}, store.ErrNotFound
	}
	p := model.SwapPreview{Slot: slot, ProductionRelease: s.ActiveRelease, SlotRelease: sl.ActiveRelease, Warmup: sl.Warmup,
		Changes: []string{}, Warnings: []string{}, Blockers: []string{}}
	key := model.SlotKey(s.ID, slot)
	prodState, _ := c.Procs.Status(s.ID)
	slotState, _ := c.Procs.Status(key)
	// A slot without a release runs production's application folder: after
	// a swap from a site that had none, that is its previous production.
	if sl.ActiveRelease == "" && !c.Procs.Running(key) {
		p.Blockers = append(p.Blockers, "Nothing has been deployed to "+slot+" yet: deploy to it first.")
	}
	if prodState.State == model.StateStopped {
		p.Blockers = append(p.Blockers, "Production is stopped: start it first (a swap does not change whether the site runs).")
	}
	if c.swapBusy(s.ID) != nil {
		p.Blockers = append(p.Blockers, "A swap is already in progress for this site.")
	}

	prep := model.SwapSite(s, slot)
	own := model.SlotSite(s, slot)
	n := prep.Node.Instances
	if !c.Procs.Running(key) {
		p.Changes = append(p.Changes, fmt.Sprintf("Start %s with production's settings (it is stopped, so its instances start cold).", slot))
	} else if diff, secretDiff := c.envDiff(own.Node.Env, prep.Node.Env, revealSecrets); len(diff) > 0 || secretDiff > 0 || own.Node.Instances != n {
		what := "its instances"
		if secretDiff > 0 {
			diff = append(diff, fmt.Sprintf("%d secret variable(s)", secretDiff))
		}
		if len(diff) > 0 {
			what += " with production's values of " + strings.Join(diff, ", ")
		}
		p.Changes = append(p.Changes, fmt.Sprintf("Restart %s's %s (a rolling recycle, like Azure applying production's slot settings first).", slot, what))
	} else {
		p.Changes = append(p.Changes, fmt.Sprintf("%s already runs with production's settings: its instances are not restarted.", slot))
	}
	if own.Node.Instances != n {
		p.Changes = append(p.Changes, fmt.Sprintf("Run %d instance(s) in %s, as production does (it runs %d).", n, slot, own.Node.Instances))
	}
	if s.Type == model.SiteWorker {
		p.Changes = append(p.Changes, "No warm-up: a background worker serves no HTTP. Its instances only need to be running.")
	} else {
		p.Changes = append(p.Changes, fmt.Sprintf("Request %s on every instance until each answers %s, for at most %d s; the swap is abandoned if they do not.",
			strings.Join(sl.Warmup.Paths, ", "), sl.Warmup.Statuses, sl.Warmup.TimeoutSec))
		p.Changes = append(p.Changes, "Move production's traffic onto those instances at once. Requests already running on the current production instances finish there.")
	}
	p.Changes = append(p.Changes, fmt.Sprintf("Production then runs %s; %s runs %s (production's release now), recycled onto %s's own settings. Swap again to roll back.",
		orNone(sl.ActiveRelease), slot, orNone(s.ActiveRelease), slot))
	if s.Routing.Cache.Enabled {
		p.Changes = append(p.Changes, "Empty the response cache of production and "+slot+".")
	}
	if len(s.Tasks) > 0 {
		p.Changes = append(p.Changes, "Scheduled tasks run the new production release from their next run (tasks run in production only).")
	}
	if slotState.State == model.StateFailed {
		p.Warnings = append(p.Warnings, slot+" was stopped by rapid-fail protection: it is started again for the swap, and the swap is abandoned if it keeps failing.")
	}
	if s.Node.LoadBalancer.Enabled {
		p.Warnings = append(p.Warnings, "This site is load balanced across servers: only this server swaps. The other servers keep their release until they are deployed to or swapped too.")
	}
	if s.Routing.Affinity.Enabled {
		p.Warnings = append(p.Warnings, "Session affinity: clients stay on the same instance number of the new release, but anything a process kept in memory (sessions) does not survive the swap, as with a recycle.")
	}
	if len(sl.Env) == 0 && !slices.ContainsFunc(s.Node.Env, func(e model.EnvVar) bool { return e.SlotSetting }) {
		p.Warnings = append(p.Warnings, slot+" has no slot settings: it runs with exactly production's variables, database included.")
	}
	return p, nil
}

func orNone(r string) string {
	if r == "" {
		return "the application folder (no release)"
	}
	return r
}

// envDiff compares two lists of variables. names are the variables that
// differ in what the site's configuration shows its viewers anyway: a
// plain value, a secret store reference, whether one is secret, being in
// one list only. Variables secret in both lists are compared decrypted
// (their values never leave this function); they are named only when
// reveal is set (an administrator), otherwise counted in secrets, so that
// the preview does not tell a viewer which secret values differ.
func (c *Core) envDiff(a, b []model.EnvVar, reveal bool) (names []string, secrets int) {
	val := func(e model.EnvVar) string {
		switch {
		case e.From != nil:
			return "f:" + e.From.String()
		case e.Secret:
			v, _ := c.Box.Unseal(e.Value)
			return "s:" + v
		}
		return "p:" + e.Value
	}
	index := func(list []model.EnvVar) map[string]model.EnvVar {
		m := map[string]model.EnvVar{}
		for _, e := range list {
			m[e.Name] = e
		}
		return m
	}
	x, y := index(a), index(b)
	for k, e := range x {
		f, ok := y[k]
		switch {
		case ok && val(e) == val(f):
		case ok && !reveal && e.Secret && f.Secret && e.From == nil && f.From == nil:
			secrets++
		default:
			names = append(names, k)
		}
	}
	for k := range y {
		if _, ok := x[k]; !ok {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	return names, secrets
}

// StartSwap begins swapping a slot into production and returns at once;
// the swap goes on in the background (see Slots for its progress, and
// the slot.swapped and slot.swap_failed events for its end).
func (c *Core) StartSwap(id, slot, user string, auto bool) (*model.SwapProgress, error) {
	slot = model.NormalizeSlot(slot)
	if slot == "" {
		return nil, &model.ValidationError{Field: "slot", Message: "choose the slot to swap into production"}
	}
	s, err := c.Site(id)
	if err != nil {
		return nil, err
	}
	pv, err := c.SwapPreview(s, slot, false)
	if err != nil {
		return nil, err
	}
	if len(pv.Blockers) > 0 {
		if c.swapBusy(id) != nil {
			return nil, deploy.ErrSwapping
		}
		return nil, &model.ValidationError{Field: "slot", Message: pv.Blockers[0]}
	}
	release, err := c.Deploy.Reserve(id)
	if err != nil {
		return nil, err
	}
	p := &model.SwapProgress{Slot: slot, Phase: model.SwapPreparing, User: user, Auto: auto, StartedAt: time.Now()}
	c.slots.mu.Lock()
	if c.slots.active[id] != nil {
		c.slots.mu.Unlock()
		release()
		return nil, deploy.ErrSwapping
	}
	if c.slots.active == nil {
		c.slots.active = map[string]*model.SwapProgress{}
	}
	c.slots.active[id] = p
	snapshot := *p
	c.slots.mu.Unlock()
	c.wg.Go(func() {
		defer release()
		c.runSwap(s, slot, p)
	})
	return &snapshot, nil
}

func (c *Core) setLastSwap(id string, r *model.SwapResult) {
	c.slots.mu.Lock()
	defer c.slots.mu.Unlock()
	if c.slots.last == nil {
		c.slots.last = map[string]*model.SwapResult{}
	}
	c.slots.last[id] = r
}

// runSwap runs a swap StartSwap registered, and records how it ended.
func (c *Core) runSwap(s *model.Site, slot string, p *model.SwapProgress) {
	ctx := c.ctx
	if ctx == nil { // not started: tests
		ctx = context.Background()
	}
	logs := c.Procs.Logs(s.ID)
	logf := func(format string, a ...any) { logs.System("swap: "+format, a...) }
	phase := func(ph, msg string) {
		c.slots.mu.Lock()
		p.Phase, p.Message = ph, msg
		c.slots.mu.Unlock()
		logf("%s", msg)
	}
	logf("swapping %s (%s) into production (%s)", slot, orNone(s.ReleaseIn(slot)), orNone(s.ActiveRelease))
	err := c.swap(ctx, s, slot, phase, logf)

	res := &model.SwapResult{Slot: slot, Succeeded: err == nil, User: p.User, Auto: p.Auto, StartedAt: p.StartedAt, FinishedAt: time.Now()}
	if cur, cerr := c.Site(s.ID); cerr == nil {
		res.ProductionRelease, res.SlotRelease = cur.ActiveRelease, cur.ReleaseIn(slot)
	}
	took := res.FinishedAt.Sub(res.StartedAt).Round(100 * time.Millisecond)
	if err != nil {
		res.Message = err.Error()
		logf("abandoned, production is unchanged: %v", err)
		c.Bus.Error(events.SlotSwapFailed, s.ID, "%s: swap of %s into production failed, production is unchanged: %v", s.Name, slot, err)
	} else {
		res.Message = fmt.Sprintf("production runs %s; %s runs %s", orNone(res.ProductionRelease), slot, orNone(res.SlotRelease))
		logf("done in %s: %s", took, res.Message)
		c.Bus.Info(events.SlotSwapped, s.ID, "%s: %s swapped into production in %s (%s)", s.Name, slot, took, res.Message)
	}
	c.slots.mu.Lock()
	delete(c.slots.active, s.ID)
	if c.slots.last == nil {
		c.slots.last = map[string]*model.SwapResult{}
	}
	c.slots.last[s.ID] = res
	c.slots.mu.Unlock()
}

// swap runs the three phases. Before the last one commits, any failure
// puts the slot back on its own settings.
func (c *Core) swap(ctx context.Context, s *model.Site, slot string, phase func(ph, msg string), logf func(string, ...any)) error {
	prodRel, slotRel := s.ActiveRelease, s.ReleaseIn(slot)
	sl := s.FindSlot(slot)
	cancel := func(stop bool) {
		if cur, err := c.Site(s.ID); err == nil {
			c.Procs.CancelSwap(cur, slot, stop)
		}
	}

	phase(model.SwapPreparing, fmt.Sprintf("preparing: %s's instances restart with production's settings", slot))
	wasRunning, err := c.Procs.PrepareSwap(ctx, model.SwapSite(s, slot), slot)
	if err != nil {
		cancel(!wasRunning)
		return err
	}
	if s.Type != model.SiteWorker {
		phase(model.SwapWarming, "warming up "+strings.Join(sl.Warmup.Paths, ", "))
	}
	host, proto := productionHost(s)
	if err := c.Procs.WarmUp(ctx, s.ID, slot, sl.Warmup, host, proto, logf); err != nil {
		cancel(!wasRunning)
		return fmt.Errorf("warm-up failed: %w", err)
	}
	if err := ctx.Err(); err != nil { // the service is stopping
		cancel(!wasRunning)
		return err
	}
	phase(model.SwapSwapping, "moving production traffic")
	if err := c.commitSwap(s.ID, slot, prodRel, slotRel); err != nil {
		cancel(!wasRunning)
		return err
	}
	return nil
}

// commitSwap stores the swapped releases, then moves the processes: a
// restart right after comes back with the slot's release in production.
func (c *Core) commitSwap(id, slot, prodRel, slotRel string) error {
	c.sitesMu.Lock()
	defer c.sitesMu.Unlock()
	cur, err := c.Site(id)
	if err != nil {
		return err
	}
	sl := cur.FindSlot(slot)
	if sl == nil {
		return errors.New("the slot was removed during the swap")
	}
	if cur.ActiveRelease != prodRel || sl.ActiveRelease != slotRel {
		return errors.New("the releases changed during the swap")
	}
	next := clone(cur)
	ns := next.FindSlot(slot)
	next.ActiveRelease, ns.ActiveRelease = ns.ActiveRelease, next.ActiveRelease
	next.UpdatedAt = time.Now()
	ctx := context.Background() // not the service's: a swap that got here completes
	if err := c.Store.PutSite(ctx, next); err != nil {
		return err
	}
	if err := c.Procs.CommitSwap(id, slot); err != nil {
		c.Store.PutSite(ctx, cur) // nothing moved: keep the configuration as it runs
		return err
	}
	c.cacheMu.Lock()
	c.sites[id] = next
	c.cacheMu.Unlock()
	c.Procs.Apply(next) // production carries on; the old production instances recycle onto the slot's settings
	c.Tasks.Apply(next)
	c.reload()
	c.Proxy.PurgeCache(id, "")
	c.Proxy.PurgeCache(model.SlotKey(id, slot), "")
	return nil
}

// productionHost is the Host header and scheme warm-up requests carry:
// those of production's first binding with a specific host name, which
// is what the instances will be asked for once they are production.
func productionHost(s *model.Site) (host, proto string) {
	for _, b := range s.SlotBindings("") {
		if b.Host == "" || strings.HasPrefix(b.Host, "*.") {
			continue
		}
		host = b.Host
		if (b.Protocol == "http" && b.Port != 80) || (b.Protocol == "https" && b.Port != 443) {
			host += ":" + strconv.Itoa(b.Port)
		}
		return host, b.Protocol
	}
	return "localhost", "http"
}
