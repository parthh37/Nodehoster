package proxy

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/ipban"
	"github.com/parthh37/nodehoster/internal/model"
)

// banServer is a proxy Server with automatic banning on, every rule
// banning at the first offence.
func banServer(t *testing.T) *Server {
	t.Helper()
	s := testServer(model.DefaultSettings())
	s.deps.Bans = ipban.New(ipban.Options{})
	b := model.DefaultIPBan()
	b.Enabled = true
	b.AuthFailures = model.BanRule{Threshold: 1, WindowSec: 60}
	b.ApplyDefaults()
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	s.deps.Bans.Apply(b, nil)
	return s
}

// TestAuthFailuresCountOnlyPasswordGuesses: a 401 bans only when a
// password was tried (basic credentials, a submitted form), never an
// expired bearer token, an API call, a heartbeat or a CORS preflight.
func TestAuthFailuresCountOnlyPasswordGuesses(t *testing.T) {
	s := banServer(t)
	for i, tc := range []struct {
		name   string
		method string
		hdr    []string
		want   bool
	}{
		{"asked to sign in", "GET", nil, false},
		{"wrong basic password", "GET", []string{"Authorization", "Basic dTp3cm9uZw=="}, true},
		{"basic, lower case", "GET", []string{"Authorization", "basic dTp3cm9uZw=="}, true},
		{"login form", "POST", []string{"Content-Type", "application/x-www-form-urlencoded"}, true},
		{"multipart login form", "POST", []string{"Content-Type", "multipart/form-data; boundary=x"}, true},
		{"expired bearer token", "GET", []string{"Authorization", "Bearer eyJhbGciOi"}, false},
		{"bearer token on a form post", "POST", []string{"Authorization", "Bearer eyJhbGciOi", "Content-Type", "application/x-www-form-urlencoded"}, false},
		{"JSON heartbeat", "POST", []string{"Content-Type", "application/json"}, false},
		{"POST without a body", "POST", nil, false},
		{"JSON PUT", "PUT", []string{"Content-Type", "application/json"}, false},
		{"form PUT", "PUT", []string{"Content-Type", "application/x-www-form-urlencoded"}, false},
		{"CORS preflight", "OPTIONS", []string{"Access-Control-Request-Method", "POST"}, false},
		{"OPTIONS with basic", "OPTIONS", []string{"Authorization", "Basic dTp3cm9uZw=="}, false},
		{"DELETE", "DELETE", nil, false},
	} {
		ip := net.ParseIP(fmt.Sprintf("203.0.113.%d", i+1))
		req := httptest.NewRequest(tc.method, "http://app.example.com/", strings.NewReader(""))
		for j := 0; j+1 < len(tc.hdr); j += 2 {
			req.Header.Set(tc.hdr[j], tc.hdr[j+1])
		}
		s.countForBan(ip, req, http.StatusUnauthorized)
		if got := s.deps.Bans.Banned(ip); got != tc.want {
			t.Errorf("%s: banned = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestClientIPBehindTrustedProxies: only entries trusted proxies appended
// are believed. Entries with a port (as Azure Application Gateway writes
// them) are read, and one that cannot be read never lets the walk go on
// into what the client wrote.
func TestClientIPBehindTrustedProxies(t *testing.T) {
	s := testServer(model.DefaultSettings())
	_, lan, _ := net.ParseCIDR("10.0.0.0/8")
	s.trusted = []*net.IPNet{lan}
	for _, tc := range []struct {
		name string
		peer string
		xff  []string
		want string
	}{
		{"direct client", "198.51.100.7:4000", []string{"1.2.3.4"}, "198.51.100.7"},
		{"no header", "10.0.0.1:4000", nil, "10.0.0.1"},
		{"one hop", "10.0.0.1:4000", []string{"198.51.100.7"}, "198.51.100.7"},
		{"client-written entry", "10.0.0.1:4000", []string{"1.2.3.4, 198.51.100.7"}, "198.51.100.7"},
		{"chain of trusted proxies", "10.0.0.1:4000", []string{"198.51.100.7, 10.0.0.2"}, "198.51.100.7"},
		{"with a port", "10.0.0.1:4000", []string{"1.2.3.4, 198.51.100.7:51234"}, "198.51.100.7"},
		{"IPv6 with a port", "10.0.0.1:4000", []string{"1.2.3.4, [2001:db8::7]:51234"}, "2001:db8::7"},
		{"IPv6 in brackets", "10.0.0.1:4000", []string{"1.2.3.4, [2001:db8::7]"}, "2001:db8::7"},
		{"trusted proxy with a port", "10.0.0.1:4000", []string{"198.51.100.7:1, 10.0.0.2:2"}, "198.51.100.7"},
		{"unreadable entry", "10.0.0.1:4000", []string{"1.2.3.4, unknown"}, "10.0.0.1"},
		{"unreadable entry after a trusted hop", "10.0.0.1:4000", []string{"1.2.3.4, bogus:80, 10.0.0.2"}, "10.0.0.1"},
		{"empty entry", "10.0.0.1:4000", []string{"1.2.3.4, , 10.0.0.2"}, "10.0.0.1"},
		{"a proxy adding its own line", "10.0.0.1:4000", []string{"1.2.3.4", "198.51.100.7"}, "198.51.100.7"},
	} {
		req := httptest.NewRequest("GET", "http://app.example.com/", nil)
		req.RemoteAddr = tc.peer
		for _, v := range tc.xff {
			req.Header.Add("X-Forwarded-For", v)
		}
		if got := s.clientIP(req); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}
