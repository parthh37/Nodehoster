package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/parthh37/nodehoster/internal/auth"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/oidc"
	"github.com/parthh37/nodehoster/internal/secrets"
)

// fakeIdP is an in-process OpenID Connect provider: discovery, a JWKS, and
// a token endpoint that checks the client, the redirect URI and the PKCE
// verifier before handing out an ID token signed with its current key.
type fakeIdP struct {
	t        *testing.T
	srv      *httptest.Server
	issuer   string
	clientID string
	secret   string

	mu        sync.Mutex
	published []fakeKey // in the JWKS
	signer    fakeKey   // signs ID tokens
	codes     map[string]fakeCode
	jwksHits  int
}

type fakeKey struct {
	kid  string
	alg  jose.SignatureAlgorithm
	priv any
}

func (k fakeKey) public() jose.JSONWebKey {
	var pub any
	switch p := k.priv.(type) {
	case *rsa.PrivateKey:
		pub = &p.PublicKey
	case *ecdsa.PrivateKey:
		pub = &p.PublicKey
	}
	return jose.JSONWebKey{Key: pub, KeyID: k.kid, Algorithm: string(k.alg), Use: "sig"}
}

type fakeCode struct {
	challenge, redirectURI string
	token                  string
}

func rsaKey(t *testing.T, kid string) fakeKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return fakeKey{kid: kid, alg: jose.RS256, priv: k}
}

func ecKey(t *testing.T, kid string) fakeKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return fakeKey{kid: kid, alg: jose.ES256, priv: k}
}

func newFakeIdP(t *testing.T) *fakeIdP {
	p := &fakeIdP{t: t, clientID: "nodehoster-client", secret: "s3cret-value", codes: map[string]fakeCode{}}
	key := rsaKey(t, "rsa-1")
	p.published, p.signer = []fakeKey{key}, key
	mux := http.NewServeMux()
	mux.HandleFunc("/tenant/v2.0/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.issuer, "authorization_endpoint": p.srv.URL + "/authorize", "token_endpoint": p.srv.URL + "/token",
			"jwks_uri": p.srv.URL + "/jwks", "response_types_supported": []string{"code"},
			"id_token_signing_alg_values_supported": []string{"RS256", "ES256"}, "code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.jwksHits++
		var set jose.JSONWebKeySet
		for _, k := range p.published {
			set.Keys = append(set.Keys, k.public())
		}
		json.NewEncoder(w).Encode(set)
	})
	mux.HandleFunc("/token", p.token)
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	p.issuer = p.srv.URL + "/tenant/v2.0"
	return p
}

func (p *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	fail := func(code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	if id != p.clientID || secret != p.secret {
		fail("invalid_client")
		return
	}
	p.mu.Lock()
	c, found := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	switch {
	case r.PostForm.Get("grant_type") != "authorization_code" || !found:
		fail("invalid_grant")
	case r.PostForm.Get("redirect_uri") != c.redirectURI:
		fail("invalid_grant")
	case base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge:
		fail("invalid_grant") // PKCE
	default:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": c.token})
	}
}

// claims are an ID token's for a sign-in the fake provider is answering;
// overrides replace them (a nil value removes one).
func (p *fakeIdP) claims(authURL *url.URL, username string, overrides map[string]any) map[string]any {
	now := time.Now()
	c := map[string]any{
		"iss": p.issuer, "aud": p.clientID, "sub": "sub-" + username, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"nonce": authURL.Query().Get("nonce"), "preferred_username": username,
	}
	for k, v := range overrides {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return c
}

func (p *fakeIdP) sign(claims map[string]any) string {
	p.mu.Lock()
	k := p.signer
	p.mu.Unlock()
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: k.alg, Key: k.priv}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", k.kid))
	if err != nil {
		p.t.Fatal(err)
	}
	raw, err := jwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		p.t.Fatal(err)
	}
	return raw
}

// forged makes a token with the given header and signature algorithm
// (none or HS256 with the client secret), which must be refused.
func forged(alg string, claims map[string]any, hmacKey string) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	head := enc(map[string]string{"alg": alg, "typ": "JWT", "kid": "rsa-1"}) + "." + enc(claims)
	if alg == "none" {
		return head + "."
	}
	m := hmac.New(sha256.New, []byte(hmacKey))
	m.Write([]byte(head))
	return head + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// authorize plays the user signing in at the provider: it checks the
