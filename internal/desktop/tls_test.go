package desktop

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func testCAPEM(t *testing.T) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "CA"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestClientCertText(t *testing.T) {
	ca := testCAPEM(t)
	for want, p := range map[string]*model.ClientCertPolicy{
		"":                nil,
		" ":               {Mode: "ignore", CAPEM: ca},
		"Required (1 CA)": {Mode: "require", CAPEM: ca},
		"Accepted (2 CAs, 3 allowed, required on /admin)": {Mode: "accept", CAPEM: ca + ca,
			AllowedSubjects: []string{"a", "b"}, AllowedFingerprints: []string{"c"}, RequirePaths: []string{"/admin"}},
		"Required (CA bundle invalid)": {Mode: "require", CAPEM: "junk"},
	} {
		if want == " " {
			want = ""
		}
		if got := ClientCertText(p); got != want {
			t.Errorf("%+v: %q, want %q", p, got, want)
		}
	}
}

func TestOCSPLevel(t *testing.T) {
	for _, tc := range []struct {
		s    *model.OCSPStatus
		want Level
	}{
		{nil, LevelOK},
		{&model.OCSPStatus{State: model.OCSPNone}, LevelOK},
		{&model.OCSPStatus{State: model.OCSPGood, Stapled: true, MustStaple: true}, LevelOK},
		{&model.OCSPStatus{State: model.OCSPPending, MustStaple: true}, LevelOK},
		{&model.OCSPStatus{State: model.OCSPError}, LevelWarning},
		{&model.OCSPStatus{State: model.OCSPError, MustStaple: true}, LevelDown},
		{&model.OCSPStatus{State: model.OCSPRevoked}, LevelDown},
	} {
		if got := OCSPLevel(tc.s); got != tc.want {
			t.Errorf("%+v: %v, want %v", tc.s, got, tc.want)
		}
	}
}
