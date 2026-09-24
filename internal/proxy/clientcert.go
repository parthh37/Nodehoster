package proxy

// Client certificates (mutual TLS), per HTTPS binding. The handshake asks
// for a certificate according to the binding its SNI host name selects;
// each request is then checked against the binding its Host header
// selects, which is what decides the site. The two differ when a client
// reuses a connection for another host (HTTP/2 coalescing) or sends a
// Host unlike its SNI name: such a request gets 421 Misdirected Request,
// and browsers retry it on a connection of its own.

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// The headers that carry a client's certificate to the application.
// Whatever a client sends under these names is removed from every
// request, so an application can trust them.
const (
	hdrClientCert        = "X-Client-Cert"             // the certificate, URL-escaped PEM (nginx $ssl_client_escaped_cert)
	hdrClientSubject     = "X-Client-Cert-Subject"     // subject DN, RFC 2253
	hdrClientFingerprint = "X-Client-Cert-Fingerprint" // SHA-256, upper-case hex
	hdrClientVerify      = "X-Client-Verify"           // SUCCESS | NONE | FAILED:<reason>
)

var clientCertHeaders = []string{hdrClientCert, hdrClientSubject, hdrClientFingerprint, hdrClientVerify}

const (
	// Verification results are remembered per certificate for a while, so
	// that each request of a keep-alive connection does not verify the
	// chain again. Bounded per policy.
	clientVerifyTTL   = 5 * time.Minute
	clientVerifyCache = 1024
)

// clientPolicy is a binding's compiled client certificate policy. Policies
// are shared by configuration (key), so their verification caches survive
// reloads that do not change them.
type clientPolicy struct {
	key          string
	mode         string
	pool         *x509.CertPool
	subjects     map[string]bool // lower case
	fingerprints map[string]bool // upper-case hex
	requirePaths []string

	mu    sync.Mutex
	cache map[[32]byte]*clientIdentity
}

// clientIdentity is what a request's client certificate amounts to.
type clientIdentity struct {
	verify      string // SUCCESS | FAILED:<reason>
	cert        string // escaped PEM, subject and fingerprint: set on SUCCESS
	subject     string
	fingerprint string
	until       time.Time
}

var identityNone = &clientIdentity{verify: "NONE"}

func (id *clientIdentity) ok() bool { return id.verify == "SUCCESS" }

// compileClientPolicy builds the policy of a binding; nil when it asks for
// no certificate. The configuration was validated when saved; one that
// does not compile (from an older version) is treated as asking for a
// certificate nobody can have, never as open.
func compileClientPolicy(cfg *model.ClientCertPolicy) (*clientPolicy, error) {
	if !cfg.Active() {
		return nil, nil
	}
	raw, _ := json.Marshal(cfg)
	sum := sha256.Sum256(raw)
	p := &clientPolicy{
		key: hex.EncodeToString(sum[:]), mode: cfg.Mode, pool: x509.NewCertPool(),
		subjects: map[string]bool{}, fingerprints: map[string]bool{},
		requirePaths: cfg.RequirePaths, cache: map[[32]byte]*clientIdentity{},
	}
	cas, err := model.ParseCABundle(cfg.CAPEM)
	for _, c := range cas {
		p.pool.AddCert(c)
	}
	for _, s := range cfg.AllowedSubjects {
		p.subjects[strings.ToLower(strings.TrimSpace(s))] = true
	}
	for _, f := range cfg.AllowedFingerprints {
		p.fingerprints[model.NormalizeFingerprint(f)] = true
	}
	return p, err
}

// clientPolicyFor returns the shared policy for a binding's configuration
// (Reload holds reloadMu).
func (s *Server) clientPolicyFor(b model.Binding) *clientPolicy {
	if b.Protocol != "https" || !b.ClientCert.Active() {
		return nil
	}
	p, err := compileClientPolicy(b.ClientCert)
	if err != nil {
		s.deps.Log.Warn("client certificate authorities of a binding cannot be read; no certificate will be accepted", "binding", b.String(), "err", err)
	}
	if s.policies == nil {
		s.policies = map[string]*clientPolicy{}
	}
	if prev, ok := s.policies[p.key]; ok {
		return prev
	}
	s.policies[p.key] = p
	return p
}

