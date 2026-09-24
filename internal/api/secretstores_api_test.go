package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
)

// A Bitwarden access token in the right format (the SDK's fake server's).
const testBWToken = "0.ec2c1d46-6a4b-4751-a310-af9601317f2d.C2IgxjjLF7qSshsbwe8JGcbM075YXw:X8vbvA0bduihIDe/qrzIQQ=="

// miniVault is a Vault KV v2 server with one token.
type miniVault struct {
	mu   sync.Mutex
	data map[string]map[string]string
}

func newMiniVault(t *testing.T) (*miniVault, *httptest.Server) {
	v := &miniVault{data: map[string]map[string]string{"app/prod": {"DB_PASSWORD": "vault-value-1"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		defer v.mu.Unlock()
		if r.Header.Get("X-Vault-Token") != "vault-token" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"errors":["permission denied"]}`))
			return
		}
		switch {
		case r.URL.Path == "/v1/auth/token/lookup-self":
			w.Write([]byte(`{"data":{"ttl":0,"renewable":false}}`))
		case strings.HasPrefix(r.URL.Path, "/v1/secret/data/"):
			d, ok := v.data[strings.TrimPrefix(r.URL.Path, "/v1/secret/data/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"errors":[]}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": d}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return v, srv
}

// putStores replaces the secret stores through the settings API.
func (e *env) putStores(admin []opt, stores []any) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := e.do(http.MethodGet, "/api/settings", nil, admin...)
	expect(e.t, rec, http.StatusOK)
	s := decodeJSON[map[string]any](e.t, rec)
	s["secretStores"] = stores
	return e.do(http.MethodPut, "/api/settings", s, admin...)
}

func vaultStoreBody(url, token string) map[string]any {
	return map[string]any{"name": "vault", "type": "vault", "url": url, "vault": map[string]any{"auth": "token", "token": token}}
}

func TestSecretStoreSettingsAreSealedAndMasked(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	rec := e.putStores(admin, []any{
		vaultStoreBody("https://vault.example.com:8200", "vault-token-plain"),
		map[string]any{"name": "inf", "type": "infisical", "infisical": map[string]any{"clientId": "cid", "clientSecret": "infisical-secret-plain", "projectId": "p1", "environment": "prod"}},
		map[string]any{"name": "bw", "type": "bitwarden", "bitwarden": map[string]any{"accessToken": testBWToken, "region": "eu"}},
	})
	expect(t, rec, http.StatusOK)
	for _, secret := range []string{"vault-token-plain", "infisical-secret-plain", "C2IgxjjLF7qSshsbwe8JGcbM075YXw"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("the settings response contains %q", secret)
		}
	}
	got := decodeJSON[model.Settings](t, rec).SecretStores
	if len(got) != 3 || got[0].Vault.Token != secrets.Mask || got[1].Infisical.ClientSecret != secrets.Mask || got[2].Bitwarden.AccessToken != secrets.Mask || got[0].ID == "" || got[0].CacheTTLSec != 300 {
		t.Fatalf("masked stores = %+v", got)
	}
	stored := e.c.Settings().SecretStores
	if !secrets.IsSealed(stored[0].Vault.Token) || e.c.Box.MustUnseal(stored[0].Vault.Token) != "vault-token-plain" ||
		e.c.Box.MustUnseal(stored[2].Bitwarden.AccessToken) != testBWToken {
		t.Fatalf("stored = %+v", stored[0].Vault)
	}

	// Saving the masked settings back keeps the credentials.
	rec = e.putStores(admin, []any{got[0], got[1], got[2]})
	expect(t, rec, http.StatusOK)
	if st := e.c.Settings().SecretStores; e.c.Box.MustUnseal(st[1].Infisical.ClientSecret) != "infisical-secret-plain" {
		t.Error("the masked client secret was not kept")
	}

	for _, c := range []struct {
		stores []any
		field  string
	}{
		{[]any{map[string]any{"name": "bw", "type": "bitwarden", "bitwarden": map[string]any{"accessToken": "not-a-token"}}}, "secretStores[0].bitwarden.accessToken"},
		{[]any{vaultStoreBody("https://a.example", "t"), vaultStoreBody("https://b.example", "t")}, "secretStores[1].name"},
		{[]any{vaultStoreBody("", "t")}, "secretStores[0].url"},
		// A new store with the mask has no credential to keep.
		{[]any{vaultStoreBody("https://a.example", secrets.Mask)}, "secretStores[0].vault.token"},
	} {
		rec := e.putStores(admin, c.stores)
		expect(t, rec, http.StatusUnprocessableEntity)
		if f := decodeJSON[map[string]string](t, rec)["field"]; f != c.field {
			t.Errorf("field = %q, want %q", f, c.field)
		}
		if strings.Contains(rec.Body.String(), "C2Igx") {
			t.Error("an error quotes the token")
		}
	}
}

func TestSecretReferencesEndToEnd(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	vault, srv := newMiniVault(t)
	expect(t, e.putStores(admin, []any{vaultStoreBody(srv.URL, "vault-token")}), http.StatusOK)

	site := func(from map[string]any) map[string]any {
		s := workerSite("shop", t.TempDir())
		s["node"].(map[string]any)["env"] = []any{map[string]any{"name": "DB_PASSWORD", "from": from}}
		return s
	}
	for _, c := range []struct {
		from  map[string]any
		field string
	}{
		{map[string]any{"store": "nope", "ref": "app/prod#DB_PASSWORD"}, "node.env[0].from.store"},
		{map[string]any{"store": "vault", "ref": "app/prod"}, "node.env[0].from.ref"},
	} {
		rec := e.do(http.MethodPost, "/api/sites", site(c.from), admin...)
		expect(t, rec, http.StatusUnprocessableEntity)
		if f := decodeJSON[map[string]string](t, rec)["field"]; f != c.field {
			t.Errorf("field = %q, want %q", f, c.field)
		}
	}
	s := e.createSite(admin, site(map[string]any{"store": "vault", "ref": "app/prod#DB_PASSWORD"}))
	if len(s.Node.Env) != 1 || s.Node.Env[0].From == nil || s.Node.Env[0].Value != "" {
		t.Fatalf("env = %+v", s.Node.Env)
	}

	// Resolving shows that the reference works, never the value.
	rec := e.do(http.MethodPost, "/api/secret-stores/resolve", map[string]any{"store": "vault", "ref": "app/prod#DB_PASSWORD"}, admin...)
	expect(t, rec, http.StatusOK)
	if res := decodeJSON[model.SecretTestResult](t, rec); !res.OK || strings.Contains(rec.Body.String(), "vault-value-1") {
		t.Fatalf("resolve = %s", rec.Body)
	}
	rec = e.do(http.MethodPost, "/api/secret-stores/resolve", map[string]any{"store": "vault", "ref": "app/prod#OTHER"}, admin...)
	if res := decodeJSON[model.SecretTestResult](t, rec); res.OK || !strings.Contains(res.Error, "OTHER") {
		t.Errorf("missing key = %s", rec.Body)
	}

	// An operator checks the site's references.
	e.user("op", model.RoleOperator, false)
	op := session(e.login("op"))
	rec = e.do(http.MethodPost, "/api/sites/"+s.ID+"/secrets/check", nil, op...)
	expect(t, rec, http.StatusOK)
	checks := decodeJSON[[]model.SecretRefCheck](t, rec)
	if len(checks) != 1 || !checks[0].OK || checks[0].Variable != "DB_PASSWORD" || strings.Contains(rec.Body.String(), "vault-value-1") {
		t.Fatalf("check = %s", rec.Body)
	}
	vault.mu.Lock()
	delete(vault.data, "app/prod")
	vault.mu.Unlock()
	checks = decodeJSON[[]model.SecretRefCheck](t, e.do(http.MethodPost, "/api/sites/"+s.ID+"/secrets/check", nil, op...))
	if checks[0].OK || !strings.Contains(checks[0].Error, "not found") {
		t.Errorf("check after deletion = %+v", checks[0])
	}

	// The status names the store and counts the reference.
	rec = e.do(http.MethodGet, "/api/secret-stores", nil, admin...)
	expect(t, rec, http.StatusOK)
	if st := decodeJSON[[]model.SecretStoreStatus](t, rec); len(st) != 1 || st[0].Name != "vault" || st[0].References != 1 || st[0].LastSuccess == nil {
		t.Errorf("status = %s", rec.Body)
	}

	// A referenced store can be neither removed nor renamed.
	expect(t, e.putStores(admin, []any{}), http.StatusUnprocessableEntity)
	stores := decodeJSON[model.Settings](t, e.do(http.MethodGet, "/api/settings", nil, admin...)).SecretStores
	stores[0].Name = "vault2"
	rec = e.putStores(admin, []any{stores[0]})
	expect(t, rec, http.StatusUnprocessableEntity)
	if msg := decodeJSON[map[string]string](t, rec)["error"]; !strings.Contains(msg, `site "shop" uses secret store "vault" (variable DB_PASSWORD)`) {
		t.Errorf("error = %q", msg)
	}

	// Operators and viewers do not manage stores.
	for _, path := range []string{"/api/secret-stores/test", "/api/secret-stores/resolve"} {
		expect(t, e.do(http.MethodPost, path, map[string]any{}, op...), http.StatusForbidden)
	}
	expect(t, e.do(http.MethodGet, "/api/secret-stores", nil, op...), http.StatusForbidden)
	e.user("viewer", model.RoleViewer, false)
	viewer := session(e.login("viewer"))
	expect(t, e.do(http.MethodPost, "/api/sites/"+s.ID+"/secrets/check", nil, viewer...), http.StatusForbidden)

	audit := strings.Join(e.auditActions(), " ")
	if !strings.Contains(audit, "root:secretstore.resolve") {
		t.Errorf("audit = %s", audit)
	}
}

func TestSecretStoreConnectionTest(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	_, srv := newMiniVault(t)
	expect(t, e.putStores(admin, []any{vaultStoreBody(srv.URL, "vault-token")}), http.StatusOK)
	saved := decodeJSON[model.Settings](t, e.do(http.MethodGet, "/api/settings", nil, admin...)).SecretStores[0]

	// As edited, with the saved (masked) token.
	rec := e.do(http.MethodPost, "/api/secret-stores/test", map[string]any{"store": saved, "ref": "app/prod#DB_PASSWORD"}, admin...)
	expect(t, rec, http.StatusOK)
	res := decodeJSON[model.SecretTestResult](t, rec)
	if !res.OK || !strings.Contains(res.Detail, "13 characters") || strings.Contains(rec.Body.String(), "vault-value-1") {
		t.Fatalf("test = %s", rec.Body)
	}
	// A new token that is wrong.
	rec = e.do(http.MethodPost, "/api/secret-stores/test", map[string]any{"store": vaultStoreBody(srv.URL, "wrong")}, admin...)
	if res := decodeJSON[model.SecretTestResult](t, rec); res.OK || !strings.Contains(res.Error, "permission denied") {
		t.Errorf("wrong token = %s", rec.Body)
	}
	rec = e.do(http.MethodPost, "/api/secret-stores/test", map[string]any{"store": map[string]any{"type": "vault"}}, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
}

// Stores travel in configuration backups like every other secret: with a
// passphrase to another server, readable with its key; without one, a
// store set up there under the same name keeps its own credentials.
func TestSecretStoresInBackups(t *testing.T) {
	t.Parallel()
	a := newEnv(t)
	adminA := a.adminSession()
	expect(t, a.putStores(adminA, []any{vaultStoreBody("https://vault.example.com", "token-a")}), http.StatusOK)
	s := workerSite("shop", t.TempDir())
	s["node"].(map[string]any)["env"] = []any{map[string]any{"name": "DB_PASSWORD", "from": map[string]any{"store": "vault", "ref": "app#DB"}}}
	a.createSite(adminA, s)
	dest := t.TempDir()
	a.backupSettings(adminA, func(b map[string]any) {
		b["passphrase"] = "correct horse battery staple"
		b["destinations"] = []any{folderDest(dest)}
	})
	if run := a.runBackup(adminA); run.Status != model.BackupSuccess {
		t.Fatalf("run = %+v", run)
	}
	data, _ := os.ReadFile(onlyArchive(t, dest))
	if bytes.Contains(data, []byte("token-a")) {
		t.Fatal("the archive contains the token")
	}

	b := newEnv(t)
	adminB := b.adminSession()
	rec := uploadRestore(b, adminB, data, "correct horse battery staple")
	expect(t, rec, http.StatusOK)
	st := b.c.Settings().SecretStores
	if len(st) != 1 || b.c.Box.MustUnseal(st[0].Vault.Token) != "token-a" {
		t.Fatalf("stores on the new server = %+v", st)
	}
	if res := decodeJSON[model.RestoreResult](t, rec); len(res.Warnings) != 0 {
		t.Errorf("warnings = %v", res.Warnings)
	}

	// Without a passphrase, on a server that has its own "vault" store.
	os.Remove(onlyArchive(t, dest))
	a.backupSettings(adminA, func(b map[string]any) { b["passphrase"] = "" })
	a.runBackup(adminA)
	data, _ = os.ReadFile(onlyArchive(t, dest))
	c := newEnv(t)
	adminC := c.adminSession()
	expect(t, c.putStores(adminC, []any{vaultStoreBody("https://vault.example.com", "token-c")}), http.StatusOK)
	rec = c.upload("/api/restore", "file", "x.zip", data, adminC...)
	expect(t, rec, http.StatusOK)
	warnings := strings.Join(decodeJSON[model.RestoreResult](t, rec).Warnings, "\n")
	if !strings.Contains(warnings, "settings.secretStores[vault].vault.token") || !strings.Contains(warnings, "kept") {
		t.Errorf("warnings = %s", warnings)
	}
	if st := c.c.Settings().SecretStores; len(st) != 1 || c.c.Box.MustUnseal(st[0].Vault.Token) != "token-c" {
		t.Errorf("stores = %+v", st)
	}
	// The restored site references the store that was kept.
	if sites := c.c.Sites(); len(sites) != 1 || sites[0].Node.Env[0].From.Store != "vault" {
		t.Errorf("restored sites = %+v", sites)
	}
}

// uploadRestore posts an archive to /api/restore with its passphrase.
func uploadRestore(e *env, admin []opt, data []byte, pass string) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "backup.zip")
	fw.Write(data)
	mw.WriteField("passphrase", pass)
	mw.Close()
	return e.do(http.MethodPost, "/api/restore", buf.Bytes(), append(append([]opt{}, admin...), withHeader("Content-Type", mw.FormDataContentType()))...)
}