// authorization request and returns a code bound to it, redeemable for
// token.
func (p *fakeIdP) authorize(loc *url.URL, token string) string {
	p.t.Helper()
	q := loc.Query()
	if q.Get("client_id") != p.clientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("nonce") == "" || !strings.Contains(q.Get("scope"), "openid") {
		p.t.Fatalf("bad authorization request: %s", loc)
	}
	code := "code-" + q.Get("state")
	p.mu.Lock()
	p.codes[code] = fakeCode{challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri"), token: token}
	p.mu.Unlock()
	return code
}

func (p *fakeIdP) rotate(k fakeKey) {
	p.mu.Lock()
	p.published, p.signer = []fakeKey{k}, k
	p.mu.Unlock()
}

// ssoEnv is an env with single sign-on configured against a fake provider.
func ssoEnv(t *testing.T, edit func(*model.SSOSettings)) (*env, *fakeIdP) {
	t.Helper()
	e := newEnv(t)
	p := newFakeIdP(t)
	e.setSSO(func(s *model.SSOSettings) {
		*s = model.SSOSettings{Enabled: true, Issuer: p.issuer, ClientID: p.clientID, ClientSecret: p.secret}
		if edit != nil {
			edit(s)
		}
	})
	return e, p
}

func (e *env) setSSO(edit func(*model.SSOSettings)) {
	e.t.Helper()
	s := e.c.Settings()
	edit(&s.SSO) // the sealed client secret passes through as it is
	if _, err := e.c.UpdateSettings(context.Background(), s); err != nil {
		e.t.Fatalf("UpdateSettings: %v", err)
	}
}

var ssoClients atomic.Int32

// ssoClient gives each sign-in its own client address: failures are
// throttled per address, and most tests fail on purpose more than five
// times.
func ssoClient() opt {
	n := ssoClients.Add(1)
	return func(r *http.Request) { r.RemoteAddr = fmt.Sprintf("10.9.%d.%d:5000", n/250, n%250+1) }
}

// ssoStart begins a sign-in and returns the provider URL and the binding
// cookie.
func (e *env) ssoStart(next string) (*url.URL, *http.Cookie) {
	e.t.Helper()
	path := "/api/auth/oidc/start"
	if next != "" {
		path += "?next=" + url.QueryEscape(next)
	}
	rec := e.do(http.MethodGet, path, nil, ssoClient())
	expect(e.t, rec, http.StatusFound)
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		e.t.Fatal(err)
	}
	for _, ck := range rec.Result().Cookies() {
		if strings.HasPrefix(ck.Name, "nh_sso_") {
			if !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode {
				e.t.Errorf("binding cookie flags: %+v", ck)
			}
			return loc, ck
		}
	}
	e.t.Fatal("no binding cookie")
	return nil, nil
}

func (e *env) ssoCallback(state, code string, ck *http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	opts := []opt{ssoClient()}
	if ck != nil {
		opts = append(opts, withCookie(ck))
	}
	return e.do(http.MethodGet, "/api/auth/oidc/callback?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil, opts...)
}

// ssoSignIn runs a whole sign-in; overrides change the ID token's claims.
func (e *env) ssoSignIn(p *fakeIdP, username string, overrides map[string]any) *httptest.ResponseRecorder {
	e.t.Helper()
	loc, ck := e.ssoStart("")
	code := p.authorize(loc, p.sign(p.claims(loc, username, overrides)))
	return e.ssoCallback(loc.Query().Get("state"), code, ck)
}

func sessionOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == auth.SessionCookie && ck.Value != "" {
			return ck
		}
	}
	return nil
}

// expectSSOError checks the callback sent the browser back to the login
// page with code, without a session.
func expectSSOError(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?sso_error="+code {
		t.Fatalf("got %d Location %q, want the login page with sso_error=%s", rec.Code, rec.Header().Get("Location"), code)
	}
	if sessionOf(rec) != nil {
		t.Fatal("a failed sign-in set a session cookie")
	}
}

