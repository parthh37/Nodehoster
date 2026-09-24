package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func testCA(t *testing.T) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Devices CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// TestTLSEndpoints: GET /api/tls is for viewers, PUT for administrators
// (audited); it changes only the TLS settings, leaving secrets as they were.
func TestTLSEndpoints(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	site := e.createSite(admin, redirectSite("s", 0))
	e.user("viewer", model.RoleViewer, false)
	e.user("operator", model.RoleOperator, false)
	e.scoped("siteop", grant(site.ID, model.RoleOperator))
	viewer, operator, siteop := session(e.login("viewer")), session(e.login("operator")), session(e.login("siteop"))

	s := e.c.Settings()
	s.ACME.EABHMAC = "hmac-secret"
	if _, err := e.c.UpdateSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	sealed := e.c.Settings().ACME.EABHMAC

	v := decodeJSON[model.TLSView](t, e.do("GET", "/api/tls", nil, viewer...))
	if v.MinVersion != "1.2" || !v.HTTP2 || v.HTTP3 || v.HTTP3Listeners == nil || len(v.HTTP3Listeners) != 0 {
		t.Fatalf("defaults: %+v", v)
	}
	on := model.TLSSettings{MinVersion: "1.3", HTTP2: true, HTTP3: true}
	for _, tc := range []struct {
		name string
		body any
		who  []opt
		want int
	}{
		{"site operator reads", nil, siteop, http.StatusForbidden},
		{"viewer changes", on, viewer, http.StatusForbidden},
		{"operator changes", on, operator, http.StatusForbidden},
		{"bad version", model.TLSSettings{MinVersion: "1.1"}, admin, http.StatusUnprocessableEntity},
		{"admin changes", on, admin, http.StatusOK},
	} {
		method := "PUT"
		if tc.body == nil {
			method = "GET"
		}
		if rec := e.do(method, "/api/tls", tc.body, tc.who...); rec.Code != tc.want {
			t.Errorf("%s: %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body)
		}
	}
	got := e.c.Settings()
	if got.TLS != on {
		t.Fatalf("TLS = %+v", got.TLS)
	}
	if got.ACME.EABHMAC != sealed {
		t.Fatal("changing TLS settings changed a stored secret")
	}
	if !contains(e.auditActions(), "root:settings.tls") {
		t.Fatalf("audit = %v", e.auditActions())
	}
	// The whole settings document shows it too.
	if st := e.settings(admin); !st.TLS.HTTP3 {
		t.Fatal("settings.tls.http3 not set")
	}
}

// TestBindingClientCertificates: the client certificate policy of a
// binding is validated and stored with the site.
func TestBindingClientCertificates(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	cert := e.selfSigned(admin, "a")
	ca := testCA(t)
	body := redirectSite("mtls", 0)
	binding := map[string]any{"protocol": "https", "ip": "127.0.0.1", "port": freePort(t), "host": "a.test",
		"certMode": "certificate", "certificateId": cert,
		"clientCert": map[string]any{"mode": "require", "caPem": "not pem"}}
	body["bindings"] = []map[string]any{binding}
	rec := e.do("POST", "/api/sites", body, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "bindings[0].clientCert.caPem" {
		t.Fatalf("field = %q", f)
	}

	binding["clientCert"] = map[string]any{"mode": "accept", "caPem": ca, "requirePaths": []string{"/admin"},
		"allowedFingerprints": []string{strings.Repeat("ab:", 31) + "ab"}}
	site := e.createSite(admin, body)
	cc := site.Bindings[0].ClientCert
	if cc == nil || cc.Mode != "accept" || cc.CAPEM == "" || cc.AllowedFingerprints[0] != strings.Repeat("AB", 32) {
		t.Fatalf("stored: %+v", cc)
	}
	got := decodeJSON[siteResp](t, e.do("GET", "/api/sites/"+site.ID, nil, admin...))
	if got.Bindings[0].ClientCert == nil || got.Bindings[0].ClientCert.RequirePaths[0] != "/admin" {
		t.Fatalf("read back: %+v", got.Bindings[0].ClientCert)
	}
}

// TestCertificateOCSPStatus: certificates show their OCSP stapling state;
// "check now" is for operators and audited. A self-signed certificate has
// no responder: nothing to staple.
func TestCertificateOCSPStatus(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	e.user("viewer", model.RoleViewer, false)
	e.user("operator", model.RoleOperator, false)
	viewer, operator := session(e.login("viewer")), session(e.login("operator"))
	id := e.selfSigned(admin, "a")

	type view struct {
		ID   string            `json:"id"`
		OCSP *model.OCSPStatus `json:"ocsp"`
	}
	list := decodeJSON[[]view](t, e.do("GET", "/api/certificates", nil, viewer...))
	if len(list) != 1 || list[0].OCSP == nil || list[0].OCSP.State != model.OCSPNone {
		t.Fatalf("list: %+v", list)
	}
	expect(t, e.do("POST", "/api/certificates/"+id+"/ocsp", nil, viewer...), http.StatusForbidden)
	rec := e.do("POST", "/api/certificates/"+id+"/ocsp", nil, operator...)
	expect(t, rec, http.StatusOK)
	if v := decodeJSON[view](t, rec); v.OCSP == nil || v.OCSP.State != model.OCSPNone {
		t.Fatalf("check: %+v", v)
	}
	expect(t, e.do("POST", "/api/certificates/nope/ocsp", nil, operator...), http.StatusNotFound)
	if !contains(e.auditActions(), "operator:cert.ocsp") {
		t.Fatalf("audit = %v", e.auditActions())
	}
}

// TestMetricsByProtocol: /metrics counts requests by HTTP version.
func TestMetricsByProtocol(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	e.createSite(admin, redirectSite("s", 0))
	u := e.user("prom", model.RoleViewer, false)
	rec := e.do("GET", "/metrics", nil, withBearer(e.token(u)))
	expect(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `nodehoster_requests_by_protocol_total{site="s",type="redirect",protocol="HTTP/3"} 0`) {
		t.Fatalf("metrics:\n%s", rec.Body)
	}
}
