package alerts

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// Messages name the metric and value the way the consoles show them, for
// example "CPU 93% (instance 1) for 10 min (limit 90%)". The site is not
// in the message: events carry it (webhooks show "[api.example.com] …",
// the status icon uses it as the notification's title).

// MetricName is a metric's name in messages, capitalized.
func MetricName(metric string) string {
	switch metric {
	case model.AlertCPU, model.AlertInstanceCPU:
		return "CPU"
	case model.AlertMemory, model.AlertMemoryPercent:
		return "Memory"
	case model.AlertEventLoopLag:
		return "Event-loop lag"
	case model.AlertErrorRate:
		return "5xx error rate"
	case model.AlertLatency:
		return "Average response time"
	case model.AlertLatencyP95:
		return "95th percentile response time"
	case model.AlertInstancesDown:
		return "Instances down"
	case model.AlertServerCPU:
		return "Server CPU"
	case model.AlertServerMemory:
		return "Server memory"
	case model.AlertDiskFree:
		return "Free disk space"
	}
	return metric
}

// FormatValue formats a metric's value with its unit.
func FormatValue(metric string, v float64) string {
	switch metric {
	case model.AlertMemory:
		return groupThousands(math.Round(v)) + " MB"
	case model.AlertEventLoopLag, model.AlertLatency, model.AlertLatencyP95:
		if v >= 1000 {
			return strconv.FormatFloat(math.Round(v/100)/10, 'f', -1, 64) + " s"
		}
		return strconv.FormatFloat(math.Round(v), 'f', -1, 64) + " ms"
	case model.AlertInstancesDown:
		return strconv.FormatFloat(math.Round(v), 'f', -1, 64)
	}
	// Percentages: whole numbers, one decimal below 10%.
	if v < 10 && v != math.Trunc(v) {
		return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) + "%"
	}
	return strconv.FormatFloat(math.Round(v), 'f', -1, 64) + "%"
}

func groupThousands(v float64) string {
	s := strconv.FormatFloat(v, 'f', 0, 64)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// reading describes an alert's value: "CPU 93% (instance 1)", "2 of 4
// instances down", "Free disk space 6% on D:\".
func reading(a *model.Alert, v float64) string {
	switch a.Metric {
	case model.AlertInstancesDown:
		// Detail is "of 4".
		return strings.TrimSpace(fmt.Sprintf("%s %s instances down", FormatValue(a.Metric, v), a.Detail))
	case model.AlertMemoryPercent:
		s := fmt.Sprintf("Memory %s of the limit", FormatValue(a.Metric, v))
		if a.Detail != "" {
			s += " (" + a.Detail + ")"
		}
		return s
	case model.AlertDiskFree:
		s := "Free disk space " + FormatValue(a.Metric, v)
		if a.Detail != "" {
			s += " on " + a.Detail
		}
		return s
	}
	s := MetricName(a.Metric) + " " + FormatValue(a.Metric, v)
	if a.Detail != "" {
		s += " (" + a.Detail + ")"
	}
	return s
}

// limit describes a rule's threshold: "(limit 90%)".
func limit(a *model.Alert) string {
	return "(limit " + FormatValue(a.Metric, a.Threshold) + ")"
}

// minutes formats a duration for messages: "45 s", "5 min", "2 h 10 min".
func minutes(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Round(time.Second)/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Round(time.Minute)/time.Minute))
	}
	d = d.Round(time.Minute)
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	if m == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

// firingMessage: "CPU 93% (instance 1) for 10 min (limit 90%)"; a rule
// that fires at once has no duration to tell yet.
func firingMessage(a *model.Alert, now time.Time) string {
	if now.Sub(a.Since) < time.Second {
		return fmt.Sprintf("%s %s", reading(a, a.Value), limit(a))
	}
	return fmt.Sprintf("%s for %s %s", reading(a, a.Value), minutes(now.Sub(a.Since)), limit(a))
}

// pendingMessage: "CPU 93% (instance 1) for 3 min (limit 90%); fires after 10 min".
func pendingMessage(a *model.Alert, now time.Time) string {
	return fmt.Sprintf("%s; fires after %s", firingMessage(a, now), minutes(time.Duration(a.ForMinutes)*time.Minute))
}

// reminderMessage: "Still firing: CPU 91% (instance 1) for 4 h (limit 90%)".
func reminderMessage(a *model.Alert, now time.Time) string {
	return "Still firing: " + firingMessage(a, now)
}

// resolvedMessage: "Resolved after 25 min: CPU 40% (instance 1) (limit 90%)",
// or with why it ended otherwise: "Resolved after 25 min (site deleted)".
func resolvedMessage(a *model.Alert, now time.Time) string {
	start := a.Since
	if a.FiredAt != nil {
		start = *a.FiredAt
	}
	head := "Resolved after " + minutes(now.Sub(start))
	if a.ResolveNote != "" {
		return fmt.Sprintf("%s (%s): %s was %s at worst %s", head, a.ResolveNote, MetricName(a.Metric), FormatValue(a.Metric, a.Peak), limit(a))
	}
	return fmt.Sprintf("%s: %s %s", head, reading(a, a.Value), limit(a))
}

// summaryMessage is the one notification standing for those left out of
// an evaluation with too many ("and 14 more alerts: …").
func summaryMessage(rest []Notice) string {
	var fired, resolved, reminded int
	var names []string
	for _, n := range rest {
		switch n.Kind {
		case Fire:
			fired++
		case Resolve:
			resolved++
		case Remind:
			reminded++
		}
		name := n.Alert.SiteName
		if name == "" {
			name = "server"
		}
		if len(names) < 5 && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	var parts []string
	for _, p := range []struct {
		n    int
		verb string
	}{{fired, "fired"}, {resolved, "resolved"}, {reminded, "still firing"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.verb))
		}
	}
	return fmt.Sprintf("%d more alert notifications (%s), for %s; see the Alerts page",
		len(rest), strings.Join(parts, ", "), strings.Join(names, ", ")+more(len(names) == 5))
}

func more(b bool) string {
	if b {
		return "…"
	}
	return ""
}
