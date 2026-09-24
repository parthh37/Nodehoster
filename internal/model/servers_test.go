package model

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeServerURL(t *testing.T) {
	for _, tc := range []struct{ in, want, err string }{
		{"https://web02:8484", "https://web02:8484", ""},
		{"https://WEB02.example.com:8484/", "https://web02.example.com:8484", ""},
		{"web02:8484", "https://web02:8484", ""},
		{"https://web02:8484/api", "https://web02:8484", ""},
		{"https://gw.example.com/nodehoster/", "https://gw.example.com/nodehoster", ""},
		{"http://127.0.0.1:8484", "http://127.0.0.1:8484", ""},
		{"http://localhost:8484", "http://localhost:8484", ""},
		{"http://[::1]:8484", "http://[::1]:8484", ""},
		{"http://web02:8484", "", "https"},
		{"ftp://web02", "", "https"},
		{"", "", "enter"},
		{"https://", "", "enter"},
		{"https://u:p@web02:8484", "", "user name"},
		{"https://web02:8484/?a=1", "", "query"},
		{"https://web02:8484/#x", "", "fragment"},
		{"https://web02:99999", "", "port"},
		{"https://web02/a/../b", "", ". or .."},
	} {
		got, err := NormalizeServerURL(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q: got %q, %v; want error containing %q", tc.in, got, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q: got %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestParseFingerprint(t *testing.T) {
	const hexFP = "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
	for _, in := range []string{
		hexFP,
		strings.ToLower(hexFP),
		FormatFingerprint(hexFP),
		"SHA256:" + strings.ToLower(hexFP),
		"sha-256: " + FormatFingerprint(hexFP),
		strings.ReplaceAll(FormatFingerprint(hexFP), ":", " "),
	} {
		got, err := ParseFingerprint(in)
		if err != nil || got != hexFP {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	if got, err := ParseFingerprint("  "); got != "" || err != nil {
		t.Errorf("empty: %q, %v", got, err)
	}
	for _, bad := range []string{"abcd", hexFP + "00", "ZZ" + hexFP[2:], "SHA1:" + hexFP} {
		if _, err := ParseFingerprint(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got := FormatFingerprint("ABCDEF"); got != "AB:CD:EF" {
		t.Errorf("format: %q", got)
	}
}

func TestServerConnectionValidate(t *testing.T) {
	const fp = "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
	ok := func() ServerConnection {
		return ServerConnection{ID: "a", Name: "Web 02", URL: "https://web02:8484", Token: "nh_x"}
	}
	others := []ServerConnection{{ID: "b", Name: "web 03"}}
	for _, tc := range []struct {
		name  string
		edit  func(*ServerConnection)
		field string // "" = valid
	}{
		{"complete", func(*ServerConnection) {}, ""},
		{"pinned", func(s *ServerConnection) { s.Fingerprint = FormatFingerprint(fp) }, ""},
		{"operators may use it", func(s *ServerConnection) { s.MinRole = RoleOperator }, ""},
		{"no name", func(s *ServerConnection) { s.Name = " " }, "name"},
		{"long name", func(s *ServerConnection) { s.Name = strings.Repeat("x", 65) }, "name"},
		{"duplicate name", func(s *ServerConnection) { s.Name = "WEB 03" }, "name"},
		{"same connection keeps its name", func(s *ServerConnection) { s.ID = "b"; s.Name = "web 03" }, ""},
		{"no url", func(s *ServerConnection) { s.URL = "" }, "url"},
		{"plain http", func(s *ServerConnection) { s.URL = "http://web02:8484" }, "url"},
		{"bad fingerprint", func(s *ServerConnection) { s.Fingerprint = "abc" }, "fingerprint"},
		{"fingerprint over http", func(s *ServerConnection) { s.URL, s.Fingerprint = "http://127.0.0.1:8484", fp }, "fingerprint"},
		{"no token", func(s *ServerConnection) { s.Token = "" }, "token"},
		{"site-scoped role", func(s *ServerConnection) { s.MinRole = RoleSites }, "minRole"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ok()
			tc.edit(&s)
			s.ApplyDefaults()
			err := s.Validate(others)
			var ve *ValidationError
			switch {
			case tc.field == "" && err != nil:
				t.Fatalf("unexpected error %v", err)
			case tc.field != "" && (!errors.As(err, &ve) || ve.Field != tc.field):
				t.Fatalf("error %v, want one on %s", err, tc.field)
			}
		})
	}
	s := ServerConnection{Name: " x ", URL: "WEB02:8484/", Token: " t ", Fingerprint: strings.ToLower(fp)}
	s.ApplyDefaults()
	if s.Name != "x" || s.URL != "https://web02:8484" || s.Token != "t" || s.Fingerprint != fp || s.MinRole != RoleAdmin {
		t.Fatalf("defaults: %+v", s)
	}
}

func TestRoleAtLeast(t *testing.T) {
	for _, tc := range []struct {
		r, need Role
		want    bool
	}{
		{RoleAdmin, RoleOperator, true},
		{RoleOperator, RoleOperator, true},
		{RoleViewer, RoleOperator, false},
		{RoleSites, RoleViewer, false},
		{RoleAdmin, "", false},
	} {
		if got := RoleAtLeast(tc.r, tc.need); got != tc.want {
			t.Errorf("RoleAtLeast(%s, %s) = %v", tc.r, tc.need, got)
		}
	}
}
