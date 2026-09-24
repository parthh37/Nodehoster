package model

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Web application firewall modes, like Azure Application Gateway WAF's
// detection and prevention modes.
const (
	WAFOff    = "off"
	WAFDetect = "detect" // log what would be blocked, block nothing
	WAFBlock  = "block"
)

// WAF rule categories. A rule belongs to one; exclusions and the events
// list filter on them.
const (
	WAFProtocol = "protocol" // protocol violations and evasion encodings
	WAFScanner  = "scanner"  // vulnerability scanners
	WAFLFI      = "lfi"      // path traversal and local file inclusion
	WAFRFI      = "rfi"      // remote file inclusion
	WAFRCE      = "rce"      // OS command injection
	WAFPHP      = "php"      // PHP injection
	WAFJava     = "java"     // Java injection (Log4Shell, OGNL, deserialization)
	WAFNode     = "nodejs"   // Node.js / JavaScript injection, prototype pollution, template injection, SSRF
	WAFXSS      = "xss"      // cross-site scripting
	WAFSQLi     = "sqli"     // SQL and NoSQL injection
)

// WAFCategories lists the categories in the order the console shows them.
var WAFCategories = []string{WAFSQLi, WAFXSS, WAFLFI, WAFRFI, WAFRCE, WAFNode, WAFPHP, WAFJava, WAFScanner, WAFProtocol}

// WAF defaults: the OWASP Core Rule Set's inbound anomaly threshold (one
// critical match blocks) and paranoia level, and the body inspection limit
// of Azure's WAF.
const (
	DefaultWAFThreshold = 5
	DefaultWAFParanoia  = 1
	DefaultWAFBodyKB    = 128
	MaxWAFBodyKB        = 4096
)

// WAFConfig is a site's web application firewall: requests are inspected
// for SQL injection, cross-site scripting, path traversal and similar
// attacks before anything reaches the application. Each rule that matches
// adds its severity's score (critical 5, error 4, warning 3, notice 2) to
// the request's anomaly score, as in the OWASP Core Rule Set; a request
// whose score reaches the threshold is blocked (403) or, in detect mode,
// only logged.
//
// Sites saved before the firewall existed have no mode, which is off: an
// upgrade never starts blocking traffic. New sites take the server's
// defaults (Settings.WAF).
type WAFConfig struct {
	Mode string `json:"mode,omitempty"` // off | detect | block; "" = off
	// ParanoiaLevel 1-3: higher levels add rules that catch more evasive
	// attacks and also more legitimate requests. 0 = 1.
	ParanoiaLevel int `json:"paranoiaLevel,omitempty"`
	// AnomalyThreshold is the score that blocks. 0 = 5 (one critical match).
	AnomalyThreshold int `json:"anomalyThreshold,omitempty"`
	// InspectBodyKB is how much of a request body is buffered and
	// inspected; the rest streams to the application uninspected. 0 = 128.
	InspectBodyKB int            `json:"inspectBodyKB,omitempty"`
	Exclusions    []WAFExclusion `json:"exclusions,omitempty"`
}

// WAFExclusion stops rules from firing where they are known to be wrong,
// like ModSecurity's ctl:ruleRemoveById/ruleRemoveTargetById or Azure WAF
// exclusions. Under Path (a prefix; "" = the whole site):
//
//   - with Args, Cookies or Headers: those are not inspected, by the listed
//     rules and categories or, when none are listed, by any rule (a CMS
//     editor posting HTML in "content");
//   - otherwise the listed rules and categories are turned off;
//   - with nothing listed at all, the firewall is off under Path.
//
// Names are not case-sensitive; a trailing * matches a prefix ("post_*").
type WAFExclusion struct {
	Path       string   `json:"path,omitempty"`
	RuleIDs    []int    `json:"ruleIds,omitempty"`
	Categories []string `json:"categories,omitempty"`
	Args       []string `json:"args,omitempty"` // query string and form fields, JSON keys as dotted paths (post.body)
	Cookies    []string `json:"cookies,omitempty"`
	Headers    []string `json:"headers,omitempty"`
	Comment    string   `json:"comment,omitempty"`
}

