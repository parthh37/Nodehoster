package model

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// Resource alerts watch a metric and notify when it stays past a limit
// for a while, like an Azure Monitor metric alert or a Prometheus alerting
// rule with a "for" clause. Rules are server-wide defaults for every site
// (Settings.alerts.siteRules), overridden or added to per site
// (Site.alerts), plus rules on the machine itself (serverRules).

// Alert metrics. Site metrics are evaluated for each site a rule applies
// to; server metrics once, for the machine.
const (
	AlertCPU           = "cpu"           // site: CPU of all instances, % of one core (as the site's Overview shows it)
	AlertInstanceCPU   = "instanceCpu"   // site: the busiest instance's CPU, % of one core
	AlertMemory        = "memory"        // site: memory of all instances, MB
	AlertMemoryPercent = "memoryPercent" // site: the largest instance, % of its memory limit (sites with one)
	AlertEventLoopLag  = "eventLoopLag"  // site: the worst instance's event-loop lag, ms (NodeHoster agent on)
	AlertErrorRate     = "errorRate"     // site: 5xx answers, % of the requests in the window
	AlertLatency       = "latency"       // site: average response time over the window, ms
	AlertLatencyP95    = "latencyP95"    // site: 95th percentile response time over the window, ms
	AlertInstancesDown = "instancesDown" // site: configured instances not ready and healthy
	AlertServerCPU     = "serverCpu"     // server: machine CPU, %
	AlertServerMemory  = "serverMemory"  // server: machine memory in use, %
	AlertDiskFree      = "diskFree"      // server: free space on the emptiest drive holding the data directory or a site, %
)