func expectSignedIn(t *testing.T, e *env, rec *httptest.ResponseRecorder, wantLocation string) *http.Cookie {
	t.Helper()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != wantLocation {
		t.Fatalf("got %d Location %q, want %q", rec.Code, rec.Header().Get("Location"), wantLocation)
	}
	ck := sessionOf(rec)
	if ck == nil {
		t.Fatal("no session cookie")
	}
	if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" {
		t.Errorf("session cookie flags differ from password sign-in: %+v", ck)
	}
	expect(t, e.do(http.MethodGet, "/api/auth/me", nil, withCookie(ck)), http.StatusOK)
	return ck
}

func TestSSOSignIn(t *testing.T) {
	t.Parallel()
	e, p := ssoEnv(t, nil)
	u := e.user("Alice@Contoso.com", model.RoleOperator, false)
	// Local two-factor authentication is the provider's job for SSO.
	u.TOTPEnabled, u.TOTPSecret = true, "x"
	e.c.Store.PutUser(context.Background(), u)

	loc, ck := e.ssoStart("/sites/abc?tab=logs")
	if got := loc.Query().Get("redirect_uri"); got != "http://example.com/api/auth/oidc/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
	// The name is matched case-insensitively.
	code := p.authorize(loc, p.sign(p.claims(loc, "alice@contoso.COM", nil)))
	rec := e.ssoCallback(loc.Query().Get("state"), code, ck)
	sess := expectSignedIn(t, e, rec, "/sites/abc?tab=logs")

	me := decodeJSON[struct {
		User model.User `json:"user"`
	}](t, e.do(http.MethodGet, "/api/auth/me", nil, withCookie(sess)))
	if me.User.ID != u.ID || me.User.Role != model.RoleOperator {
		t.Errorf("signed in as %+v", me.User)
	}
	if !contains(e.auditActions(), "Alice@Contoso.com:login") {
		t.Errorf("audit: %v", e.auditActions())
	}
	list, _ := e.c.Store.ListAudit(context.Background(), 10, 0)
	if list[0].Detail != "method: sso" {
		t.Errorf("login audit detail = %q", list[0].Detail)
	}

	// The state is single-use: replaying the callback fails.
	expectSSOError(t, e.ssoCallback(loc.Query().Get("state"), code, ck), "state")
}

func TestSSOStateChecks(t *testing.T) {
	t.Parallel()
	e, p := ssoEnv(t, nil)
	e.user("alice", model.RoleViewer, false)

	expectSSOError(t, e.ssoCallback("made-up", "code", nil), "state")

	// A callback without the browser's binding cookie (another browser, or
	// an attacker's link) is refused, and does not use up the sign-in.
	loc, ck := e.ssoStart("")
	code := p.authorize(loc, p.sign(p.claims(loc, "alice", nil)))
	state := loc.Query().Get("state")
	expectSSOError(t, e.ssoCallback(state, code, nil), "state")
	expectSSOError(t, e.ssoCallback(state, code, &http.Cookie{Name: ck.Name, Value: "wrong"}), "state")
	expectSignedIn(t, e, e.ssoCallback(state, code, ck), "/")

	// A provider error is reported, not a session.
	loc, ck = e.ssoStart("")
	rec := e.do(http.MethodGet, "/api/auth/oidc/callback?"+url.Values{"state": {loc.Query().Get("state")}, "error": {"access_denied"}, "error_description": {"AADSTS50105: not assigned"}}.Encode(), nil, withCookie(ck))
	expectSSOError(t, rec, "idp")
	list, _ := e.c.Store.ListAudit(context.Background(), 1, 0)
	if list[0].Action != "login.failed" || !strings.Contains(list[0].Detail, "AADSTS50105") {
		t.Errorf("audit = %+v", list[0])
	}
}