// Enabled reports whether the firewall inspects requests.
func (w *WAFConfig) Enabled() bool { return w.Mode == WAFDetect || w.Mode == WAFBlock }

// Paranoia is the effective paranoia level.
func (w *WAFConfig) Paranoia() int {
	if w.ParanoiaLevel <= 0 {
		return DefaultWAFParanoia
	}
	return w.ParanoiaLevel
}

// Threshold is the effective anomaly threshold.
func (w *WAFConfig) Threshold() int {
	if w.AnomalyThreshold <= 0 {
		return DefaultWAFThreshold
	}
	return w.AnomalyThreshold
}

// BodyLimit is the effective body inspection limit in bytes.
func (w *WAFConfig) BodyLimit() int {
	if w.InspectBodyKB <= 0 {
		return DefaultWAFBodyKB << 10
	}
	return w.InspectBodyKB << 10
}

// Validate checks a site's firewall configuration. Rule IDs are checked
// against the rule catalog by waf.Validate, which calls this.
func (w *WAFConfig) Validate(siteType SiteType) error {
	switch w.Mode {
	case "", WAFOff:
	case WAFDetect, WAFBlock:
		if siteType == SiteWorker {
			return verr("routing.waf.mode", "a background worker does not serve HTTP; there is nothing to inspect")
		}
	default:
		return verr("routing.waf.mode", "must be off, detect or block")
	}
	if w.ParanoiaLevel < 0 || w.ParanoiaLevel > 3 {
		return verr("routing.waf.paranoiaLevel", "must be 1, 2 or 3")
	}
	if w.AnomalyThreshold < 0 || w.AnomalyThreshold > 1000 {
		return verr("routing.waf.anomalyThreshold", "must be between 1 and 1000")
	}
	if w.InspectBodyKB < 0 || w.InspectBodyKB > MaxWAFBodyKB {
		return verr("routing.waf.inspectBodyKB", "must be between 1 and %d KB", MaxWAFBodyKB)
	}
	if len(w.Exclusions) > 500 {
		return verr("routing.waf.exclusions", "at most 500 exclusions")
	}
	for i, x := range w.Exclusions {
		if err := x.validate(fmt.Sprintf("routing.waf.exclusions[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}

func (x *WAFExclusion) validate(f string) error {
	if x.Path != "" && !strings.HasPrefix(x.Path, "/") {
		return verr(f+".path", "must start with /")
	}
	for _, id := range x.RuleIDs {
		if id <= 0 {
			return verr(f+".ruleIds", "%d is not a rule ID", id)
		}
	}
	for _, c := range x.Categories {
		if !slices.Contains(WAFCategories, c) {
			return verr(f+".categories", "unknown category %q (one of %s)", c, strings.Join(WAFCategories, ", "))
		}
	}
	for _, l := range []struct {
		name  string
		names []string
	}{{"args", x.Args}, {"cookies", x.Cookies}, {"headers", x.Headers}} {
		for j, n := range l.names {
			n = strings.TrimSpace(n)
			if n == "" || n == "*" {
				return verr(fmt.Sprintf("%s.%s[%d]", f, l.name, j), "enter a name (a trailing * matches a prefix)")
			}
			if strings.ContainsAny(n, "\r\n\x00") || len(n) > 256 {
				return verr(fmt.Sprintf("%s.%s[%d]", f, l.name, j), "not a valid name")
			}
		}
	}
	if x.Path == "" && len(x.RuleIDs)+len(x.Categories)+len(x.Args)+len(x.Cookies)+len(x.Headers) == 0 {
		return verr(f, "list rules, categories or what not to inspect, or give a path under which the firewall is off")
	}
	if len(x.Comment) > 500 {
		return verr(f+".comment", "at most 500 characters")
	}
	return nil
}

// WAFSettings are the server-wide firewall settings.
type WAFSettings struct {
	// New sites (other than redirects and background workers) start with
	// this mode, paranoia level and threshold. Detect by default: a new
	// site shows what would be blocked without blocking anything.
	DefaultMode             string `json:"defaultMode"`
	DefaultParanoiaLevel    int    `json:"defaultParanoiaLevel"`
	DefaultAnomalyThreshold int    `json:"defaultAnomalyThreshold"`
	// EventRetentionDays is how long firewall events are kept (at most
	// 100,000 of them in any case).
	EventRetentionDays int `json:"eventRetentionDays"`
}

// DefaultWAF is the configuration used until one is saved.
func DefaultWAF() WAFSettings {
	return WAFSettings{DefaultMode: WAFDetect, DefaultParanoiaLevel: DefaultWAFParanoia, DefaultAnomalyThreshold: DefaultWAFThreshold, EventRetentionDays: 30}
}

// ApplyDefaults fills a configuration saved before the firewall existed,
// and zero values that would make no sense.
func (s *WAFSettings) ApplyDefaults() {
	if reflect.ValueOf(*s).IsZero() {
		*s = DefaultWAF()
		return
	}
	d := DefaultWAF()
	if s.DefaultMode == "" {
		s.DefaultMode = WAFOff
	}
	if s.DefaultParanoiaLevel <= 0 {
		s.DefaultParanoiaLevel = d.DefaultParanoiaLevel
	}
	if s.DefaultAnomalyThreshold <= 0 {
		s.DefaultAnomalyThreshold = d.DefaultAnomalyThreshold
	}
	if s.EventRetentionDays <= 0 {
		s.EventRetentionDays = d.EventRetentionDays
	}
}

// Validate checks the settings after ApplyDefaults.
func (s *WAFSettings) Validate() error {
	switch s.DefaultMode {
	case WAFOff, WAFDetect, WAFBlock:
	default:
		return verr("waf.defaultMode", "must be off, detect or block")
	}
	if s.DefaultParanoiaLevel > 3 {
		return verr("waf.defaultParanoiaLevel", "must be 1, 2 or 3")
	}
	if s.DefaultAnomalyThreshold > 1000 {
		return verr("waf.defaultAnomalyThreshold", "must be between 1 and 1000")
	}
	if s.EventRetentionDays > 3650 {
		return verr("waf.eventRetentionDays", "must be between 1 and 3650 days")
	}
	return nil
}

// ForNewSite is the firewall configuration a new site of type t starts
// with when it was created without one.
func (s WAFSettings) ForNewSite(t SiteType) WAFConfig {
	if t == SiteWorker || t == SiteRedirect {
		return WAFConfig{Mode: WAFOff}
	}
	return WAFConfig{Mode: s.DefaultMode, ParanoiaLevel: s.DefaultParanoiaLevel, AnomalyThreshold: s.DefaultAnomalyThreshold}
}

// WAF actions recorded in events.
const (
	WAFActionBlocked  = "blocked"
	WAFActionDetected = "detected" // detect mode: would have been blocked
)

// WAFEvent is a request the firewall blocked (or, in detect mode, would
// have). ID is the request ID shown on the block page.
type WAFEvent struct {
	Seq       int64      `json:"seq"` // for paging: list with before=seq
	ID        string     `json:"id"`
	Time      time.Time  `json:"time"`
	SiteID    string     `json:"siteId"`
	Action    string     `json:"action"`
	ClientIP  string     `json:"clientIp"`
	Method    string     `json:"method"`
	Host      string     `json:"host"`
	Path      string     `json:"path"` // without the query string, which may carry secrets
	UserAgent string     `json:"userAgent,omitempty"`
	Score     int        `json:"score"`
	Threshold int        `json:"threshold"`
	Paranoia  int        `json:"paranoiaLevel"`
	Matches   []WAFMatch `json:"matches"`
}

// Where a rule matched.
const (
	WAFInPath    = "path"
	WAFInArg     = "arg"     // a query string or form field, or a JSON value
	WAFInArgName = "argName" // the name of one
	WAFInCookie  = "cookie"
	WAFInHeader  = "header"
	WAFInFile    = "file" // an uploaded file's name
	WAFInBody    = "body" // a body inspected as a whole (text, XML, unparsable JSON)
	WAFInRequest = "request"
	WAFInQuery   = "query" // the raw query string
)

// WAFMatch is one rule that matched.
type WAFMatch struct {
	RuleID   int    `json:"ruleId"`
	Category string `json:"category"`
	Severity string `json:"severity"` // critical | error | warning | notice
	Score    int    `json:"score"`
	Message  string `json:"message"`
	In       string `json:"in"`             // path, arg, argName, cookie, header, file, body, request, query
	Name     string `json:"name,omitempty"` // the argument, cookie or header
	// Snippet is the text that matched, decoded, truncated and with
	// control characters escaped; "[redacted]" for fields that look like
	// passwords or tokens.
	Snippet string `json:"snippet,omitempty"`
}

// Where says where a rule matched: "argument q", "cookie session", "the
// path".
func (m WAFMatch) Where() string {
	switch m.In {
	case WAFInPath:
		return "the path"
	case WAFInArg:
		return strings.TrimSpace("argument " + m.Name)
	case WAFInArgName:
		return strings.TrimSpace("argument name " + m.Name)
	case WAFInCookie:
		return strings.TrimSpace("cookie " + m.Name)
	case WAFInHeader:
		return strings.TrimSpace("header " + m.Name)
	case WAFInFile:
		return "an uploaded file name"
	case WAFInBody:
		return "the body"
	case WAFInQuery:
		return "the query string"
	}
	return "the request"
}

// SuggestExclusion is the narrowest exclusion that would have let an
// event's request through: its rules, under its path, and, when every
// match was in a named argument, cookie or header, only for those. The
// console's and NodeHoster Manager's "exclude" start from it.
func (ev *WAFEvent) SuggestExclusion() WAFExclusion {
	x := WAFExclusion{Path: ev.Path, Comment: "From request " + ev.ID}
	named := len(ev.Matches) > 0
	for _, m := range ev.Matches {
		if !slices.Contains(x.RuleIDs, m.RuleID) {
			x.RuleIDs = append(x.RuleIDs, m.RuleID)
		}
		switch {
		case m.Name == "":
			named = false
		case m.In == WAFInArg || m.In == WAFInArgName || m.In == WAFInFile:
			if !slices.Contains(x.Args, m.Name) {
				x.Args = append(x.Args, m.Name)
			}
		case m.In == WAFInCookie:
			if !slices.Contains(x.Cookies, m.Name) {
				x.Cookies = append(x.Cookies, m.Name)
			}
		case m.In == WAFInHeader:
			if !slices.Contains(x.Headers, m.Name) {
				x.Headers = append(x.Headers, m.Name)
			}
		default:
			named = false
		}
	}
	slices.Sort(x.RuleIDs)
	if !named {
		x.Args, x.Cookies, x.Headers = nil, nil, nil
	}
	return x
}

// String describes an exclusion in a few words: "rule 942100 for argument
// content under /admin".
func (x WAFExclusion) String() string {
	var parts []string
	for _, id := range x.RuleIDs {
		parts = append(parts, fmt.Sprintf("rule %d", id))
	}
	parts = append(parts, x.Categories...)
	what := strings.Join(parts, ", ")
	var targets []string
	for _, l := range []struct {
		kind  string
		names []string
	}{{"argument", x.Args}, {"cookie", x.Cookies}, {"header", x.Headers}} {
		for _, n := range l.names {
			targets = append(targets, l.kind+" "+n)
		}
	}
	switch {
	case what == "" && len(targets) == 0:
		what = "firewall off"
	case what == "":
		what = "all rules"
	}
	if len(targets) > 0 {
		what += " for " + strings.Join(targets, ", ")
	}
	if x.Path != "" {
		return what + " under " + x.Path
	}
	return what + " on the whole site"
}

// WAFRuleInfo describes a rule for the console (GET /api/waf/rules).
type WAFRuleInfo struct {
	ID       int    `json:"id"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	Score    int    `json:"score"`
	Paranoia int    `json:"paranoiaLevel"`
	Message  string `json:"message"`
}

// WAFEventQuery filters the events list.
type WAFEventQuery struct {
	SiteIDs   []string // nil = every site; empty = none
	Action    string
	ClientIP  string
	RuleID    int
	Category  string
	RequestID string
	Since     time.Time
	Before    int64 // Seq; 0 = the newest
	Limit     int
}
