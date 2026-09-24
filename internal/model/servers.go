package model

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// ServerConnection is another NodeHoster server managed from this one, like
// a connection added with IIS Manager's "Connect to a Server": the web
// console of this server can switch to it and operate it through an
// authenticated proxy, and the Servers page shows its health.
//
// The connection authenticates with an API token created on the remote
// server (a dedicated one, limited to what this server's users should do
// there). What the token allows is enforced there; MinRole decides which
// of this server's users may use the connection at all.
type ServerConnection struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// URL is the remote web console's base URL, e.g. https://web02:8484
	// (a path prefix is kept when the console sits behind a reverse
	// proxy). Its API is URL + /api.
	URL   string `json:"url"`
	Token string `json:"token"` // secret: an API token created on the remote server
	// Fingerprint pins the remote certificate (SHA-256 of the leaf, 64
	// hex digits) instead of verifying it against the trusted roots, for
	// the self-signed certificates admin listeners often have. Empty: the
	// certificate must be valid for the host name.
	Fingerprint string `json:"fingerprint,omitempty"`
	// MinRole is the least local role allowed to use the connection:
	// admin (the default), operator or viewer. The remote server caps the
	// token at the caller's local role (see RoleLimitHeader); one too old
	// to do so can only be used by administrators.
	MinRole   Role      `json:"minRole"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// RoleLimitHeader asks the server to narrow the caller's access to at most
// the given role for this request. It can only take rights away, so any
// client may send it; the proxy of a server connection sends the local
// user's role, so a local operator cannot act as an administrator on the
// remote server even with an administrator's token.
const RoleLimitHeader = "X-NodeHoster-Role-Limit"

// RoleLimitAppliedHeader is the answer to RoleLimitHeader: a server that
// applied the limit echoes the role in it. A server too old to know the
// limit ignores it and answers without, so the proxy of a connection
// relays a non-administrator's request only to a server that echoes it.
const RoleLimitAppliedHeader = "X-NodeHoster-Role-Limit-Applied"

// ServerHealth is what the last check of a connection found.
type ServerHealth struct {
	// Reachable: the server answered with the token accepted. False with
	// CheckedAt nil means the connection was not checked yet.
	Reachable bool       `json:"reachable"`
	Error     string     `json:"error,omitempty"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	LatencyMs int64      `json:"latencyMs"`
	// Since when the server has been in its current state (reachable or
	// not), as seen from here.
	Since *time.Time `json:"since,omitempty"`

	Version    string  `json:"version,omitempty"` // "" when the server does not say
	Commit     string  `json:"commit,omitempty"`
	Hostname   string  `json:"hostname,omitempty"`
	OS         string  `json:"os,omitempty"`
	CPUPercent float64 `json:"cpuPercent"`
	CPUCount   int     `json:"cpuCount"`
	MemTotal   uint64  `json:"memTotal"`
	MemUsed    uint64  `json:"memUsed"`

	// The sites the token can see, by state.
	Sites    int `json:"sites"`
	Running  int `json:"running"`
	Degraded int `json:"degraded"`
	Failed   int `json:"failed"`
	Stopped  int `json:"stopped"`

	// Who the token is on the remote server, and its role there
	// (RoleSites for a token limited to some sites).
	User string `json:"user,omitempty"`
	Role Role   `json:"role,omitempty"`
	// RoleLimits: the server applies RoleLimitHeader. Known when User is
	// set (the check read who the token is); a server without it can
	// only be used by this server's administrators.
	RoleLimits bool `json:"roleLimits"`
}

// ServerView is a connection as the API lists it: the token masked, with
// its health.
type ServerView struct {
	ServerConnection
	Health ServerHealth `json:"health"`
}

