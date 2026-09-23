package model

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// MailSettings configure the SMTP virtual server: a send-only relay in the
// style of the IIS 6 SMTP service. Applications on this server (or on the
// allowed addresses) submit mail over SMTP or drop .eml files in the
// pickup folder; NodeHoster queues it on disk and delivers it, directly to
// the recipients' mail servers or through a smart host, retrying until it
// expires. It never accepts mail for local mailboxes.
type MailSettings struct {
	Enabled  bool   `json:"enabled"`
	ListenIP string `json:"listenIp"`           // "" = all addresses
	Port     int    `json:"port"`               // 25
	Hostname string `json:"hostname,omitempty"` // fully-qualified name for EHLO and Received; "" = computer name

	// Connection and relay restrictions: only these clients may connect.
	AllowIPs []string `json:"allowIps"`
	// Authentication. Without RequireAuth, allowed clients relay without
	// logging in (the IIS default for 127.0.0.1).
	RequireAuth bool       `json:"requireAuth"`
	Users       []MailUser `json:"users,omitempty"`
	// STARTTLS certificate from the certificate store; "" = no STARTTLS.
	// Passwords are only accepted over TLS or from this computer.
	CertificateID string `json:"certificateId,omitempty"`

	AllowedSenderDomains []string `json:"allowedSenderDomains,omitempty"` // MAIL FROM domains; empty = any
	MaxMessageMB         int      `json:"maxMessageMB"`
	MaxRecipients        int      `json:"maxRecipients"`

	Delivery  string        `json:"delivery"` // direct | smarthost
	SmartHost MailSmartHost `json:"smartHost"`

	ExpireHours    int `json:"expireHours"`    // give up on a message after this long
	KeepFailedDays int `json:"keepFailedDays"` // how long undeliverable mail is kept (IIS Badmail)

	DKIM []DKIMKey `json:"dkim,omitempty"`

	// Pickup directory: .eml files written to data\mail\pickup are sent.
	PickupDirectory bool `json:"pickupDirectory"`
}

type MailUser struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash,omitempty"` // bcrypt; set via Password on write
	Password     string `json:"password,omitempty"`     // write-only
}

// MailSmartHost is the relay all mail is handed to when Delivery is
// "smarthost": SendGrid, Amazon SES, Microsoft 365, the ISP's server...
type MailSmartHost struct {
	Host               string `json:"host"`
	Port               int    `json:"port"`     // 587
	Security           string `json:"security"` // starttls | tls | none
	Username           string `json:"username,omitempty"`
	Password           string `json:"password,omitempty"` // secret
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
}

// DKIMKey signs mail from Domain. A key without PrivateKey gets a new
// RSA-2048 key when the settings are saved; DNSRecord is what to publish
// at <selector>._domainkey.<domain> and is filled in by the server.
type DKIMKey struct {
	Domain     string `json:"domain"`
	Selector   string `json:"selector"`
	Enabled    bool   `json:"enabled"`
	PrivateKey string `json:"privateKey,omitempty"` // PEM, secret
	DNSName    string `json:"dnsName,omitempty"`    // read-only
	DNSRecord  string `json:"dnsRecord,omitempty"`  // read-only TXT value
}

// Mail message and recipient states.
const (
	MailQueued    = "queued"    // waiting for its first or next attempt
	MailSending   = "sending"   // an attempt is in progress
	MailFailed    = "failed"    // undeliverable; kept for KeepFailedDays
	RcptPending   = "pending"   // not delivered yet
	RcptDelivered = "delivered" // accepted by the next server
	RcptFailed    = "failed"    // rejected permanently or expired
)

// MailMessage is a message in the queue, without its content.
type MailMessage struct {
	ID          string          `json:"id"`
	From        string          `json:"from"`
	Recipients  []MailRecipient `json:"recipients"`
	Subject     string          `json:"subject,omitempty"`
	Size        int64           `json:"size"`
	Source      string          `json:"source"` // smtp | pickup | test
	ClientIP    string          `json:"clientIp,omitempty"`
	User        string          `json:"user,omitempty"` // authenticated SMTP user
	State       string          `json:"state"`
	Attempts    int             `json:"attempts"`
	ReceivedAt  time.Time       `json:"receivedAt"`
	NextAttempt *time.Time      `json:"nextAttempt,omitempty"`
	LastAttempt *time.Time      `json:"lastAttempt,omitempty"`
	LastError   string          `json:"lastError,omitempty"`
}

type MailRecipient struct {
	Address     string     `json:"address"`
	State       string     `json:"state"`
	Error       string     `json:"error,omitempty"`
	DeliveredAt *time.Time `json:"deliveredAt,omitempty"`
}

// MailStatus is the SMTP server at a glance.
type MailStatus struct {
	Enabled   bool      `json:"enabled"`
	Listening bool      `json:"listening"`
	Addr      string    `json:"addr,omitempty"`
	Error     string    `json:"error,omitempty"` // why it is not listening
	Queued    int       `json:"queued"`
	Failed    int       `json:"failed"`
	Accepted  int64     `json:"accepted"`  // messages received since the service started
	Delivered int64     `json:"delivered"` // recipients delivered since the service started
	Bounced   int64     `json:"bounced"`   // recipients failed since the service started
	Since     time.Time `json:"since"`
}

