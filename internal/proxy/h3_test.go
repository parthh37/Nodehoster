package proxy

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/quic-go/quic-go/http3"
)

func (e *tlsEnv) h3Client(sni string, cert *tls.Certificate) *http.Client {
	tr := &http3.Transport{TLSClientConfig: e.tlsConfig(sni, cert)}
	e.t.Cleanup(func() { tr.Close() })
	return &http.Client{Timeout: 5 * time.Second, Transport: tr}
}

// TestHTTP3 serves the same sites over QUIC, advertised with Alt-Svc, with
// SNI certificate choice and client certificates, and closes when turned
// off.
func TestHTTP3(t *testing.T) {
	e := newTLSEnv(t)
	ca := newClientCA(t, "Devices CA")
	cert := e.serverCert("a.example.com", "b.example.com")
	e.setHTTP3(true)
	e.apply(
		e.binding("a.example.com", cert, nil),
		e.binding("b.example.com", cert, &model.ClientCertPolicy{Mode: "require", CAPEM: ca.pem}),
	)
	udp := "udp 127.0.0.1:" + strconv.Itoa(e.port)
	if got := e.s.HTTP3Listeners(); len(got) != 1 || got[0] != udp {
		t.Fatalf("HTTP/3 listeners: %v", got)
	}

	r := e.mustGet(e.client("a.example.com", nil), "a.example.com", "/")
	if want := `h3=":` + strconv.Itoa(e.port) + `"; ma=86400`; r.header.Get("Alt-Svc") != want {
		t.Fatalf("Alt-Svc = %q, want %q", r.header.Get("Alt-Svc"), want)
	}

	h3 := e.h3Client("a.example.com", nil)
	req, _ := http.NewRequest(http.MethodGet, "https://127.0.0.1:"+strconv.Itoa(e.port)+"/", nil)
	req.Host = "a.example.com"
	resp, err := h3.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Proto != "HTTP/3.0" || resp.Header.Get("Alt-Svc") != "" {
		t.Fatalf("over HTTP/3: %d %s Alt-Svc=%q", resp.StatusCode, resp.Proto, resp.Header.Get("Alt-Svc"))
	}

	// Client certificates over QUIC.
	r = e.mustGet(e.h3Client("b.example.com", ca.issue(t, "device-1", time.Time{})), "b.example.com", "/")
	if r.status != 200 || r.seen.Get(hdrClientVerify) != "SUCCESS" {
		t.Fatalf("mTLS over HTTP/3: %d %v", r.status, r.seen)
	}
	if _, err := e.get(e.h3Client("b.example.com", nil), "b.example.com", "/"); err == nil {
		t.Fatal("HTTP/3 handshake without the required client certificate succeeded")
	}
	if p := e.s.Protocols("site0"); p.HTTP3 != 1 || p.HTTP1 != 1 {
		t.Fatalf("protocols = %+v", p)
	}

	// Off: the UDP listener closes, nothing is advertised.
	e.setHTTP3(false)
	e.apply(e.binding("a.example.com", cert, nil))
	if got := e.s.HTTP3Listeners(); len(got) != 0 {
		t.Fatalf("HTTP/3 listeners after turning it off: %v", got)
	}
	if r := e.mustGet(e.client("a.example.com", nil), "a.example.com", "/"); r.header.Get("Alt-Svc") != "" {
		t.Fatalf("Alt-Svc after turning HTTP/3 off: %q", r.header.Get("Alt-Svc"))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		pc, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(e.port)))
		if err == nil {
			pc.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the UDP port stays bound: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestHTTP3GracefulShutdown: Shutdown does not wait out an idle QUIC
// connection's timeout (clients get GOAWAY) and frees the UDP port.
func TestHTTP3GracefulShutdown(t *testing.T) {
	e := newTLSEnv(t)
	cert := e.serverCert("a.example.com")
	e.setHTTP3(true)
	e.apply(e.binding("a.example.com", cert, nil))
	h3 := e.h3Client("a.example.com", nil)
	if r := e.mustGet(h3, "a.example.com", "/"); r.status != 200 {
		t.Fatal(r.status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	e.s.Shutdown(ctx)
	if time.Since(start) > 9*time.Second {
		t.Fatal("shutdown waited for the idle connection's timeout")
	}
	pc, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(e.port)))
	if err != nil {
		t.Fatalf("UDP port still bound after shutdown: %v", err)
	}
	pc.Close()
}

// TestAltSvcOnlyWhereQUICMatchesTheSameBinding: a binding to a specific
// address on a port whose listener takes all addresses is not advertised
// (over QUIC the local address may be unknown, so another binding could
// answer); HTTP/3 requests and plain HTTP never get the header.
func TestAltSvcOnlyWhereQUICMatchesTheSameBinding(t *testing.T) {
	s := testServer(model.DefaultSettings())
	s.h3.Store(&h3Ports{any: map[int]bool{443: true}, ips: map[int]map[string]bool{8443: {"10.0.0.5": true}}})
	local := net.ParseIP("10.0.0.5")
	for name, tc := range map[string]struct {
		port  int
		route *route
		tls   bool
		major int
		want  bool
	}{
		"all addresses":          {443, &route{}, true, 1, true},
		"no binding (404)":       {443, nil, true, 2, true},
		"specific address":       {443, &route{ip: local}, true, 1, false},
		"specific listener":      {8443, &route{ip: local}, true, 2, true},
		"other port":             {9443, &route{}, true, 1, false},
		"plain http":             {443, &route{}, false, 1, false},
		"already over HTTP/3":    {443, &route{}, true, 3, false},
		"specific, wrong local":  {8443, &route{}, true, 1, false},
		"specific listener, h11": {8443, nil, true, 1, true},
	} {
		req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
		req.ProtoMajor = tc.major
		if tc.tls {
			req.TLS = &tls.ConnectionState{}
		}
		rec := &headerRecorder{h: http.Header{}}
		l := local
		if name == "specific, wrong local" {
			l = net.ParseIP("10.0.0.6")
		}
		s.advertiseHTTP3(rec, req, tc.port, l, tc.route)
		if got := rec.h.Get("Alt-Svc") != ""; got != tc.want {
			t.Errorf("%s: advertised = %v, want %v", name, got, tc.want)
		}
	}
}

type headerRecorder struct{ h http.Header }

func (r *headerRecorder) Header() http.Header         { return r.h }
func (r *headerRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *headerRecorder) WriteHeader(int)             {}

// TestHTTP3ListenFailureReported: a UDP port in use fails only the QUIC
// listener, reported under its own name, and HTTPS keeps working.
func TestHTTP3ListenFailureReported(t *testing.T) {
	e := newTLSEnv(t)
	busy, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(e.port)))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	cert := e.serverCert("a.example.com")
	e.setHTTP3(true)
	e.apply(e.binding("a.example.com", cert, nil))
	got := e.s.HTTP3Listeners()
	if len(got) != 1 || !strings.HasPrefix(got[0], "FAILED udp 127.0.0.1:") {
		t.Fatalf("HTTP/3 listeners: %v", got)
	}
	r := e.mustGet(e.client("a.example.com", nil), "a.example.com", "/")
	if r.status != 200 || r.header.Get("Alt-Svc") != "" {
		t.Fatalf("HTTPS with QUIC failed: %d Alt-Svc=%q", r.status, r.header.Get("Alt-Svc"))
	}
}
