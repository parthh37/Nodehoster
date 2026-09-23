package model

import "time"

type CertSource string

const (
	CertACME       CertSource = "acme"
	CertImported   CertSource = "imported"
	CertSelfSigned CertSource = "selfsigned"
)

type ACMEOptions struct {
	Challenge     string `json:"challenge"`               // http-01 | dns-01
	DNSProviderID string `json:"dnsProviderId,omitempty"` // references Settings.DNSProviders
	KeyType       string `json:"keyType,omitempty"`       // ec256 | ec384 | rsa2048 | rsa4096
}

// Certificate is an entry in the server certificate store (IIS "Server
// Certificates"). PEM material lives on disk under data/certs/<id>/.
type Certificate struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Source      CertSource   `json:"source"`
	Domains     []string     `json:"domains"`
	ACME        *ACMEOptions `json:"acme,omitempty"`
	AutoRenew   bool         `json:"autoRenew"`
	Managed     bool         `json:"managed"` // created automatically for a binding with certMode=auto
	Status      string       `json:"status"`  // pending | valid | error | expired
	LastError   string       `json:"lastError,omitempty"`
	Issuer      string       `json:"issuer,omitempty"`
	Subject     string       `json:"subject,omitempty"`
	Serial      string       `json:"serial,omitempty"`
	Fingerprint string       `json:"fingerprint,omitempty"` // SHA-256
	NotBefore   *time.Time   `json:"notBefore,omitempty"`
	NotAfter    *time.Time   `json:"notAfter,omitempty"`
	RenewAfter  *time.Time   `json:"renewAfter,omitempty"`
	LastAttempt *time.Time   `json:"lastAttempt,omitempty"`
	CreatedAt   time.Time    `json:"createdAt"`
	UpdatedAt   time.Time    `json:"updatedAt"`
}

type DNSProvider struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Provider    string            `json:"provider"`    // lego provider code: cloudflare, route53, ...
	Credentials map[string]string `json:"credentials"` // env-style keys, secret
}

type WebhookTarget struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Format  string   `json:"format"` // generic | slack | teams | discord
	Events  []string `json:"events"` // empty = all
	Enabled bool     `json:"enabled"`
}

type ACMESettings struct {
	Email           string `json:"email"`
	Directory       string `json:"directory"` // letsencrypt | letsencrypt-staging | zerossl | https://...
	EABKeyID        string `json:"eabKeyId,omitempty"`
	EABHMAC         string `json:"eabHmac,omitempty"` // secret
	KeyType         string `json:"keyType"`
	AgreeTOS        bool   `json:"agreeTos"`
	RenewBeforeDays int    `json:"renewBeforeDays"` // 0 = renew at 2/3 of lifetime
}

type TLSSettings struct {
	MinVersion string `json:"minVersion"` // "1.2" | "1.3"
	HTTP2      bool   `json:"http2"`
}

type ProxySettings struct {
	ServerHeader       string   `json:"serverHeader"`
	TrustedProxies     []string `json:"trustedProxies,omitempty"` // CIDRs whose X-Forwarded-For is trusted
	DefaultPageHTML    string   `json:"defaultPageHtml,omitempty"`
	ReadHeaderTimeoutS int      `json:"readHeaderTimeoutSec"`
	IdleTimeoutS       int      `json:"idleTimeoutSec"`
}

// Settings are server-wide and editable from the UI. Bootstrap settings that
// are needed before the database opens live in config.Bootstrap instead.
type Settings struct {
	ACME               ACMESettings    `json:"acme"`
	TLS                TLSSettings     `json:"tls"`
	Proxy              ProxySettings   `json:"proxy"`
	PortRangeStart     int             `json:"portRangeStart"`
	PortRangeEnd       int             `json:"portRangeEnd"`
	DefaultNodeVersion string          `json:"defaultNodeVersion"` // "" = node on PATH
	DNSProviders       []DNSProvider   `json:"dnsProviders"`
	Webhooks           []WebhookTarget `json:"webhooks"`
	LogMaxSizeMB       int             `json:"logMaxSizeMB"`
	LogMaxFiles        int             `json:"logMaxFiles"`
	LogRetentionDays   int             `json:"logRetentionDays"`
	CertExpiryWarnDays int             `json:"certExpiryWarnDays"`
	Mime               MimeSettings    `json:"mime"`
	Mail               MailSettings    `json:"mail"`
	SSO                SSOSettings     `json:"sso"`
}

