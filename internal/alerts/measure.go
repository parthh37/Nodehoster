package alerts

import (
	"fmt"
	"math"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// SiteSample is what one evaluation knows about a site: its live status
// (Status.Traffic counts since the site's counters were created) and its
// cumulative response time histogram. A deployment slot is sampled as
// the configuration it runs (model.SlotSite: Site.Slot set), and alerts
// on it are the site's, apart from those on production.
type SiteSample struct {
	Site    *model.Site
	Status  model.SiteStatus
	Latency []int64 // per Options.LatencyBounds bucket, then slower
	// Hold: the instances are being replaced on purpose (a swap prepares
	// the slot): nothing is judged, what is pending or firing stays.
	Hold bool
}

// ServerSample is what one evaluation knows about the machine. A negative
// percentage is unknown.
type ServerSample struct {
	CPUPercent    float64
	MemoryPercent float64
	Disks         []Disk
}

// Disk is the free space of a drive holding the data directory or a site.
type Disk struct {
	Path        string // the drive (C:\) or mount
	FreePercent float64
}

// Reading is a measured value and what it is about ("instance 2", "D:\").
type Reading struct {
	Value  float64
	Detail string
}

// ---- traffic windows

// counters are a site's cumulative traffic counters at one evaluation.
type counters struct {
	at       time.Time
	requests int64
	errors   int64   // 5xx
	latMs    float64 // total response time
	hist     []int64
}

// window keeps a site's counters for the longest rate window, so that a
// rate over the last N minutes is the difference of two readings.
type window struct {
	buf []counters // oldest first
}

// keepWindow is how much history a window keeps: the longest window a
// rule may ask for, and a little.
const keepWindow = model.MaxAlertWindowMinutes*time.Minute + time.Minute

// maxWindowEntries bounds a window whatever the evaluation interval.
const maxWindowEntries = 512

func sampleCounters(now time.Time, in SiteSample) counters {
	t := in.Status.Traffic
	return counters{
		at: now, requests: t.Requests, errors: t.Status5xx,
		latMs: t.AvgLatencyMs * float64(t.Requests), hist: in.Latency,
	}
}

func (w *window) add(c counters) {
	if n := len(w.buf); n > 0 {
		last := w.buf[n-1]
		if c.requests < last.requests || c.errors < last.errors {
			w.buf = w.buf[:0] // the counters started over (the site was recreated)
		}
	}
	w.buf = append(w.buf, c)
	drop := 0
	for drop < len(w.buf)-1 && (c.at.Sub(w.buf[drop].at) > keepWindow || len(w.buf)-drop > maxWindowEntries) {
		drop++
	}
	if drop > 0 {
		w.buf = append(w.buf[:0], w.buf[drop:]...)
	}
}

// delta is the traffic over the last d: the latest counters minus the
// newest ones at least d old, or the oldest there are (just after a
// start, a window is shorter than asked).
func (w *window) delta(d time.Duration) (counters, bool) {
	n := len(w.buf)
	if n < 2 {
		return counters{}, false
	}
	last := w.buf[n-1]
	base := w.buf[0]
	for i := n - 2; i >= 0; i-- {
		if last.at.Sub(w.buf[i].at) >= d {
			base = w.buf[i]
			break
		}
	}
	out := counters{
		at: base.at, requests: last.requests - base.requests, errors: last.errors - base.errors,
		latMs: last.latMs - base.latMs,
	}
	if len(last.hist) == len(base.hist) {
		out.hist = make([]int64, len(last.hist))
		for i := range last.hist {
			out.hist[i] = last.hist[i] - base.hist[i]
		}
	}
	return out, true
}

// Percentile estimates the p-th percentile (0 < p <= 1) of a histogram:
// counts[i] values at most bounds[i] (and above bounds[i-1]), the last
// count above every bound. Values are assumed spread evenly within a
// bucket; one in the last bucket is reported as its lower bound.
func Percentile(bounds []float64, counts []int64, p float64) (float64, bool) {
	var total int64
	for _, c := range counts {
		total += c
	}
	if total == 0 || len(bounds) == 0 {
		return 0, false
	}
	rank := p * float64(total)
	var cum float64
	for i, c := range counts {
		if c <= 0 {
			continue
		}
		if cum+float64(c) >= rank {
			lo := 0.0
			if i > 0 && len(bounds) > 0 {
				lo = bounds[min(i, len(bounds))-1]
			}
			if i >= len(bounds) {
				return lo, true
			}
			return lo + (bounds[i]-lo)*(rank-cum)/float64(c), true
		}
		cum += float64(c)
	}
	return bounds[len(bounds)-1], true
}

// ---- measuring

// past compares a value with a rule's threshold.
func past(metric string, v, threshold float64) bool {
	if model.AlertMetrics[metric].Below {
		return v < threshold
	}
	return v > threshold
}

// worse reports whether a is a worse reading than b for the metric.
func worse(metric string, a, b float64) bool {
	if model.AlertMetrics[metric].Below {
		return a < b
	}
	return a > b
}

func observe(r model.AlertRule, rd Reading) (Observation, Reading) {
	if past(r.Metric, rd.Value, r.Threshold) {
		return Breach, rd
	}
	return Clear, rd
}

// measureSite evaluates a site rule. The site's state decides first:
// nothing runs in a stopped site, so it is clear (and a firing alert
// resolves); a starting site has nothing to measure yet; a failed site
// (rapid-fail protection gave up) only has instances down.
func measureSite(r model.AlertRule, in SiteSample, w *window, bounds []float64) (Observation, Reading) {
	switch in.Status.State {
	case model.StateStopped, model.StateStopping:
		return Clear, Reading{Detail: "site stopped"}
	case model.StateStarting:
		return NoData, Reading{}
	case model.StateFailed:
		if r.Metric != model.AlertInstancesDown {
			return Clear, Reading{Detail: "site failed"}
		}
	}
	inst := in.Status.Instances
	switch r.Metric {
	case model.AlertCPU:
		var sum float64
		for _, i := range inst {
			sum += i.CPUPercent
		}
		return observe(r, Reading{Value: sum})
	case model.AlertMemory:
		var sum uint64
		for _, i := range inst {
			sum += i.MemoryBytes
		}
		return observe(r, Reading{Value: float64(sum) / (1 << 20)})
	case model.AlertInstanceCPU, model.AlertEventLoopLag, model.AlertMemoryPercent:
		limit := 0
		if in.Site.Node != nil {
			limit = in.Site.Node.MemoryLimitMB()
		}
		if r.Metric == model.AlertMemoryPercent && limit <= 0 {
			return NoData, Reading{}
		}
		var best *model.InstanceStatus
		var bestV float64
		for k := range inst {
			i := &inst[k]
			if i.PID == 0 || (i.State != "ready" && i.State != "unhealthy") {
				continue
			}
			v := i.CPUPercent
			switch r.Metric {
			case model.AlertEventLoopLag:
				v = i.EventLoopLagMs
			case model.AlertMemoryPercent:
				v = float64(i.MemoryBytes) / (1 << 20) / float64(limit) * 100
			}
			if best == nil || v > bestV {
				best, bestV = i, v
			}
		}
		if best == nil {
			return NoData, Reading{}
		}
		rd := Reading{Value: bestV, Detail: fmt.Sprintf("instance %d", best.Index)}
		if r.Metric == model.AlertMemoryPercent {
			rd.Detail = fmt.Sprintf("instance %d: %d of %d MB", best.Index, best.MemoryBytes>>20, limit)
		}
		return observe(r, rd)
	case model.AlertInstancesDown:
		expected := 1
		if in.Site.Node != nil && in.Site.Node.Instances > 1 {
			expected = in.Site.Node.Instances
		}
		healthy := 0
		for _, i := range inst {
			if i.State == "ready" && i.Healthy {
				healthy++
			}
		}
		down := max(expected-healthy, 0)
		return observe(r, Reading{Value: float64(down), Detail: fmt.Sprintf("of %d", expected)})
	case model.AlertErrorRate, model.AlertLatency, model.AlertLatencyP95:
		d, ok := w.delta(time.Duration(r.WindowMinutes) * time.Minute)
		if !ok || d.requests <= 0 {
			return Clear, Reading{Detail: "no requests"}
		}
		span := fmt.Sprintf("in %s", minutes(time.Duration(r.WindowMinutes)*time.Minute))
		var rd Reading
		switch r.Metric {
		case model.AlertErrorRate:
			rd = Reading{Value: float64(d.errors) / float64(d.requests) * 100, Detail: fmt.Sprintf("%d of %d requests %s", d.errors, d.requests, span)}
		case model.AlertLatency:
			rd = Reading{Value: d.latMs / float64(d.requests), Detail: fmt.Sprintf("%d requests %s", d.requests, span)}
		default:
			p, ok := Percentile(bounds, d.hist, 0.95)
			if !ok {
				return Clear, Reading{Detail: "no requests"}
			}
			rd = Reading{Value: p, Detail: fmt.Sprintf("%d requests %s", d.requests, span)}
		}
		if d.requests < int64(r.MinRequests) {
			// Too few requests to judge: one failure out of one request is
			// not a 100% error rate.
			rd.Detail = "only " + rd.Detail
			return Clear, rd
		}
		return observe(r, rd)
	}
	return NoData, Reading{}
}

// measureServer evaluates a server rule.
func measureServer(r model.AlertRule, in ServerSample) (Observation, Reading) {
	switch r.Metric {
	case model.AlertServerCPU:
		if in.CPUPercent < 0 {
			return NoData, Reading{}
		}
		return observe(r, Reading{Value: in.CPUPercent})
	case model.AlertServerMemory:
		if in.MemoryPercent < 0 {
			return NoData, Reading{}
		}
		return observe(r, Reading{Value: in.MemoryPercent})
	case model.AlertDiskFree:
		var worst *Disk
		for k := range in.Disks {
			if d := &in.Disks[k]; worst == nil || d.FreePercent < worst.FreePercent {
				worst = d
			}
		}
		if worst == nil {
			return NoData, Reading{}
		}
		return observe(r, Reading{Value: worst.FreePercent, Detail: worst.Path})
	}
	return NoData, Reading{}
}

// round keeps readings stored and shown to a sensible precision.
func round(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*10) / 10
}