func TestSSOTokenChecks(t *testing.T) {
	t.Parallel()
	e, p := ssoEnv(t, nil)
	e.user("alice", model.RoleViewer, false)
	past := time.Now().Add(-time.Hour)

	// lastReason is the audit detail of the latest failure.
	lastReason := func() string {
		list, _ := e.c.Store.ListAudit(context.Background(), 1, 0)
		return list[0].Detail
	}
	for _, tc := range []struct {
		name      string
		overrides map[string]any
		reason    error
	}{
		{"wrong nonce", map[string]any{"nonce": "another"}, oidc.ErrNonce},
		{"no nonce", map[string]any{"nonce": nil}, oidc.ErrNonce},
		{"wrong audience", map[string]any{"aud": "another-app"}, oidc.ErrAudience},
		{"several audiences, other azp", map[string]any{"aud": []string{p.clientID, "another-app"}, "azp": "another-app"}, oidc.ErrAudience},
		{"several audiences, no azp", map[string]any{"aud": []string{p.clientID, "another-app"}}, oidc.ErrAudience},
		{"wrong issuer", map[string]any{"iss": "https://login.example.org/evil/v2.0"}, oidc.ErrIssuer},
		{"expired", map[string]any{"exp": past.Unix(), "iat": past.Add(-time.Hour).Unix()}, oidc.ErrExpired},
		{"no expiry", map[string]any{"exp": nil}, oidc.ErrExpired},
		{"issued in the future", map[string]any{"iat": time.Now().Add(time.Hour).Unix()}, oidc.ErrExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectSSOError(t, e.ssoSignIn(p, "alice", tc.overrides), "token")
			if got := lastReason(); got != "sso: "+tc.reason.Error() {
				t.Errorf("reason = %q, want %q", got, tc.reason)
			}
		})
	}

	// Within the allowed clock skew is fine.
	expectSignedIn(t, e, e.ssoSignIn(p, "alice", map[string]any{"exp": time.Now().Add(-30 * time.Second).Unix()}), "/")
	// Several audiences with azp naming us is fine too.
	expectSignedIn(t, e, e.ssoSignIn(p, "alice", map[string]any{"aud": []string{p.clientID, "api://other"}, "azp": p.clientID}), "/")

	for _, alg := range []string{"none", "HS256"} {
		t.Run("alg "+alg, func(t *testing.T) {
			loc, ck := e.ssoStart("")
			tok := forged(alg, p.claims(loc, "alice", nil), p.secret)
			expectSSOError(t, e.ssoCallback(loc.Query().Get("state"), p.authorize(loc, tok), ck), "token")
			if got := lastReason(); got != "sso: "+oidc.ErrMalformed.Error() {
				t.Errorf("reason = %q", got)
			}
		})
	}

	// Signed by a key the provider does not publish.
	loc, ck := e.ssoStart("")
	p.mu.Lock()
	p.signer = rsaKey(t, "rsa-1") // same key ID, other key
	p.mu.Unlock()
	expectSSOError(t, e.ssoCallback(loc.Query().Get("state"), p.authorize(loc, p.sign(p.claims(loc, "alice", nil))), ck), "token")
	if got := lastReason(); got != "sso: "+oidc.ErrSignature.Error() {
		t.Errorf("reason = %q", got)
	}

	// The client secret is checked by the provider.
	e.setSSO(func(s *model.SSOSettings) { s.ClientSecret = "wrong" })
	expectSSOError(t, e.ssoSignIn(p, "alice", nil), "token")
	if got := lastReason(); !strings.Contains(got, "invalid_client") {
		t.Errorf("reason = %q", got)
	}
}

func TestSSOKeyRotation(t *testing.T) {
	t.Parallel()
	e, p := ssoEnv(t, nil)
	e.user("alice", model.RoleViewer, false)
	expectSignedIn(t, e, e.ssoSignIn(p, "alice", nil), "/")
	expectSignedIn(t, e, e.ssoSignIn(p, "alice", nil), "/")
	if p.jwksHits != 1 {
		t.Fatalf("JWKS fetched %d times, want 1 (cached)", p.jwksHits)
	}
	// The provider rotates to an EC key: the unknown key ID makes us
	// fetch the keys again.
	p.rotate(ecKey(t, "ec-1"))
	expectSignedIn(t, e, e.ssoSignIn(p, "alice", nil), "/")
	if p.jwksHits != 2 {
		t.Fatalf("JWKS fetched %d times, want 2", p.jwksHits)
	}
}

