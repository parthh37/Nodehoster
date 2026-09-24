package model

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func testCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func httpsSite(b Binding) *Site {
	s := &Site{Name: "shop", Type: SiteStatic, Static: &StaticConfig{Root: "."}, Bindings: []Binding{b}}
	s.ApplyDefaults()
	return s
}

// TestClientCertDefaults: old documents (no clientCert) stay as they were,
// an http binding never keeps a policy, and values are normalized.
func TestClientCertDefaults(t *testing.T) {
	s := httpsSite(Binding{Protocol: "https", Host: "a.example.com"})
	if s.Bindings[0].ClientCert != nil {
		t.Fatal("a binding without a policy gained one")
	}
	raw, _ := json.Marshal(s.Bindings[0])
	if strings.Contains(string(raw), "clientCert") {
		t.Fatalf("absent policy serialized: %s", raw)
	}
	s = httpsSite(Binding{Protocol: "https", Host: "a.example.com", ClientCert: &ClientCertPolicy{Mode: " "}})
	if s.Bindings[0].ClientCert != nil {
		t.Fatal("an empty ignore policy is dropped")
	}
	s = httpsSite(Binding{Protocol: "http", Host: "a.example.com", ClientCert: &ClientCertPolicy{Mode: "require"}})
	if s.Bindings[0].ClientCert != nil {
		t.Fatal("http bindings have no client certificates")
	}
	s = httpsSite(Binding{Protocol: "https", Host: "a.example.com", ClientCert: &ClientCertPolicy{
		Mode:                " Accept ",
		AllowedFingerprints: []string{" ab:cd ", ""},
		AllowedSubjects:     []string{" CN=x ", " "},
		RequirePaths:        []string{" /admin "},
	}})
	p := s.Bindings[0].ClientCert
	if p.Mode != ClientCertAccept || p.AllowedFingerprints[0] != "ABCD" || len(p.AllowedFingerprints) != 1 ||
		p.AllowedSubjects[0] != "CN=x" || len(p.AllowedSubjects) != 1 || p.RequirePaths[0] != "/admin" {
		t.Fatalf("normalized = %+v", p)
	}
	// Switched off, it keeps what was entered (and is not checked).
	s = httpsSite(Binding{Protocol: "https", Host: "a.example.com", CertMode: CertModeAuto, ClientCert: &ClientCertPolicy{Mode: "ignore", CAPEM: "junk"}})
	if s.Bindings[0].ClientCert == nil || s.Validate() != nil {
		t.Fatalf("ignored policy: %+v %v", s.Bindings[0].ClientCert, s.Validate())
	}
}

func TestClientCertValidation(t *testing.T) {
	ca := testCAPEM(t)
	fp := strings.Repeat("ab", 32)
	ok := &ClientCertPolicy{Mode: "require", CAPEM: ca, AllowedFingerprints: []string{fp}, AllowedSubjects: []string{"device-1"}}
	s := httpsSite(Binding{Protocol: "https", Host: "a.example.com", ClientCert: ok})
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	// Two CAs, a self-signed client certificate among them.
	s = httpsSite(Binding{Protocol: "https", Host: "a.example.com", ClientCert: &ClientCertPolicy{Mode: "accept", CAPEM: ca + "\n" + testCAPEM(t)}})
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}

	for field, p := range map[string]*ClientCertPolicy{
		"bindings[0].clientCert.mode":                   {Mode: "optional", CAPEM: ca},
		"bindings[0].clientCert.caPem":                  {Mode: "require"},
		"bindings[0].clientCert.allowedFingerprints[0]": {Mode: "require", CAPEM: ca, AllowedFingerprints: []string{"1234"}},
		"bindings[0].clientCert.requirePaths[0]":        {Mode: "accept", CAPEM: ca, RequirePaths: []string{"admin"}},
	} {
		s := httpsSite(Binding{Protocol: "https", Host: "a.example.com", ClientCert: p})
		err := s.Validate()
		ve, isVE := err.(*ValidationError)
		if !isVE || ve.Field != field {
			t.Errorf("%s: got %v", field, err)
		}
	}

	// requirePaths are compared canonically, so only plain paths.
	for path, ok := range map[string]bool{
		"/admin": true, "/admin/": true, "/": true, "/api/v1.2/x": true,
		"/a\\b": false, "/a%2Fb": false, "/a?x": false, "/a#x": false, "/a;x": false, "/a:x": false,
		"/a/../b": false, "/./a": false, "/a/...": false, "/a/. ": false, "/a\x00": false,
	} {
		s := httpsSite(Binding{Protocol: "https", Host: "a.example.com", ClientCert: &ClientCertPolicy{Mode: "accept", CAPEM: ca, RequirePaths: []string{path}}})
		err := s.Validate()
		if ve, isVE := err.(*ValidationError); ok != (err == nil) || (!ok && (!isVE || ve.Field != "bindings[0].clientCert.requirePaths[0]")) {
			t.Errorf("%q: got %v", path, err)
		}
	}
}

func TestParseCABundle(t *testing.T) {
	ca := testCAPEM(t)
	if certs, err := ParseCABundle(ca + ca); err != nil || len(certs) != 2 {
		t.Fatalf("two certificates: %d %v", len(certs), err)
	}
	for name, bundle := range map[string]string{
		"empty":       "  ",
		"not pem":     "hello",
		"private key": ca + "-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----\n",
		"bad der":     "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
		"trailing":    ca + "garbage",
		"too large":   strings.Repeat(ca, 300),
	} {
		if _, err := ParseCABundle(bundle); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	if got := NormalizeFingerprint(" aa:bb cc-dd "); got != "AABBCCDD" {
		t.Fatal(got)
	}
}

func TestOCSPSummary(t *testing.T) {
	until := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	for want, s := range map[string]*OCSPStatus{
		"-":                                   nil,
		"no responder":                        {State: OCSPNone},
		"not checked yet":                     {State: OCSPPending},
		"stapled, until 2026-10-01 12:00":     {State: OCSPGood, Stapled: true, NextUpdate: &until},
		"REVOKED 2026-10-01 (key compromise)": {State: OCSPRevoked, RevokedAt: &until, RevocationReason: "key compromise"},
		"error: HTTP 500":                     {State: OCSPError, LastError: "HTTP 500"},
		"unknown to the responder; Must-Staple without a staple": {State: OCSPUnknown, MustStaple: true},
	} {
		if got := s.Summary(); got != want {
			t.Errorf("%+v: %q, want %q", s, got, want)
		}
	}
}

// TestTLSSettingsHTTP3Default: HTTP/3 is off on new servers and for
// settings saved before it existed.
func TestTLSSettingsHTTP3Default(t *testing.T) {
	if DefaultSettings().TLS.HTTP3 {
		t.Fatal("HTTP/3 on by default")
	}
	var s Settings
	if err := json.Unmarshal([]byte(`{"tls":{"minVersion":"1.2","http2":true}}`), &s); err != nil || s.TLS.HTTP3 {
		t.Fatalf("%+v %v", s.TLS, err)
	}
}
