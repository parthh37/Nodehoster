package model

import (
	"errors"
	"testing"
)

func TestSSOValidate(t *testing.T) {
	ok := func() SSOSettings {
		return SSOSettings{Enabled: true, Issuer: "https://login.microsoftonline.com/0f1c/v2.0", ClientID: "app", ClientSecret: "sealed"}
	}
	for _, tc := range []struct {
		name  string
		edit  func(*SSOSettings)
		field string // "" = valid
	}{
		{"complete", func(*SSOSettings) {}, ""},
		{"disabled and empty", func(s *SSOSettings) { *s = SSOSettings{} }, ""},
		{"no issuer", func(s *SSOSettings) { s.Issuer = "" }, "sso.issuer"},
		{"plain http", func(s *SSOSettings) { s.Issuer = "http://idp.example.com" }, "sso.issuer"},
		{"http to this computer", func(s *SSOSettings) { s.Issuer = "http://127.0.0.1:5556/dex" }, ""},
		{"entra common endpoint", func(s *SSOSettings) { s.Issuer = "https://login.microsoftonline.com/common/v2.0" }, "sso.issuer"},
		{"issuer with query", func(s *SSOSettings) { s.Issuer = "https://idp.example.com/?x=1" }, "sso.issuer"},
		{"no client ID", func(s *SSOSettings) { s.ClientID = "" }, "sso.clientId"},
		{"no secret", func(s *SSOSettings) { s.ClientSecret = "" }, "sso.clientSecret"},
		{"auto-create without a role", func(s *SSOSettings) { s.AutoCreate = true }, "sso.defaultRole"},
		{"auto-create with a default role", func(s *SSOSettings) { s.AutoCreate, s.DefaultRole = true, RoleViewer }, ""},
		{"auto-create with a mapping", func(s *SSOSettings) {
			s.AutoCreate, s.RoleClaim, s.RoleMap = true, "groups", []SSORoleRule{{Value: "g", Role: RoleAdmin}}
		}, ""},
		{"default role sites", func(s *SSOSettings) { s.DefaultRole = RoleSites }, "sso.defaultRole"},
		{"rule to sites", func(s *SSOSettings) { s.RoleClaim, s.RoleMap = "groups", []SSORoleRule{{Value: "g", Role: RoleSites}} }, "sso.roleMap[0].role"},
		{"rule without value", func(s *SSOSettings) { s.RoleClaim, s.RoleMap = "groups", []SSORoleRule{{Role: RoleViewer}} }, "sso.roleMap[0].value"},
		{"rules without claim", func(s *SSOSettings) { s.RoleMap = []SSORoleRule{{Value: "g", Role: RoleViewer}} }, "sso.roleClaim"},
		{"bad rule while disabled", func(s *SSOSettings) { s.Enabled, s.RoleMap = false, []SSORoleRule{{Value: "g", Role: "root"}} }, "sso.roleMap[0].role"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ok()
			tc.edit(&s)
			s.Normalize()
			err := s.Validate()
			var ve *ValidationError
			switch {
			case tc.field == "" && err != nil:
				t.Fatalf("unexpected error %v", err)
			case tc.field != "" && (!errors.As(err, &ve) || ve.Field != tc.field):
				t.Fatalf("error = %v, want one on %s", err, tc.field)
			}
		})
	}
}

func TestSSONormalizeAndLabel(t *testing.T) {
	s := SSOSettings{Issuer: " https://idp.example.com/ ", Scopes: []string{"groups  offline_access", "openid", "groups"}, RoleMap: []SSORoleRule{{Value: " g1 "}}}
	s.Normalize()
	if s.Issuer != "https://idp.example.com/" || s.UsernameClaim != "preferred_username" || len(s.Scopes) != 2 || s.Scopes[1] != "offline_access" || s.RoleMap[0].Value != "g1" {
		t.Fatalf("normalized: %+v", s)
	}
	if l := s.ButtonLabel(); l != "Sign in with SSO" {
		t.Errorf("label = %q", l)
	}
	s.Issuer = "https://login.microsoftonline.com/0f1c/v2.0"
	if l := s.ButtonLabel(); l != "Sign in with Microsoft" {
		t.Errorf("label = %q", l)
	}
	s.Label = "Contoso account"
	if l := s.ButtonLabel(); l != "Contoso account" {
		t.Errorf("label = %q", l)
	}
	if !s.PasswordAllowed() {
		t.Error("password sign-in off while SSO is disabled")
	}
	s.DisablePassword = true
	if !s.PasswordAllowed() {
		t.Error("DisablePassword applies only with SSO enabled")
	}
	s.Enabled = true
	if s.PasswordAllowed() {
		t.Error("password sign-in still allowed")
	}
}
