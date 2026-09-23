package oidc

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Flow is a sign-in in progress: what the callback needs to finish it and
// to check that it is the answer to a request this server made, from this
// browser.
//
// Flows live in memory on the server, not in a cookie. The PKCE verifier
// and the nonce never leave the server, a state is accepted once (Take
// removes it, so a replayed callback fails), and no key has to be managed
// for an encrypted cookie. The cost is that a service restart drops the
// sign-ins in progress, which only means clicking the button again. The
// browser gets a random binding value in a cookie; its hash is kept with
// the flow, so a callback URL started in one browser cannot be completed
// in another (login CSRF: signing a victim in to the attacker's account).
type Flow struct {
	State       string
	Nonce       string
	Verifier    string // PKCE code verifier
	RedirectURL string // exactly as sent, for the code exchange
	Next        string // where the console goes after signing in
	Issuer      string // the configuration the flow was started with
	ClientID    string
	binding     [32]byte
	expires     time.Time
}

// FlowTTL is how long a user has to sign in at the provider.
const FlowTTL = 10 * time.Minute

// maxFlows bounds memory: starting a sign-in needs no authentication.
const maxFlows = 5000

var (
	ErrState   = errors.New("this sign-in is unknown, expired or was already used")
	ErrBinding = errors.New("this sign-in was started in another browser")
	ErrTooMany = errors.New("too many sign-ins in progress; try again in a few minutes")
)

// Flows holds the sign-ins in progress.
type Flows struct {
	mu  sync.Mutex
	m   map[string]*Flow
	now func() time.Time
}

func NewFlows() *Flows { return &Flows{m: map[string]*Flow{}, now: time.Now} }

func random(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Begin starts a flow. It returns the flow and the binding value to set
// in the browser's cookie.
func (f *Flows) Begin(issuer, clientID, redirectURL, next string) (*Flow, string, error) {
	binding := random(32)
	fl := &Flow{
		State: random(24), Nonce: random(24), Verifier: oauth2.GenerateVerifier(),
		RedirectURL: redirectURL, Next: next, Issuer: issuer, ClientID: clientID,
		binding: sha256.Sum256([]byte(binding)), expires: f.now().Add(FlowTTL),
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruneLocked()
	if len(f.m) >= maxFlows {
		return nil, "", ErrTooMany
	}
	f.m[fl.State] = fl
	return fl, binding, nil
}

func (f *Flows) pruneLocked() {
	now := f.now()
	for k, fl := range f.m {
		if now.After(fl.expires) {
			delete(f.m, k)
		}
	}
}

// Take ends the flow of state, if binding is the value its browser got.
// A wrong binding leaves the flow in place for the right browser.
func (f *Flows) Take(state, binding string) (*Flow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.m[state]
	if !ok || f.now().After(fl.expires) {
		delete(f.m, state)
		return nil, ErrState
	}
	h := sha256.Sum256([]byte(binding))
	if binding == "" || subtle.ConstantTimeCompare(h[:], fl.binding[:]) != 1 {
		return nil, ErrBinding
	}
	delete(f.m, state)
	return fl, nil
}

// CookieName is the binding cookie of a flow. It is per flow so that
// sign-ins started in two tabs do not overwrite each other's cookie.
func CookieName(state string) string {
	if len(state) > 12 {
		state = state[:12]
	}
	return "nh_sso_" + state
}
