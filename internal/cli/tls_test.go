//go:build !windows

package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestTLSCommands(t *testing.T) {
	s := newServer(t)
	r := s.run("tls").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "HTTP/3 (QUIC over UDP)  off") || !strings.Contains(r.stdout, "Minimum TLS version     1.2") {
		t.Fatalf("tls:\n%s", r.stdout)
	}
	s.run("tls", "set").expect(t, ExitUsage)
	s.run("tls", "set", "--http3", "maybe").expect(t, ExitUsage)
	r = s.run("tls", "set", "--min-version", "1.0").expect(t, ExitError)
	if !strings.Contains(r.stderr, "1.2 or 1.3") {
		t.Fatalf("bad version: %s", r.stderr)
	}

	r = s.run("tls", "set", "--http3", "on", "--min-version", "1.3").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "allow UDP") {
		t.Fatalf("tls set:\n%s", r.stdout)
	}
	if got := s.c.Settings().TLS; !got.HTTP3 || !got.HTTP2 || got.MinVersion != "1.3" {
		t.Fatalf("settings = %+v", got)
	}
	r = s.run("tls", "--json").expect(t, ExitOK)
	var v model.TLSView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || !v.HTTP3 {
		t.Fatalf("tls --json: %s", r.stdout)
	}
	s.run("tls", "set", "--http3", "off").expect(t, ExitOK)
	if s.c.Settings().TLS.HTTP3 {
		t.Fatal("HTTP/3 still on")
	}
}

func TestCertOCSPCommand(t *testing.T) {
	s := newServer(t)
	var c certView
	if err := s.cl.Post(context.Background(), "/api/certificates/selfsigned", map[string]any{"name": "intranet", "domains": []string{"intranet.local"}}, &c); err != nil {
		t.Fatal(err)
	}
	r := s.run("cert", "list").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "OCSP") || !strings.Contains(r.stdout, "no responder") {
		t.Fatalf("cert list:\n%s", r.stdout)
	}
	r = s.run("cert", "ocsp", "intranet").expect(t, ExitOK)
	if !strings.Contains(r.stdout, "no responder") || !strings.Contains(r.stdout, "intranet") {
		t.Fatalf("cert ocsp:\n%s", r.stdout)
	}
	s.run("cert", "ocsp", "nothing.local").expect(t, ExitError)
}
