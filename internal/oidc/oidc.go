// Package oidc is the relying-party side of OpenID Connect that the web
// console's single sign-on needs: discovery, the provider's signing keys
// (JWKS, refetched when it rotates them), ID token verification and the
// short-lived state of sign-ins in progress. The OAuth 2.0 code exchange
// itself is golang.org/x/oauth2's; signatures are go-jose's.
//
// It is deliberately small: the authorization code flow with PKCE, a
// confidential client, ID tokens signed with RSA or ECDSA. No implicit
// flow, no userinfo endpoint, no encrypted tokens.
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Algorithms are the ID token signature algorithms accepted. "none" and the
// HMAC family (HS256...) are not among them: an HMAC token would be signed
// with the client secret, which is not proof that the provider issued it,
// and accepting HS* with a public key as the secret is the classic JWT
// algorithm-confusion forgery.
var Algorithms = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512,
	jose.PS256, jose.PS384, jose.PS512,
	jose.ES256, jose.ES384, jose.ES512,
}

// ClockSkew is how far the provider's clock may be from ours.
const ClockSkew = 2 * time.Minute

// Verification failures, for the audit log.
var (
	ErrMalformed    = errors.New("the ID token is malformed or uses an unsupported algorithm")
	ErrSignature    = errors.New("the ID token signature is not valid for any of the provider's keys")
	ErrIssuer       = errors.New("the ID token comes from another issuer")
	ErrAudience     = errors.New("the ID token was issued for another application")
	ErrExpired      = errors.New("the ID token has expired or is not valid yet")
	ErrNonce        = errors.New("the ID token's nonce does not match this sign-in")
	ErrNoKeys       = errors.New("the provider publishes no usable signing keys")
	errUnknownKeyID = errors.New("unknown key ID")
)

// Discovery is the part of the provider's configuration document
// (/.well-known/openid-configuration) that is used.
type Discovery struct {
	Issuer                 string   `json:"issuer"`
	AuthorizationEndpoint  string   `json:"authorization_endpoint"`
	TokenEndpoint          string   `json:"token_endpoint"`
	JWKSURI                string   `json:"jwks_uri"`
	ResponseTypes          []string `json:"response_types_supported"`
	SigningAlgs            []string `json:"id_token_signing_alg_values_supported"`
	CodeChallengeMethods   []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMeths []string `json:"token_endpoint_auth_methods_supported"`
}

// Provider is a discovered provider with a cache of its signing keys.
type Provider struct {
	Discovery
	client *http.Client

	mu        sync.Mutex
	keys      []jose.JSONWebKey
	refetched time.Time // last refetch caused by an unknown key ID
	// MinRefetch limits refetches for unknown key IDs, so that a stream
	// of tokens with made-up key IDs cannot make us hammer the provider.
	MinRefetch time.Duration
}

const maxBody = 1 << 20

func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s did not return JSON: %v", url, err)
	}
	return nil
}