// prunePolicies forgets policies no route uses any more.
func (s *Server) prunePolicies(t *routeTable) {
	used := map[string]bool{}
	for _, routes := range t.byPort {
		for _, r := range routes {
			if r.client != nil {
				used[r.client.key] = true
			}
		}
	}
	for k := range s.policies {
		if !used[k] {
			delete(s.policies, k)
		}
	}
}

// configure asks for a certificate in a handshake. accept verifies later,
// per request, so a certificate that fails verification never fails the
// handshake (the application sees X-Client-Verify: FAILED:...); require
// makes the handshake fail without a certificate the CAs issued, like IIS
// "Require". The CA names are sent either way, so that browsers offer
// only matching certificates.
func (p *clientPolicy) configure(cfg *tls.Config) {
	cfg.ClientCAs = p.pool
	if p.mode == model.ClientCertRequire {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		cfg.ClientAuth = tls.RequestClientCert
	}
}

// identify verifies the certificate a connection presented.
func (p *clientPolicy) identify(cs *tls.ConnectionState, now time.Time) *clientIdentity {
	if cs == nil || len(cs.PeerCertificates) == 0 {
		return identityNone
	}
	leaf := cs.PeerCertificates[0]
	sum := sha256.Sum256(leaf.Raw)
	p.mu.Lock()
	id, ok := p.cache[sum]
	p.mu.Unlock()
	if ok && now.Before(id.until) {
		return id
	}
	id = p.verify(cs.PeerCertificates, sum, now)
	p.mu.Lock()
	if len(p.cache) >= clientVerifyCache {
		clear(p.cache)
	}
	p.cache[sum] = id
	p.mu.Unlock()
	return id
}

func (p *clientPolicy) verify(chain []*x509.Certificate, sum [32]byte, now time.Time) *clientIdentity {
	leaf := chain[0]
	id := &clientIdentity{until: now.Add(clientVerifyTTL)}
	if leaf.NotAfter.Before(id.until) {
		id.until = leaf.NotAfter
	}
	opts := x509.VerifyOptions{
		Roots: p.pool, Intermediates: x509.NewCertPool(), CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	for _, c := range chain[1:] {
		opts.Intermediates.AddCert(c)
	}
	fingerprint := strings.ToUpper(hex.EncodeToString(sum[:]))
	if _, err := leaf.Verify(opts); err != nil {
		id.verify = "FAILED:" + verifyReason(err)
		return id
	}
	if !p.allowed(leaf, fingerprint) {
		id.verify = "FAILED:certificate not allowed"
		return id
	}
	id.verify = "SUCCESS"
	id.cert = escapeComponent(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})))
	id.subject = headerSafe(leaf.Subject.String())
	id.fingerprint = fingerprint
	return id
}

// allowed applies the allow lists, if any.
func (p *clientPolicy) allowed(leaf *x509.Certificate, fingerprint string) bool {
	if len(p.subjects) == 0 && len(p.fingerprints) == 0 {
		return true
	}
	if p.fingerprints[fingerprint] {
		return true
	}
	names := []string{leaf.Subject.CommonName, leaf.Subject.String()}
	names = append(names, leaf.DNSNames...)
	names = append(names, leaf.EmailAddresses...)
	for _, u := range leaf.URIs {
		names = append(names, u.String())
	}
	for _, n := range names {
		if n != "" && p.subjects[strings.ToLower(n)] {
			return true
		}
	}
	return false
}

// required reports whether a request must come with a valid certificate.
func (p *clientPolicy) required(path string) bool {
	if p.mode == model.ClientCertRequire {
		return true
	}
	for _, x := range p.requirePaths {
		if path == x || strings.HasPrefix(path, strings.TrimSuffix(x, "/")+"/") {
			return true
		}
	}
	return false
}

