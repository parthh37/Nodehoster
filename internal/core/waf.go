package core

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/waf"
)

// maxWAFEvents caps the stored firewall events whatever the retention:
// an attack can produce them far faster than anyone reads them.
const maxWAFEvents = 100_000

// openWAF prepares the firewall's event table and recorder.
func (c *Core) openWAF(ctx context.Context) error {
	if err := c.Store.InitWAF(ctx); err != nil {
		return err
	}
	c.WAF = waf.NewRecorder(waf.RecorderOptions{
		Log:  c.Log,
		Save: c.Store.AddWAFEvents,
		OnBlock: func(ev model.WAFEvent, msg string) {
			c.Bus.Warn(events.SecurityWAF, ev.SiteID, "%s", msg)
		},
	})
	return nil
}

// wafLoop saves firewall events as they come and prunes old ones hourly.
// At shutdown it saves what is queued before returning.
func (c *Core) wafLoop(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		c.WAF.Run(ctx)
		close(done)
	}()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		days := c.Settings().WAF.EventRetentionDays
		if days <= 0 {
			days = model.DefaultWAF().EventRetentionDays
		}
		if err := c.Store.PruneWAFEvents(ctx, time.Now().AddDate(0, 0, -days), maxWAFEvents); err != nil && ctx.Err() == nil {
			c.Log.Warn("prune firewall events", "err", err)
		}
		select {
		case <-ctx.Done():
			<-done
			return
		case <-t.C:
		}
	}
}

// applyWAFDefaults gives a site created without a firewall mode the
// server's defaults for new sites.
func (c *Core) applyWAFDefaults(in *model.Site) {
	w := &in.Routing.WAF
	if w.Mode != "" {
		return
	}
	d := c.Settings().WAF.ForNewSite(in.Type)
	w.Mode = d.Mode
	if w.ParanoiaLevel == 0 {
		w.ParanoiaLevel = d.ParanoiaLevel
	}
	if w.AnomalyThreshold == 0 {
		w.AnomalyThreshold = d.AnomalyThreshold
	}
}

// forgetWAF drops a deleted site's firewall counters and events.
func (c *Core) forgetWAF(id string) {
	c.WAF.Forget(id)
	if err := c.Store.DeleteWAFEvents(context.Background(), id); err != nil {
		c.Log.Warn("delete firewall events", "site", id, "err", err)
	}
}

// WAFEvents lists firewall events.
func (c *Core) WAFEvents(ctx context.Context, q model.WAFEventQuery) ([]model.WAFEvent, error) {
	return c.Store.ListWAFEvents(ctx, q)
}

// SetSiteWAF replaces a site's firewall configuration, leaving the rest of
// the site as it is stored (read and written under the lock, so an edit
// saved meanwhile is not lost).
func (c *Core) SetSiteWAF(ctx context.Context, id string, cfg model.WAFConfig) (*model.Site, error) {
	c.sitesMu.Lock()
	defer c.sitesMu.Unlock()
	existing, err := c.Site(id)
	if err != nil {
		return nil, err
	}
	s := Masked(existing) // masked secrets keep their stored values
	s.Routing.WAF = cfg
	return c.updateSite(ctx, id, s)
}

// ErrExclusionExists is returned by AddWAFExclusion for an exclusion the
// site already has.
var ErrExclusionExists = fmt.Errorf("the site already has this exclusion")

// AddWAFExclusion adds an exclusion to a site's firewall, as the console's
// "exclude" shortcut on an event does.
func (c *Core) AddWAFExclusion(ctx context.Context, id string, x model.WAFExclusion) (*model.Site, error) {
	c.sitesMu.Lock()
	defer c.sitesMu.Unlock()
	existing, err := c.Site(id)
	if err != nil {
		return nil, err
	}
	s := Masked(existing)
	if slices.ContainsFunc(s.Routing.WAF.Exclusions, func(o model.WAFExclusion) bool { return reflect.DeepEqual(o, x) }) {
		return nil, ErrExclusionExists
	}
	s.Routing.WAF.Exclusions = append(s.Routing.WAF.Exclusions, x)
	return c.updateSite(ctx, id, s)
}