// Alert severities.
const (
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Alert states.
const (
	AlertPending  = "pending" // the condition holds, not yet for long enough (API only; never stored)
	AlertFiring   = "firing"
	AlertResolved = "resolved"
)

// AlertMetricInfo describes a metric for validation and messages.
type AlertMetricInfo struct {
	Server   bool    // a server metric (serverRules), otherwise a site metric
	Below    bool    // fires when the value drops below the threshold (free disk space)
	Max      float64 // largest threshold accepted
	Node     bool    // only sites running Node.js processes (node, worker)
	HTTP     bool    // only sites that answer HTTP (not workers)
	Windowed bool    // a rate over windowMinutes, with minRequests
}

// AlertMetrics is the catalog of metrics rules can watch.
var AlertMetrics = map[string]AlertMetricInfo{
	AlertCPU:           {Max: 100000, Node: true},
	AlertInstanceCPU:   {Max: 10000, Node: true},
	AlertMemory:        {Max: 10 << 20, Node: true},
	AlertMemoryPercent: {Max: 1000, Node: true},
	AlertEventLoopLag:  {Max: 600000, Node: true},
	AlertErrorRate:     {Max: 100, HTTP: true, Windowed: true},
	AlertLatency:       {Max: 600000, HTTP: true, Windowed: true},
	AlertLatencyP95:    {Max: 600000, HTTP: true, Windowed: true},
	AlertInstancesDown: {Max: 1000, Node: true},
	AlertServerCPU:     {Server: true, Max: 100},
	AlertServerMemory:  {Server: true, Max: 100},
	AlertDiskFree:      {Server: true, Below: true, Max: 100},
}

// AlertRule fires when Metric stays past Threshold for ForMinutes.
type AlertRule struct {
	// ID names the rule, unique among the server's rules; a site rule
	// with the ID of a server-wide site rule replaces it for that site.
	ID         string  `json:"id"`
	Metric     string  `json:"metric"`
	Threshold  float64 `json:"threshold"`
	ForMinutes int     `json:"forMinutes"` // 0 = fire on the first evaluation past the limit
	Severity   string  `json:"severity"`   // warning | critical
	// WindowMinutes and MinRequests apply to the rate metrics (errorRate,
	// latency, latencyP95): the rate is taken over the last WindowMinutes,
	// and a window with fewer than MinRequests requests counts as within
	// the limit (one failure out of one request is not a 100% error rate).
	WindowMinutes int `json:"windowMinutes,omitempty"`
	MinRequests   int `json:"minRequests,omitempty"`
	// RepeatHours sends a reminder every so many hours while the alert
	// fires; 0 notifies once.
	RepeatHours int `json:"repeatHours,omitempty"`
	// Disabled turns the rule off; on a site, it turns off the server-wide
	// rule of the same ID for that site.
	Disabled bool `json:"disabled,omitempty"`
}

// AlertSettings are the server-wide alert rules and delivery.
type AlertSettings struct {
	Enabled     bool        `json:"enabled"`
	SiteRules   []AlertRule `json:"siteRules"`   // evaluated for every site (sites override them by ID)
	ServerRules []AlertRule `json:"serverRules"` // evaluated for the machine
	// RecoveryMinutes is how long a firing alert's condition must stay
	// clear before it resolves, so a value hovering at the limit does not
	// fire and resolve over and over.
	RecoveryMinutes int `json:"recoveryMinutes"`
	// EmailTo receives alerts by mail through the built-in SMTP server's
	// queue (delivered directly or through its smart host).
	EmailTo []string `json:"emailTo"`
}

// SiteAlerts is a site's part in alerting.
type SiteAlerts struct {
	Disabled bool        `json:"disabled,omitempty"` // no site alerts for this site at all
	Rules    []AlertRule `json:"rules,omitempty"`    // overrides (same ID as a server-wide site rule) and additions
}

// Alert is one occurrence of a rule's condition, from the moment it fired
// until it resolved.
type Alert struct {
	ID         string  `json:"id"`
	RuleID     string  `json:"ruleId"`
	SiteID     string  `json:"siteId,omitempty"`   // "" for server alerts
	SiteName   string  `json:"siteName,omitempty"` // as it was named then
	Metric     string  `json:"metric"`
	Severity   string  `json:"severity"`
	Threshold  float64 `json:"threshold"`
	ForMinutes int     `json:"forMinutes"`
	State      string  `json:"state"` // pending | firing | resolved
	Value      float64 `json:"value"` // the latest reading
	Peak       float64 `json:"peak"`  // the worst reading while active
	Detail     string  `json:"detail,omitempty"`
	Message    string  `json:"message"`
	// Since is when the condition started holding; FiredAt when it had
	// held for forMinutes.
	Since      time.Time  `json:"since"`
	FiredAt    *time.Time `json:"firedAt,omitempty"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	// ResolveNote says why an alert resolved other than by recovering:
	// its rule was removed or turned off, its site deleted, alerts off.
	ResolveNote string `json:"resolveNote,omitempty"`
	// Notified: the firing notification went out (it does not while the
	// alert is silenced); only then is its resolution notified too.
	Notified       bool          `json:"notified"`
	LastNotifiedAt *time.Time    `json:"lastNotifiedAt,omitempty"`
	Silence        *AlertSilence `json:"silence,omitempty"`
}

// AlertSilence keeps an alert quiet: no firing notification or reminders.
type AlertSilence struct {
	// Until ends the silence; nil = acknowledged, silent until the alert
	// resolves. A timed silence also covers the rule's next alerts on the
	// same site until it ends (a flapping condition stays quiet).
	Until *time.Time `json:"until,omitempty"`
	By    string     `json:"by"`
	At    time.Time  `json:"at"`
	Note  string     `json:"note,omitempty"`
}

// Active reports whether the silence is in force.
func (s *AlertSilence) Active(now time.Time) bool {
	return s != nil && (s.Until == nil || now.Before(*s.Until))
}

// AlertSilenceRequest is the body of POST /api/alerts/{id}/silence.
type AlertSilenceRequest struct {
	Minutes int    `json:"minutes"` // 0 = until the alert resolves (acknowledge)
	Note    string `json:"note"`
}

// AlertList is GET /api/alerts: the alerts in progress.
type AlertList struct {
	Enabled bool    `json:"enabled"`
	Firing  []Alert `json:"firing"`
	Pending []Alert `json:"pending"`
}

// DefaultAlerts is the configuration used until one is saved. Alerts are
// off: the rules are a starting point an administrator turns on.
func DefaultAlerts() AlertSettings {
	return AlertSettings{
		SiteRules: []AlertRule{
			{ID: "cpu", Metric: AlertInstanceCPU, Threshold: 90, ForMinutes: 10, Severity: SeverityWarning},
			{ID: "memory-limit", Metric: AlertMemoryPercent, Threshold: 90, ForMinutes: 5, Severity: SeverityWarning},
			{ID: "event-loop-lag", Metric: AlertEventLoopLag, Threshold: 250, ForMinutes: 5, Severity: SeverityWarning},
			{ID: "errors", Metric: AlertErrorRate, Threshold: 5, ForMinutes: 5, Severity: SeverityCritical, WindowMinutes: 5, MinRequests: 20},
			{ID: "latency-p95", Metric: AlertLatencyP95, Threshold: 2000, ForMinutes: 10, Severity: SeverityWarning, WindowMinutes: 5, MinRequests: 20},
			{ID: "instances-down", Metric: AlertInstancesDown, Threshold: 0, ForMinutes: 5, Severity: SeverityCritical},
		},
		ServerRules: []AlertRule{
			{ID: "server-cpu", Metric: AlertServerCPU, Threshold: 90, ForMinutes: 15, Severity: SeverityWarning},
			{ID: "server-memory", Metric: AlertServerMemory, Threshold: 90, ForMinutes: 10, Severity: SeverityWarning},
			{ID: "disk-free", Metric: AlertDiskFree, Threshold: 10, ForMinutes: 5, Severity: SeverityCritical},
		},
		RecoveryMinutes: 2,
		EmailTo:         []string{},
	}
}

// ApplyDefaults fills settings saved before alerts existed, and zero
// values that would make no sense.
func (s *AlertSettings) ApplyDefaults() {
	if reflect.ValueOf(*s).IsZero() {
		*s = DefaultAlerts()
		return
	}
	if s.SiteRules == nil {
		s.SiteRules = []AlertRule{}
	}
	if s.ServerRules == nil {
		s.ServerRules = []AlertRule{}
	}
	if s.EmailTo == nil {
		s.EmailTo = []string{}
	}
	if s.RecoveryMinutes <= 0 {
		s.RecoveryMinutes = 2
	}
	applyRuleDefaults(s.SiteRules, s.ServerRules, "")
	applyRuleDefaults(s.ServerRules, s.SiteRules, "")
}

// applyDefaults names a site's unnamed rules "site-<metric>": a site rule
// named like a server-wide one would replace it.
func (s *SiteAlerts) applyDefaults() { applyRuleDefaults(s.Rules, nil, "site-") }

// applyRuleDefaults names unnamed rules after their metric (cpu, cpu-2,
// ...; others holds IDs taken elsewhere) and fills a rule's defaults.
func applyRuleDefaults(rules, others []AlertRule, prefix string) {
	taken := map[string]bool{}
	for _, list := range [][]AlertRule{rules, others} {
		for _, r := range list {
			taken[r.ID] = true
		}
	}
	for i := range rules {
		r := &rules[i]
		r.ID = strings.TrimSpace(r.ID)
		if r.ID == "" && r.Metric != "" {
			base := prefix + strings.ToLower(r.Metric)
			id := base
			for n := 2; taken[id]; n++ {
				id = fmt.Sprintf("%s-%d", base, n)
			}
			r.ID, taken[id] = id, true
		}
		if r.Severity == "" {
			r.Severity = SeverityWarning
		}
		if AlertMetrics[r.Metric].Windowed {
			if r.WindowMinutes <= 0 {
				r.WindowMinutes = 5
			}
			if r.MinRequests <= 0 {
				r.MinRequests = 20
			}
		} else {
			r.WindowMinutes, r.MinRequests = 0, 0
		}
	}
}

var alertRuleIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,47}$`)

// MaxAlertWindowMinutes bounds a rate metric's window (the evaluator keeps
// that much traffic history per site).
const MaxAlertWindowMinutes = 30

// validate checks one rule; server says which list it is in.
func (r *AlertRule) validate(f string, server bool) error {
	if !alertRuleIDRe.MatchString(r.ID) {
		return verr(f+".id", "must be 1-48 letters, digits, '.', '_' or '-'")
	}
	info, ok := AlertMetrics[r.Metric]
	if !ok {
		return verr(f+".metric", "unknown metric %q", r.Metric)
	}
	if info.Server != server {
		if server {
			return verr(f+".metric", "%s is a site metric; add it to the site rules", r.Metric)
		}
		return verr(f+".metric", "%s is a server metric; add it to the server rules", r.Metric)
	}
	if r.Threshold < 0 || r.Threshold > info.Max {
		return verr(f+".threshold", "must be between 0 and %g", info.Max)
	}
	if r.ForMinutes < 0 || r.ForMinutes > 1440 {
		return verr(f+".forMinutes", "must be between 0 and 1440 minutes (a day)")
	}
	if r.Severity != SeverityWarning && r.Severity != SeverityCritical {
		return verr(f+".severity", "must be warning or critical")
	}
	if info.Windowed {
		if r.WindowMinutes < 1 || r.WindowMinutes > MaxAlertWindowMinutes {
			return verr(f+".windowMinutes", "must be between 1 and %d minutes", MaxAlertWindowMinutes)
		}
		if r.MinRequests > 1000000 {
			return verr(f+".minRequests", "must be at most 1000000")
		}
	}
	if r.RepeatHours < 0 || r.RepeatHours > 168 {
		return verr(f+".repeatHours", "must be between 0 (notify once) and 168 hours (a week)")
	}
	return nil
}

// Validate checks the settings after ApplyDefaults.
func (s *AlertSettings) Validate() error {
	if s.RecoveryMinutes > 60 {
		return verr("alerts.recoveryMinutes", "must be between 1 and 60 minutes")
	}
	ids := map[string]string{}
	for _, l := range []struct {
		f      string
		rules  []AlertRule
		server bool
	}{{"alerts.siteRules", s.SiteRules, false}, {"alerts.serverRules", s.ServerRules, true}} {
		if len(l.rules) > 100 {
			return verr(l.f, "at most 100 rules")
		}
		for i := range l.rules {
			f := fmt.Sprintf("%s[%d]", l.f, i)
			if err := l.rules[i].validate(f, l.server); err != nil {
				return err
			}
			key := strings.ToLower(l.rules[i].ID)
			if _, dup := ids[key]; dup {
				return verr(f+".id", "another rule is named %q", l.rules[i].ID)
			}
			ids[key] = f
		}
	}
	if len(s.EmailTo) > 20 {
		return verr("alerts.emailTo", "at most 20 recipients")
	}
	for i, a := range s.EmailTo {
		if !plausibleEmail(a) {
			return verr(fmt.Sprintf("alerts.emailTo[%d]", i), "not an e-mail address")
		}
	}
	return nil
}

// validate checks a site's rules. Only rules in force must suit the
// site's type: an override that turns a server-wide rule off may name any
// site metric.
func (s *SiteAlerts) validate(site *Site) error {
	if len(s.Rules) > 100 {
		return verr("alerts.rules", "at most 100 rules")
	}
	ids := map[string]bool{}
	for i := range s.Rules {
		r := &s.Rules[i]
		f := fmt.Sprintf("alerts.rules[%d]", i)
		if err := r.validate(f, false); err != nil {
			return err
		}
		if ids[strings.ToLower(r.ID)] {
			return verr(f+".id", "another rule is named %q", r.ID)
		}
		ids[strings.ToLower(r.ID)] = true
		if r.Disabled {
			continue
		}
		info := AlertMetrics[r.Metric]
		if info.Node && !site.RunsNode() {
			return verr(f+".metric", "%s applies to Node.js sites and workers only", r.Metric)
		}
		if info.HTTP && site.Type == SiteWorker {
			return verr(f+".metric", "%s applies to sites that answer HTTP requests, not workers", r.Metric)
		}
	}
	return nil
}

func plausibleEmail(a string) bool {
	local, domain, ok := strings.Cut(a, "@")
	return ok && local != "" && domain != "" && strings.Count(a, "@") == 1 && !strings.ContainsAny(a, " \t\r\n,;<>\"")
}

// EffectiveAlertRules are the rules evaluated for a site: the server-wide
// site rules, each replaced by the site's rule of the same ID if it has
// one, then the site's own rules; turned-off rules and rules that do not
// suit the site's type (event-loop lag on a static site, error rate on a
// worker) are left out. None when the site opted out.
func EffectiveAlertRules(defaults []AlertRule, s *Site) []AlertRule {
	if s.Alerts.Disabled {
		return nil
	}
	own := map[string]AlertRule{}
	for _, r := range s.Alerts.Rules {
		own[strings.ToLower(r.ID)] = r
	}
	var out []AlertRule
	add := func(r AlertRule) {
		if !r.Disabled && AlertRuleApplies(r, s) {
			out = append(out, r)
		}
	}
	seen := map[string]bool{}
	for _, d := range defaults {
		key := strings.ToLower(d.ID)
		seen[key] = true
		if o, ok := own[key]; ok {
			add(o)
		} else {
			add(d)
		}
	}
	for _, r := range s.Alerts.Rules {
		if !seen[strings.ToLower(r.ID)] {
			add(r)
		}
	}
	return out
}

// AlertRuleApplies reports whether a site metric can be measured for a
// site: Node.js metrics need Node.js processes, request metrics HTTP,
// memoryPercent a memory limit and eventLoopLag the NodeHoster agent.
func AlertRuleApplies(r AlertRule, s *Site) bool {
	info, ok := AlertMetrics[r.Metric]
	if !ok || info.Server {
		return false
	}
	if info.Node && (!s.RunsNode() || s.Node == nil) {
		return false
	}
	if info.HTTP && s.Type == SiteWorker {
		return false
	}
	switch r.Metric {
	case AlertMemoryPercent:
		return s.Node.MemoryLimitMB() > 0
	case AlertEventLoopLag:
		return s.Node.AgentEnabled
	}
	return true
}

// MemoryLimitMB is the memory an instance may use before NodeHoster acts:
// the smaller of the hard limit (the Job Object kills the process) and the
// recycle limit, whichever are set; 0 = none.
func (n *NodeConfig) MemoryLimitMB() int {
	limit := 0
	for _, v := range []int{n.Limits.MemoryLimitMB, n.Recycle.MemoryLimitMB} {
		if v > 0 && (limit == 0 || v < limit) {
			limit = v
		}
	}
	return limit
}
