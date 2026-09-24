package model

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
)

// SSOSettings configure single sign-on to the web console with an OpenID
// Connect provider: Microsoft Entra ID (what IIS sites get from Windows
// authentication plus Azure AD) or any other (Okta, Google, Keycloak,
// AD FS...). The console uses the authorization code flow with PKCE;
// multi-factor authentication is the provider's job, so SSO sign-ins skip
// NodeHoster's own two-factor code.
type SSOSettings struct {
	Enabled bool   `json:"enabled"`
	Label   string `json:"label,omitempty"` // sign-in button; "" = see ButtonLabel
	// Issuer is the provider's issuer URL; its discovery document is at
	// <issuer>/.well-known/openid-configuration. For Entra ID:
	// https://login.microsoftonline.com/<tenant ID>/v2.0.
	Issuer       string   `json:"issuer"`
	ClientID     string   `json:"clientId"`
	ClientSecret string   `json:"clientSecret"` // secret
	Scopes       []string `json:"scopes"`       // requested besides openid, profile and email
	// UsernameClaim names the ID token claim matched (case-insensitively)
	// against NodeHoster user names. When it is missing from a token,
	// preferred_username, upn and email are tried in that order.
	UsernameClaim string `json:"usernameClaim"`
	// DisablePassword turns password sign-in off while SSO is enabled.
	// Local administrators keep their way in: NodeHoster Manager needs no
	// sign-in, and `nodehoster reset-password` turns password sign-in back
	// on.
	DisablePassword bool `json:"disablePassword"`

	// Provisioning. By default only existing users can sign in.
	//
	// AutoCreate creates a user for an unknown name, with the role from the
	// mapping or DefaultRole. Such users are marked User.SSO and have no
	// password.
	AutoCreate bool `json:"autoCreate"`
	// DefaultRole is the role of a new user no mapping rule matches (and,
	// with a mapping, of an existing one). "" = none: they are refused.
	DefaultRole Role `json:"defaultRole,omitempty"`
	// RoleClaim names a claim holding groups or app roles ("groups",
	// "roles"). With it and RoleMap set, the role of every SSO sign-in is
	// worked out again from the token: the first matching rule wins.
	RoleClaim string        `json:"roleClaim,omitempty"`
	RoleMap   []SSORoleRule `json:"roleMap"`
}

// SSORoleRule maps a value of the role claim (an Entra group object ID or
// app role name) to a server role. Mapping never gives site grants: those
// stay manual.
type SSORoleRule struct {
	Value string `json:"value"`
	Role  Role   `json:"role"` // admin | operator | viewer
}

// DefaultUsernameClaim is used when SSOSettings.UsernameClaim is empty.
const DefaultUsernameClaim = "preferred_username"

// IsEntraIssuer reports whether issuer is Microsoft Entra ID's.
func IsEntraIssuer(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "login.microsoftonline.com", "login.microsoftonline.us", "login.partner.microsoftonline.cn", "sts.windows.net":
		return true
	}
	return false
}

// ButtonLabel is the text of the login page's SSO button.
func (s *SSOSettings) ButtonLabel() string {
	if l := strings.TrimSpace(s.Label); l != "" {
		return l
	}
	if IsEntraIssuer(s.Issuer) {
		return "Sign in with Microsoft"
	}
	return "Sign in with SSO"
}

// PasswordAllowed reports whether password sign-in is on.
func (s *SSOSettings) PasswordAllowed() bool { return !s.Enabled || !s.DisablePassword }

// MapsRoles reports whether roles come from the role claim.
func (s *SSOSettings) MapsRoles() bool { return s.RoleClaim != "" && len(s.RoleMap) > 0 }

func serverRole(r Role) bool { return r == RoleAdmin || r == RoleOperator || r == RoleViewer }

// Normalize trims the fields and fills defaults. It keeps the settings of
// a disabled configuration, so turning SSO off and on again loses nothing.
func (s *SSOSettings) Normalize() {
	s.Label = strings.TrimSpace(s.Label)
	s.Issuer = strings.TrimSpace(s.Issuer) // exactly as the provider writes it: some end in "/"
	s.ClientID = strings.TrimSpace(s.ClientID)
	s.UsernameClaim = strings.TrimSpace(s.UsernameClaim)
	if s.UsernameClaim == "" {
		s.UsernameClaim = DefaultUsernameClaim
	}
	s.RoleClaim = strings.TrimSpace(s.RoleClaim)
	scopes := []string{}
	for _, sc := range s.Scopes {
		for _, f := range strings.Fields(sc) {
			if f != "openid" && !slices.Contains(scopes, f) {
				scopes = append(scopes, f)
			}
		}
	}
	s.Scopes = scopes
	for i := range s.RoleMap {
		s.RoleMap[i].Value = strings.TrimSpace(s.RoleMap[i].Value)
	}
}

// Validate checks normalized settings. Only an enabled configuration must
// be complete.
func (s *SSOSettings) Validate() error {
	if s.DefaultRole != "" && !serverRole(s.DefaultRole) {
		return verr("sso.defaultRole", "admin, operator or viewer (or none)")
	}
	for i, r := range s.RoleMap {
		f := fmt.Sprintf("sso.roleMap[%d]", i)
		if r.Value == "" {
			return verr(f+".value", "enter the group or role value")
		}
		if !serverRole(r.Role) {
			return verr(f+".role", "admin, operator or viewer: site access is granted by hand")
		}
	}
	if len(s.RoleMap) > 0 && s.RoleClaim == "" {
		return verr("sso.roleClaim", "name the claim holding the groups or roles, e.g. groups or roles")
	}
	if !s.Enabled {
		return nil
	}
	if err := CheckIssuerURL(s.Issuer); err != nil {
		return verr("sso.issuer", "%v", err)
	}
	if s.ClientID == "" {
		return verr("sso.clientId", "enter the application (client) ID")
	}
	if s.ClientSecret == "" {
		return verr("sso.clientSecret", "enter the single sign-on client secret")
	}
	if s.AutoCreate && s.DefaultRole == "" && !s.MapsRoles() {
		return verr("sso.defaultRole", "new users need a role: choose a default role or map roles from a claim")
	}
	return nil
}

// CheckIssuerURL requires HTTPS, except to this computer (a test provider).
func CheckIssuerURL(issuer string) error {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("enter the issuer URL, e.g. https://login.microsoftonline.com/<tenant ID>/v2.0")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if ip := net.ParseIP(u.Hostname()); !strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("the issuer must use https://")
		}
	default:
		return fmt.Errorf("the issuer must use https://")
	}
	if IsEntraIssuer(issuer) {
		switch strings.ToLower(strings.Trim(u.Path, "/")) {
		case "common/v2.0", "organizations/v2.0", "consumers/v2.0", "common", "organizations", "consumers":
			return fmt.Errorf("use your directory (tenant) ID instead of %q: sign-ins must come from your tenant", strings.Split(strings.Trim(u.Path, "/"), "/")[0])
		}
	}
	return nil
}
