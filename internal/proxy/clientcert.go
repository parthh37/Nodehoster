package proxy

// Client certificates (mutual TLS), per HTTPS binding. The handshake asks
// for a certificate according to the binding its SNI host name selects;
// each request is then checked against the binding its Host header
// selects, which is what decides the site. The two differ when a client
// reuses a connection for another host (HTTP/2 coalescing) or sends a
// Host unlike its SNI name: such a request gets 421 Misdirected Request,
// and browsers retry it on a connection of its own.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/rewrite"
)

// The headers that carry a client's certificate to the application.
// Whatever a client sends under these names is removed from every
// request, so an application can trust them: also spelled with
// underscores, which CGI-style servers (IIS server variables, WSGI and
// ASGI) read as the same variable, and from the Connection header, where
// a client could list them to have a proxy drop NodeHoster's own copies
// as hop-by-hop headers.
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
	requirePaths []string        // canonical (see requirePrefix); "" = everything

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
		cache: map[[32]byte]*clientIdentity{},
	}
	for _, x := range cfg.RequirePaths {
		p.requirePaths = append(p.requirePaths, requirePrefix(x))
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

// requiredFor reports whether a request for path (decoded; raw is its
// escaped form as received, "" if none) must come with a valid
// certificate. ok is false for a path that cannot be checked safely.
//
// The prefixes are compared with both forms pathForms gives: what an
// application routing on the path as sent sees, and what one that
// resolves dot segments (or a file system) sees. A certificate is
// required if either falls under a prefix.
func (p *clientPolicy) requiredFor(path, raw string) (required, ok bool) {
	if p.mode == model.ClientCertRequire {
		return true, true
	}
	if len(p.requirePaths) == 0 {
		return false, true
	}
	literal, resolved, ok := pathForms(path, raw)
	if !ok {
		return false, false
	}
	for _, x := range p.requirePaths {
		if underPrefix(literal, x) || underPrefix(resolved, x) {
			return true, true
		}
	}
	return false, true
}

// underPrefix: p is x or below it, segment by segment (x "" is the root).
func underPrefix(p, x string) bool {
	return p == x || strings.HasPrefix(p, x+"/")
}

// requirePrefix is a requirePaths entry in the form pathForms gives
// request paths, without a trailing slash, so that "/admin" and "/admin/"
// both cover /admin and everything below it. An entry that cannot be read
// (validated when saved, so only from an older version) covers everything:
// a policy is never more open than configured.
func requirePrefix(x string) string {
	_, resolved, ok := pathForms(x, "")
	if !ok {
		return ""
	}
	return strings.TrimSuffix(resolved, "/")
}

// pathForms canonicalises a decoded request path for requirePaths the
// way the most lenient application or file system behind the proxy could
// read it: ignoring case (IIS, Express and Koa route that way, NTFS
// names are), empty segments (//admin), path parameters (/admin;x, which
// Java servers drop), what follows a colon (NTFS streams) and the
// trailing dots and spaces Windows ignores in file names. literal keeps
// dot segments as they are; resolved resolves them (/x/../admin is
// /admin).
//
// ok is false for a path that cannot be checked safely: an encoded slash
// or backslash (in raw), which applications decode differently; a
// backslash, a separator on Windows but not in URLs; control characters;
// a percent escape left after decoding (a double-encoded path, which IIS
// refuses too); or a segment of only dots and spaces other than . and ..
// (Windows and URL resolution read those differently).
func pathForms(path, raw string) (literal, resolved string, ok bool) {
	if raw != "" {
		u := strings.ToUpper(raw)
		if strings.Contains(u, "%2F") || strings.Contains(u, "%5C") {
			return "", "", false
		}
	}
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c < 0x20 || c == 0x7f || c == '\\' {
			return "", "", false
		}
		if c == '%' && i+2 < len(path) && isHex(path[i+1]) && isHex(path[i+2]) {
			return "", "", false
		}
	}
	var lit, res []string
	for _, seg := range strings.Split(strings.ToLower(path), "/") {
		seg, _, _ = strings.Cut(seg, ";")
		seg, _, _ = strings.Cut(seg, ":")
		trimmed := strings.TrimRight(seg, ". ")
		switch {
		case seg == "":
		case seg == "." || seg == "..":
			lit = append(lit, seg)
			if seg == ".." && len(res) > 0 {
				res = res[:len(res)-1]
			}
		case trimmed == "":
			return "", "", false
		default:
			lit = append(lit, trimmed)
			res = append(res, trimmed)
		}
	}
	return "/" + strings.Join(lit, "/"), "/" + strings.Join(res, "/"), true
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
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

// certCheck is what a request's path is checked against again when a
// later stage changes it (a rewrite rule, a location that strips its
// prefix): the policies with requirePaths that apply to the request and
// the identity it has.
type certCheck struct {
	policies []*clientPolicy
	id       *clientIdentity
}

type certCheckKey struct{}

// clientCertGate applies the client certificate policy of the binding a
// request matched: it removes client-supplied certificate headers, refuses
// requests without the certificate they need, and forwards the verified
// identity. It returns the request to serve, which carries what later
// path checks need; false means the request was answered.
func (s *Server) clientCertGate(w http.ResponseWriter, r *http.Request, t *routeTable, rt *route, port int, local net.IP) (*http.Request, bool) {
	stripClientCertHeaders(r.Header)
	if r.TLS == nil {
		return plainCertGate(w, r, rt)
	}
	p := rt.client
	if p == nil {
		return r, true
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
		return r, false
	}
	id := p.identify(r.TLS, time.Now())
	if !id.ok() {
		c := &certCheck{policies: []*clientPolicy{p}, id: id}
		if !c.allows(w, r, rt.site.site.Routing.ErrorPages, r.URL.Path, r.URL.RawPath) {
			return r, false
		}
		if len(p.requirePaths) > 0 {
			r = r.WithContext(context.WithValue(r.Context(), certCheckKey{}, c))
		}
	}
	r.Header.Set(hdrClientVerify, id.verify)
	if id.ok() {
		r.Header.Set(hdrClientCert, id.cert)
		r.Header.Set(hdrClientSubject, id.subject)
		r.Header.Set(hdrClientFingerprint, id.fingerprint)
	}
	return r, true
}

// plainCertGate applies client certificate policies to a request over
// plain HTTP. An http binding has no policy of its own, but the site's
// https bindings for the same host name may: what they require a
// certificate for is not served over plain HTTP instead (403), unless the
// site redirects plain HTTP to HTTPS, which its pipeline does first.
func plainCertGate(w http.ResponseWriter, r *http.Request, rt *route) (*http.Request, bool) {
	if len(rt.secure) == 0 {
		return r, true
	}
	site := rt.site
	if site.site.Routing.HTTPSRedirect && site.httpsPort != 0 {
		return r, true
	}
	host := strings.ToLower(strings.TrimSuffix(hostOnly(r.Host), "."))
	c := &certCheck{id: identityNone}
	for _, sp := range rt.secure {
		if hostMatches(sp.host, host) && !slices.Contains(c.policies, sp.policy) {
			c.policies = append(c.policies, sp.policy)
		}
	}
	if len(c.policies) == 0 {
		return r, true
	}
	if !c.allows(w, r, site.site.Routing.ErrorPages, r.URL.Path, r.URL.RawPath) {
		return r, false
	}
	return r.WithContext(context.WithValue(r.Context(), certCheckKey{}, c)), true
}

// allows checks a path a request takes against the policies; false means
// the request was answered: 403, or 400 for a path that cannot be checked.
func (c *certCheck) allows(w http.ResponseWriter, r *http.Request, pages map[string]string, path, raw string) bool {
	for _, p := range c.policies {
		required, ok := p.requiredFor(path, raw)
		if !ok {
			errorPage(w, pages, http.StatusBadRequest, "The request path is not valid.")
			return false
		}
		if !required {
			continue
		}
		msg := "A client certificate is required."
		switch {
		case r.TLS == nil:
			msg = "A client certificate is required, which needs HTTPS."
		case c.id != identityNone:
			msg = "Your client certificate is not accepted here (" + strings.TrimPrefix(c.id.verify, "FAILED:") + ")."
		}
		errorPage(w, pages, http.StatusForbidden, msg)
		return false
	}
	return true
}

// clientCertPaths checks the paths a request takes after the client
// certificate gate against the policies that applied there: as URL
// rewrite rules left it, and as a location that strips its prefix passes
// it on (to a mounted site, say). It runs before the response cache, so
// that a stored response is not given to a request refused here. false
// means the request was answered.
func (rt *siteRuntime) clientCertPaths(w http.ResponseWriter, r *http.Request) bool {
	c, _ := r.Context().Value(certCheckKey{}).(*certCheck)
	if c == nil {
		return true
	}
	pages := rt.site.Routing.ErrorPages
	if !c.allows(w, r, pages, r.URL.Path, "") {
		return false
	}
	if _, proxied := r.Context().Value(ctxRewriteProxy).(*rewrite.Result); proxied {
		return true // to another server's URL, not a path of this site
	}
	if loc := rt.locationFor(r.URL.Path); loc != nil && loc.StripPrefix {
		return c.allows(w, r, pages, loc.strip(r.URL.Path), "")
	}
	return true
}

// isClientCertHeader reports whether a header name is, or would be read
// by an application as, one of the client certificate headers.
func isClientCertHeader(name string) bool {
	n := strings.ReplaceAll(name, "_", "-")
	for _, h := range clientCertHeaders {
		if strings.EqualFold(n, h) {
			return true
		}
	}
	return false
}

// stripClientCertHeaders removes what a client sent as client certificate
// headers, and their names from Connection.
func stripClientCertHeaders(h http.Header) {
	for k := range h {
		if isClientCertHeader(k) {
			delete(h, k)
		}
	}
	conn := h.Values("Connection")
	if len(conn) == 0 {
		return
	}
	var keep []string
	changed := false
	for _, v := range conn {
		for _, tok := range strings.Split(v, ",") {
			if tok = strings.TrimSpace(tok); tok == "" {
				continue
			}
			if isClientCertHeader(tok) {
				changed = true
				continue
			}
			keep = append(keep, tok)
		}
	}
	switch {
	case !changed:
	case len(keep) == 0:
		h.Del("Connection")
	default:
		h.Set("Connection", strings.Join(keep, ", "))
	}
}

// copyClientCertHeaders gives a request a reverse proxy forwards the
// client certificate headers of the incoming one, after the proxy removed
// hop-by-hop headers (so that no Connection header can drop them).
func copyClientCertHeaders(out, in http.Header) {
	for _, h := range clientCertHeaders {
		if v := in.Values(h); len(v) > 0 {
			out[h] = slices.Clone(v)
		} else {
			delete(out, h)
		}
	}
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
