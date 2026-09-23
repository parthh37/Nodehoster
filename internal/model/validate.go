package model

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	siteNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)
	hostRe     = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	hhmmRe     = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	envNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ValidationError carries a field path so the UI can highlight it.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

func verr(field, format string, a ...any) error {
	return &ValidationError{Field: field, Message: fmt.Sprintf(format, a...)}
}

// ApplyDefaults fills zero values with sensible defaults. It is idempotent
// and called on every write, so it is also how old documents gain new fields.
func (s *Site) ApplyDefaults() {
	for i := range s.Bindings {
		b := &s.Bindings[i]
		b.Protocol = strings.ToLower(strings.TrimSpace(b.Protocol))
		b.Host = strings.ToLower(strings.TrimSpace(b.Host))
		if b.IP == "*" {
			b.IP = ""
		}
		if b.Port == 0 {
			if b.Protocol == "https" {
				b.Port = 443
			} else {
				b.Port = 80
			}
		}
		if b.Protocol == "https" && b.CertMode == "" {
			if b.CertificateID != "" {
				b.CertMode = CertModeManual
			} else {
				b.CertMode = CertModeAuto
			}
		}
		if b.Protocol == "http" {
			b.CertMode, b.CertificateID = "", ""
		}
	}
	switch s.Type {
	case SiteNode:
		if s.Node == nil {
			s.Node = &NodeConfig{}
		}
		n := s.Node
		if n.Instances <= 0 {
			n.Instances = 1
		}
		if n.PortMode == "" {
			n.PortMode = "auto"
		}
		if n.RestartPolicy == "" {
			n.RestartPolicy = "always"
		}
		if n.MaxRestarts <= 0 {
			n.MaxRestarts = 10
		}
		if n.RestartWindowSec <= 0 {
			n.RestartWindowSec = 300
		}
		if n.StartupTimeoutSec <= 0 {
			n.StartupTimeoutSec = 60
		}
		if n.ShutdownTimeoutSec <= 0 {
			n.ShutdownTimeoutSec = 15
		}
		if n.HealthCheck.Path == "" {
			n.HealthCheck.Path = "/"
		}
		if n.HealthCheck.IntervalSec <= 0 {
			n.HealthCheck.IntervalSec = 30
		}
		if n.HealthCheck.TimeoutSec <= 0 {
			n.HealthCheck.TimeoutSec = 5
		}
		if n.HealthCheck.UnhealthyThreshold <= 0 {
			n.HealthCheck.UnhealthyThreshold = 3
		}
		if len(n.WatchIgnore) == 0 {
			n.WatchIgnore = []string{"node_modules", ".git", "logs"}
		}
	case SiteProxy:
		if s.Proxy == nil {
			s.Proxy = &ProxyConfig{}
		}
		if s.Proxy.LoadBalancing == "" {
			s.Proxy.LoadBalancing = "round_robin"
		}
		hc := &s.Proxy.HealthCheck
		if hc.Path == "" {
			hc.Path = "/"
		}
		if hc.IntervalSec <= 0 {
			hc.IntervalSec = 15
		}
		if hc.TimeoutSec <= 0 {
			hc.TimeoutSec = 5
		}
		if hc.UnhealthyThreshold <= 0 {
			hc.UnhealthyThreshold = 3
		}
	case SiteStatic:
		if s.Static == nil {
			s.Static = &StaticConfig{}
		}
		if len(s.Static.IndexFiles) == 0 {
			s.Static.IndexFiles = []string{"index.html", "index.htm", "default.htm"}
		}
	case SiteRedirect:
		if s.Redirect == nil {
			s.Redirect = &RedirectConfig{}
		}
		if s.Redirect.StatusCode <= 0 {
			s.Redirect.StatusCode = 301
		}
	}
	if s.Routing.HSTS.Enabled && s.Routing.HSTS.MaxAgeSec <= 0 {
		s.Routing.HSTS.MaxAgeSec = 31536000
	}
	if s.Routing.BasicAuth.Realm == "" {
		s.Routing.BasicAuth.Realm = "Restricted"
	}
	if s.Routing.RateLimit.Enabled && s.Routing.RateLimit.Burst <= 0 {
		s.Routing.RateLimit.Burst = int(s.Routing.RateLimit.RequestsPerSecond*2) + 1
	}
	if s.Deploy.KeepReleases <= 0 {
		s.Deploy.KeepReleases = 5
	}
	if s.Deploy.InstallCommand == "" && s.Type == SiteNode {
		s.Deploy.InstallCommand = "npm ci --omit=dev"
	}
}