func TestSSOProvisioning(t *testing.T) {
	t.Parallel()

	t.Run("off by default", func(t *testing.T) {
		t.Parallel()
		e, p := ssoEnv(t, nil)
		expectSSOError(t, e.ssoSignIn(p, "stranger@contoso.com", nil), "unknown_user")
		if n, _ := e.c.Store.CountUsers(context.Background()); n != 0 {
			t.Fatalf("%d users created", n)
		}
		if !contains(e.auditActions(), "stranger@contoso.com:login.failed") {
			t.Errorf("audit: %v", e.auditActions())
		}
	})

	t.Run("disabled user", func(t *testing.T) {
		t.Parallel()
		e, p := ssoEnv(t, func(s *model.SSOSettings) { s.AutoCreate, s.DefaultRole = true, model.RoleViewer })
		u := e.user("bob", model.RoleAdmin, false)
		u.Disabled = true
		e.c.Store.PutUser(context.Background(), u)
		expectSSOError(t, e.ssoSignIn(p, "bob", nil), "disabled")
	})

	t.Run("auto-create with the default role", func(t *testing.T) {
		t.Parallel()
		e, p := ssoEnv(t, func(s *model.SSOSettings) { s.AutoCreate, s.DefaultRole = true, model.RoleViewer })
		// The username claim is configurable, with fallbacks.
		e.setSSO(func(s *model.SSOSettings) { s.UsernameClaim = "email" })
		expectSignedIn(t, e, e.ssoSignIn(p, "carol@contoso.com", nil), "/")
		u, err := e.c.Store.GetUserByName(context.Background(), "carol@contoso.com")
		if err != nil {
			t.Fatal(err)
		}
		if !u.SSO || u.Role != model.RoleViewer || u.PasswordHash != "" || u.MustChange {
			t.Fatalf("created %+v", u)
		}
		if !contains(e.auditActions(), "carol@contoso.com:user.create") {
			t.Errorf("audit: %v", e.auditActions())
		}
		// No password: password sign-in is impossible for them.
		rec := e.do(http.MethodPost, "/api/auth/login", map[string]string{"username": "carol@contoso.com", "password": ""})
		expect(t, rec, http.StatusUnauthorized)

		// The email claim wins over preferred_username when configured.
		expectSignedIn(t, e, e.ssoSignIn(p, "ignored", map[string]any{"email": "dave@contoso.com"}), "/")
		if _, err := e.c.Store.GetUserByName(context.Background(), "dave@contoso.com"); err != nil {
			t.Fatal("email claim not used:", err)
		}
		// An unverified email address is not a fallback for a missing name.
		e.setSSO(func(s *model.SSOSettings) { s.UsernameClaim = "upn" })
		expectSSOError(t, e.ssoSignIn(p, "", map[string]any{"preferred_username": nil, "email": "eve@contoso.com", "email_verified": false}), "claims")

		// An administrator setting a password makes them ordinary users.
		admin := e.adminSession()
		rec = e.do(http.MethodPut, "/api/users/"+u.ID, map[string]string{"password": "a brand new password"}, admin...)
		expect(t, rec, http.StatusOK)
		if decodeJSON[model.User](t, rec).SSO {
			t.Error("still marked SSO after setting a password")
		}
	})

	t.Run("role mapping", func(t *testing.T) {
		t.Parallel()
		e, p := ssoEnv(t, func(s *model.SSOSettings) {
			s.AutoCreate, s.RoleClaim = true, "groups"
			s.RoleMap = []model.SSORoleRule{{Value: "g-admins", Role: model.RoleAdmin}, {Value: "g-ops", Role: model.RoleOperator}}
		})
		e.user("root", model.RoleAdmin, false) // so that erin can lose admin
		role := func(name string) model.Role {
			u, err := e.c.Store.GetUserByName(context.Background(), name)
			if err != nil {
				t.Fatal(err)
			}
			return u.Role
		}
		expectSignedIn(t, e, e.ssoSignIn(p, "erin", map[string]any{"groups": []string{"other", "g-ops"}}), "/")
		if r := role("erin"); r != model.RoleOperator {
			t.Fatalf("role = %s", r)
		}
		// Re-evaluated at every sign-in; the first matching rule wins.
		expectSignedIn(t, e, e.ssoSignIn(p, "erin", map[string]any{"groups": []string{"g-ops", "G-ADMINS"}}), "/")
		if r := role("erin"); r != model.RoleAdmin {
			t.Fatalf("role = %s", r)
		}
		// Matching no rule, without a default role: refused.
		expectSSOError(t, e.ssoSignIn(p, "erin", map[string]any{"groups": []string{"other"}}), "no_role")
		expectSSOError(t, e.ssoSignIn(p, "erin", nil), "no_role")
		expectSSOError(t, e.ssoSignIn(p, "frank", map[string]any{"groups": []string{"other"}}), "no_role")
		if _, err := e.c.Store.GetUserByName(context.Background(), "frank"); err == nil {
			t.Fatal("a refused user was created")
		}
		// With a default role, they get it.
		e.setSSO(func(s *model.SSOSettings) { s.DefaultRole = model.RoleViewer })
		expectSignedIn(t, e, e.ssoSignIn(p, "erin", map[string]any{"groups": []string{"other"}}), "/")
		if r := role("erin"); r != model.RoleViewer {
			t.Fatalf("role = %s", r)
		}
		// Site grants stay manual: a site-scoped user no rule matches
		// keeps them instead of getting the default role.
		site := e.createSite(session(e.login("root")), redirectSite("shop", 0))
		g := e.user("gina", model.RoleSites, false)
		g.Sites = []model.SiteGrant{{SiteID: site.ID, Role: model.RoleOperator}}
		e.c.Store.PutUser(context.Background(), g)
		expectSignedIn(t, e, e.ssoSignIn(p, "gina", nil), "/")
		if u, _ := e.c.Store.GetUserByName(context.Background(), "gina"); u.Role != model.RoleSites || len(u.Sites) != 1 {
			t.Fatalf("site-scoped user changed: %+v", u.User)
		}
		// Entra ID group overage is explained rather than refused as "no role".
		expectSSOError(t, e.ssoSignIn(p, "erin", map[string]any{"_claim_names": map[string]string{"groups": "src1"}}), "claims")
	})

	t.Run("mapping keeps the last administrator", func(t *testing.T) {
		t.Parallel()
		e, p := ssoEnv(t, func(s *model.SSOSettings) {
			s.RoleClaim, s.DefaultRole = "roles", model.RoleViewer
			s.RoleMap = []model.SSORoleRule{{Value: "NodeHoster.Admin", Role: model.RoleAdmin}}
		})
		e.user("root", model.RoleAdmin, false)
		expectSignedIn(t, e, e.ssoSignIn(p, "root", map[string]any{"roles": []string{}}), "/")
		if u, _ := e.c.Store.GetUserByName(context.Background(), "root"); u.Role != model.RoleAdmin {
			t.Fatalf("the last administrator was demoted to %s", u.Role)
		}
	})
}

