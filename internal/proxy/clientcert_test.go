package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/certs"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/procmgr"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/store"
)

// tlsEnv is a proxy Server with real listeners, a certificate store and an
// upstream that reports the headers it received.
type tlsEnv struct {
	t     *testing.T
	s     *Server
	certs *certs.Manager
	roots *x509.CertPool // trusts the server certificates
	port  int
	up    *httptest.Server
	seen  chan http.Header

	mu       sync.Mutex
	settings model.Settings
}

func newTLSEnv(t *testing.T) *tlsEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secrets.Open(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	e := &tlsEnv{t: t, settings: model.DefaultSettings(), seen: make(chan http.Header, 16), roots: x509.NewCertPool()}
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New(st, lg, e.Settings, nil)
	e.certs = certs.New(st, box, filepath.Join(dir, "certs"), filepath.Join(dir, "acme"), lg, bus, e.Settings)
	e.s = &Server{
		deps:      Deps{Log: lg, Bus: bus, Certs: e.certs, Settings: e.Settings},
		stdLog:    log.New(io.Discard, "", 0),
		listeners: map[string]*listener{},
		failed:    map[string]error{},
		stats:     map[string]*siteStats{},
		backends:  func(string) []*procmgr.Backend { return nil },
	}
	e.s.table.Store(&routeTable{byPort: map[int][]*route{}, sites: map[string]*siteRuntime{}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		e.s.Shutdown(ctx)
	})
	e.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case e.seen <- r.Header.Clone():
		default:
		}
		io.WriteString(w, "ok "+r.Host)
	}))
	t.Cleanup(e.up.Close)
	e.port = freePort(t)
	return e
}

func (e *tlsEnv) Settings() model.Settings {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.settings
}

func (e *tlsEnv) setHTTP3(on bool) {
	e.mu.Lock()
	e.settings.TLS.HTTP3 = on
	e.mu.Unlock()
}

// freePort is a port free for TCP and UDP on loopback.
func freePort(t *testing.T) int {
	t.Helper()
	for range 20 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		if u, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); err == nil {
			u.Close()
			return port
		}
	}
	t.Fatal("no free port")
	return 0
}

// serverCert creates a self-signed certificate in the store for hosts.
func (e *tlsEnv) serverCert(hosts ...string) string {
	e.t.Helper()
	c, err := e.certs.SelfSigned(context.Background(), "", hosts, 30)
	if err != nil {
		e.t.Fatal(err)
	}
	e.roots.AddCert(e.certs.Get(c.ID).Leaf)
	return c.ID
}

// binding is an https binding on the env's port on loopback.
func (e *tlsEnv) binding(host, certID string, cc *model.ClientCertPolicy) model.Binding {
	return model.Binding{ID: host, Protocol: "https", IP: "127.0.0.1", Port: e.port, Host: host,
		CertMode: model.CertModeManual, CertificateID: certID, ClientCert: cc}
}

// apply loads sites made of the bindings (one site per binding).
func (e *tlsEnv) apply(bindings ...model.Binding) {
	e.t.Helper()
	var sites []*model.Site
	for i, b := range bindings {
		site := &model.Site{ID: fmt.Sprint("site", i), Name: fmt.Sprint("site", i), Type: model.SiteProxy,
			Proxy: &model.ProxyConfig{Upstreams: []model.Upstream{{URL: e.up.URL}}}, Bindings: []model.Binding{b}}
		site.ApplyDefaults()
		if err := site.Validate(); err != nil {
			e.t.Fatal(err)
		}
		sites = append(sites, site)
	}
	e.s.Reload(sites, func(*model.Site) bool { return true })
	if l := e.s.Listeners(); !slices.Contains(l, "https 127.0.0.1:"+strconv.Itoa(e.port)) {
		e.t.Fatalf("listeners: %v", l)
	}
}

// client is an HTTP/1.1 client with SNI sni, presenting cert (if any)
// when asked, even if the server names other CAs.
func (e *tlsEnv) client(sni string, cert *tls.Certificate) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: e.tlsConfig(sni, cert)}}
}

func (e *tlsEnv) tlsConfig(sni string, cert *tls.Certificate) *tls.Config {
	cfg := &tls.Config{RootCAs: e.roots, ServerName: sni}
	if cert != nil {
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return cert, nil }
	}
	return cfg
}

type reply struct {
	status int
	body   string
	header http.Header
	seen   http.Header // what the application received; nil if nothing
}

