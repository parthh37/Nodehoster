package model

import (
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Client certificate modes of an HTTPS binding, like IIS "SSL Settings" ›
// Client certificates: Ignore, Accept, Require.
const (
	ClientCertIgnore  = "ignore"  // no certificate is asked for (also "")
	ClientCertAccept  = "accept"  // asked for; clients without one are served too
	ClientCertRequire = "require" // the TLS handshake fails without a trusted one
)

// Limits on a client certificate policy, so that a binding stays a small
// JSON document and a handshake a cheap one.
const (
	maxCABundleBytes    = 256 << 10
	maxCABundleCerts    = 100
	maxClientCertAllows = 1000
)

// ClientCertPolicy asks HTTPS clients for a certificate (mutual TLS) on a
// binding. Bindings that share an IP address and port pick their policy by
// SNI host name; a binding without a host name is the default for clients
// that send none. The verified identity reaches the application in
// X-Client-Cert, X-Client-Cert-Subject, X-Client-Cert-Fingerprint and
// X-Client-Verify request headers (client-supplied copies are removed).
type ClientCertPolicy struct {
	Mode string `json:"mode,omitempty"` // ignore | accept | require
	// CAPEM are the trusted issuing CAs, PEM (a self-signed client
	// certificate may be listed to trust exactly that certificate). CA
	// certificates are public: stored as they are, not as secrets.
	CAPEM string `json:"caPem,omitempty"`
	// Allow lists: when either is set, a verified certificate must also
	// match one entry. Subjects match the subject common name, the whole
	// subject DN (as in X-Client-Cert-Subject) or a DNS, email or URI
	// subject alternative name, ignoring case. Fingerprints are SHA-256
	// of the certificate, hex (colons and spaces allowed).
	AllowedSubjects     []string `json:"allowedSubjects,omitempty"`
	AllowedFingerprints []string `json:"allowedFingerprints,omitempty"`
	// RequirePaths (mode accept): path prefixes answered 403 without a
	// valid certificate. TLS 1.3 cannot ask for a certificate after the
	// handshake, so "require for /admin" is a certificate accepted
	// everywhere and enforced here.
	RequirePaths []string `json:"requirePaths,omitempty"`
}

// Active reports whether the policy asks clients for a certificate.
func (p *ClientCertPolicy) Active() bool {
	return p != nil && (p.Mode == ClientCertAccept || p.Mode == ClientCertRequire)
}

func (p *ClientCertPolicy) applyDefaults() {
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	if p.Mode == "" {
		p.Mode = ClientCertIgnore
	}
	p.CAPEM = strings.TrimSpace(p.CAPEM)
	p.AllowedSubjects = trimList(p.AllowedSubjects)
	var fps []string
	for _, f := range trimList(p.AllowedFingerprints) {
		fps = append(fps, NormalizeFingerprint(f))
	}
	p.AllowedFingerprints = fps
	p.RequirePaths = trimList(p.RequirePaths)
}

func (p *ClientCertPolicy) empty() bool {
	return p.Mode == ClientCertIgnore && p.CAPEM == "" && len(p.AllowedSubjects) == 0 &&
		len(p.AllowedFingerprints) == 0 && len(p.RequirePaths) == 0
}

// validate checks the policy; field is its path ("bindings[0].clientCert").
// An ignored policy keeps what was entered unchecked, so that switching it
// off does not lose (or trip over) the configuration.
func (p *ClientCertPolicy) validate(field string) error {
	switch p.Mode {
	case ClientCertIgnore:
		return nil
	case ClientCertAccept, ClientCertRequire:
	default:
		return verr(field+".mode", "must be ignore, accept or require")
	}
	if _, err := ParseCABundle(p.CAPEM); err != nil {
		return verr(field+".caPem", "%s", err.Error())
	}
	if len(p.AllowedSubjects)+len(p.AllowedFingerprints) > maxClientCertAllows {
		return verr(field, "at most %d allowed subjects and fingerprints", maxClientCertAllows)
	}
	for i, s := range p.AllowedSubjects {
		if len(s) > 1024 {
			return verr(fmt.Sprintf("%s.allowedSubjects[%d]", field, i), "too long")
		}
	}
	for i, f := range p.AllowedFingerprints {
		if b, err := hex.DecodeString(f); err != nil || len(b) != 32 {
			return verr(fmt.Sprintf("%s.allowedFingerprints[%d]", field, i), "%q is not a SHA-256 fingerprint (64 hex digits)", f)
		}
	}
	for i, path := range p.RequirePaths {
		if !strings.HasPrefix(path, "/") {
			return verr(fmt.Sprintf("%s.requirePaths[%d]", field, i), "must start with /")
		}
	}
	return nil
}

// ParseCABundle reads the certificates of a PEM bundle: at least one, and
// nothing that is not a certificate.
func ParseCABundle(bundle string) ([]*x509.Certificate, error) {
	if strings.TrimSpace(bundle) == "" {
		return nil, errors.New("add the certificate authorities that issue the client certificates (PEM)")
	}
	if len(bundle) > maxCABundleBytes {
		return nil, fmt.Errorf("the bundle is larger than %d KB", maxCABundleBytes>>10)
	}
	var out []*x509.Certificate
	rest := []byte(bundle)
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("the bundle contains a %s; only certificates belong here", strings.ToLower(b.Type))
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, fmt.Errorf("certificate %d cannot be read: %v", len(out)+1, err)
		}
		out = append(out, c)
		if len(out) > maxCABundleCerts {
			return nil, fmt.Errorf("at most %d certificates", maxCABundleCerts)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no PEM certificate found (-----BEGIN CERTIFICATE-----)")
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("text after the last certificate is not PEM")
	}
	return out, nil
}