// verifyReason says briefly why a certificate did not verify, for
// X-Client-Verify (like nginx's $ssl_client_verify).
func verifyReason(err error) string {
	var inv x509.CertificateInvalidError
	var unknown x509.UnknownAuthorityError
	switch {
	case errors.As(err, &inv) && inv.Reason == x509.Expired:
		return "certificate expired or not yet valid"
	case errors.As(err, &inv) && inv.Reason == x509.IncompatibleUsage:
		return "certificate not for client authentication"
	case errors.As(err, &unknown):
		return "unknown issuer"
	}
	return "certificate invalid"
}

// tlsRoute is the route a TLS handshake on a port picks by SNI name: the
// binding for the name, else the port's default binding (no host name) so
// clients still get a proper 404 over TLS, else any.
func (t *routeTable) tlsRoute(port int, local net.IP, sni string) *route {
	host := strings.ToLower(sni)
	r := t.match(port, local, host)
	if r == nil && host != "" {
		r = t.match(port, local, "")
	}
	if r == nil {
		for _, rr := range t.byPort[port] {
			r = rr
			break
		}
	}
	return r
}

// handshakeRoute is tlsRoute for a ClientHello, over TCP or QUIC.
func (s *Server) handshakeRoute(hello *tls.ClientHelloInfo) *route {
	port, local := 443, net.IP(nil)
	if hello.Conn != nil {
		if ip, p, ok := addrIPPort(hello.Conn.LocalAddr()); ok {
			port, local = p, ip
		}
	}
	return s.table.Load().tlsRoute(port, local, hello.ServerName)
}

// addrIPPort reads a TCP (HTTP/1.1, HTTP/2) or UDP (HTTP/3) address.
func addrIPPort(a net.Addr) (net.IP, int, bool) {
	switch a := a.(type) {
	case *net.TCPAddr:
		return a.IP, a.Port, true
	case *net.UDPAddr:
		return a.IP, a.Port, true
	}
	return nil, 0, false
}

// clientCertGate applies the client certificate policy of the binding a
// request matched: it removes client-supplied certificate headers, refuses
// requests without the certificate they need, and forwards the verified
// identity. false means the request was answered.
func (s *Server) clientCertGate(w http.ResponseWriter, r *http.Request, t *routeTable, rt *route, port int, local net.IP) bool {
	for _, h := range clientCertHeaders {
		r.Header.Del(h)
	}
	p := rt.client
	if p == nil || r.TLS == nil {
		return true
	}
	// The handshake asked for a certificate as the SNI name's binding
	// says. If that is another policy, or a required certificate is
	// missing (the policy changed since the connection was made), this
	// connection cannot serve the request: 421 makes the client retry on
	// a new connection for this host name.
	hs := t.tlsRoute(port, local, r.TLS.ServerName)
	if hs == nil || hs.client == nil || hs.client.key != p.key ||
		(p.mode == model.ClientCertRequire && len(r.TLS.PeerCertificates) == 0) {
		w.Header().Set("Connection", "close")
		errorPage(w, nil, http.StatusMisdirectedRequest, "This connection cannot be used for this host name. Please try again.")
		return false
	}
	id := p.identify(r.TLS, time.Now())
	if !id.ok() && p.required(r.URL.Path) {
		msg := "A client certificate is required."
		if id != identityNone {
			msg = "Your client certificate is not accepted here (" + strings.TrimPrefix(id.verify, "FAILED:") + ")."
		}
		errorPage(w, rt.site.site.Routing.ErrorPages, http.StatusForbidden, msg)
		return false
	}
	r.Header.Set(hdrClientVerify, id.verify)
	if id.ok() {
		r.Header.Set(hdrClientCert, id.cert)
		r.Header.Set(hdrClientSubject, id.subject)
		r.Header.Set(hdrClientFingerprint, id.fingerprint)
	}
	return true
}

// escapeComponent percent-encodes everything but RFC 3986 unreserved
// characters, which decodeURIComponent and every URL decoder read back.
func escapeComponent(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s) * 3 / 2)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

// headerSafe replaces control characters, which a certificate's subject
// may contain but a header value may not.
func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}
