package certs

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/store"
	"golang.org/x/crypto/ocsp"
)

// testPKI is a CA that issues server certificates naming an OCSP
// responder it runs.
type testPKI struct {
	t      *testing.T
	caKey  *ecdsa.PrivateKey
	ca     *x509.Certificate
	caPEM  []byte
	serial atomic.Int64

	responder *httptest.Server
	requests  atomic.Int32
	mu        sync.Mutex
	// answer builds the responder's reply; nil answers good for a day.
	answer func(w http.ResponseWriter, req *ocsp.Request)
}

func newPKI(t *testing.T) *testPKI {
	t.Helper()
	p := &testPKI{t: t}
	p.caKey, p.ca, p.caPEM = selfSignedCA(t, "Test Issuing CA")
	p.serial.Store(100)
	p.responder = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		req, err := ocsp.ParseRequest(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		answer := p.answer
		p.mu.Unlock()
		if answer == nil {
			w.Write(p.response(req.SerialNumber, ocsp.Good, time.Now().Add(-time.Minute), time.Now().Add(24*time.Hour)))
			return
		}
		answer(w, req)
	}))
	t.Cleanup(p.responder.Close)
	return p
}

func (p *testPKI) setAnswer(f func(w http.ResponseWriter, req *ocsp.Request)) {
	p.mu.Lock()
	p.answer = f
	p.mu.Unlock()
}

// response is a response signed by the CA itself.
func (p *testPKI) response(serial *big.Int, status int, this, next time.Time) []byte {
	return p.signedBy(p.ca, p.caKey, serial, status, this, next)
}

func (p *testPKI) signedBy(responder *x509.Certificate, key crypto.Signer, serial *big.Int, status int, this, next time.Time) []byte {
	p.t.Helper()
	tmpl := ocsp.Response{Status: status, SerialNumber: serial, ThisUpdate: this, NextUpdate: next}
	if status == ocsp.Revoked {
		tmpl.RevokedAt = this.Add(-time.Hour)
		tmpl.RevocationReason = ocsp.KeyCompromise
	}
	if responder != p.ca {
		tmpl.Certificate = responder
	}
	der, err := ocsp.CreateResponse(p.ca, responder, tmpl, key)
	if err != nil {
		p.t.Fatal(err)
	}
	return der
}

// leaf issues a server certificate: PEM of the certificate and its chain
// (the CA), and of its key.
func (p *testPKI) leaf(host string, withResponder, mustStaple, withChain bool) (certPEM, keyPEM []byte) {
	p.t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(p.serial.Add(1)), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if withResponder {
		tmpl.OCSPServer = []string{p.responder.URL + "/ocsp"}
	}
	if mustStaple {
		v, _ := asn1.Marshal([]int{5})
		tmpl.ExtraExtensions = append(tmpl.ExtraExtensions, pkix.Extension{Id: oidTLSFeature, Value: v})
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.caKey)
	if err != nil {
		p.t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if withChain {
		certPEM = append(certPEM, p.caPEM...)
	}
	kder, _ := x509.MarshalPKCS8PrivateKey(key)
	return certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})
}

