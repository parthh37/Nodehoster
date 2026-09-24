package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/alerts"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/proxy"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

// alertInterval is how often the alert rules are evaluated. The process
// manager samples CPU and memory every 5 s; traffic counters are live.
const alertInterval = 15 * time.Second

func (c *Core) openAlerts(ctx context.Context) error {
	c.Alerts = alerts.New(alerts.Options{
		Store: c.Store, Log: c.Log, Deliver: c.deliverAlerts, LatencyBounds: proxy.LatencyBounds,
	})
	return c.Alerts.Load(ctx, time.Now())
}

// alertLoop evaluates the alert rules until the server stops, and prunes
// their history hourly (resolved alerts are kept as long as events).
func (c *Core) alertLoop(ctx context.Context) {
	t := time.NewTicker(alertInterval)
	defer t.Stop()
	var meter cpuMeter
	var pruned time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s := c.Settings()
			cfg := s.Alerts
			var sites []alerts.SiteSample
			server := alerts.ServerSample{CPUPercent: -1, MemoryPercent: -1}
			if cfg.Enabled {
				sites = c.alertSamples()
				if len(cfg.ServerRules) > 0 {
					server = c.serverSample(&meter)
				}
			}
			// Not cancelled by Shutdown halfway through: the store is
			// closed only after this loop has returned.
			c.Alerts.Evaluate(context.WithoutCancel(ctx), now, cfg, sites, server)
			if now.Sub(pruned) >= time.Hour {
				pruned = now
				days := s.LogRetentionDays
				if days <= 0 {
					days = 30
				}
				if err := c.Alerts.Prune(ctx, now, time.Duration(days)*24*time.Hour); err != nil {
					c.Log.Warn("prune alert history", "err", err)
				}
			}
		}
	}
}

// alertSamples is every site's live status and response time histogram,
// and those of their deployment slots. Previews have no alerts: they are
// temporary, and a pull request that does not start is no incident.
func (c *Core) alertSamples() []alerts.SiteSample {
	sites := c.Sites()
	out := make([]alerts.SiteSample, 0, len(sites))
	for _, s := range sites {
		if s.IsPreview() {
			continue
		}
		out = append(out, alerts.SiteSample{Site: s, Status: c.Status(s), Latency: c.Proxy.LatencyHistogram(s.ID)})
		out = append(out, c.slotSamples(s)...)
	}
	return out
}

// slotSamples are a site's deployment slots, each as the configuration
// it runs. A stopped slot measures as a stopped site (no alerts); the
// slot a swap is preparing is held, its instances being replaced on
// purpose.
func (c *Core) slotSamples(s *model.Site) []alerts.SiteSample {
	if len(s.Slots) == 0 {
		return nil
	}
	swapping := ""
	c.slots.mu.Lock()
	if p := c.slots.active[s.ID]; p != nil {
		swapping = p.Slot
	}
	c.slots.mu.Unlock()
	var out []alerts.SiteSample
	for _, sl := range s.Slots {
		v := model.SlotSite(s, sl.Name)
		if v == nil {
			continue
		}
		key := model.SlotKey(s.ID, sl.Name)
		st, ok := c.Procs.Status(key)
		if !ok || sl.ActiveRelease == "" {
			st = model.SiteStatus{State: model.StateStopped}
		}
		st.SiteID = s.ID
		st.Traffic = c.Proxy.Traffic(key)
		out = append(out, alerts.SiteSample{Site: v, Status: st, Latency: c.Proxy.LatencyHistogram(key), Hold: sl.Name == swapping})
	}
	return out
}

func (c *Core) serverSample(meter *cpuMeter) alerts.ServerSample {
	out := alerts.ServerSample{CPUPercent: meter.percent(), MemoryPercent: -1, Disks: c.alertDisks()}
	if v, err := mem.VirtualMemory(); err == nil && v.Total > 0 {
		out.MemoryPercent = v.UsedPercent
	}
	return out
}

// cpuMeter measures machine CPU between two calls. It keeps its own
// previous reading rather than using cpu.Percent(0), whose shared state
// the minute metrics also use.
type cpuMeter struct {
	last cpu.TimesStat
	ok   bool
}

