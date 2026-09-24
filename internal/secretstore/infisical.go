package secretstore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// DefaultInfisicalURL is Infisical Cloud (US); the EU cloud is
// https://eu.infisical.com.
const DefaultInfisicalURL = "https://app.infisical.com"

// infisical reads secrets of one project environment with a machine
// identity's Universal Auth credentials: POST
// /api/v1/auth/universal-auth/login for an access token (kept until shortly
// before it expires, then obtained again), then GET /api/v4/secrets/{name}.
// Servers from before the v4 API answer that route with 404 "Route … not
// found": the store then uses /api/v3/secrets/raw/{name}, which they have.
type infisical struct {
	base string
	cfg  model.InfisicalStore
	hc   *http.Client
	now  func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
	v3      bool // the server has no v4 secrets API
	exp     expiry
}

func newInfisical(s model.SecretStore, hc *http.Client, now func() time.Time) *infisical {
	base := s.URL
	if base == "" {
		base = DefaultInfisicalURL
	}
	return &infisical{base: base, cfg: *s.Infisical, hc: hc, now: now}
}

// login signs in. Called with mu held.
func (c *infisical) login(ctx context.Context) error {
	var out struct {
		AccessToken string `json:"accessToken"`
		ExpiresIn   int64  `json:"expiresIn"`
	}
	body := map[string]string{"clientId": c.cfg.ClientID, "clientSecret": c.cfg.ClientSecret}
	if err := do(ctx, c.hc, http.MethodPost, c.base+"/api/v1/auth/universal-auth/login", nil, body, &out); err != nil {
		return fmt.Errorf("Universal Auth sign-in failed: %w", err)
	}
	if out.AccessToken == "" {
		return fmt.Errorf("Universal Auth sign-in returned no token")
	}
	c.token = out.AccessToken
	c.expires = time.Time{}
	if out.ExpiresIn > 0 {
		life := time.Duration(out.ExpiresIn) * time.Second
		// Sign in again before the end: a tenth of the lifetime, at
		// most a minute, early.
		c.expires = c.now().Add(life - min(life/10, time.Minute))
	}
	return nil
}

func (c *infisical) tokenFor(ctx context.Context) (string, error) {
	if c.token == "" || (!c.expires.IsZero() && !c.now().Before(c.expires)) {
		if err := c.login(ctx); err != nil {
			return "", err
		}
	}
	return c.token, nil
}

func (c *infisical) fetch(ctx context.Context, refs []string) []result {
	res := make([]result, len(refs))
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.publish()
	for i, r := range refs {
		res[i].value, res[i].err = c.get(ctx, r)
	}
	return res
}

// splitInfisicalRef splits "/folder/NAME" into ("/folder", "NAME"); a bare
// name is in the root folder.
func splitInfisicalRef(ref string) (dir, name string) {
	i := strings.LastIndex(ref, "/")
	if i < 0 {
		return "/", ref
	}
	dir = ref[:i]
	if dir == "" {
		dir = "/"
	}
	return dir, ref[i+1:]
}

// get reads one secret. A 401 signs in again once.
func (c *infisical) get(ctx context.Context, ref string) (string, error) {
	dir, name := splitInfisicalRef(ref)
	for attempt := 0; ; attempt++ {
		token, err := c.tokenFor(ctx)
		if err != nil {
			return "", err
		}
		v, err := c.getWith(ctx, token, dir, name)
		if statusOf(err) == http.StatusUnauthorized && attempt == 0 {
			c.token = ""
			continue
		}
		return v, err
	}
}

func (c *infisical) getWith(ctx context.Context, token, dir, name string) (string, error) {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	q := url.Values{}
	q.Set("environment", c.cfg.Environment)
	q.Set("secretPath", dir)
	q.Set("expandSecretReferences", "true")
	var u string
	if !c.v3 {
		q.Set("projectId", c.cfg.ProjectID)
		q.Set("viewSecretValue", "true")
		q.Set("includeImports", "true")
		u = c.base + "/api/v4/secrets/" + url.PathEscape(name) + "?" + q.Encode()
	} else {
		q.Set("workspaceId", c.cfg.ProjectID)
		q.Set("include_imports", "true")
		u = c.base + "/api/v3/secrets/raw/" + url.PathEscape(name) + "?" + q.Encode()
	}
	var out struct {
		Secret *struct {
			SecretKey         string `json:"secretKey"`
			SecretValue       string `json:"secretValue"`
			SecretValueHidden bool   `json:"secretValueHidden"`
		} `json:"secret"`
	}
	err := do(ctx, c.hc, http.MethodGet, u, h, nil, &out)
	if statusOf(err) == http.StatusNotFound {
		if !c.v3 && isRouteNotFound(err) {
			c.v3 = true
			return c.getWith(ctx, token, dir, name)
		}
		return "", notFound("secret %s was not found in %s (environment %s)", name, dir, c.cfg.Environment)
	}
	if err != nil {
		if s := statusOf(err); s == http.StatusForbidden {
			return "", fmt.Errorf("reading %s was refused (the machine identity has no access to this project, environment or folder): %w", name, err)
		}
		return "", fmt.Errorf("reading %s: %w", name, err)
	}
	if out.Secret == nil {
		return "", notFound("secret %s was not found in %s", name, dir)
	}
	if out.Secret.SecretValueHidden {
		return "", fmt.Errorf("the machine identity may not read the value of %s (its role hides secret values)", name)
	}
	return out.Secret.SecretValue, nil
}

func (c *infisical) maintain(context.Context) error { return nil } // a token is obtained when needed

func (c *infisical) test(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.publish()
	c.token = ""
	if err := c.login(ctx); err != nil {
		return "", err
	}
	// Listing the root folder without values shows that the identity can
	// reach the project and environment.
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.token)
	q := url.Values{}
	q.Set("projectId", c.cfg.ProjectID)
	q.Set("environment", c.cfg.Environment)
	q.Set("secretPath", "/")
	q.Set("viewSecretValue", "false")
	var out struct {
		Secrets []struct{} `json:"secrets"`
	}
	err := do(ctx, c.hc, http.MethodGet, c.base+"/api/v4/secrets?"+q.Encode(), h, nil, &out)
	switch {
	case isRouteNotFound(err):
		return "signed in with Universal Auth (this server is too old to check the project without reading values)", nil
	case err != nil:
		return "", fmt.Errorf("signed in, but the project's %s environment cannot be read: %w", c.cfg.Environment, err)
	}
	return fmt.Sprintf("signed in with Universal Auth; the %s environment has %d secrets in its root folder", c.cfg.Environment, len(out.Secrets)), nil
}

// isRouteNotFound recognizes the answer of a server without an API route
// (Fastify's 404 "Route GET:/api/v4/... not found").
func isRouteNotFound(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.Status == http.StatusNotFound && strings.HasPrefix(he.Message, "Route ")
}

func (c *infisical) tokenExpires() time.Time { return c.exp.get() }

// publish makes the token's end visible to tokenExpires. Called with mu
// held.
func (c *infisical) publish() {
	if c.token == "" {
		c.exp.set(time.Time{})
		return
	}
	c.exp.set(c.expires)
}
