package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/parthh37/nodehoster/internal/auth"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/oidc"
)

// Single sign-on to the web console with OpenID Connect (Entra ID and
// others): the authorization code flow with PKCE. The start and callback
// endpoints are unauthenticated browser navigations, not API calls, so
// they answer with redirects: into the console on success, to the login
// page with a short error code otherwise (the audit log has the details).

// setSessionCookie is the console's session cookie, the same for password
// and single sign-on.
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: auth.SessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: int(auth.SessionTTL.Seconds()),
	})
}

// authMethods tells the login page how users sign in. It is public: it
// shows no more than the login page itself would.
func (a *API) authMethods(w http.ResponseWriter, r *http.Request) {
	s := a.c.Settings().SSO
	out := map[string]any{"password": s.PasswordAllowed(), "sso": s.Enabled}
	if s.Enabled {
		out["ssoLabel"] = s.ButtonLabel()
	}
	writeJSON(w, http.StatusOK, out)
}

// origin is the console's scheme and host as the browser sees them. Behind
// a TLS-terminating proxy (the console on plain HTTP), the proxy's
// X-Forwarded-Proto and X-Forwarded-Host are used when it connects from
// this computer or a trusted proxy address (Settings.Proxy.TrustedProxies).
// A forged Host only yields a redirect URI the provider does not have
// registered, so it cannot redirect a sign-in elsewhere.
func (a *API) origin(r *http.Request) string {
	scheme, host := "http", r.Host
	if r.TLS != nil {
		scheme = "https"
	}
	if a.trustedForwarder(r) {
		if p := firstHeaderValue(r, "X-Forwarded-Proto"); p == "https" || p == "http" {
			scheme = p
		}
		if h := firstHeaderValue(r, "X-Forwarded-Host"); h != "" {
			if u, err := url.Parse("http://" + h); err == nil && u.Host == h && u.Path == "" {
				host = h
			}
		}
	}
	return scheme + "://" + host
}

func firstHeaderValue(r *http.Request, name string) string {
	v, _, _ := strings.Cut(r.Header.Get(name), ",")
	return strings.TrimSpace(v)
}

func (a *API) trustedForwarder(r *http.Request) bool {
	ip := net.ParseIP(clientIP(r))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, c := range a.c.Settings().Proxy.TrustedProxies {
		if n, err := model.ParseCIDROrIP(c); err == nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// ssoRedirectURL is the redirect URI to register at the provider.
func (a *API) ssoRedirectURL(r *http.Request) string {
	return a.origin(r) + "/api/auth/oidc/callback"
}

// safeNext keeps where the console goes after signing in to a path on
// this site: anything else (another origin, //host, a backslash that some
// browsers read as a slash, control characters) becomes "/".
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/api/") || strings.HasPrefix(next, "/login") {
		return "/"
	}
	for _, c := range next {
		if c < 0x20 || c == 0x7f || c == '\\' {
			return "/"
		}
	}
	if u, err := url.Parse(next); err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	return next
}

func (a *API) oidcConfig(s model.SSOSettings) oidc.Config {
	return oidc.Config{Issuer: s.Issuer, ClientID: s.ClientID, ClientSecret: a.c.Box.MustUnseal(s.ClientSecret), Scopes: s.Scopes}
}

// ssoFail records a failed single sign-on and sends the browser back to
// the login page with an error code (see web/src/lib/sso.ts).
func (a *API) ssoFail(w http.ResponseWriter, r *http.Request, username, code string, reason error) {
	detail := "sso: " + code
	if reason != nil {
		detail = "sso: " + reason.Error()
	}
	if len(detail) > 500 {
		detail = strings.ToValidUTF8(detail[:500], "") + "…"
	}
	a.c.AddAudit(context.Background(), model.AuditEntry{Time: time.Now(), User: username, IP: clientIP(r), Action: "login.failed", Target: username, Detail: detail})
	http.Redirect(w, r, "/login?sso_error="+url.QueryEscape(code), http.StatusSeeOther)
}

