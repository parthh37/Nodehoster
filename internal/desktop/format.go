// Package desktop holds the presentation logic of the desktop manager and
// the notification-area icon: everything that is not Win32 plumbing, so it
// can be tested on any OS.
package desktop

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// BindingText renders a binding the way IIS Manager lists them:
// "https 10.0.0.5:443 shop.example.com", with * for all addresses.
func BindingText(b model.Binding) string {
	ip := b.IP
	if ip == "" {
		ip = "*"
	}
	s := b.Protocol + " " + net.JoinHostPort(ip, strconv.Itoa(b.Port))
	if b.Host != "" {
		s += " " + b.Host
	}
	return s
}

// BindingsText joins a site's bindings for a list column.
func BindingsText(bs []model.Binding) string {
	parts := make([]string, len(bs))
	for i, b := range bs {
		parts[i] = BindingText(b)
	}
	return strings.Join(parts, ", ")
}

// BrowseURL is the address that opens a binding in a browser from this
// machine. Wildcard host names and "all addresses" become localhost.
func BrowseURL(b model.Binding) string {
	host := b.Host
	if host == "" || strings.HasPrefix(host, "*") {
		host = b.IP
		if host == "" || host == "*" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
	}
	u := b.Protocol + "://" + host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		u = b.Protocol + "://[" + host + "]"
	}
	if (b.Protocol == "http" && b.Port != 80) || (b.Protocol == "https" && b.Port != 443) {
		u += ":" + strconv.Itoa(b.Port)
	}
	return u + "/"
}

// StateText capitalizes a site state for display.
func StateText(s model.SiteState) string {
	if s == "" {
		return "Unknown"
	}
	return strings.ToUpper(string(s[:1])) + string(s[1:])
}

// InstancesText is "ready/configured" for Node.js sites, "–" otherwise.
func InstancesText(site *model.Site, st model.SiteStatus) string {
	if !site.RunsNode() || site.Node == nil {
		return "–"
	}
	ready := 0
	for _, in := range st.Instances {
		if in.State == "ready" {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, site.Node.Instances)
}

// Bytes formats a size with a binary unit: 512 B, 1.5 KB, 612 MB.
func Bytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	if v >= 100 {
		return fmt.Sprintf("%.0f %cB", v, "KMGTPE"[exp])
	}
	return fmt.Sprintf("%.1f %cB", v, "KMGTPE"[exp])
}

// Uptime formats how long ago t was: "3d 4h", "2h 5m", "45s".
func Uptime(since, now time.Time) string {
	if since.IsZero() {
		return "–"
	}
	d := now.Sub(since)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

// SiteUsage sums CPU and memory over a site's instances.
func SiteUsage(st model.SiteStatus) (cpu float64, mem uint64) {
	for _, in := range st.Instances {
		cpu += in.CPUPercent
		mem += in.MemoryBytes
	}
	return cpu, mem
}

// ConsoleURL turns the web console's listen URL into one a browser on this
// machine can open: "https://0.0.0.0:8484" becomes "https://localhost:8484/".
func ConsoleURL(listenURL string) string {
	scheme, hostport, ok := strings.Cut(listenURL, "://")
	if !ok || (scheme != "http" && scheme != "https") {
		// It is opened with the shell: never anything but a web page.
		return ""
	}
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "localhost"
	}
	return scheme + "://" + net.JoinHostPort(host, port) + "/"
}

// SiteLevel is the badge color for a site in the manager's tree: green
// when serving, amber when partly, red when NodeHoster gave up, grey when
// stopped.
func SiteLevel(s model.SiteState) Level {
	switch s {
	case model.StateRunning:
		return LevelOK
	case model.StateDegraded, model.StateStarting, model.StateStopping:
		return LevelWarning
	case model.StateFailed:
		return LevelDown
	}
	return LevelNotInstalled
}

// TaskRunText summarizes a scheduled task's run for a list column:
// "Succeeded 14:03 (12s)", "Failed 2026-05-01 03:00 (exit code 2)",
// "Running since 14:03".
func TaskRunText(r *model.TaskRun, now time.Time) string {
	if r == nil {
		return "Never run"
	}
	when := r.StartedAt.Local()
	stamp := when.Format("2006-01-02 15:04")
	if y, m, d := when.Date(); y == now.Local().Year() && m == now.Local().Month() && d == now.Local().Day() {
		stamp = when.Format("15:04")
	}
	if r.Status == model.RunRunning {
		return "Running since " + stamp
	}
	status := strings.ToUpper(r.Status[:1]) + r.Status[1:]
	detail := ""
	switch {
	case r.Status == model.RunFailed && r.ExitCode != nil:
		detail = fmt.Sprintf("exit code %d", *r.ExitCode)
	case r.Status == model.RunFailed || r.Status == model.RunSkipped:
		detail = r.Error
	case r.FinishedAt != nil:
		detail = r.FinishedAt.Sub(r.StartedAt).Round(time.Second).String()
	}
	if detail != "" {
		return fmt.Sprintf("%s %s (%s)", status, stamp, detail)
	}
	return status + " " + stamp
}

// ScheduleText is a task's schedule for a list column.
func ScheduleText(t model.ScheduledTask) string {
	switch {
	case t.Schedule == "":
		return "On demand"
	case !t.Enabled:
		return t.Schedule + " (disabled)"
	}
	return t.Schedule
}
