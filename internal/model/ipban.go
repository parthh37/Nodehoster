package model

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// IPBanSettings configure automatic IP banning, like fail2ban (or IIS
// Dynamic IP Restrictions): a client address that fails to authenticate,
// scans for missing pages, keeps hitting rate limits or asks for a trap
// path is refused by every site for a while.
type IPBanSettings struct {
	Enabled bool `json:"enabled"`
	// AuthFailures counts failed sign-ins: 401 answers from basic
	// authentication or the application to requests that carried
	// credentials or submitted a form (not a browser merely being asked
	// to sign in), and failed web console logins.
	AuthFailures BanRule `json:"authFailures"`
	NotFound     BanRule `json:"notFound"`    // 404 answers (vulnerability scanners)
	RateLimited  BanRule `json:"rateLimited"` // 429 answers (a site's rate limit, or the application's)
	// TrapPaths ban on the first request: paths no site here serves, which
	// only scanners ask for. Prefixes, not case-sensitive. Sites that do
	// serve them (WordPress, PHP) opt out in their routing settings.
	TrapPaths []string `json:"trapPaths"`
	// BanMinutes is the first ban; each further ban of the same address
	// (within a week of the last one) doubles, up to MaxBanMinutes.
	BanMinutes    int `json:"banMinutes"`
	MaxBanMinutes int `json:"maxBanMinutes"`
	// AllowList is never banned. Loopback and the trusted proxies are
	// always allowed: bans apply to the client address resolved through
	// them, never to the proxies themselves.
	AllowList []string `json:"allowList"`
	// IPv6Prefix is how much of an IPv6 address is banned: a single address
	// is free to change within the /64 a client is given.
	IPv6Prefix int `json:"ipv6Prefix"`
}

// BanRule bans an address after Threshold events within WindowSec.
// Threshold 0 turns the rule off.
type BanRule struct {
	Threshold int `json:"threshold"`
	WindowSec int `json:"windowSec"`
}

// Ban is a banned address, automatic or manual.
type Ban struct {
	Address   string     `json:"address"` // an IP address, or a CIDR range (an IPv6 prefix)
	Reason    string     `json:"reason"`
	Manual    bool       `json:"manual,omitempty"`
	Strikes   int        `json:"strikes"` // automatic bans of this address so far, for escalation
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"` // nil = until removed (manual bans only)
	CreatedBy string     `json:"createdBy,omitempty"` // manual bans
}

// Active reports whether the ban is in force.
func (b *Ban) Active(now time.Time) bool {
	return b.ExpiresAt == nil || now.Before(*b.ExpiresAt)
}

// BanRequest is the body of POST /api/bans.
type BanRequest struct {
	Address string `json:"address"`
	Minutes int    `json:"minutes"` // 0 = until removed
	Reason  string `json:"reason"`
}

// DefaultIPBan is the configuration used until one is saved. Banning is
// off: an administrator turns it on knowing who shares their addresses.
func DefaultIPBan() IPBanSettings {
	return IPBanSettings{
		AuthFailures:  BanRule{Threshold: 10, WindowSec: 300},
		NotFound:      BanRule{Threshold: 50, WindowSec: 60},
		RateLimited:   BanRule{Threshold: 30, WindowSec: 60},
		TrapPaths:     []string{"/wp-login.php", "/xmlrpc.php", "/wp-admin", "/.env", "/.git/", "/phpmyadmin", "/pma", "/cgi-bin/", "/vendor/phpunit"},
		BanMinutes:    15,
		MaxBanMinutes: 24 * 60,
		AllowList:     []string{},
		IPv6Prefix:    64,
	}
}

// ApplyDefaults fills a configuration saved before banning existed, and
// zero values that would make no sense.
func (s *IPBanSettings) ApplyDefaults() {
	if reflect.ValueOf(*s).IsZero() {
		*s = DefaultIPBan()
		return
	}
	d := DefaultIPBan()
	for _, r := range []struct{ rule, def *BanRule }{{&s.AuthFailures, &d.AuthFailures}, {&s.NotFound, &d.NotFound}, {&s.RateLimited, &d.RateLimited}} {
		if r.rule.WindowSec <= 0 {
			r.rule.WindowSec = r.def.WindowSec
		}
		if r.rule.Threshold < 0 {
			r.rule.Threshold = 0
		}
	}
	if s.TrapPaths == nil {
		s.TrapPaths = []string{}
	}
	if s.AllowList == nil {
		s.AllowList = []string{}
	}
	if s.BanMinutes <= 0 {
		s.BanMinutes = d.BanMinutes
	}
	if s.MaxBanMinutes <= 0 {
		s.MaxBanMinutes = d.MaxBanMinutes
	}
	if s.IPv6Prefix <= 0 {
		s.IPv6Prefix = d.IPv6Prefix
	}
}

// Validate checks the settings after ApplyDefaults.
func (s *IPBanSettings) Validate() error {
	for _, r := range []struct {
		f    string
		rule BanRule
	}{{"authFailures", s.AuthFailures}, {"notFound", s.NotFound}, {"rateLimited", s.RateLimited}} {
		if r.rule.Threshold > 100000 {
			return verr("ipBan."+r.f+".threshold", "must be between 0 (off) and 100000")
		}
		if r.rule.WindowSec > 86400 {
			return verr("ipBan."+r.f+".windowSec", "must be at most 86400 seconds (a day)")
		}
	}
	for i, p := range s.TrapPaths {
		if !strings.HasPrefix(p, "/") || len(p) < 2 {
			return verr(fmt.Sprintf("ipBan.trapPaths[%d]", i), "must start with / and not be the root")
		}
	}
	if s.BanMinutes > 525600 {
		return verr("ipBan.banMinutes", "must be at most 525600 minutes (a year)")
	}
	if s.MaxBanMinutes < s.BanMinutes || s.MaxBanMinutes > 525600 {
		return verr("ipBan.maxBanMinutes", "must be between the first ban's length and 525600 minutes (a year)")
	}
	for i, c := range s.AllowList {
		if _, err := ParseCIDROrIP(c); err != nil {
			return verr(fmt.Sprintf("ipBan.allowList[%d]", i), "%v", err)
		}
	}
	if s.IPv6Prefix < 32 || s.IPv6Prefix > 128 {
		return verr("ipBan.ipv6Prefix", "must be between 32 and 128")
	}
	return nil
}

// SiteBanning is a site's part in automatic IP banning.
type SiteBanning struct {
	// Exempt: banned addresses still reach this site (a status page, an
	// API for devices behind shared addresses), and its answers never
	// count towards a ban.
	Exempt bool `json:"exempt,omitempty"`
	// AllowTrapPaths: the server's trap paths are real pages here (a
	// WordPress or PHP site), so requesting them bans nobody.
	AllowTrapPaths bool `json:"allowTrapPaths,omitempty"`
}
