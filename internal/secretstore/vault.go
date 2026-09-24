package secretstore

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// vault reads a KV engine of HashiCorp Vault or OpenBao (their HTTP APIs
// are the same). With AppRole it signs in itself and keeps its token
// alive: renewed once half its lifetime has passed, and replaced by a new
// sign-in when it cannot be renewed any more (its max TTL). A token given
// directly is renewed the same way if it is renewable (a periodic token,
// the usual kind for services), so it does not expire while NodeHoster
// runs.
type vault struct {
	addr string
	cfg  model.VaultStore
	hc   *http.Client
	now  func() time.Time

	mu        sync.Mutex
	token     string
	ttl       time.Duration // the token's lifetime when issued or last renewed; 0 = does not expire
	renewable bool
	issued    time.Time // when ttl started
	checked   bool      // a direct token's lifetime has been looked up
	exp       expiry
}

func newVault(s model.SecretStore, hc *http.Client, now func() time.Time) *vault {
	v := &vault{addr: s.URL, cfg: *s.Vault, hc: hc, now: now}
	if v.cfg.Auth == model.VaultAuthToken {
		v.token = v.cfg.Token
	}
	return v
}

func (v *vault) header(token string) http.Header {
	h := http.Header{}
	if token != "" {
		h.Set("X-Vault-Token", token)
	}
	if v.cfg.Namespace != "" {
		h.Set("X-Vault-Namespace", v.cfg.Namespace)
	}
	h.Set("X-Vault-Request", "true")
	return h
}

// vaultAuth is the "auth" section of a login or renewal answer.
type vaultAuth struct {
	Auth *struct {
		ClientToken   string `json:"client_token"`
		LeaseDuration int64  `json:"lease_duration"`
		Renewable     bool   `json:"renewable"`
	} `json:"auth"`
}

// login signs in with AppRole. Called with mu held.
func (v *vault) login(ctx context.Context) error {
	var out vaultAuth
	body := map[string]string{"role_id": strings.TrimSpace(v.cfg.RoleID), "secret_id": v.cfg.SecretID}
	u := v.addr + "/v1/auth/" + escapePath(v.cfg.AuthMount) + "/login"
	if err := do(ctx, v.hc, http.MethodPost, u, v.header(""), body, &out); err != nil {
		return fmt.Errorf("AppRole sign-in failed: %w", err)
	}
	if out.Auth == nil || out.Auth.ClientToken == "" {
		return fmt.Errorf("AppRole sign-in returned no token")
	}
	v.token, v.renewable, v.issued = out.Auth.ClientToken, out.Auth.Renewable, v.now()
	v.ttl = time.Duration(out.Auth.LeaseDuration) * time.Second
	return nil
}

// tokenFor returns a token to use, signing in when there is none or it has
// expired. Called with mu held.
func (v *vault) tokenFor(ctx context.Context) (string, error) {
	if v.cfg.Auth == model.VaultAuthAppRole && (v.token == "" || v.expired()) {
		if err := v.login(ctx); err != nil {
			return "", err
		}
	}
	return v.token, nil
}

func (v *vault) expired() bool {
	return v.ttl > 0 && !v.now().Before(v.issued.Add(v.ttl-5*time.Second))
}

func (v *vault) fetch(ctx context.Context, refs []string) []result {
	res := make([]result, len(refs))
	byPath := map[string][]int{}
	var order []string
	for i, r := range refs {
		path, _, _ := strings.Cut(r, "#")
		if _, ok := byPath[path]; !ok {
			order = append(order, path)
		}
		byPath[path] = append(byPath[path], i)
	}
	for _, path := range order {
		data, err := v.read(ctx, path)
		for _, i := range byPath[path] {
			if err != nil {
				res[i].err = err
				continue
			}
			_, key, _ := strings.Cut(refs[i], "#")
			val, ok := data[key]
			if !ok {
				res[i].err = notFound("secret %s has no key %q", path, key)
				continue
			}
			res[i].value = valueString(val)
		}
	}
	return res
}

// read reads one secret's key/value pairs. A 403 with AppRole signs in
// again once: the token may have been revoked.
func (v *vault) read(ctx context.Context, path string) (map[string]any, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	defer v.publish()
	for attempt := 0; ; attempt++ {
		token, err := v.tokenFor(ctx)
		if err != nil {
			return nil, err
		}
		data, err := v.readWith(ctx, token, path)
		if statusOf(err) == http.StatusForbidden && attempt == 0 && v.cfg.Auth == model.VaultAuthAppRole {
			v.token = ""
			continue
		}
		return data, err
	}
}