func TestSSOPasswordSignInDisabled(t *testing.T) {
	t.Parallel()
	e, p := ssoEnv(t, func(s *model.SSOSettings) { s.DisablePassword = true })
	e.user("alice", model.RoleAdmin, false)

	m := decodeJSON[map[string]any](t, e.do(http.MethodGet, "/api/auth/methods", nil))
	if m["password"] != false || m["sso"] != true || m["ssoLabel"] != "Sign in with SSO" {
		t.Fatalf("methods = %v", m)
	}
	rec := e.do(http.MethodPost, "/api/auth/login", map[string]string{"username": "alice", "password": testPassword})
	expect(t, rec, http.StatusForbidden)
	expectSignedIn(t, e, e.ssoSignIn(p, "alice", nil), "/")

	// Turning SSO off brings password sign-in back.
	e.setSSO(func(s *model.SSOSettings) { s.Enabled = false })
	e.login("alice")
	expectSSOError(t, e.do(http.MethodGet, "/api/auth/oidc/start", nil), "off")
}

func TestSSOOpenRedirect(t *testing.T) {
	t.Parallel()
	e, p := ssoEnv(t, nil)
	e.user("alice", model.RoleViewer, false)
	for _, next := range []string{"//evil.example", "https://evil.example/x", "/\\evil.example", "/\t/evil.example", "evil", "/api/auth/logout", "/login"} {
		loc, ck := e.ssoStart(next)
		code := p.authorize(loc, p.sign(p.claims(loc, "alice", nil)))
		expectSignedIn(t, e, e.ssoCallback(loc.Query().Get("state"), code, ck), "/")
	}
}

