package secretstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

const tuxID = "5f6a7c1e-8f8a-4a3e-9c1e-2b7f0a6d4c10"

// fakeJWT is an unsigned token with the claims a machine account gets.
func fakeJWT(org string) string {
	enc := base64.RawURLEncoding.EncodeToString
	claims, _ := json.Marshal(map[string]any{"organization": org, "scope": []string{"api.secrets"}, "exp": 1})
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc(claims) + ".c2ln"
}

// fakeBitwarden is a self-hosted Bitwarden server (at /identity and /api)
// answering with the SDK's fake-server fixtures: the encrypted payload
// that decrypts, with the SDK's access token, into its organization key,
// and the "TUX" secret encrypted with that key.
type fakeBitwarden struct {
	mu      sync.Mutex
	org     string // organization of the secrets
	access  string // the API token currently valid
	logins  atomic.Int32
	lastReq *http.Request
	form    map[string]string
}

func (f *fakeBitwarden) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	f.lastReq = r
	switch {
	case r.URL.Path == "/identity/connect/token" && r.Method == http.MethodPost:
		r.ParseForm()
		f.form = map[string]string{}
		for k := range r.PostForm {
			f.form[k] = r.PostForm.Get(k)
		}
		if f.form["client_id"] != "ec2c1d46-6a4b-4751-a310-af9601317f2d" || f.form["client_secret"] != "C2IgxjjLF7qSshsbwe8JGcbM075YXw" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_client","error_description":"invalid_client"}`))
			return
		}
		n := f.logins.Add(1)
		f.access = fakeJWT(sdkOrgID) + string(rune('a'+n))
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": f.access, "expires_in": 3600, "token_type": "Bearer", "scope": "api.secrets",
			"encrypted_payload": sdkEncryptedPayload,
		})
	case strings.HasPrefix(r.URL.Path, "/api/secrets/") && r.Method == http.MethodGet:
		if r.Header.Get("Authorization") != "Bearer "+f.access {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/secrets/")
		if id != tuxID {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Resource not found."}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"object": "secret", "id": id, "organizationId": f.org,
			"key": sdkTuxKey, "value": sdkTuxValue, "note": sdkTuxNote,
			"creationDate": "2025-06-26T14:27:24Z", "revisionDate": "2025-06-26T14:27:24Z",
		})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func bitwardenStore(url, token string) model.SecretStore {
	s := model.SecretStore{Name: "bw", Type: model.SecretStoreBitwarden, URL: url, Bitwarden: &model.BitwardenStore{AccessToken: token}}
	s.ApplyDefaults()
	return s
}

func TestBitwardenSignsInAndDecrypts(t *testing.T) {
	f := &fakeBitwarden{org: sdkOrgID}
	srv := httptest.NewServer(f)
	defer srv.Close()
	clock := newClock()
	c, err := newBitwarden(bitwardenStore(srv.URL, sdkAccessToken), srv.Client(), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	// Uppercase IDs are the same secret.
	res := c.fetch(context.Background(), []string{tuxID, strings.ToUpper(tuxID), "00000000-0000-4000-8000-000000000000"})
	if res[0].value != "🐧" || res[0].err != nil || res[1].value != "🐧" {
		t.Fatalf("results = %+v", res)
	}
	if !IsNotFound(res[2].err) {
		t.Errorf("unknown secret: %v", res[2].err)
	}
	if f.logins.Load() != 1 {
		t.Errorf("%d sign-ins", f.logins.Load())
	}
	want := map[string]string{"scope": "api.secrets", "grant_type": "client_credentials",
		"client_id": "ec2c1d46-6a4b-4751-a310-af9601317f2d", "client_secret": "C2IgxjjLF7qSshsbwe8JGcbM075YXw"}
	for k, v := range want {
		if f.form[k] != v {
			t.Errorf("form %s = %q, want %q", k, f.form[k], v)
		}
	}
	if got := f.lastReq.Header.Get("Device-Type"); got != "21" {
		t.Errorf("Device-Type = %q", got)
	}
	if c.orgID != sdkOrgID {
		t.Errorf("organization = %q", c.orgID)
	}

	// Expired session: signed in again before the read.
	clock.Add(time.Hour)
	fetchOne(t, c, tuxID)
	if f.logins.Load() != 2 {
		t.Errorf("logins = %d after expiry", f.logins.Load())
	}
	// A token the server revoked: signed in again once.
	f.mu.Lock()
	f.access = "revoked"
	f.mu.Unlock()
	if v, err := fetchOne(t, c, tuxID); v != "🐧" || err != nil {
		t.Errorf("after 401 = %q, %v", v, err)
	}

	detail, err := c.test(context.Background())
	if err != nil || !strings.Contains(detail, "organization key") {
		t.Errorf("test = %q, %v", detail, err)
	}
}

func TestBitwardenErrors(t *testing.T) {
	f := &fakeBitwarden{org: "11111111-1111-4111-8111-111111111111"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c, _ := newBitwarden(bitwardenStore(srv.URL, sdkAccessToken), srv.Client(), time.Now)
	if _, err := fetchOne(t, c, tuxID); err == nil || !strings.Contains(err.Error(), "another organization") {
		t.Errorf("secret of another organization: %v", err)
	}

	// Another token (a different client secret) is refused by the server.
	other := strings.Replace(sdkAccessToken, "C2Igx", "XXXXX", 1)
	c, _ = newBitwarden(bitwardenStore(srv.URL, other), srv.Client(), time.Now)
	_, err := c.test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("refused token: %v", err)
	}
	if strings.Contains(err.Error(), "XXXXX") {
		t.Errorf("the error quotes the token: %v", err)
	}

	// The right client credentials with the wrong key: the payload's MAC
	// does not match.
	wrongKey := strings.Replace(sdkAccessToken, ":X8vb", ":Y8vb", 1)
	c, _ = newBitwarden(bitwardenStore(srv.URL, wrongKey), srv.Client(), time.Now)
	if _, err := c.test(context.Background()); err == nil || !strings.Contains(err.Error(), "organization key") {
		t.Errorf("wrong key: %v", err)
	}

	if _, err := newBitwarden(bitwardenStore(srv.URL, "not a token"), srv.Client(), time.Now); err == nil {
		t.Error("a malformed token was accepted")
	}
}

func TestBitwardenURLs(t *testing.T) {
	for _, c := range []struct {
		store              model.SecretStore
		api, identity      string
		apiOver, identOver string
	}{
		{store: bitwardenStore("", sdkAccessToken), api: bitwardenUSAPI, identity: bitwardenUSIdentity},
		{store: model.SecretStore{Type: model.SecretStoreBitwarden, Bitwarden: &model.BitwardenStore{AccessToken: sdkAccessToken, Region: "eu"}}, api: bitwardenEUAPI, identity: bitwardenEUIdentity},
		{store: bitwardenStore("https://bw.example.com", sdkAccessToken), api: "https://bw.example.com/api", identity: "https://bw.example.com/identity"},
		{store: model.SecretStore{Type: model.SecretStoreBitwarden, Bitwarden: &model.BitwardenStore{AccessToken: sdkAccessToken, APIURL: "https://a.example", IdentityURL: "https://i.example"}}, api: "https://a.example", identity: "https://i.example"},
	} {
		b, err := newBitwarden(c.store, http.DefaultClient, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		if b.api != c.api || b.identity != c.identity {
			t.Errorf("%+v: api %s identity %s", c.store.Bitwarden, b.api, b.identity)
		}
	}
}