func (v *vault) readWith(ctx context.Context, token, path string) (map[string]any, error) {
	u := v.addr + "/v1/" + escapePath(v.cfg.Mount) + "/"
	if v.cfg.KVVersion == 2 {
		u += "data/"
	}
	u += escapePath(path)
	var out struct {
		Data map[string]any `json:"data"`
	}
	err := do(ctx, v.hc, http.MethodGet, u, v.header(token), nil, &out)
	if statusOf(err) == http.StatusNotFound {
		return nil, notFound("secret %s was not found in %s", path, v.cfg.Mount)
	}
	if err != nil {
		if statusOf(err) == http.StatusForbidden {
			return nil, fmt.Errorf("reading %s was refused (the token is invalid or expired, or its policy does not allow it): %w", path, err)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if v.cfg.KVVersion == 1 {
		if out.Data == nil {
			return nil, notFound("secret %s was not found in %s", path, v.cfg.Mount)
		}
		return out.Data, nil
	}
	inner, _ := out.Data["data"].(map[string]any)
	if inner == nil {
		// KV v2 answers a deleted (or destroyed) latest version with
		// data null; a KV v1 mount read as v2 has no "data" either.
		return nil, notFound("secret %s has no current version in %s (deleted, or the mount is not KV version 2)", path, v.cfg.Mount)
	}
	return inner, nil
}

// lookup reads the token's lifetime. Called with mu held.
func (v *vault) lookup(ctx context.Context, token string) error {
	var out struct {
		Data struct {
			TTL       int64 `json:"ttl"`
			Renewable bool  `json:"renewable"`
		} `json:"data"`
	}
	if err := do(ctx, v.hc, http.MethodGet, v.addr+"/v1/auth/token/lookup-self", v.header(token), nil, &out); err != nil {
		return err
	}
	v.ttl, v.renewable, v.issued = time.Duration(out.Data.TTL)*time.Second, out.Data.Renewable, v.now()
	v.checked = true
	return nil
}

// maintain renews the token once half of its lifetime has passed; an
// AppRole token that cannot be renewed (or whose renewal is capped by its
// max TTL) is replaced by signing in again.
func (v *vault) maintain(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	defer v.publish()
	if v.cfg.Auth == model.VaultAuthToken && !v.checked {
		if err := v.lookup(ctx, v.token); err != nil {
			// A policy without lookup-self is not asked again: the
			// token is used as it is (reads tell whether it works).
			// Other failures (the server is down) are retried.
			v.checked = statusOf(err) == http.StatusForbidden
			return fmt.Errorf("looking up the token's lifetime: %w", err)
		}
	}
	if v.token == "" || v.ttl <= 0 || v.now().Before(v.issued.Add(v.ttl/2)) {
		return nil
	}
	if v.renewable {
		var out vaultAuth
		err := do(ctx, v.hc, http.MethodPost, v.addr+"/v1/auth/token/renew-self", v.header(v.token), map[string]any{}, &out)
		if err == nil && out.Auth != nil {
			lease := time.Duration(out.Auth.LeaseDuration) * time.Second
			// Renewal hands back less than asked for near the max TTL:
			// an AppRole token is then replaced rather than left to run
			// out.
			if v.cfg.Auth == model.VaultAuthToken || lease >= v.ttl/2 {
				v.issued, v.ttl = v.now(), lease
				return nil
			}
		} else if v.cfg.Auth == model.VaultAuthToken {
			return fmt.Errorf("renewing the token: %w", err)
		}
	}
	if v.cfg.Auth == model.VaultAuthAppRole {
		return v.login(ctx)
	}
	return nil
}

func (v *vault) test(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	defer v.publish()
	token, err := v.tokenFor(ctx)
	if err != nil {
		return "", err
	}
	if err := v.lookup(ctx, token); err != nil {
		return "", fmt.Errorf("the token was refused: %w", err)
	}
	how := "the token is valid"
	if v.cfg.Auth == model.VaultAuthAppRole {
		how = "signed in with AppRole"
	}
	switch {
	case v.ttl <= 0:
		return how + " and does not expire", nil
	case v.renewable:
		return fmt.Sprintf("%s; the token lasts %s and is renewed", how, v.ttl.Round(time.Second)), nil
	default:
		return fmt.Sprintf("%s; the token lasts %s and cannot be renewed", how, v.ttl.Round(time.Second)), nil
	}
}

func (v *vault) tokenExpires() time.Time { return v.exp.get() }

// publish makes the token's end visible to tokenExpires. Called with mu
// held.
func (v *vault) publish() {
	if v.token == "" || v.ttl <= 0 {
		v.exp.set(time.Time{})
		return
	}
	v.exp.set(v.issued.Add(v.ttl))
}

// escapePath escapes each segment of a slash-separated path.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