func selfSignedCA(t *testing.T, name string) (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return key, c, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

type testManager struct {
	*Manager
	t      *testing.T
	st     *store.Store
	events <-chan model.Event
}

func newTestManager(t *testing.T, dir string) *testManager {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secrets.Open(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	settings := func() model.Settings { return model.DefaultSettings() }
	bus := events.New(st, log, settings, nil)
	evs, unsub := bus.Subscribe()
	t.Cleanup(unsub)
	m := New(st, box, filepath.Join(dir, "certs"), filepath.Join(dir, "acme"), log, bus, settings)
	if err := m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &testManager{Manager: m, t: t, st: st, events: evs}
}

func (m *testManager) importPEM(certPEM, keyPEM []byte) *model.Certificate {
	t := m.t
	t.Helper()
	c, err := m.Import(context.Background(), "test", certPEM, keyPEM, "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// due makes a certificate's next check now.
func (m *testManager) due(id string) {
	m.ocsp.mu.Lock()
	m.ocsp.entries[id].nextCheck = time.Now().Add(-time.Second)
	m.ocsp.mu.Unlock()
}

func (m *testManager) eventTypes() []string {
	var out []string
	for {
		select {
		case e := <-m.events:
			out = append(out, e.Type)
		default:
			return out
		}
	}
}

func TestOCSPStaplesGoodResponse(t *testing.T) {
	pki := newPKI(t)
	dir := t.TempDir()
	m := newTestManager(t, dir)
	c := m.importPEM(pki.leaf("a.example.com", true, false, true))
	if st := m.OCSPStatus(c.ID); st == nil || st.State != model.OCSPPending || st.Responder == "" {
		t.Fatalf("before the first check: %+v", st)
	}
	m.ocspPass(context.Background())

	st := m.OCSPStatus(c.ID)
	if st.State != model.OCSPGood || !st.Stapled || st.NextUpdate == nil || st.NextCheck == nil || st.LastError != "" {
		t.Fatalf("status = %+v", st)
	}
	staple := m.Get(c.ID).OCSPStaple
	if len(staple) == 0 {
		t.Fatal("nothing stapled")
	}
	// Refreshed halfway to NextUpdate, not before.
	if d := time.Until(*st.NextCheck); d < 11*time.Hour || d > 13*time.Hour {
		t.Fatalf("next check in %v", d)
	}
	m.ocspPass(context.Background())
	if n := pki.requests.Load(); n != 1 {
		t.Fatalf("%d requests", n)
	}
	if saved, err := os.ReadFile(filepath.Join(dir, "certs", c.ID, ocspFile)); err != nil || !bytes.Equal(saved, staple) {
		t.Fatalf("saved response: %v", err)
	}

	// A restart staples the saved response without the responder.
	pki.setAnswer(func(w http.ResponseWriter, _ *ocsp.Request) { http.Error(w, "down", http.StatusServiceUnavailable) })
	m.st.Close()
	m2 := newTestManager(t, dir)
	if got := m2.Get(c.ID).OCSPStaple; !bytes.Equal(got, staple) {
		t.Fatal("the saved response was not stapled after a restart")
	}
	if st := m2.OCSPStatus(c.ID); st.State != model.OCSPGood || !st.Stapled {
		t.Fatalf("after restart: %+v", st)
	}
}

// TestOCSPNeverStaplesExpired: an expired response (fetched, or saved
// before a long shutdown) is never stapled, and a staple is dropped before
// its NextUpdate.
func TestOCSPNeverStaplesExpired(t *testing.T) {
	pki := newPKI(t)
	dir := t.TempDir()
	m := newTestManager(t, dir)
	c := m.importPEM(pki.leaf("a.example.com", true, false, true))

	pki.setAnswer(func(w http.ResponseWriter, req *ocsp.Request) {
		w.Write(pki.response(req.SerialNumber, ocsp.Good, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)))
	})
	m.ocspPass(context.Background())
	if st := m.OCSPStatus(c.ID); st.State != model.OCSPError || st.Stapled || !strings.Contains(st.LastError, "expired") {
		t.Fatalf("expired response: %+v", st)
	}
	if m.Get(c.ID).OCSPStaple != nil {
		t.Fatal("expired response stapled")
	}

	// Valid for one more minute: inside the margin, so dropped at once.
	pki.setAnswer(func(w http.ResponseWriter, req *ocsp.Request) {
		w.Write(pki.response(req.SerialNumber, ocsp.Good, time.Now().Add(-time.Hour), time.Now().Add(time.Minute)))
	})
	m.due(c.ID)
	m.ocspPass(context.Background())
	if m.Get(c.ID).OCSPStaple != nil || m.OCSPStatus(c.ID).Stapled {
		t.Fatal("a response about to expire stays stapled")
	}

	// A saved response that expired while the server was off.
	old := pki.response(m.Get(c.ID).Leaf.SerialNumber, ocsp.Good, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	os.WriteFile(filepath.Join(dir, "certs", c.ID, ocspFile), old, 0o644)
	m.st.Close()
	m2 := newTestManager(t, dir)
	if m2.Get(c.ID).OCSPStaple != nil {
		t.Fatal("an expired saved response was stapled")
	}
	if st := m2.OCSPStatus(c.ID); st.State != model.OCSPPending {
		t.Fatalf("after restart: %+v", st)
	}
}

func TestOCSPRevoked(t *testing.T) {
	pki := newPKI(t)
	m := newTestManager(t, t.TempDir())
	c := m.importPEM(pki.leaf("a.example.com", true, false, true))
	m.ocspPass(context.Background())
	if m.Get(c.ID).OCSPStaple == nil {
		t.Fatal("good response not stapled")
	}
	m.eventTypes()

	pki.setAnswer(func(w http.ResponseWriter, req *ocsp.Request) {
		w.Write(pki.response(req.SerialNumber, ocsp.Revoked, time.Now().Add(-time.Minute), time.Now().Add(24*time.Hour)))
	})
	for range 2 {
		m.due(c.ID)
		m.ocspPass(context.Background())
	}
	st := m.OCSPStatus(c.ID)
	if st.State != model.OCSPRevoked || st.Stapled || st.RevokedAt == nil || st.RevocationReason != "key compromise" {
		t.Fatalf("status = %+v", st)
	}
	if m.Get(c.ID).OCSPStaple != nil {
		t.Fatal("a revoked certificate keeps its staple")
	}
	if evs := m.eventTypes(); strings.Join(evs, ",") != events.CertRevoked {
		t.Fatalf("events = %v, want one %s", evs, events.CertRevoked)
	}
}

// TestOCSPFailuresBackOff: a failing responder is asked again after 1, 2,
// 4... minutes; a Must-Staple certificate without a staple is reported once.
func TestOCSPFailuresBackOff(t *testing.T) {
	pki := newPKI(t)
	pki.setAnswer(func(w http.ResponseWriter, _ *ocsp.Request) { http.Error(w, "busy", http.StatusInternalServerError) })
	m := newTestManager(t, t.TempDir())
	c := m.importPEM(pki.leaf("a.example.com", true, true, true))
	for i, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		m.due(c.ID)
		m.ocspPass(context.Background())
		st := m.OCSPStatus(c.ID)
		if st.State != model.OCSPError || !strings.Contains(st.LastError, "HTTP 500") || !st.MustStaple {
			t.Fatalf("attempt %d: %+v", i+1, st)
		}
		if d := time.Until(*st.NextCheck); d < want-5*time.Second || d > want {
			t.Fatalf("attempt %d: next check in %v, want %v", i+1, d, want)
		}
	}
	if evs := m.eventTypes(); strings.Join(evs, ",") != events.CertStapling {
		t.Fatalf("events = %v, want one %s", evs, events.CertStapling)
	}
	// Recovered: stapled, and a later failure is reported again.
	pki.setAnswer(nil)
	m.due(c.ID)
	m.ocspPass(context.Background())
	if st := m.OCSPStatus(c.ID); st.State != model.OCSPGood || !st.Stapled {
		t.Fatalf("recovered: %+v", st)
	}
}

// TestOCSPNothingToStaple: Let's Encrypt (since 2025) and self-signed
// certificates name no responder; nothing is asked, nothing is wrong.
func TestOCSPNothingToStaple(t *testing.T) {
	pki := newPKI(t)
	m := newTestManager(t, t.TempDir())
	c := m.importPEM(pki.leaf("a.example.com", false, false, true))
	ss, err := m.SelfSigned(context.Background(), "", []string{"b.example.com"}, 30)
	if err != nil {
		t.Fatal(err)
	}
	m.ocspPass(context.Background())
	for _, id := range []string{c.ID, ss.ID} {
		st := m.OCSPStatus(id)
		if st == nil || st.State != model.OCSPNone || st.Stapled || st.NextCheck != nil || st.LastError != "" {
			t.Fatalf("%s: %+v", id, st)
		}
	}
	if n := pki.requests.Load(); n != 0 {
		t.Fatalf("%d requests", n)
	}
	if len(m.eventTypes()) != 0 {
		t.Fatal("events for certificates without OCSP")
	}
	if st, err := m.CheckOCSP(context.Background(), c.ID); err != nil || st.State != model.OCSPNone {
		t.Fatalf("check now: %+v %v", st, err)
	}

	// A responder but no issuer in the file: explained, never asked.
	nochain := m.importPEM(pki.leaf("c.example.com", true, false, false))
	m.ocspPass(context.Background())
	if st := m.OCSPStatus(nochain.ID); st.State != model.OCSPError || !strings.Contains(st.LastError, "full chain") || st.NextCheck != nil {
		t.Fatalf("no chain: %+v", st)
	}
	if n := pki.requests.Load(); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

// TestOCSPRejectsForeignResponses: responses signed by someone else, about
// another certificate, or by an unauthorized delegate are not stapled.
func TestOCSPRejectsForeignResponses(t *testing.T) {
	pki := newPKI(t)
	m := newTestManager(t, t.TempDir())
	c := m.importPEM(pki.leaf("a.example.com", true, false, true))
	otherKey, otherCA, _ := selfSignedCA(t, "Somebody Else")
	delegate := func(eku []x509.ExtKeyUsage) (*x509.Certificate, *ecdsa.PrivateKey) {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "OCSP responder"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: eku}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, pki.ca, &key.PublicKey, pki.caKey)
		if err != nil {
			t.Fatal(err)
		}
		cert, _ := x509.ParseCertificate(der)
		return cert, key
	}
	now := time.Now()
	for name, answer := range map[string]func(req *ocsp.Request) []byte{
		"another signer": func(req *ocsp.Request) []byte {
			tmpl := ocsp.Response{Status: ocsp.Good, SerialNumber: req.SerialNumber, ThisUpdate: now, NextUpdate: now.Add(time.Hour)}
			der, _ := ocsp.CreateResponse(otherCA, otherCA, tmpl, otherKey)
			return der
		},
		"another certificate": func(*ocsp.Request) []byte {
			return pki.response(big.NewInt(424242), ocsp.Good, now, now.Add(time.Hour))
		},
		"unauthorized delegate": func(req *ocsp.Request) []byte {
			cert, key := delegate([]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
			return pki.signedBy(cert, key, req.SerialNumber, ocsp.Good, now, now.Add(time.Hour))
		},
		"dated in the future": func(req *ocsp.Request) []byte {
			return pki.response(req.SerialNumber, ocsp.Good, now.Add(time.Hour), now.Add(2*time.Hour))
		},
		"garbage": func(*ocsp.Request) []byte { return []byte("not ocsp") },
	} {
		pki.setAnswer(func(w http.ResponseWriter, req *ocsp.Request) { w.Write(answer(req)) })
		m.due(c.ID)
		m.ocspPass(context.Background())
		if st := m.OCSPStatus(c.ID); st.State != model.OCSPError || st.Stapled || st.LastError == "" {
			t.Errorf("%s: %+v", name, st)
		}
		if m.Get(c.ID).OCSPStaple != nil {
			t.Errorf("%s: stapled", name)
		}
	}

	// An authorized delegate is fine.
	pki.setAnswer(func(w http.ResponseWriter, req *ocsp.Request) {
		cert, key := delegate([]x509.ExtKeyUsage{x509.ExtKeyUsageOCSPSigning})
		w.Write(pki.signedBy(cert, key, req.SerialNumber, ocsp.Good, now, now.Add(time.Hour)))
	})
	m.due(c.ID)
	m.ocspPass(context.Background())
	if st := m.OCSPStatus(c.ID); st.State != model.OCSPGood || !st.Stapled {
		t.Fatalf("delegated responder: %+v", st)
	}
}

// TestOCSPNewCertificateStartsOver: renewing replaces the saved response
// (it is about the old certificate) and asks again; deleting forgets it.
func TestOCSPNewCertificateStartsOver(t *testing.T) {
	pki := newPKI(t)
	dir := t.TempDir()
	m := newTestManager(t, dir)
	c := m.importPEM(pki.leaf("a.example.com", true, false, true))
	m.ocspPass(context.Background())
	saved := filepath.Join(dir, "certs", c.ID, ocspFile)
	if _, err := os.Stat(saved); err != nil {
		t.Fatal(err)
	}

	certPEM, keyPEM := pki.leaf("a.example.com", true, false, true)
	if err := m.save(c, certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(saved); !os.IsNotExist(err) {
		t.Fatal("the previous certificate's response was kept")
	}
	if st := m.OCSPStatus(c.ID); st.State != model.OCSPPending || st.Stapled || m.Get(c.ID).OCSPStaple != nil {
		t.Fatalf("after renewal: %+v", st)
	}
	m.ocspPass(context.Background())
	if st := m.OCSPStatus(c.ID); st.State != model.OCSPGood || !st.Stapled {
		t.Fatalf("renewed: %+v", st)
	}
	if err := m.Delete(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	if m.OCSPStatus(c.ID) != nil {
		t.Fatal("deleted certificate still tracked")
	}
}

// TestOCSPStapleReachesHandshake: the pair handed to TLS carries the
// staple, and replacing it never changes a pair already handed out.
func TestOCSPStapleReachesHandshake(t *testing.T) {
	pki := newPKI(t)
	m := newTestManager(t, t.TempDir())
	c := m.importPEM(pki.leaf("a.example.com", true, false, true))
	before := m.Get(c.ID)
	m.ocspPass(context.Background())
	after := m.Get(c.ID)
	if before.OCSPStaple != nil || after.OCSPStaple == nil || before == after {
		t.Fatal("the stapled pair must be a new one")
	}
	resp, err := ocsp.ParseResponseForCert(after.OCSPStaple, after.Leaf, pki.ca)
	if err != nil || resp.Status != ocsp.Good {
		t.Fatalf("%v %v", resp, err)
	}
}

func TestMustStapleDetection(t *testing.T) {
	pki := newPKI(t)
	for _, want := range []bool{true, false} {
		certPEM, _ := pki.leaf("a.example.com", true, want, false)
		b, _ := pem.Decode(certPEM)
		leaf, _ := x509.ParseCertificate(b.Bytes)
		if got := mustStaple(leaf); got != want {
			t.Errorf("mustStaple = %v, want %v", got, want)
		}
	}
}

func TestRefreshAt(t *testing.T) {
	now := time.Now()
	if got := refreshAt(now, now.Add(4*time.Hour), now); !got.Equal(now.Add(2 * time.Hour)) {
		t.Errorf("fresh: %v", got.Sub(now))
	}
	// Already past halfway when fetched (a cached answer): halfway through what is left.
	if got := refreshAt(now.Add(-3*time.Hour), now.Add(time.Hour), now); !got.Equal(now.Add(30 * time.Minute)) {
		t.Errorf("old: %v", got.Sub(now))
	}
	if got := refreshAt(now.Add(-time.Hour), now.Add(time.Second), now); !got.Equal(now.Add(time.Minute)) {
		t.Errorf("nearly expired: %v", got.Sub(now))
	}
}