// MimeSettings are the server-wide MIME types (IIS "MIME Types" at the
// server level), added to or overriding the built-in table. Sites can add
// their own on top.
type MimeSettings struct {
	Types        []MimeMap `json:"types"`
	UnknownTypes string    `json:"unknownTypes"` // serve | deny
}

type Role string

const (
	RoleAdmin    Role = "admin"    // everything
	RoleOperator Role = "operator" // start/stop/deploy, no settings, users or secrets
	RoleViewer   Role = "viewer"   // read-only
	// RoleSites makes a user site-scoped, like an IIS Manager user granted
	// permission on individual sites: they have no rights on the server
	// itself, only the role of each grant in User.Sites on that site. A
	// separate role value (rather than, say, "viewer" plus grants) makes
	// every server-wide check fail closed for them.
	RoleSites Role = "sites"
)

// SiteGrant gives a site-scoped user a role on one site. It is viewer or
// operator, never admin: a site's configuration (application folder, the
// account it runs as, bindings, environment) can take over the server or
// other sites' host names, so editing it stays with server administrators.
type SiteGrant struct {
	SiteID string `json:"siteId"`
	Role   Role   `json:"role"` // viewer | operator
}

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     Role   `json:"role"`
	// Sites are the grants of a site-scoped user (Role "sites"); always
	// empty for everyone else. Grants are by site ID, so renaming a site
	// keeps them; deleting it removes them.
	Sites        []SiteGrant `json:"sites,omitempty"`
	PasswordHash string      `json:"-"`
	TOTPSecret   string      `json:"-"`
	TOTPEnabled  bool        `json:"totpEnabled"`
	Disabled     bool        `json:"disabled"`
	LastLogin    *time.Time  `json:"lastLogin,omitempty"`
	CreatedAt    time.Time   `json:"createdAt"`
	// SSO marks a user created by a single sign-on. They have no password
	// (so cannot sign in with one) until an administrator sets one, which
	// makes them an ordinary user.
	SSO bool `json:"sso,omitempty"`
}

type APIToken struct {
	ID     string `json:"id"`
	UserID string `json:"userId"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"` // first characters, for identification
	Hash   string `json:"-"`
	// Role and SiteIDs restrict the token to part of its owner's access,
	// e.g. a CI token that can only deploy one site. The token never has
	// more than its owner has now: the effective access is the
	// intersection, so downgrading the user downgrades their tokens.
	Role      Role       `json:"role,omitempty"` // "" = the owner's role
	SiteIDs   []string   `json:"siteIds"`        // nil (JSON null) = every site the owner can access
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// Restricted reports whether the token has less than its owner's access.
func (t *APIToken) Restricted() bool { return t.Role != "" || t.SiteIDs != nil }

type AuditEntry struct {
	ID     int64     `json:"id"`
	Time   time.Time `json:"time"`
	User   string    `json:"user"`
	IP     string    `json:"ip"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail,omitempty"`
}

// Event is an operational event (crash, restart, certificate renewal...),
// shown on the dashboard and delivered to webhooks.
type Event struct {
	ID      int64     `json:"id"`
	Time    time.Time `json:"time"`
	Level   string    `json:"level"` // info | warning | error
	Type    string    `json:"type"`  // site.crashed, cert.renewed, ...
	SiteID  string    `json:"siteId,omitempty"`
	Message string    `json:"message"`
}