func TestSSOThrottling(t *testing.T) {
	t.Parallel()
	e, _ := ssoEnv(t, nil)
	bad := func() *httptest.ResponseRecorder {
		return e.do(http.MethodGet, "/api/auth/oidc/callback?state=made-up&code=x", nil)
	}
	for range 5 {
		expectSSOError(t, bad(), "state")
	}
	expectSSOError(t, bad(), "locked")
	rec := e.do(http.MethodGet, "/api/auth/oidc/start", nil)
	if rec.Header().Get("Location") != "/login?sso_error=locked" {
		t.Fatalf("start while locked: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSSOSettings(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	p := newFakeIdP(t)
	admin := e.adminSession()

	s := decodeJSON[map[string]any](t, e.do(http.MethodGet, "/api/settings", nil, admin...))
	sso := s["sso"].(map[string]any)
	if sso["usernameClaim"] != "preferred_username" || sso["enabled"] != false {
		t.Fatalf("default sso = %v", sso)
	}
	sso["enabled"], sso["issuer"], sso["clientId"] = true, p.issuer, p.clientID
	rec := e.do(http.MethodPut, "/api/settings", s, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if decodeJSON[map[string]string](t, rec)["field"] != "sso.clientSecret" {
		t.Fatalf("validation: %s", rec.Body)
	}
	sso["clientSecret"] = p.secret
	sso["scopes"] = []string{"offline_access User.Read", "openid"}
	rec = e.do(http.MethodPut, "/api/settings", s, admin...)
	expect(t, rec, http.StatusOK)
	got := decodeJSON[model.Settings](t, rec).SSO
	if got.ClientSecret != secrets.Mask || strings.Join(got.Scopes, ",") != "offline_access,User.Read" {
		t.Fatalf("saved sso = %+v", got)
	}
	if stored := e.c.Settings().SSO.ClientSecret; stored == p.secret || e.c.Box.MustUnseal(stored) != p.secret {
		t.Fatal("client secret not sealed")
	}
	// The mask keeps the secret.
	rec = e.do(http.MethodPut, "/api/settings", decodeJSON[map[string]any](t, rec), admin...)
	expect(t, rec, http.StatusOK)
	if e.c.Box.MustUnseal(e.c.Settings().SSO.ClientSecret) != p.secret {
		t.Fatal("masked secret not kept")
	}

	// Only administrators configure it.
	e.user("op", model.RoleOperator, false)
	expect(t, e.do(http.MethodPost, "/api/settings/sso/test", map[string]any{}, session(e.login("op"))...), http.StatusForbidden)

	rec = e.do(http.MethodGet, "/api/settings/sso/callback-url", nil, admin...)
	expect(t, rec, http.StatusOK)
	if u := decodeJSON[map[string]string](t, rec)["redirectUrl"]; u != "http://example.com/api/auth/oidc/callback" {
		t.Errorf("redirectUrl = %q", u)
	}

	type testResult struct {
		OK       bool     `json:"ok"`
		Keys     int      `json:"keys"`
		Problems []string `json:"problems"`
	}
	rec = e.do(http.MethodPost, "/api/settings/sso/test", map[string]any{"issuer": p.issuer, "clientId": "x", "clientSecret": secrets.Mask}, admin...)
	expect(t, rec, http.StatusOK)
	if r := decodeJSON[testResult](t, rec); !r.OK || r.Keys != 1 {
		t.Fatalf("test = %+v", r)
	}
	rec = e.do(http.MethodPost, "/api/settings/sso/test", map[string]any{"issuer": p.srv.URL + "/nothing-here", "clientId": "x", "clientSecret": "y"}, admin...)
	if r := decodeJSON[testResult](t, rec); r.OK || len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "discovery") {
		t.Fatalf("test = %+v", r)
	}
	rec = e.do(http.MethodPost, "/api/settings/sso/test", map[string]any{"issuer": "http://login.example.org/x"}, admin...)
	if r := decodeJSON[testResult](t, rec); r.OK || len(r.Problems) != 3 {
		t.Fatalf("test = %+v", r)
	}
}

func TestCreateSSOUser(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	rec := e.do(http.MethodPost, "/api/users", map[string]any{"username": "hank@contoso.com", "role": "viewer", "sso": true}, admin...)
	expect(t, rec, http.StatusCreated)
	u := decodeJSON[model.User](t, rec)
	if !u.SSO {
		t.Fatalf("created %+v", u)
	}
	stored, _ := e.c.Store.GetUser(context.Background(), u.ID)
	if stored.PasswordHash != "" || stored.MustChange {
		t.Fatalf("stored %+v", stored)
	}
}
