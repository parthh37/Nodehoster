package secretstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// Bitwarden cloud endpoints (bitwarden-core ClientSettings defaults, and
// their EU counterparts).
const (
	bitwardenUSAPI      = "https://api.bitwarden.com"
	bitwardenUSIdentity = "https://identity.bitwarden.com"
	bitwardenEUAPI      = "https://api.bitwarden.eu"
	bitwardenEUIdentity = "https://identity.bitwarden.eu"
)

// bwDeviceTypeSDK is the Device-Type header the official SDK sends
// (DeviceType::SDK).
const bwDeviceTypeSDK = "21"

// bitwarden reads Bitwarden Secrets Manager as a machine account, the way
// the official SDK (and its bws command) does: the access token signs in
// to the identity server with OAuth client credentials (scope
// api.secrets), the answer's encrypted payload is decrypted with the
// token's own key into the organization's key, and secrets fetched from
// GET /secrets/{id} are decrypted with it. Signing in again is how a
// machine account renews its session, as in the SDK.
type bitwarden struct {
	api, identity string
	tok           bwAccessToken
	hc            *http.Client
	now           func() time.Time

	mu      sync.Mutex
	access  string // bearer token for the API
	expires time.Time
	orgKey  bwKey
	orgID   string // from the token's "organization" claim
	exp     expiry
}

func newBitwarden(s model.SecretStore, hc *http.Client, now func() time.Time) (*bitwarden, error) {
	b := s.Bitwarden
	tok, err := parseAccessToken(b.AccessToken)
	if err != nil {
		return nil, err
	}
	c := &bitwarden{tok: tok, hc: hc, now: now}
	switch {
	case s.URL != "":
		c.api, c.identity = s.URL+"/api", s.URL+"/identity"
	case b.Region == model.BitwardenEU:
		c.api, c.identity = bitwardenEUAPI, bitwardenEUIdentity
	default:
		c.api, c.identity = bitwardenUSAPI, bitwardenUSIdentity
	}
	if b.APIURL != "" {
		c.api = b.APIURL
	}
	if b.IdentityURL != "" {
		c.identity = b.IdentityURL
	}
	return c, nil
}

// ValidateAccessToken checks the format of a machine account access token
// (0.<id>.<secret>:<key>) without using it.
func ValidateAccessToken(tok string) error {
	_, err := parseAccessToken(tok)
	return err
}

func (c *bitwarden) header() http.Header {
	h := http.Header{}
	h.Set("Device-Type", bwDeviceTypeSDK)
	if c.access != "" {
		h.Set("Authorization", "Bearer "+c.access)
	}
	return h
}

// login signs in and decrypts the organization key. Called with mu held.
func (c *bitwarden) login(ctx context.Context) error {
	form := url.Values{}
	form.Set("scope", "api.secrets")
	form.Set("client_id", c.tok.id)
	form.Set("client_secret", c.tok.secret)
	form.Set("grant_type", "client_credentials")
	var out struct {
		AccessToken      string `json:"access_token"`
		ExpiresIn        int64  `json:"expires_in"`
		EncryptedPayload string `json:"encrypted_payload"`
	}
	c.access = ""
	h := http.Header{}
	h.Set("Device-Type", bwDeviceTypeSDK)
	if err := do(ctx, c.hc, http.MethodPost, c.identity+"/connect/token", h, formBody(form.Encode()), &out); err != nil {
		if s := statusOf(err); s == http.StatusBadRequest || s == http.StatusUnauthorized {
			return fmt.Errorf("the access token was refused (revoked, expired, or for another server): %w", err)
		}
		return fmt.Errorf("signing in to %s: %w", c.identity, err)
	}
	if out.AccessToken == "" || out.EncryptedPayload == "" {
		return errors.New("the identity server's answer has no token or no encrypted payload (is this a Secrets Manager machine account token?)")
	}
	payload, err := decryptEncString(out.EncryptedPayload, c.tok.key)
	if err != nil {
		return fmt.Errorf("decrypting the organization key: %w", err)
	}
	var p struct {
		EncryptionKey string `json:"encryptionKey"`
	}
	if err := json.Unmarshal(payload, &p); err != nil || p.EncryptionKey == "" {
		return errors.New("the decrypted payload has no organization key")
	}
	raw, err := base64.StdEncoding.DecodeString(p.EncryptionKey)
	if err != nil {
		return errors.New("the organization key is not valid base64")
	}
	key, err := bwKeyFromBytes(raw)
	if err != nil {
		return err
	}
	c.access, c.orgKey, c.orgID = out.AccessToken, key, jwtOrganization(out.AccessToken)
	c.expires = time.Time{}
	if out.ExpiresIn > 0 {
		life := time.Duration(out.ExpiresIn) * time.Second
		c.expires = c.now().Add(life - min(life/10, time.Minute))
	}
	return nil
}

// jwtOrganization reads the "organization" claim of the API token, which
// a machine account's token always has. The token is not verified: it is
// only compared with the organization of the secrets read with it.
func jwtOrganization(tok string) string {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Organization string `json:"organization"`
	}
	json.Unmarshal(raw, &claims)
	return strings.ToLower(claims.Organization)
}

func (c *bitwarden) ensure(ctx context.Context) error {
	if c.access == "" || (!c.expires.IsZero() && !c.now().Before(c.expires)) {
		return c.login(ctx)
	}
	return nil
}

func (c *bitwarden) fetch(ctx context.Context, refs []string) []result {
	res := make([]result, len(refs))
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.publish()
	for i, r := range refs {
		res[i].value, res[i].err = c.get(ctx, strings.ToLower(r))
	}
	return res
}

// get reads and decrypts one secret. A 401 signs in again once.
func (c *bitwarden) get(ctx context.Context, id string) (string, error) {
	for attempt := 0; ; attempt++ {
		if err := c.ensure(ctx); err != nil {
			return "", err
		}
		var out struct {
			ID             string `json:"id"`
			OrganizationID string `json:"organizationId"`
			Value          string `json:"value"`
		}
		err := do(ctx, c.hc, http.MethodGet, c.api+"/secrets/"+url.PathEscape(id), c.header(), nil, &out)
		switch statusOf(err) {
		case http.StatusUnauthorized:
			if attempt == 0 {
				c.access = ""
				continue
			}
		case http.StatusNotFound:
			return "", notFound("secret %s was not found, or the machine account has no access to it", id)
		}
		if err != nil {
			return "", fmt.Errorf("reading secret %s: %w", id, err)
		}
		if c.orgID != "" && out.OrganizationID != "" && !strings.EqualFold(out.OrganizationID, c.orgID) {
			return "", fmt.Errorf("secret %s belongs to another organization", id)
		}
		plain, err := decryptEncString(out.Value, c.orgKey)
		if err != nil {
			return "", fmt.Errorf("decrypting secret %s: %w", id, err)
		}
		return string(plain), nil
	}
}

func (c *bitwarden) maintain(context.Context) error { return nil } // signs in when needed

func (c *bitwarden) test(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.publish()
	if err := c.login(ctx); err != nil {
		return "", err
	}
	return "signed in as the machine account and decrypted the organization key", nil
}

func (c *bitwarden) tokenExpires() time.Time { return c.exp.get() }

// publish makes the session's end visible to tokenExpires. Called with mu
// held.
func (c *bitwarden) publish() {
	if c.access == "" {
		c.exp.set(time.Time{})
		return
	}
	c.exp.set(c.expires)
}