// Validate checks a single site in isolation. Cross-site checks such as
// binding conflicts are done by ValidateBindings.
func (s *Site) Validate() error {
	if !siteNameRe.MatchString(s.Name) {
		return verr("name", "must be 1-64 characters: letters, digits, space, '.', '_' or '-'")
	}
	switch s.Type {
	case SiteNode, SiteProxy, SiteStatic, SiteRedirect:
	default:
		return verr("type", "unknown site type %q", s.Type)
	}
	seen := map[string]bool{}
	for i, b := range s.Bindings {
		f := fmt.Sprintf("bindings[%d]", i)
		if b.Protocol != "http" && b.Protocol != "https" {
			return verr(f+".protocol", "must be http or https")
		}
		if b.Port < 1 || b.Port > 65535 {
			return verr(f+".port", "must be between 1 and 65535")
		}
		if b.IP != "" && net.ParseIP(b.IP) == nil {
			return verr(f+".ip", "%q is not an IP address", b.IP)
		}
		if b.Host != "" && !hostRe.MatchString(b.Host) {
			return verr(f+".host", "%q is not a valid host name", b.Host)
		}
		if b.Protocol == "https" {
			switch b.CertMode {
			case CertModeAuto:
				if b.Host == "" || strings.HasPrefix(b.Host, "*.") {
					return verr(f+".certMode", "automatic certificates need a specific host name (use a DNS-01 certificate for wildcards)")
				}
			case CertModeManual:
				if b.CertificateID == "" {
					return verr(f+".certificateId", "select a certificate")
				}
			default:
				return verr(f+".certMode", "must be auto or certificate")
			}
		}
		k := b.Key()
		if seen[k] {
			return verr(f, "duplicate binding %s", b.String())
		}
		seen[k] = true
	}
	switch s.Type {
	case SiteNode:
		n := s.Node
		if strings.TrimSpace(n.AppRoot) == "" && s.ActiveRelease == "" {
			return verr("node.appRoot", "application path is required")
		}
		if n.Script == "" && n.NpmScript == "" {
			return verr("node.script", "set an entry script or an npm script")
		}
		if n.Instances > 64 {
			return verr("node.instances", "at most 64 instances")
		}
		if n.PortMode == "fixed" {
			if n.FixedPort < 1 || n.FixedPort > 65535 {
				return verr("node.fixedPort", "must be between 1 and 65535")
			}
			if n.Instances != 1 {
				return verr("node.instances", "a fixed port allows only one instance")
			}
		} else if n.PortMode != "auto" {
			return verr("node.portMode", "must be auto or fixed")
		}
		switch n.RestartPolicy {
		case "always", "on-failure", "never":
		default:
			return verr("node.restartPolicy", "must be always, on-failure or never")
		}
		for i, e := range n.Env {
			if !envNameRe.MatchString(e.Name) {
				return verr(fmt.Sprintf("node.env[%d].name", i), "%q is not a valid variable name", e.Name)
			}
		}
		for i, t := range n.Recycle.ScheduleTimes {
			if !hhmmRe.MatchString(t) {
				return verr(fmt.Sprintf("node.recycle.scheduleTimes[%d]", i), "use HH:MM (24h)")
			}
		}
		if n.Limits.CPUPercent < 0 || n.Limits.CPUPercent > 100 {
			return verr("node.limits.cpuPercent", "must be between 0 and 100")
		}
		if n.RunAs.Enabled && n.RunAs.Username == "" {
			return verr("node.runAs.username", "required when running as another user")
		}
	case SiteProxy:
		if len(s.Proxy.Upstreams) == 0 {
			return verr("proxy.upstreams", "add at least one upstream")
		}
		for i, u := range s.Proxy.Upstreams {
			if err := validURL(u.URL); err != nil {
				return verr(fmt.Sprintf("proxy.upstreams[%d].url", i), "%v", err)
			}
		}
		switch s.Proxy.LoadBalancing {
		case "round_robin", "least_conn", "ip_hash", "random":
		default:
			return verr("proxy.loadBalancing", "unknown strategy %q", s.Proxy.LoadBalancing)
		}
	case SiteStatic:
		if strings.TrimSpace(s.Static.Root) == "" && s.ActiveRelease == "" {
			return verr("static.root", "a root directory is required")
		}
	case SiteRedirect:
		if err := validURL(s.Redirect.TargetURL); err != nil {
			return verr("redirect.targetUrl", "%v", err)
		}
		switch s.Redirect.StatusCode {
		case 301, 302, 303, 307, 308:
		default:
			return verr("redirect.statusCode", "must be 301, 302, 303, 307 or 308")
		}
	}
	r := s.Routing
	for i, rw := range r.Rewrites {
		f := fmt.Sprintf("routing.rewrites[%d]", i)
		if _, err := regexp.Compile(rw.Match); err != nil {
			return verr(f+".match", "invalid regular expression: %v", err)
		}
		if rw.Host != "" {
			if _, err := regexp.Compile(rw.Host); err != nil {
				return verr(f+".host", "invalid regular expression: %v", err)
			}
		}
		switch rw.Action {
		case "rewrite", "redirect", "block", "respond":
		default:
			return verr(f+".action", "must be rewrite, redirect, block or respond")
		}
	}
	for i, l := range r.Locations {
		f := fmt.Sprintf("routing.locations[%d]", i)
		if !strings.HasPrefix(l.Path, "/") || l.Path == "/" {
			return verr(f+".path", "must start with / and not be the root")
		}
		switch l.Kind {
		case "site":
			if l.SiteID == "" || l.SiteID == s.ID {
				return verr(f+".siteId", "select another site")
			}
		case "url":
			if err := validURL(l.URL); err != nil {
				return verr(f+".url", "%v", err)
			}
		case "static":
			if l.Root == "" {
				return verr(f+".root", "a directory is required")
			}
		default:
			return verr(f+".kind", "must be site, url or static")
		}
	}
	for i, c := range append(append([]string{}, r.IP.Allow...), r.IP.Deny...) {
		if _, err := ParseCIDROrIP(c); err != nil {
			return verr(fmt.Sprintf("routing.ip[%d]", i), "%v", err)
		}
	}
	for code := range r.ErrorPages {
		if n, err := strconv.Atoi(code); err != nil || n < 400 || n > 599 {
			return verr("routing.errorPages", "%q is not an HTTP error status", code)
		}
	}
	if r.RateLimit.Enabled && r.RateLimit.RequestsPerSecond <= 0 {
		return verr("routing.rateLimit.requestsPerSecond", "must be greater than zero")
	}
	if r.BasicAuth.Enabled && len(r.BasicAuth.Users) == 0 {
		return verr("routing.basicAuth.users", "add at least one user")
	}
	return nil
}

func validURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("must be an http:// or https:// URL")
	}
	if u.Host == "" {
		return fmt.Errorf("host is missing")
	}
	return nil
}

// ParseCIDROrIP accepts "10.0.0.0/8" or a single address.
func ParseCIDROrIP(s string) (*net.IPNet, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		return n, err
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("%q is not an IP address or CIDR", s)
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	} else {
		ip = ip.To4()
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
}

// Key identifies what a binding answers; two bindings with the same key
// conflict, exactly as in IIS.
func (b Binding) Key() string {
	return fmt.Sprintf("%s|%s|%d|%s", b.Protocol, b.IP, b.Port, b.Host)
}

// ListenAddr is the socket the binding needs.
func (b Binding) ListenAddr() string {
	return net.JoinHostPort(b.IP, strconv.Itoa(b.Port))
}

func (b Binding) String() string {
	ip := b.IP
	if ip == "" {
		ip = "*"
	}
	return fmt.Sprintf("%s %s:%d:%s", b.Protocol, ip, b.Port, b.Host)
}

// ValidateBindings checks a candidate site against every other site: no
// duplicate bindings, and no port used for http by one site and https by
// another, because a single socket speaks only one protocol.
func ValidateBindings(candidate *Site, others []*Site) error {
	proto := map[string]string{} // listen addr -> protocol
	keys := map[string]string{}  // binding key -> site name
	for _, o := range others {
		if o.ID == candidate.ID {
			continue
		}
		for _, b := range o.Bindings {
			keys[b.Key()] = o.Name
			proto[portKey(b)] = b.Protocol
		}
	}
	for i, b := range candidate.Bindings {
		f := fmt.Sprintf("bindings[%d]", i)
		if name, ok := keys[b.Key()]; ok {
			return verr(f, "binding %s is already used by site %q", b.String(), name)
		}
		if p, ok := proto[portKey(b)]; ok && p != b.Protocol {
			return verr(f, "port %d is already bound as %s by another site", b.Port, p)
		}
	}
	return nil
}

// portKey is keyed on port alone because a wildcard IP listener and a
// specific IP listener on the same port would collide at the socket level.
func portKey(b Binding) string { return strconv.Itoa(b.Port) }

// DefaultSettings returns the settings used on first start.
func DefaultSettings() Settings {
	return Settings{
		ACME: ACMESettings{Directory: "letsencrypt", KeyType: "ec256"},
		TLS:  TLSSettings{MinVersion: "1.2", HTTP2: true},
		Proxy: ProxySettings{
			ServerHeader:       "NodeHoster",
			ReadHeaderTimeoutS: 30,
			IdleTimeoutS:       120,
		},
		PortRangeStart:     41000,
		PortRangeEnd:       48999,
		LogMaxSizeMB:       20,
		LogMaxFiles:        10,
		LogRetentionDays:   30,
		CertExpiryWarnDays: 14,
	}
}