// sameIssuer compares issuer URLs, ignoring one trailing slash: the
// configured one is typed by a person.
func sameIssuer(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

// Discover reads the provider's configuration document. The issuer it
// declares must be the one configured (OpenID Connect Discovery 1.0,
// section 4.3): a document served for another issuer is refused.
func Discover(ctx context.Context, client *http.Client, issuer string) (*Provider, error) {
	var d Discovery
	if err := getJSON(ctx, client, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", &d); err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	if !sameIssuer(d.Issuer, issuer) {
		return nil, fmt.Errorf("discovery: the provider calls itself %q, not %q", d.Issuer, issuer)
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.JWKSURI == "" {
		return nil, errors.New("discovery: the document lacks the authorization, token or JWKS endpoint")
	}
	return &Provider{Discovery: d, client: client, MinRefetch: 10 * time.Second}, nil
}

// fetchKeys reads the JWKS. Keys that go-jose cannot parse (an algorithm
// it does not know) and encryption keys are skipped rather than failing
// the whole set.
func (p *Provider) fetchKeys(ctx context.Context) ([]jose.JSONWebKey, error) {
	var raw struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := getJSON(ctx, p.client, p.JWKSURI, &raw); err != nil {
		return nil, fmt.Errorf("signing keys: %w", err)
	}
	var keys []jose.JSONWebKey
	for _, r := range raw.Keys {
		var k jose.JSONWebKey
		if k.UnmarshalJSON(r) != nil || !k.Valid() || !k.IsPublic() || k.Use == "enc" {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, ErrNoKeys
	}
	return keys, nil
}

// Keys returns the cached signing keys, fetching them the first time.
func (p *Provider) Keys(ctx context.Context) ([]jose.JSONWebKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.keys == nil {
		keys, err := p.fetchKeys(ctx)
		if err != nil {
			return nil, err
		}
		p.keys = keys
	}
	return p.keys, nil
}

// refetchKeys reloads the JWKS after a token named a key we do not have:
// the provider rotated its keys (Entra ID does every few weeks).
func (p *Provider) refetchKeys(ctx context.Context) ([]jose.JSONWebKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.refetched.IsZero() && time.Since(p.refetched) < p.MinRefetch {
		return p.keys, nil
	}
	p.refetched = time.Now()
	keys, err := p.fetchKeys(ctx)
	if err != nil {
		return nil, err
	}
	p.keys = keys
	return keys, nil
}

// Claims are an ID token's claims.
type Claims map[string]any

// String returns a string claim ("" if missing or not a string).
func (c Claims) String(name string) string {
	s, _ := c[name].(string)
	return s
}

// Strings returns a claim holding a string or a list of strings.
func (c Claims) Strings(name string) []string {
	switch v := c[name].(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Has reports whether the claim is present.
func (c Claims) Has(name string) bool {
	_, ok := c[name]
	return ok
}

// Verify checks an ID token (OpenID Connect Core 1.0, section 3.1.3.7):
// the signature with one of the provider's keys and an accepted
// algorithm, the issuer, the audience (and the authorized party when there
// are several audiences), the expiry and issue time within ClockSkew, and
// the nonce of this sign-in.
func (p *Provider) Verify(ctx context.Context, raw, clientID, nonce string, now time.Time) (Claims, error) {
	tok, err := jwt.ParseSigned(raw, Algorithms)
	if err != nil || len(tok.Headers) != 1 {
		return nil, ErrMalformed
	}
	keys, err := p.Keys(ctx)
	if err != nil {
		return nil, err
	}
	var std jwt.Claims
	var all Claims
	err = verifyWith(tok, keys, &std, &all)
	if errors.Is(err, errUnknownKeyID) {
		if keys, err = p.refetchKeys(ctx); err != nil {
			return nil, err
		}
		err = verifyWith(tok, keys, &std, &all)
	}
	if err != nil {
		return nil, ErrSignature
	}
	if std.Issuer != p.Issuer {
		return nil, ErrIssuer
	}
	if !std.Audience.Contains(clientID) {
		return nil, ErrAudience
	}
	if azp := all.String("azp"); (len(std.Audience) > 1 || azp != "") && azp != clientID {
		return nil, ErrAudience
	}
	if std.Expiry == nil || std.IssuedAt == nil {
		return nil, ErrExpired
	}
	if err := std.ValidateWithLeeway(jwt.Expected{Time: now}, ClockSkew); err != nil {
		return nil, ErrExpired
	}
	if got := all.String("nonce"); nonce == "" || got != nonce {
		return nil, ErrNonce
	}
	return all, nil
}

// verifyWith tries the key the token names by its key ID, or every key when
// it names none. errUnknownKeyID asks the caller to refetch the keys.
func verifyWith(tok *jwt.JSONWebToken, keys []jose.JSONWebKey, dest ...any) error {
	kid := tok.Headers[0].KeyID
	tried := false
	for _, k := range keys {
		if kid != "" && k.KeyID != kid {
			continue
		}
		if k.Algorithm != "" && k.Algorithm != tok.Headers[0].Algorithm {
			continue
		}
		tried = true
		if tok.Claims(k.Key, dest...) == nil {
			return nil
		}
	}
	if !tried && kid != "" && !slices.ContainsFunc(keys, func(k jose.JSONWebKey) bool { return k.KeyID == kid }) {
		return errUnknownKeyID
	}
	return ErrSignature
}
