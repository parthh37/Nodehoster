package procmgr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
)

// Deployment slots. Every slot of a site is an App of its own, registered
// under model.SlotKey(siteID, slot): production under the site ID, as
// always, and "<site ID>@<slot>" for the others. An App keeps its
// processes for life. A swap exchanges the keys two Apps are registered
// under, in one step under the registry lock: the proxy looks a key's
// backends up on every request, so production traffic moves onto the
// slot's warm instances at once, requests already running on the old
// production instances finish there, and those instances carry on as the
// slot.

// ErrNoSlot is returned for a slot the site does not have.
var ErrNoSlot = errors.New("no such deployment slot")

// slotName is the deployment slot the app is in ("" = production).
func (a *App) slotName() string {
	if p := a.slot.Load(); p != nil {
		return *p
	}
	return ""
}

func (a *App) setSlot(name string) { a.slot.Store(&name) }

// key is where the app is registered.
func (a *App) key() string { return model.SlotKey(a.id, a.slotName()) }

// slotSuffix labels a slot's events: "shop [staging] instance 0 …".
func (a *App) slotSuffix() string {
	if s := a.slotName(); s != "" {
		return " [" + s + "]"
	}
	return ""
}

// view returns a sink that writes to s, tagging every line with the slot
// the function names at the time ("" = production). Only Write and System
// may be used on it.
func (s *LogSink) view(slot func() string) *LogSink {
	return &LogSink{parent: s, slot: slot}
}

// applySlots registers, reconfigures or removes the apps of a site's
// slots to match its configuration. Apply runs it last. A site in the
// middle of a swap is left alone: the swap applies the configuration when
// it ends.
func (m *Manager) applySlots(site *model.Site) {
	want := map[string]*model.Site{}
	if site.RunsNode() {
		for _, sl := range site.Slots {
			if cfg := model.SlotSite(site, sl.Name); cfg != nil {
				want[sl.Name] = cfg
			}
		}
	}
	sink := m.Logs(site.ID)
	type change struct {
		a   *App
		cfg *model.Site
	}
	var changed []change
	var gone []*App
	m.mu.Lock()
	if m.swapping[site.ID] {
		m.mu.Unlock()
		return
	}
	for key, a := range m.apps {
		id, slot := model.SplitSlotKey(key)
		if id != site.ID || slot == "" {
			continue
		}
		if cfg, ok := want[slot]; ok {
			changed = append(changed, change{a, cfg})
			delete(want, slot)
		} else {
			delete(m.apps, key)
			gone = append(gone, a)
		}
	}
	for slot, cfg := range want {
		a := &App{m: m, id: site.ID, site: cfg, schedFired: map[string]string{}}
		a.setSlot(slot)
		a.logs = sink.view(a.slotName)
		m.apps[model.SlotKey(site.ID, slot)] = a
	}
	// A removed slot stops in the background (its shutdown timeout
	// should not hold up a configuration change); Shutdown waits for it.
	inline := false
	select {
	case <-m.stop:
		inline = true
	default:
		m.retiring.Add(len(gone))
	}
	m.mu.Unlock()

	for _, c := range changed {
		c.a.reconfigure(c.cfg, false)
	}
	for _, a := range gone {
		if inline {
			a.stop()
			continue
		}
		go func(a *App) {
			defer m.retiring.Done()
			a.stop()
		}(a)
	}
}

// removeSlots stops and forgets every slot of a site (Remove).
func (m *Manager) removeSlots(siteID string) {
	m.mu.Lock()
	var apps []*App
	for key, a := range m.apps {
		if id, slot := model.SplitSlotKey(key); id == siteID && slot != "" {
			apps = append(apps, a)
			delete(m.apps, key)
		}
	}
	delete(m.swapping, siteID)
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, a := range apps {
		wg.Add(1)
		go func(a *App) {
			defer wg.Done()
			a.stop()
		}(a)
	}
	wg.Wait()
}

func (m *Manager) slotApp(siteID, slot string) (*App, error) {
	a := m.app(model.SlotKey(siteID, slot))
	if a == nil {
		if slot == "" {
			return nil, ErrNotNode
		}
		return nil, ErrNoSlot
	}
	return a, nil
}

// StartSlot starts a slot's instances ("" = production, like Start).
func (m *Manager) StartSlot(siteID, slot string) error {
	a, err := m.slotApp(siteID, slot)
	if err != nil {
		return err
	}
	if err := a.start(); err != nil {
		return err
	}
	m.opts.Bus.Info(events.SiteStarted, siteID, "%s started", a.config().Name+a.slotSuffix())
	return nil
}

// StopSlot stops a slot's instances, draining them first.
func (m *Manager) StopSlot(siteID, slot string) error {
	a, err := m.slotApp(siteID, slot)
	if err != nil {
		return err
	}
	a.stop()
	m.opts.Bus.Info(events.SiteStopped, siteID, "%s stopped", a.config().Name+a.slotSuffix())
	return nil
}

// RecycleSlot is a zero-downtime rolling restart of a slot; a stopped or
// failed slot is started instead.
func (m *Manager) RecycleSlot(siteID, slot, reason string) error {
	a, err := m.slotApp(siteID, slot)
	if err != nil {
		return err
	}
	a.mu.Lock()
	running := a.running
	a.mu.Unlock()
	if !running {
		return a.start()
	}
	return a.recycle(reason)
}