// ServerTest is the body of POST /api/servers/test: a connection being
// set up. With ID set and Token masked, the stored token is used.
type ServerTest struct {
	ID          string `json:"id,omitempty"`
	URL         string `json:"url"`
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// ServerTestResult is what testing a connection found: the certificate
// the server presented (for trust on first use) and, when the TLS
// connection could be trusted, the server's answer to the token.
type ServerTestResult struct {
	Certificate *PeerCertificate `json:"certificate,omitempty"` // nil over plain HTTP
	// Trusted: the connection's TLS settings accept the certificate (it
	// matches the pinned fingerprint, or verifies against the trusted
	// roots when none is pinned). Nothing is sent with the token otherwise.
	Trusted bool         `json:"trusted"`
	Health  ServerHealth `json:"health"`
}

// PeerCertificate describes the certificate a server presented.
type PeerCertificate struct {
	Fingerprint string    `json:"fingerprint"` // SHA-256, uppercase hex
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	DNSNames    []string  `json:"dnsNames"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
	// Verified: the chain verifies for the host name against the trusted
	// roots of the computer that connected; VerifyError says why not.
	Verified    bool   `json:"verified"`
	VerifyError string `json:"verifyError,omitempty"`
}

// ApplyDefaults normalizes a connection as entered.
func (s *ServerConnection) ApplyDefaults() {
	s.Name = strings.TrimSpace(s.Name)
	s.URL = strings.TrimSpace(s.URL)
	s.Token = strings.TrimSpace(s.Token)
	if u, err := NormalizeServerURL(s.URL); err == nil {
		s.URL = u
	}
	if fp, err := ParseFingerprint(s.Fingerprint); err == nil {
		s.Fingerprint = fp
	}
	if s.MinRole == "" {
		s.MinRole = RoleAdmin
	}
}

// Validate checks a normalized connection; others are the other
// connections, whose names must differ.
func (s *ServerConnection) Validate(others []ServerConnection) error {
	if s.Name == "" {
		return verr("name", "enter a name for the server")
	}
	if utf8.RuneCountInString(s.Name) > 64 {
		return verr("name", "at most 64 characters")
	}
	for _, o := range others {
		if o.ID != s.ID && strings.EqualFold(o.Name, s.Name) {
			return verr("name", "another server connection is named %q", o.Name)
		}
	}
	u, err := NormalizeServerURL(s.URL)
	if err != nil {
		return verr("url", "%v", err)
	}
	if _, err := ParseFingerprint(s.Fingerprint); err != nil {
		return verr("fingerprint", "%v", err)
	}
	if s.Fingerprint != "" && strings.HasPrefix(u, "http://") {
		return verr("fingerprint", "a certificate fingerprint needs an https:// URL")
	}
	if s.Token == "" {
		return verr("token", "enter an API token created on that server")
	}
	if !serverRole(s.MinRole) {
		return verr("minRole", "admin, operator or viewer")
	}
	return nil
}

// NormalizeServerURL checks the base URL of a NodeHoster web console and
// returns it without a trailing slash. HTTPS is required, except to this
// computer: the API token would otherwise cross the network in clear.
func NormalizeServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("enter the server's web console URL, e.g. https://web02:8484")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("enter the server's web console URL, e.g. https://web02:8484")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("the URL cannot have a user name, a query or a fragment")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !IsLoopbackHost(u.Hostname()) {
			return "", fmt.Errorf("use https://: the API token must not cross the network in clear text")
		}
	default:
		return "", fmt.Errorf("the URL must start with https://")
	}
	if p := u.Port(); p != "" {
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("port %s is not valid", p)
		}
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	for _, seg := range strings.Split(path, "/") {
		if seg == "." || seg == ".." {
			return "", fmt.Errorf("the URL's path cannot contain . or ..")
		}
	}
	path = strings.TrimSuffix(path, "/api") // the API's own URL was entered
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + path, nil
}

// IsLoopbackHost reports whether a host name is this computer.
func IsLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

// ParseFingerprint normalizes a SHA-256 certificate fingerprint as tools
// print it (AB:CD:…, ab cd …, SHA256:abcd…) to 64 uppercase hex digits.
// An empty string stays empty (no pinning).
func ParseFingerprint(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if i := strings.IndexByte(s, ':'); i > 0 && strings.EqualFold(strings.ReplaceAll(s[:i], "-", ""), "sha256") {
		s = s[i+1:]
	}
	s = strings.NewReplacer(":", "", " ", "", "-", "").Replace(s)
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("a SHA-256 fingerprint is 64 hexadecimal digits (colons allowed)")
	}
	return strings.ToUpper(s), nil
}

// FormatFingerprint groups a fingerprint by bytes (AB:CD:…), for reading
// it out against the remote server's.
func FormatFingerprint(fp string) string {
	if len(fp)%2 != 0 {
		return fp
	}
	parts := make([]string, 0, len(fp)/2)
	for i := 0; i < len(fp); i += 2 {
		parts = append(parts, fp[i:i+2])
	}
	return strings.Join(parts, ":")
}

// RoleAtLeast reports whether role r is at least need (viewer < operator
// < admin). Site-scoped and unknown roles are below viewer.
func RoleAtLeast(r, need Role) bool {
	rank := func(r Role) int {
		switch r {
		case RoleViewer:
			return 1
		case RoleOperator:
			return 2
		case RoleAdmin:
			return 3
		}
		return 0
	}
	return rank(need) > 0 && rank(r) >= rank(need)
}
