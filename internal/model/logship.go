package model

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
)

// LogShippingSettings send the server's logs to a central log system, the
// way IIS's logging goes to a syslog collector through an agent, without
// installing one: NodeHoster ships its own server log, the sites'
// application output and access logs, events and the audit log.
type LogShippingSettings struct {
	Targets []LogTarget `json:"targets"`
}

// Log sources a target can receive.
const (
	LogSourceServer = "server" // NodeHoster's own log
	LogSourceApp    = "app"    // sites' stdout, stderr and system lines
	LogSourceAccess = "access" // sites' access logs (sites with access logging on)
	LogSourceEvent  = "event"  // operational events
	LogSourceAudit  = "audit"  // the audit log
)

var LogSources = []string{LogSourceServer, LogSourceApp, LogSourceAccess, LogSourceEvent, LogSourceAudit}

// Log target types.
const (
	LogTargetSyslog = "syslog"
	LogTargetSeq    = "seq"
	LogTargetHTTP   = "http"
)

// LogTarget is one log collector. Only the section matching Type is used.
type LogTarget struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Type    string   `json:"type"` // syslog | seq | http
	Enabled bool     `json:"enabled"`
	Sources []string `json:"sources"` // see LogSources
	// SiteIDs limits the records that belong to a site (app, access and
	// site events) to these sites; empty = every site. The server log,
	// the audit log and server events are chosen by Sources alone.
	SiteIDs []string `json:"siteIds"`
	// MinLevel is the lowest server-log level sent: debug | info |
	// warning | error. Other sources are not filtered by level.
	MinLevel string `json:"minLevel"`

	Syslog *SyslogTarget `json:"syslog,omitempty"`
	Seq    *SeqTarget    `json:"seq,omitempty"`
	HTTP   *HTTPTarget   `json:"http,omitempty"`
}

// SyslogTarget sends RFC 5424 messages over UDP, TCP (octet counting,
// RFC 6587) or TLS (RFC 5425).
type SyslogTarget struct {
	Address            string `json:"address"`   // host:port
	Transport          string `json:"transport"` // udp | tcp | tls
	Facility           string `json:"facility"`  // user, daemon, local0 … local7
	AppName            string `json:"appName"`   // "" = nodehoster
	Hostname           string `json:"hostname"`  // "" = this computer's name
	CACert             string `json:"caCert,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
}

// SeqTarget posts CLEF events to a Seq server.
type SeqTarget struct {
	URL    string `json:"url"`              // https://seq.example.com
	APIKey string `json:"apiKey,omitempty"` // secret
}

// HTTPTarget posts batches of JSON records (Logstash, Vector, Fluent Bit,
// a custom collector).
type HTTPTarget struct {
	URL     string       `json:"url"`
	Format  string       `json:"format"` // json (an array) | ndjson
	Headers []HTTPHeader `json:"headers"`
}

type HTTPHeader struct {
	Name   string `json:"name"`
	Value  string `json:"value"` // secret when Secret is set
	Secret bool   `json:"secret"`
}

// Log levels, lowest first.
var LogLevels = []string{"debug", "info", "warning", "error"}

// LogLevelRank orders levels; unknown ones rank as info.
func LogLevelRank(l string) int {
	if i := slices.Index(LogLevels, l); i >= 0 {
		return i
	}
	return 1
}

// SyslogFacilities are the facility codes by name.
var SyslogFacilities = map[string]int{
	"kern": 0, "user": 1, "mail": 2, "daemon": 3, "auth": 4, "syslog": 5, "lpr": 6, "news": 7,
	"uucp": 8, "cron": 9, "authpriv": 10, "ftp": 11,
	"local0": 16, "local1": 17, "local2": 18, "local3": 19, "local4": 20, "local5": 21, "local6": 22, "local7": 23,
}

func (s *LogShippingSettings) Validate() error {
	ids := map[string]bool{}
	for i := range s.Targets {
		t := &s.Targets[i]
		f := fmt.Sprintf("logShipping.targets[%d]", i)
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" {
			return verr(f+".name", "give the target a name")
		}
		if ids[t.ID] {
			return verr(f+".id", "duplicate target")
		}
		ids[t.ID] = true
		if err := t.Validate(f); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks one target; f is the field path for errors.
func (t *LogTarget) Validate(f string) error {
	if t.Sources == nil {
		t.Sources = []string{}
	}
	if t.SiteIDs == nil {
		t.SiteIDs = []string{}
	}
	for _, s := range t.Sources {
		if !slices.Contains(LogSources, s) {
			return verr(f+".sources", "unknown source %q", s)
		}
	}
	if t.Enabled && len(t.Sources) == 0 {
		return verr(f+".sources", "choose at least one source")
	}
	if t.MinLevel == "" {
		t.MinLevel = "info"
	}
	if !slices.Contains(LogLevels, t.MinLevel) {
		return verr(f+".minLevel", "debug, info, warning or error")
	}
	switch t.Type {
	case LogTargetSyslog:
		t.Seq, t.HTTP = nil, nil
		s := t.Syslog
		if s == nil || strings.TrimSpace(s.Address) == "" {
			return verr(f+".syslog.address", "enter the collector's host:port")
		}
		s.Address = strings.TrimSpace(s.Address)
		if s.Transport == "" {
			s.Transport = "udp"
		}
		if s.Transport != "udp" && s.Transport != "tcp" && s.Transport != "tls" {
			return verr(f+".syslog.transport", "udp, tcp or tls")
		}
		if _, port, err := net.SplitHostPort(s.Address); err != nil || port == "" {
			return verr(f+".syslog.address", "use host:port, e.g. logs.example.com:514")
		}
		if s.Facility == "" {
			s.Facility = "local0"
		}
		if _, ok := SyslogFacilities[s.Facility]; !ok {
			return verr(f+".syslog.facility", "unknown facility %q", s.Facility)
		}
		if strings.ContainsAny(s.AppName+s.Hostname, " \t\r\n") || len(s.AppName) > 48 || len(s.Hostname) > 255 {
			return verr(f+".syslog.appName", "no spaces; at most 48 characters")
		}
	case LogTargetSeq:
		t.Syslog, t.HTTP = nil, nil
		if t.Seq == nil || !isHTTPURL(t.Seq.URL) {
			return verr(f+".seq.url", "enter the Seq server's URL, e.g. https://seq.example.com")
		}
		t.Seq.URL = strings.TrimRight(strings.TrimSpace(t.Seq.URL), "/")
	case LogTargetHTTP:
		t.Syslog, t.Seq = nil, nil
		h := t.HTTP
		if h == nil || !isHTTPURL(h.URL) {
			return verr(f+".http.url", "enter an http(s) URL")
		}
		h.URL = strings.TrimSpace(h.URL)
		if h.Format == "" {
			h.Format = "json"
		}
		if h.Format != "json" && h.Format != "ndjson" {
			return verr(f+".http.format", "json or ndjson")
		}
		if h.Headers == nil {
			h.Headers = []HTTPHeader{}
		}
		for j := range h.Headers {
			hd := &h.Headers[j]
			hd.Name = strings.TrimSpace(hd.Name)
			if hd.Name == "" || strings.ContainsAny(hd.Name, " :\r\n") {
				return verr(fmt.Sprintf("%s.http.headers[%d].name", f, j), "not a header name")
			}
			if strings.ContainsAny(hd.Value, "\r\n") {
				return verr(fmt.Sprintf("%s.http.headers[%d].value", f, j), "no line breaks")
			}
		}
	default:
		return verr(f+".type", "choose syslog, seq or http")
	}
	return nil
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
