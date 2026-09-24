package secretstore

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// fakeClock is a settable clock for tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeVault serves the parts of Vault's API the store uses: AppRole
// login, token lookup and renewal, and KV v1 (mount "kv1") and v2 (mount
// "secret") reads.
type fakeVault struct {
	t         *testing.T
	mu        sync.Mutex
	tokens    map[string]bool // valid tokens
	kv        map[string]map[string]any
	namespace string
	ttl       int64 // lease of issued tokens
	renewTTL  int64 // lease granted by renew-self (the max TTL shortens it)
	logins    atomic.Int32
	renewals  atomic.Int32
	reads     atomic.Int32
}

func newFakeVault(t *testing.T) *fakeVault {
	return &fakeVault{t: t, tokens: map[string]bool{"root-token": true}, ttl: 3600, renewTTL: 3600,
		kv: map[string]map[string]any{"app/prod": {"DB_PASSWORD": "hunter2", "PORT": json.Number("5432")}}}
}

func (f *fakeVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("X-Vault-Namespace") != f.namespace {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"errors":["wrong namespace"]}`))
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/approle/login" {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["role_id"] != "role-1" || in["secret_id"] != "secret-1" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"errors":["invalid role or secret ID"]}`))
			return
		}
		n := f.logins.Add(1)
		tok := "approle-token-" + string(rune('a'+n))
		f.tokens[tok] = true
		json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": tok, "lease_duration": f.ttl, "renewable": true}})
		return
	}
	tok := r.Header.Get("X-Vault-Token")
	if !f.tokens[tok] {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"errors":["permission denied"]}`))
		return
	}
	switch {
	case r.URL.Path == "/v1/auth/token/lookup-self":
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ttl": f.ttl, "renewable": true}})
	case r.URL.Path == "/v1/auth/token/renew-self" && r.Method == http.MethodPost:
		f.renewals.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": tok, "lease_duration": f.renewTTL, "renewable": true}})
	case strings.HasPrefix(r.URL.Path, "/v1/secret/data/"):
		f.reads.Add(1)
		data, ok := f.kv[strings.TrimPrefix(r.URL.Path, "/v1/secret/data/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errors":[]}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": data, "metadata": map[string]any{"version": 3}}})
	case strings.HasPrefix(r.URL.Path, "/v1/kv1/"):
		f.reads.Add(1)
		data, ok := f.kv[strings.TrimPrefix(r.URL.Path, "/v1/kv1/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errors":[]}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	default:
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"errors":["no handler for route"]}`))
	}
}

func (f *fakeVault) revoke(tok string) {
	f.mu.Lock()
	delete(f.tokens, tok)
	f.mu.Unlock()
}

func (f *fakeVault) set(path, key string, v any) {
	f.mu.Lock()
	if f.kv[path] == nil {
		f.kv[path] = map[string]any{}
	}
	f.kv[path][key] = v
	f.mu.Unlock()
}

func vaultStore(url string, v model.VaultStore) model.SecretStore {
	s := model.SecretStore{Name: "vault", Type: model.SecretStoreVault, URL: url, Vault: &v}
	s.ApplyDefaults()
	return s
}

func fetchOne(t *testing.T, p provider, ref string) (string, error) {
	t.Helper()
	r := p.fetch(context.Background(), []string{ref})
	return r[0].value, r[0].err
}

func TestVaultTokenKV2AndKV1(t *testing.T) {
	f := newFakeVault(t)
	f.namespace = "team-a"
	srv := httptest.NewServer(f)
	defer srv.Close()
	v := newVault(vaultStore(srv.URL, model.VaultStore{Token: "root-token", Namespace: "team-a"}), srv.Client(), time.Now)

	res := v.fetch(context.Background(), []string{"app/prod#DB_PASSWORD", "app/prod#PORT", "app/prod#MISSING", "app/none#X"})
	if res[0].value != "hunter2" || res[0].err != nil {
		t.Errorf("DB_PASSWORD = %+v", res[0])
	}
	if res[1].value != "5432" || res[1].err != nil {
		t.Errorf("a number is its JSON text: %+v", res[1])
	}
	if !IsNotFound(res[2].err) || !IsNotFound(res[3].err) {
		t.Errorf("missing key or secret: %v / %v", res[2].err, res[3].err)
	}
	if n := f.reads.Load(); n != 2 {
		t.Errorf("%d reads for two paths", n)
	}

	kv1 := newVault(vaultStore(srv.URL, model.VaultStore{Token: "root-token", Namespace: "team-a", Mount: "kv1", KVVersion: 1}), srv.Client(), time.Now)
	if got, err := fetchOne(t, kv1, "app/prod#DB_PASSWORD"); got != "hunter2" || err != nil {
		t.Errorf("KV v1 = %q, %v", got, err)
	}

	bad := newVault(vaultStore(srv.URL, model.VaultStore{Token: "wrong", Namespace: "team-a"}), srv.Client(), time.Now)
	_, err := fetchOne(t, bad, "app/prod#DB_PASSWORD")
	if err == nil || IsNotFound(err) || !strings.Contains(err.Error(), "refused") {
		t.Errorf("a wrong token: %v", err)
	}
	if strings.Contains(err.Error(), "wrong") && strings.Contains(err.Error(), "X-Vault") {
		t.Errorf("the error shows the request: %v", err)
	}
}