// get requests path with Host host over a client.
func (e *tlsEnv) get(c *http.Client, host, path string, hdr ...string) (reply, error) {
	e.t.Helper()
	for len(e.seen) > 0 {
		<-e.seen
	}
	req, _ := http.NewRequest(http.MethodGet, "https://127.0.0.1:"+strconv.Itoa(e.port)+path, nil)
	req.Host = host
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	r := reply{status: resp.StatusCode, body: string(body), header: resp.Header}
	select {
	case r.seen = <-e.seen:
	default:
	}
	return r, nil
}

func (e *tlsEnv) mustGet(c *http.Client, host, path string, hdr ...string) reply {
	e.t.Helper()
	r, err := e.get(c, host, path, hdr...)
	if err != nil {
		e.t.Fatalf("GET %s%s: %v", host, path, err)
	}
	return r
}

// clientCA issues client certificates.
type clientCA struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pem  string
}

func newClientCA(t *testing.T, name string) *clientCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &clientCA{key: key, cert: c, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

// issue makes a client certificate; notAfter zero = a day from now.
func (ca *clientCA) issue(t *testing.T, cn string, notAfter time.Time) *tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if notAfter.IsZero() {
		notAfter = time.Now().Add(24 * time.Hour)
	}
	u, _ := url.Parse("spiffe://example.com/device/7")
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn, Organization: []string{"Example"}},
		NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: notAfter, URIs: []*url.URL{u},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func fingerprintOf(c *tls.Certificate) string {
	sum := sha256.Sum256(c.Certificate[0])
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func TestClientCertRequire(t *testing.T) {
	e := newTLSEnv(t)
	ca := newClientCA(t, "Devices CA")
	e.apply(e.binding("a.example.com", e.serverCert("a.example.com"), &model.ClientCertPolicy{Mode: "require", CAPEM: ca.pem}))
	cert := ca.issue(t, "device-1", time.Time{})

	if _, err := e.get(e.client("a.example.com", nil), "a.example.com", "/"); err == nil {
		t.Fatal("the handshake succeeded without a client certificate")
	}
	other := newClientCA(t, "Somebody Else").issue(t, "intruder", time.Time{})
	if _, err := e.get(e.client("a.example.com", other), "a.example.com", "/"); err == nil {
		t.Fatal("the handshake succeeded with an untrusted certificate")
	}

	r := e.mustGet(e.client("a.example.com", cert), "a.example.com", "/", hdrClientSubject, "CN=admin")
	if r.status != http.StatusOK || r.seen == nil {
		t.Fatalf("%d %s", r.status, r.body)
	}
	h := r.seen
	if h.Get(hdrClientVerify) != "SUCCESS" || h.Get(hdrClientSubject) != "CN=device-1,O=Example" || h.Get(hdrClientFingerprint) != fingerprintOf(cert) {
		t.Fatalf("identity headers: %v", h)
	}
	pemText, err := url.PathUnescape(h.Get(hdrClientCert))
	if err != nil || strings.ContainsAny(h.Get(hdrClientCert), " \n+/=") {
		t.Fatalf("X-Client-Cert is not escaped: %q", h.Get(hdrClientCert))
	}
	if b, _ := pem.Decode([]byte(pemText)); b == nil || string(b.Bytes) != string(cert.Certificate[0]) {
		t.Fatalf("X-Client-Cert does not carry the certificate: %q", pemText)
	}
}

// TestClientCertAccept: accept serves everyone, tells the application
// what it got, and enforces per-path requirements over HTTP.
func TestClientCertAccept(t *testing.T) {
	e := newTLSEnv(t)
	ca := newClientCA(t, "Devices CA")
	e.apply(e.binding("a.example.com", e.serverCert("a.example.com"),
		&model.ClientCertPolicy{Mode: "accept", CAPEM: ca.pem, RequirePaths: []string{"/admin"}}))

	anon := e.client("a.example.com", nil)
	r := e.mustGet(anon, "a.example.com", "/", hdrClientVerify, "SUCCESS", hdrClientCert, "forged")
	if r.status != 200 || r.seen.Get(hdrClientVerify) != "NONE" || r.seen.Get(hdrClientCert) != "" {
		t.Fatalf("anonymous: %d %v", r.status, r.seen)
	}
	for _, p := range []string{"/admin", "/admin/users"} {
		if r := e.mustGet(anon, "a.example.com", p); r.status != http.StatusForbidden || r.seen != nil || !strings.Contains(r.body, "client certificate is required") {
			t.Fatalf("%s without a certificate: %d %s", p, r.status, r.body)
		}
	}
	if r := e.mustGet(anon, "a.example.com", "/administrator"); r.status != 200 {
		t.Fatalf("/administrator is not under /admin: %d", r.status)
	}

	good := e.client("a.example.com", ca.issue(t, "device-1", time.Time{}))
	if r := e.mustGet(good, "a.example.com", "/admin"); r.status != 200 || r.seen.Get(hdrClientVerify) != "SUCCESS" {
		t.Fatalf("with a certificate: %d %v", r.status, r.seen)
	}

	// Presented but not valid: the handshake still succeeds; the
	// application is told why, and learns nothing else.
	for name, c := range map[string]struct {
		cert   *tls.Certificate
		reason string
	}{
		"untrusted": {newClientCA(t, "Somebody Else").issue(t, "x", time.Time{}), "FAILED:unknown issuer"},
		"expired":   {ca.issue(t, "old", time.Now().Add(-time.Hour)), "FAILED:certificate expired or not yet valid"},
	} {
		cl := e.client("a.example.com", c.cert)
		r := e.mustGet(cl, "a.example.com", "/")
		if r.status != 200 || r.seen.Get(hdrClientVerify) != c.reason || r.seen.Get(hdrClientSubject) != "" {
			t.Fatalf("%s: %d %v", name, r.status, r.seen)
		}
		if r := e.mustGet(cl, "a.example.com", "/admin"); r.status != http.StatusForbidden || !strings.Contains(r.body, "not accepted") {
			t.Fatalf("%s on /admin: %d %s", name, r.status, r.body)
		}
	}
}

func TestClientCertAllowList(t *testing.T) {
	e := newTLSEnv(t)
	ca := newClientCA(t, "Devices CA")
	byFP := ca.issue(t, "device-9", time.Time{})
	e.apply(e.binding("a.example.com", e.serverCert("a.example.com"), &model.ClientCertPolicy{
		Mode: "require", CAPEM: ca.pem,
		AllowedSubjects:     []string{"DEVICE-1", "spiffe://example.com/device/7-not"},
		AllowedFingerprints: []string{fingerprintOf(byFP)},
	}))
	for cn, want := range map[string]int{"device-1": 200, "device-2": http.StatusForbidden} {
		r := e.mustGet(e.client("a.example.com", ca.issue(t, cn, time.Time{})), "a.example.com", "/")
		if r.status != want {
			t.Errorf("%s: %d %s", cn, r.status, r.body)
		}
		if want == http.StatusForbidden && !strings.Contains(r.body, "not allowed") {
			t.Errorf("%s: %s", cn, r.body)
		}
	}
	if r := e.mustGet(e.client("a.example.com", byFP), "a.example.com", "/"); r.status != 200 {
		t.Errorf("allowed by fingerprint: %d", r.status)
	}
}

// TestClientCertPerHost: bindings on one IP:port have their own policies,
// picked by SNI; a request whose Host picks another policy than its
// connection's SNI gets 421; the binding without a host name is the
// default for clients that send no SNI.
func TestClientCertPerHost(t *testing.T) {
	e := newTLSEnv(t)
	ca := newClientCA(t, "Devices CA")
	cert := e.serverCert("a.example.com", "b.example.com", "127.0.0.1")
	e.apply(
		e.binding("a.example.com", cert, &model.ClientCertPolicy{Mode: "require", CAPEM: ca.pem}),
		e.binding("b.example.com", cert, nil),
		e.binding("", cert, &model.ClientCertPolicy{Mode: "accept", CAPEM: ca.pem}),
	)
	device := ca.issue(t, "device-1", time.Time{})

	if r := e.mustGet(e.client("b.example.com", nil), "b.example.com", "/"); r.status != 200 || r.seen.Get(hdrClientVerify) != "" {
		t.Fatalf("b without mTLS: %d %v", r.status, r.seen)
	}
	// A connection made for b (no certificate asked) used for a.
	r := e.mustGet(e.client("b.example.com", nil), "a.example.com", "/")
	if r.status != http.StatusMisdirectedRequest || r.seen != nil {
		t.Fatalf("SNI b, Host a: %d %s", r.status, r.body)
	}
	// A certificate on a's connection does not leak into b.
	if r := e.mustGet(e.client("a.example.com", device), "b.example.com", "/"); r.status != 200 || r.seen.Get(hdrClientVerify) != "" || r.seen.Get(hdrClientCert) != "" {
		t.Fatalf("SNI a, Host b: %d %v", r.status, r.seen)
	}
	if r := e.mustGet(e.client("a.example.com", device), "a.example.com", "/"); r.status != 200 || r.seen.Get(hdrClientVerify) != "SUCCESS" {
		t.Fatalf("a: %d %v", r.status, r.seen)
	}
	// No SNI (an IP address): the default binding's policy (accept).
	noSNI := e.client("127.0.0.1", device)
	if r := e.mustGet(noSNI, "127.0.0.1", "/"); r.status != 200 || r.seen.Get(hdrClientVerify) != "SUCCESS" {
		t.Fatalf("default binding: %d %v", r.status, r.seen)
	}
	if r := e.mustGet(e.client("127.0.0.1", nil), "127.0.0.1", "/"); r.status != 200 || r.seen.Get(hdrClientVerify) != "NONE" {
		t.Fatalf("default binding without a certificate: %d %v", r.status, r.seen)
	}
}

// TestClientCertHeadersAlwaysStripped: without client certificates, a
// client cannot make up an identity for the application.
func TestClientCertHeadersAlwaysStripped(t *testing.T) {
	e := newTLSEnv(t)
	e.apply(e.binding("a.example.com", e.serverCert("a.example.com"), nil))
	r := e.mustGet(e.client("a.example.com", nil), "a.example.com", "/",
		hdrClientVerify, "SUCCESS", hdrClientSubject, "CN=admin", hdrClientFingerprint, "AB", hdrClientCert, "x")
	if r.status != 200 {
		t.Fatal(r.status)
	}
	for _, h := range clientCertHeaders {
		if v := r.seen.Get(h); v != "" {
			t.Errorf("%s reached the application: %q", h, v)
		}
	}
}

// TestClientPolicySharedAcrossReloads: a reload that keeps a binding's
// policy keeps its verification cache; changing it compiles a new one.
func TestClientPolicySharedAcrossReloads(t *testing.T) {
	e := newTLSEnv(t)
	ca := newClientCA(t, "Devices CA")
	cert := e.serverCert("a.example.com")
	pol := &model.ClientCertPolicy{Mode: "require", CAPEM: ca.pem}
	e.apply(e.binding("a.example.com", cert, pol))
	first := e.s.table.Load().byPort[e.port][0].client
	e.apply(e.binding("a.example.com", cert, pol))
	if e.s.table.Load().byPort[e.port][0].client != first {
		t.Fatal("unchanged policy recompiled")
	}
	e.apply(e.binding("a.example.com", cert, &model.ClientCertPolicy{Mode: "accept", CAPEM: ca.pem}))
	if p := e.s.table.Load().byPort[e.port][0].client; p == first || p.mode != "accept" {
		t.Fatal("changed policy not recompiled")
	}
	if len(e.s.policies) != 1 {
		t.Fatalf("%d policies kept", len(e.s.policies))
	}
}

func TestClientIdentityCacheBounded(t *testing.T) {
	ca := newClientCA(t, "Devices CA")
	p, err := compileClientPolicy(&model.ClientCertPolicy{Mode: "accept", CAPEM: ca.pem})
	if err != nil {
		t.Fatal(err)
	}
	cert := ca.issue(t, "d", time.Time{})
	now := time.Now()
	cs := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert.Leaf}}
	id := p.identify(cs, now)
	if !id.ok() || p.identify(cs, now) != id {
		t.Fatal("not cached")
	}
	if p.identify(cs, now.Add(clientVerifyTTL+time.Second)) == id {
		t.Fatal("cached past its lifetime")
	}
	for i := range clientVerifyCache + 10 {
		leaf := &x509.Certificate{Raw: []byte(fmt.Sprint(i))}
		p.identify(&tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}, now)
	}
	if n := len(p.cache); n > clientVerifyCache {
		t.Fatalf("%d cached", n)
	}
}

func TestEscapeComponent(t *testing.T) {
	in := "-----BEGIN CERTIFICATE-----\nMIIB+/=\n"
	got := escapeComponent(in)
	if got != "-----BEGIN%20CERTIFICATE-----%0AMIIB%2B%2F%3D%0A" {
		t.Fatal(got)
	}
	if back, _ := url.PathUnescape(got); back != in {
		t.Fatal(back)
	}
	if headerSafe("CN=a\r\nX: y") != "CN=a??X: y" {
		t.Fatal(headerSafe("CN=a\r\nX: y"))
	}
}

// Keep the JSON shape the API documents.
func TestClientCertPolicyJSON(t *testing.T) {
	raw, _ := json.Marshal(model.Binding{Protocol: "https", ClientCert: &model.ClientCertPolicy{Mode: "require", CAPEM: "x", RequirePaths: []string{"/a"}}})
	for _, k := range []string{`"clientCert":{`, `"mode":"require"`, `"caPem":"x"`, `"requirePaths":["/a"]`} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("%s missing in %s", k, raw)
		}
	}
}