// NormalizeFingerprint writes a SHA-256 fingerprint as the certificate
// store does: upper-case hex without separators.
func NormalizeFingerprint(f string) string {
	f = strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(f))
	return strings.ToUpper(f)
}

func trimList(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// applyTLSDefaults normalizes a binding's TLS options (called from
// Site.ApplyDefaults): http bindings have none, and a policy that asks for
// nothing is dropped so documents stay as they were.
func (b *Binding) applyTLSDefaults() {
	if b.Protocol != "https" {
		b.ClientCert = nil
		return
	}
	if b.ClientCert != nil {
		b.ClientCert.applyDefaults()
		if b.ClientCert.empty() {
			b.ClientCert = nil
		}
	}
}

// validateTLS checks a binding's TLS options; f is its path.
func (b *Binding) validateTLS(f string) error {
	if b.ClientCert == nil {
		return nil
	}
	return b.ClientCert.validate(f + ".clientCert")
}

// OCSP states of a certificate (OCSPStatus.State).
const (
	OCSPNone    = "none"    // no responder URL (Let's Encrypt since 2025, self-signed): nothing to staple
	OCSPPending = "pending" // not asked yet
	OCSPGood    = "good"
	OCSPRevoked = "revoked"
	OCSPUnknown = "unknown" // the responder does not know the certificate
	OCSPError   = "error"   // no valid answer could be obtained
)

// OCSPStatus is what OCSP stapling knows about a certificate. It is runtime
// state (kept with the certificate files, not in its record) and appears as
// "ocsp" in the certificates API.
type OCSPStatus struct {
	State      string `json:"state"`
	Responder  string `json:"responder,omitempty"`
	MustStaple bool   `json:"mustStaple,omitempty"` // the certificate has the TLS Feature (status_request) extension
	// Stapled: handshakes carry a valid response now.
	Stapled          bool       `json:"stapled"`
	ThisUpdate       *time.Time `json:"thisUpdate,omitempty"`
	NextUpdate       *time.Time `json:"nextUpdate,omitempty"`
	RevokedAt        *time.Time `json:"revokedAt,omitempty"`
	RevocationReason string     `json:"revocationReason,omitempty"`
	LastCheck        *time.Time `json:"lastCheck,omitempty"`
	NextCheck        *time.Time `json:"nextCheck,omitempty"`
	LastError        string     `json:"lastError,omitempty"`
}

// Summary is the status in a few words, for lists (command line, NodeHoster
// Manager): "stapled, until 2026-10-01", "no responder", "revoked".
func (s *OCSPStatus) Summary() string {
	if s == nil {
		return "-"
	}
	var out string
	switch s.State {
	case OCSPNone:
		return "no responder"
	case OCSPPending:
		out = "not checked yet"
	case OCSPGood:
		out = "good"
		if s.Stapled {
			out = "stapled"
		}
		if s.NextUpdate != nil {
			out += ", until " + s.NextUpdate.Local().Format("2006-01-02 15:04")
		}
	case OCSPRevoked:
		out = "REVOKED"
		if s.RevokedAt != nil {
			out += " " + s.RevokedAt.Local().Format("2006-01-02")
		}
		if s.RevocationReason != "" {
			out += " (" + s.RevocationReason + ")"
		}
	case OCSPUnknown:
		out = "unknown to the responder"
	default:
		out = "error"
		if s.LastError != "" {
			out += ": " + s.LastError
		}
		return out
	}
	if s.MustStaple && !s.Stapled {
		out += "; Must-Staple without a staple"
	}
	return out
}

// TLSView is GET/PUT /api/tls: the server-wide TLS settings and the
// HTTP/3 (QUIC, UDP) listeners they opened.
type TLSView struct {
	TLSSettings
	HTTP3Listeners []string `json:"http3Listeners"`
}