func TestVaultAppRoleLoginRenewAndRelogin(t *testing.T) {
	f := newFakeVault(t)
	srv := httptest.NewServer(f)
	defer srv.Close()
	clock := newClock()
	v := newVault(vaultStore(srv.URL, model.VaultStore{Auth: model.VaultAuthAppRole, RoleID: "role-1", SecretID: "secret-1"}), srv.Client(), clock.Now)

	if got, err := fetchOne(t, v, "app/prod#DB_PASSWORD"); got != "hunter2" || err != nil {
		t.Fatalf("read = %q, %v", got, err)
	}
	fetchOne(t, v, "app/prod#DB_PASSWORD")
	if n := f.logins.Load(); n != 1 {
		t.Fatalf("%d logins for two reads", n)
	}
	if exp := v.tokenExpires(); !exp.Equal(clock.Now().Add(time.Hour)) {
		t.Errorf("token expires %v", exp)
	}

	// Before half the TTL nothing happens; after, the token is renewed.
	clock.Add(20 * time.Minute)
	v.maintain(context.Background())
	if f.renewals.Load() != 0 {
		t.Fatal("renewed before half its lifetime")
	}
	clock.Add(15 * time.Minute)
	if err := v.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.renewals.Load() != 1 || f.logins.Load() != 1 {
		t.Fatalf("renewals %d, logins %d", f.renewals.Load(), f.logins.Load())
	}

	// Near the max TTL renewal gives less: the store signs in again.
	f.mu.Lock()
	f.renewTTL = 600
	f.mu.Unlock()
	clock.Add(40 * time.Minute)
	if err := v.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.logins.Load() != 2 {
		t.Fatalf("logins = %d after a capped renewal", f.logins.Load())
	}

	// A revoked token: the read is refused, the store signs in again once.
	v.mu.Lock()
	tok := v.token
	v.mu.Unlock()
	f.revoke(tok)
	if got, err := fetchOne(t, v, "app/prod#DB_PASSWORD"); got != "hunter2" || err != nil {
		t.Fatalf("after revocation = %q, %v", got, err)
	}
	if f.logins.Load() != 3 {
		t.Fatalf("logins = %d after a revocation", f.logins.Load())
	}

	// An expired token is replaced before use.
	clock.Add(2 * time.Hour)
	fetchOne(t, v, "app/prod#DB_PASSWORD")
	if f.logins.Load() != 4 {
		t.Fatalf("logins = %d after expiry", f.logins.Load())
	}

	wrong := newVault(vaultStore(srv.URL, model.VaultStore{Auth: model.VaultAuthAppRole, RoleID: "role-1", SecretID: "nope"}), srv.Client(), clock.Now)
	if _, err := wrong.test(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid role or secret ID") {
		t.Errorf("a wrong secret ID: %v", err)
	}
	detail, err := v.test(context.Background())
	if err != nil || !strings.Contains(detail, "AppRole") {
		t.Errorf("test = %q, %v", detail, err)
	}
}

// A renewable token given directly (a periodic token) is renewed too.
func TestVaultDirectTokenRenewal(t *testing.T) {
	f := newFakeVault(t)
	srv := httptest.NewServer(f)
	defer srv.Close()
	clock := newClock()
	v := newVault(vaultStore(srv.URL, model.VaultStore{Token: "root-token"}), srv.Client(), clock.Now)
	if err := v.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock.Add(31 * time.Minute)
	if err := v.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.renewals.Load() != 1 || f.logins.Load() != 0 {
		t.Fatalf("renewals %d, logins %d", f.renewals.Load(), f.logins.Load())
	}
}

// A self-hosted server with a private CA: refused without the CA,
// trusted with it.
func TestVaultCustomCA(t *testing.T) {
	f := newFakeVault(t)
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))

	plain, err := newHTTPClient("")
	if err != nil {
		t.Fatal(err)
	}
	v := newVault(vaultStore(srv.URL, model.VaultStore{Token: "root-token"}), plain, time.Now)
	if _, err := fetchOne(t, v, "app/prod#DB_PASSWORD"); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("without the CA: %v", err)
	}
	withCA, err := newHTTPClient(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	v = newVault(vaultStore(srv.URL, model.VaultStore{Token: "root-token"}), withCA, time.Now)
	if got, err := fetchOne(t, v, "app/prod#DB_PASSWORD"); got != "hunter2" || err != nil {
		t.Errorf("with the CA: %q, %v", got, err)
	}
	if _, err := newHTTPClient("not a certificate"); err == nil {
		t.Error("an unreadable CA was accepted")
	}
}
