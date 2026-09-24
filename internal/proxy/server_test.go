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