func (m *cpuMeter) percent() float64 {
	t, err := cpu.Times(false)
	if err != nil || len(t) == 0 {
		return -1
	}
	cur, prev, had := t[0], m.last, m.ok
	m.last, m.ok = cur, true
	if !had {
		return -1 // the first reading only sets the baseline
	}
	total := cur.Total() - prev.Total()
	if total <= 0 {
		return -1
	}
	idle := (cur.Idle + cur.Iowait) - (prev.Idle + prev.Iowait)
	return min(max((total-idle)/total*100, 0), 100)
}

// alertDisks is the free space of each drive holding the data directory,
// the sites folder or a site's own folder.
func (c *Core) alertDisks() []alerts.Disk {
	paths := []string{c.Paths.Data, c.Paths.Sites}
	for _, s := range c.Sites() {
		if s.Node != nil && filepath.IsAbs(s.Node.AppRoot) {
			paths = append(paths, s.Node.AppRoot)
		}
		if s.Static != nil && filepath.IsAbs(s.Static.Root) {
			paths = append(paths, s.Static.Root)
		}
	}
	var out []alerts.Disk
	seen := map[string]bool{}
	for _, p := range paths {
		// Windows: one reading per drive (C:, \\server\share). Elsewhere
		// paths have no volume, and readings of the same filesystem are
		// told apart by their size and type.
		vol := filepath.VolumeName(p)
		if vol != "" {
			vol = strings.ToUpper(vol) + string(os.PathSeparator)
			if seen[vol] {
				continue
			}
		}
		u, err := disk.Usage(p)
		if err != nil || u.Total == 0 {
			continue
		}
		key, name := vol, vol
		if key == "" {
			key, name = fmt.Sprintf("%d/%s", u.Total, u.Fstype), p
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, alerts.Disk{Path: name, FreePercent: float64(u.Free) / float64(u.Total) * 100})
	}
	return out
}

// deliverAlerts turns alert notifications into events (the event log,
// webhooks, the status icon, log shipping) and, when recipients are set,
// one e-mail per evaluation.
func (c *Core) deliverAlerts(batch []alerts.Notice) {
	for _, n := range batch {
		typ, level := events.AlertFiring, "warning"
		if n.Alert.Severity == model.SeverityCritical {
			level = "error"
		}
		if n.Kind == alerts.Resolve || (n.Kind == alerts.None && n.Alert.State == model.AlertResolved) {
			typ, level = events.AlertResolved, "info"
		}
		msg := n.Message
		if n.Alert.Slot != "" {
			msg = "slot " + n.Alert.Slot + ": " + msg // the event is the site's
		}
		c.Bus.Emit(level, typ, n.Alert.SiteID, msg)
	}
	if to := c.Settings().Alerts.EmailTo; len(to) > 0 {
		subject, body := c.alertMail(batch)
		if _, err := c.Mail.Notify(to, subject, body); err != nil {
			c.Log.Warn("queue alert e-mail", "err", err)
		}
	}
}

// alertMail is an evaluation's notifications as one message.
func (c *Core) alertMail(batch []alerts.Notice) (subject, body string) {
	host, _ := os.Hostname()
	line := func(n alerts.Notice) string {
		what := n.Alert.SiteName
		if what == "" {
			what = "Server " + host
		}
		switch {
		case n.Kind == alerts.None:
			return n.Message
		case n.Kind == alerts.Resolve:
			return fmt.Sprintf("%s: %s", what, n.Message)
		case n.Alert.Severity == model.SeverityCritical:
			return fmt.Sprintf("Critical: %s: %s", what, n.Message)
		}
		return fmt.Sprintf("Warning: %s: %s", what, n.Message)
	}
	var b strings.Builder
	if len(batch) == 1 {
		subject = "[NodeHoster] " + line(batch[0])
	} else {
		subject = fmt.Sprintf("[NodeHoster] %d alert notifications from %s", len(batch), host)
	}
	for _, n := range batch {
		b.WriteString(line(n) + "\n")
	}
	fmt.Fprintf(&b, "\nNodeHoster on %s", host)
	if c.AdminURL != "" {
		fmt.Fprintf(&b, "\nAlerts: %s/alerts", strings.TrimRight(c.AdminURL, "/"))
	}
	b.WriteString("\n")
	return subject, b.String()
}