// MailTest is the body of POST /mail/test.
type MailTest struct {
	From string `json:"from,omitempty"`
	To   string `json:"to"`
}

var (
	dkimSelectorRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
	mailDomainRe   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
)

// ApplyDefaults fills zero values; settings saved before the SMTP server
// existed decode with an all-zero MailSettings.
func (m *MailSettings) ApplyDefaults() {
	if m.Port == 0 {
		m.Port = 25
	}
	if m.AllowIPs == nil {
		m.AllowIPs = []string{"127.0.0.1", "::1"}
	}
	if m.MaxMessageMB <= 0 {
		m.MaxMessageMB = 25
	}
	if m.MaxRecipients <= 0 {
		m.MaxRecipients = 100
	}
	if m.Delivery == "" {
		m.Delivery = "direct"
	}
	if m.SmartHost.Security == "" {
		m.SmartHost.Security = "starttls"
	}
	if m.SmartHost.Port == 0 {
		m.SmartHost.Port = 587
	}
	if m.ExpireHours <= 0 {
		m.ExpireHours = 48
	}
	if m.KeepFailedDays <= 0 {
		m.KeepFailedDays = 7
	}
	m.ListenIP = strings.TrimSpace(m.ListenIP)
	if m.ListenIP == "*" {
		m.ListenIP = ""
	}
	m.Hostname = strings.ToLower(strings.TrimSpace(m.Hostname))
	for i := range m.DKIM {
		d := &m.DKIM[i]
		d.Domain = strings.ToLower(strings.TrimSpace(d.Domain))
		d.Selector = strings.ToLower(strings.TrimSpace(d.Selector))
	}
	for i, d := range m.AllowedSenderDomains {
		m.AllowedSenderDomains[i] = strings.ToLower(strings.TrimSpace(d))
	}
}

// Validate checks the SMTP server settings. Field paths are relative to
// Settings ("mail.port").
func (m *MailSettings) Validate() error {
	if m.ListenIP != "" && net.ParseIP(m.ListenIP) == nil {
		return verr("mail.listenIp", "not an IP address")
	}
	if m.Port < 1 || m.Port > 65535 {
		return verr("mail.port", "must be between 1 and 65535")
	}
	if m.Hostname != "" && !hostRe.MatchString(m.Hostname) {
		return verr("mail.hostname", "not a host name")
	}
	for i, c := range m.AllowIPs {
		if _, err := ParseCIDROrIP(c); err != nil {
			return verr(fmt.Sprintf("mail.allowIps[%d]", i), "%v", err)
		}
	}
	if m.Enabled && len(m.AllowIPs) == 0 {
		return verr("mail.allowIps", "allow at least one address, e.g. 127.0.0.1")
	}
	users := map[string]bool{}
	for i, u := range m.Users {
		f := fmt.Sprintf("mail.users[%d]", i)
		if strings.TrimSpace(u.Username) == "" || strings.ContainsAny(u.Username, " \t\x00") {
			return verr(f+".username", "enter a user name without spaces")
		}
		if users[strings.ToLower(u.Username)] {
			return verr(f+".username", "%q is listed twice", u.Username)
		}
		users[strings.ToLower(u.Username)] = true
		if u.PasswordHash == "" && u.Password == "" {
			return verr(f+".password", "a password is required")
		}
	}
	if m.RequireAuth && len(m.Users) == 0 {
		return verr("mail.users", "add at least one user, or turn off authentication")
	}
	for i, d := range m.AllowedSenderDomains {
		if !mailDomainRe.MatchString(d) {
			return verr(fmt.Sprintf("mail.allowedSenderDomains[%d]", i), "%q is not a domain name", d)
		}
	}
	if m.MaxMessageMB > 150 {
		return verr("mail.maxMessageMB", "must be 150 MB or less")
	}
	switch m.Delivery {
	case "direct":
	case "smarthost":
		h := m.SmartHost
		if strings.TrimSpace(h.Host) == "" || strings.ContainsAny(h.Host, " /:") {
			return verr("mail.smartHost.host", "enter the smart host's name, e.g. smtp.sendgrid.net")
		}
		if h.Port < 1 || h.Port > 65535 {
			return verr("mail.smartHost.port", "must be between 1 and 65535")
		}
		switch h.Security {
		case "starttls", "tls", "none":
		default:
			return verr("mail.smartHost.security", "must be starttls, tls or none")
		}
		if h.Username != "" && h.Security == "none" {
			return verr("mail.smartHost.security", "a password is only sent over TLS; choose STARTTLS or TLS")
		}
	default:
		return verr("mail.delivery", "must be direct or smarthost")
	}
	if m.ExpireHours > 24*30 {
		return verr("mail.expireHours", "must be 720 hours (30 days) or less")
	}
	dk := map[string]bool{}
	for i, d := range m.DKIM {
		f := fmt.Sprintf("mail.dkim[%d]", i)
		if !mailDomainRe.MatchString(d.Domain) {
			return verr(f+".domain", "not a domain name")
		}
		if !dkimSelectorRe.MatchString(d.Selector) {
			return verr(f+".selector", "use letters, digits and '-', e.g. nodehoster")
		}
		if d.Enabled {
			if dk[d.Domain] {
				return verr(f+".domain", "another enabled key signs %s", d.Domain)
			}
			dk[d.Domain] = true
		}
	}
	return nil
}