// PrepareSwap is the first phase of a swap: the slot's instances are
// given prep (production's configuration running the slot's release, see
// model.SwapSite), which restarts them with a rolling recycle when it
// differs from what they run, or they are started if the slot is stopped;
// then it waits until every one of them is ready. Until CommitSwap or
// CancelSwap the site's slots are not reconfigured by Apply. It reports
// whether the slot was running before.
func (m *Manager) PrepareSwap(ctx context.Context, prep *model.Site, slot string) (wasRunning bool, err error) {
	key := model.SlotKey(prep.ID, slot)
	m.mu.Lock()
	a := m.apps[key]
	if a == nil || slot == "" {
		m.mu.Unlock()
		return false, ErrNoSlot
	}
	if m.swapping[prep.ID] {
		m.mu.Unlock()
		return false, errors.New("a swap is already in progress for this site")
	}
	if m.swapping == nil {
		m.swapping = map[string]bool{}
	}
	m.swapping[prep.ID] = true
	m.mu.Unlock()

	a.mu.Lock()
	wasRunning = a.running
	a.mu.Unlock()
	if wasRunning {
		a.logs.System("swap: restarting with production's settings")
		if err := a.reconfigure(prep, true); err != nil {
			return true, fmt.Errorf("the slot's instances could not be restarted with production's settings: %w", err)
		}
	} else {
		a.logs.System("swap: starting the slot with production's settings")
		a.reconfigure(prep, false) // not running: only stores it
		if err := a.start(); err != nil {
			return false, fmt.Errorf("the slot could not be started: %w", err)
		}
	}
	n := prep.Node
	timeout := time.Duration(n.StartupTimeoutSec+n.ShutdownTimeoutSec)*time.Second + 30*time.Second
	return wasRunning, a.waitRunning(ctx, n.Instances, timeout)
}

// waitRunning waits until the app runs n instances, all ready and
// healthy.
func (a *App) waitRunning(ctx context.Context, n int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		st := a.status()
		ready := 0
		for _, in := range st.Instances {
			if in.State == "ready" && in.Healthy {
				ready++
			}
		}
		switch {
		case st.State == model.StateRunning && len(st.Instances) == n && ready == n:
			return nil
		case st.State == model.StateFailed:
			return fmt.Errorf("the slot failed: %s", st.Message)
		case st.State == model.StateStopped:
			return errors.New("the slot was stopped")
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("only %d of %d instances were ready after %s", ready, n, timeout)
			}
			return ctx.Err()
		case <-t.C:
		}
	}
}

// CancelSwap ends a swap that did not happen: the slot goes back to its
// own configuration (a rolling recycle when it differs) and is stopped
// again if PrepareSwap started it.
func (m *Manager) CancelSwap(site *model.Site, slot string, stop bool) {
	m.mu.Lock()
	delete(m.swapping, site.ID)
	m.mu.Unlock()
	if stop {
		if a := m.app(model.SlotKey(site.ID, slot)); a != nil {
			a.stop()
		}
	}
	m.applySlots(site)
}

// CommitSwap moves the slot's app into production and production's app
// into the slot, atomically for the proxy. The caller then applies the
// site's new configuration (Apply): production's is what the slot was
// prepared with, so its instances carry on; the old production instances
// are recycled onto the slot's settings.
func (m *Manager) CommitSwap(siteID, slot string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	pk, sk := siteID, model.SlotKey(siteID, slot)
	prod, stg := m.apps[pk], m.apps[sk]
	if prod == nil || stg == nil || slot == "" {
		return ErrNoSlot
	}
	delete(m.swapping, siteID)
	m.apps[pk], m.apps[sk] = stg, prod
	stg.setSlot("")
	prod.setSlot(slot)
	return nil
}

// WarmUp requests the slot's warm-up paths on each of its instances (see
// warmUp). A slot that serves no HTTP (a worker) has nothing to warm up.
// host and proto are what production's binding would forward.
func (m *Manager) WarmUp(ctx context.Context, siteID, slot string, w model.WarmupConfig, host, proto string, logf func(format string, a ...any)) error {
	a, err := m.slotApp(siteID, slot)
	if err != nil {
		return err
	}
	if a.isWorker(a.config()) {
		logf("warm-up skipped: the slot serves no HTTP")
		return nil
	}
	backends := m.Backends(model.SlotKey(siteID, slot))
	if len(backends) == 0 {
		return errors.New("the slot has no ready instance to warm up")
	}
	ranges, err := model.ParseStatusRanges(w.Statuses)
	if err != nil {
		return err
	}
	addrs := make([]string, len(backends))
	for i, b := range backends {
		addrs[i] = b.Addr
	}
	return warmUp(ctx, warmupClient, warmup{
		addrs: addrs, paths: w.Paths, accept: ranges, host: host, proto: proto,
		timeout: time.Duration(w.TimeoutSec) * time.Second, logf: logf,
	})
}

// warmupClient never follows redirects: a redirect is an answer.
var warmupClient = &http.Client{
	Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 1 << 20},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}