func (a *API) oidcStart(w http.ResponseWriter, r *http.Request) {
	cfg := a.c.Settings().SSO
	if !cfg.Enabled {
		http.Redirect(w, r, "/login?sso_error=off", http.StatusSeeOther)
		return
	}
	if a.c.Auth.SSOThrottled(clientIP(r)) {
		http.Redirect(w, r, "/login?sso_error=locked", http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	p, err := a.sso.Provider(ctx, cfg.Issuer)
	if err != nil {
		a.log.Warn("single sign-on: the identity provider is not reachable", "issuer", cfg.Issuer, "err", err)
		a.ssoFail(w, r, "", "provider", err)
		return
	}
	fl, binding, err := a.sso.Flows.Begin(cfg.Issuer, cfg.ClientID, a.ssoRedirectURL(r), safeNext(r.URL.Query().Get("next")))
	if err != nil {
		http.Redirect(w, r, "/login?sso_error=busy", http.StatusSeeOther)
		return
	}
	// Lax, not Strict: the callback is a navigation from the provider's
	// site, and a Strict cookie would not come with it.
	http.SetCookie(w, &http.Cookie{
		Name: oidc.CookieName(fl.State), Value: binding, Path: "/api/auth/oidc/", HttpOnly: true,
		Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: int(oidc.FlowTTL.Seconds()),
	})
	http.Redirect(w, r, a.oidcConfig(cfg).AuthCodeURL(p, fl), http.StatusFound)
}

// ssoErrorCodes are the login page's codes for sign-in refusals.
var ssoErrorCodes = []struct {
	err  error
	code string
}{
	{auth.ErrLocked, "locked"},
	{auth.ErrDisabled, "disabled"},
	{auth.ErrSSOUnknownUser, "unknown_user"},
	{auth.ErrSSONoRole, "no_role"},
	{auth.ErrSSOBadUsername, "claims"},
}

func (a *API) oidcCallback(w http.ResponseWriter, r *http.Request) {
	q, ip := r.URL.Query(), clientIP(r)
	state := q.Get("state")
	binding := ""
	if ck, err := r.Cookie(oidc.CookieName(state)); err == nil {
		binding = ck.Value
	}
	http.SetCookie(w, &http.Cookie{Name: oidc.CookieName(state), Value: "", Path: "/api/auth/oidc/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})

	cfg := a.c.Settings().SSO
	if !cfg.Enabled {
		a.ssoFail(w, r, "", "off", errors.New("single sign-on is turned off"))
		return
	}
	if a.c.Auth.SSOThrottled(ip) {
		a.ssoFail(w, r, "", "locked", auth.ErrLocked)
		return
	}
	fl, err := a.sso.Flows.Take(state, binding)
	if err != nil {
		a.c.Auth.SSOFailed(ip)
		a.ssoFail(w, r, "", "state", err)
		return
	}
	if fl.Issuer != cfg.Issuer || fl.ClientID != cfg.ClientID {
		a.ssoFail(w, r, "", "state", errors.New("the single sign-on settings changed during the sign-in"))
		return
	}
	if e := q.Get("error"); e != "" {
		// The user cancelled, or the provider refused them (not assigned
		// to the application, conditional access...). Not counted as a
		// failed attempt: nothing was guessed.
		a.ssoFail(w, r, "", "idp", errors.New("the identity provider answered "+e+": "+q.Get("error_description")))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	p, err := a.sso.Provider(ctx, cfg.Issuer)
	if err != nil {
		a.ssoFail(w, r, "", "provider", err)
		return
	}
	claims, err := a.sso.Exchange(ctx, p, a.oidcConfig(cfg), fl, q.Get("code"))
	if err != nil {
		a.c.Auth.SSOFailed(ip)
		a.ssoFail(w, r, "", "token", err)
		return
	}
	id := auth.SSOIdentity{Username: ssoUsername(claims, cfg.UsernameClaim)}
	if id.Username == "" {
		a.ssoFail(w, r, claims.String("sub"), "claims", errors.New("the ID token has no "+cfg.UsernameClaim+", preferred_username, upn or email claim"))
		return
	}
	if cfg.MapsRoles() {
		if !claims.Has(cfg.RoleClaim) && groupOverage(claims, cfg.RoleClaim) {
			a.ssoFail(w, r, id.Username, "claims", errors.New("the user is in too many groups for the token to list them (Entra ID group overage); map app roles (the roles claim) or filter the groups sent to the application"))
			return
		}
		id.RoleValues = claims.Strings(cfg.RoleClaim)
	}
	u, token, res, err := a.c.Auth.SSOLogin(ctx, cfg, id, ip, r.UserAgent())
	if err != nil {
		code := "error"
		for _, c := range ssoErrorCodes {
			if errors.Is(err, c.err) {
				code = c.code
			}
		}
		a.ssoFail(w, r, id.Username, code, err)
		return
	}
	setSessionCookie(w, r, token)
	audit := func(action, detail string) {
		a.c.AddAudit(context.Background(), model.AuditEntry{Time: time.Now(), User: u.Username, IP: ip, Action: action, Target: u.Username, Detail: detail})
	}
	if res.Created {
		audit("user.create", "created by single sign-on: "+a.describeAccess(&u.User))
	}
	if res.RoleChanged != "" {
		audit("user.update", "role "+res.RoleChanged+" (single sign-on role mapping)")
	}
	if res.KeptAdmin {
		audit("user.update", "kept the admin role: the role mapping would remove the last administrator")
	}
	audit("login", "method: sso")
	http.Redirect(w, r, fl.Next, http.StatusSeeOther)
}

// ssoUsername is the configured claim, else the usual ones for a login
// name. Entra ID puts the UPN in preferred_username (v2.0 tokens) or upn
// (v1.0 tokens). An email address the provider says it has not verified
// is not a fallback: at some providers users type it themselves.
func ssoUsername(c oidc.Claims, claim string) string {
	for _, name := range []string{claim, "preferred_username", "upn", "email"} {
		if name == "email" && c["email_verified"] == false {
			continue
		}
		if v := strings.TrimSpace(c.String(name)); v != "" {
			return v
		}
	}
	return ""
}

// groupOverage reports whether Entra ID left the claim out because the
// user has too many groups, pointing to Graph instead (_claim_names).
func groupOverage(c oidc.Claims, claim string) bool {
	names, _ := c["_claim_names"].(map[string]any)
	_, ok := names[claim]
	return ok
}

// ---- administration

func (a *API) ssoCallbackURL(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"redirectUrl": a.ssoRedirectURL(r)})
}

// ssoTestResult is what "Test configuration" reports: the provider's
// endpoints as discovered, and every problem found.
type ssoTestResult struct {
	OK                    bool     `json:"ok"`
	Issuer                string   `json:"issuer,omitempty"`
	AuthorizationEndpoint string   `json:"authorizationEndpoint,omitempty"`
	TokenEndpoint         string   `json:"tokenEndpoint,omitempty"`
	JWKSURI               string   `json:"jwksUri,omitempty"`
	Keys                  int      `json:"keys"`
	RedirectURL           string   `json:"redirectUrl"`
	Problems              []string `json:"problems"`
}

// ssoTest checks the (possibly unsaved) settings against the provider:
// discovery and the signing keys. The client secret can only be proven by
// a real sign-in.
func (a *API) ssoTest(w http.ResponseWriter, r *http.Request) {
	var in model.SSOSettings
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	in.Normalize()
	res := ssoTestResult{RedirectURL: a.ssoRedirectURL(r), Problems: []string{}}
	defer func() {
		res.OK = len(res.Problems) == 0
		writeJSON(w, http.StatusOK, res)
	}()
	problem := func(s string) { res.Problems = append(res.Problems, s) }
	if in.ClientID == "" {
		problem("Enter the application (client) ID.")
	}
	if in.ClientSecret == "" {
		problem("Enter the client secret.")
	}
	if err := model.CheckIssuerURL(in.Issuer); err != nil {
		problem("Issuer: " + err.Error() + ".")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	p, err := oidc.Discover(ctx, a.sso.HTTP, in.Issuer)
	if err != nil {
		problem(err.Error())
		return
	}
	res.Issuer, res.AuthorizationEndpoint, res.TokenEndpoint, res.JWKSURI = p.Issuer, p.AuthorizationEndpoint, p.TokenEndpoint, p.JWKSURI
	if len(p.ResponseTypes) > 0 && !slices.Contains(p.ResponseTypes, "code") {
		problem("The provider does not offer the authorization code flow (response type \"code\").")
	}
	if len(p.CodeChallengeMethods) > 0 && !slices.Contains(p.CodeChallengeMethods, "S256") {
		problem("The provider does not support PKCE with S256.")
	}
	if len(p.SigningAlgs) > 0 && !slices.ContainsFunc(oidc.Algorithms, func(alg jose.SignatureAlgorithm) bool { return slices.Contains(p.SigningAlgs, string(alg)) }) {
		problem("The provider signs ID tokens only with " + strings.Join(p.SigningAlgs, ", ") + "; NodeHoster accepts RS256, PS256 or ES256 (and their longer variants).")
	}
	keys, err := p.Keys(ctx)
	if err != nil {
		problem(err.Error())
		return
	}
	res.Keys = len(keys)
}
