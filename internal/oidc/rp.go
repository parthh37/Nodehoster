package oidc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Config is the console's registration at the provider.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string // besides openid, profile and email
}

// RP is the console as a relying party: the sign-ins in progress and the
// provider's discovered configuration, cached for an hour.
type RP struct {
	HTTP  *http.Client
	Flows *Flows

	mu         sync.Mutex
	prov       *Provider
	provIssuer string
	provAt     time.Time
}

const discoveryTTL = time.Hour

func NewRP() *RP {
	return &RP{HTTP: &http.Client{Timeout: 15 * time.Second}, Flows: NewFlows()}
}

// Provider returns the provider of issuer, discovering it when the issuer
// changed or the cached document is an hour old.
func (rp *RP) Provider(ctx context.Context, issuer string) (*Provider, error) {
	rp.mu.Lock()
	if rp.prov != nil && rp.provIssuer == issuer && time.Since(rp.provAt) < discoveryTTL {
		p := rp.prov
		rp.mu.Unlock()
		return p, nil
	}
	rp.mu.Unlock()
	p, err := Discover(ctx, rp.HTTP, issuer)
	if err != nil {
		return nil, err
	}
	rp.mu.Lock()
	rp.prov, rp.provIssuer, rp.provAt = p, issuer, time.Now()
	rp.mu.Unlock()
	return p, nil
}

func (c Config) oauth(p *Provider, redirectURL string) *oauth2.Config {
	scopes := append([]string{"openid", "profile", "email"}, c.Scopes...)
	return &oauth2.Config{
		ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: redirectURL, Scopes: dedupe(scopes),
		Endpoint: oauth2.Endpoint{AuthURL: p.AuthorizationEndpoint, TokenURL: p.TokenEndpoint},
	}
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	out := list[:0:0]
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// AuthCodeURL is where the browser goes to sign in: the authorization
// endpoint with the flow's state, nonce and S256 PKCE challenge.
func (c Config) AuthCodeURL(p *Provider, fl *Flow) string {
	return c.oauth(p, fl.RedirectURL).AuthCodeURL(fl.State,
		oauth2.S256ChallengeOption(fl.Verifier),
		oauth2.SetAuthURLParam("nonce", fl.Nonce))
}

// Exchange redeems the authorization code (with the flow's PKCE verifier)
// and verifies the ID token that comes back.
func (rp *RP) Exchange(ctx context.Context, p *Provider, c Config, fl *Flow, code string) (Claims, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, rp.HTTP)
	tok, err := c.oauth(p, fl.RedirectURL).Exchange(ctx, code, oauth2.VerifierOption(fl.Verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode != "" {
			return nil, fmt.Errorf("the provider refused the authorization code: %s %s", re.ErrorCode, re.ErrorDescription)
		}
		return nil, fmt.Errorf("the provider's token endpoint failed: %v", err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, errors.New("the provider returned no ID token (is the openid scope allowed?)")
	}
	return p.Verify(ctx, raw, c.ClientID, fl.Nonce, time.Now())
}
