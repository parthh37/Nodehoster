package secretstore

import (
	"context"
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

// fakeInfisical serves Universal Auth login and the v4 (or, with v3only,
// only the v3 raw) secrets API for project "proj-1", environment "prod".
type fakeInfisical struct {
	mu      sync.Mutex
	v3only  bool
	hidden  bool
	token   string
	secrets map[string]string // "<path>|<name>" → value
	logins  atomic.Int32
	queries []string
}

func newFakeInfisical() *fakeInfisical {
	return &fakeInfisical{secrets: map[string]string{"/|DB_PASSWORD": "root-pw", "/backend|API_KEY": "key-123"}}
}

func (f *fakeInfisical) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"reqId": "r1", "statusCode": code, "message": msg, "error": http.StatusText(code)})
	}
	if r.URL.Path == "/api/v1/auth/universal-auth/login" && r.Method == http.MethodPost {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["clientId"] != "client-1" || in["clientSecret"] != "secret-1" {
			fail(http.StatusUnauthorized, "Invalid credentials")
			return
		}
		n := f.logins.Add(1)
		f.token = "tok-" + string(rune('a'+n))
		json.NewEncoder(w).Encode(map[string]any{"accessToken": f.token, "expiresIn": 7200, "accessTokenMaxTTL": 7200, "tokenType": "Bearer"})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token || f.token == "" {
		fail(http.StatusUnauthorized, "Token invalid")
		return
	}
	q := r.URL.Query()
	f.queries = append(f.queries, r.URL.Path+"?"+q.Encode())
	var name, project string
	switch {
	case r.URL.Path == "/api/v4/secrets" && !f.v3only:
		if q.Get("viewSecretValue") != "false" {
			fail(http.StatusBadRequest, "the test should not read values")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"secrets": []any{map[string]any{"secretKey": "DB_PASSWORD"}}})
		return
	case strings.HasPrefix(r.URL.Path, "/api/v4/secrets/") && !f.v3only:
		name, project = strings.TrimPrefix(r.URL.Path, "/api/v4/secrets/"), q.Get("projectId")
	case strings.HasPrefix(r.URL.Path, "/api/v3/secrets/raw/"):
		name, project = strings.TrimPrefix(r.URL.Path, "/api/v3/secrets/raw/"), q.Get("workspaceId")
	default:
		fail(http.StatusNotFound, "Route "+r.Method+":"+r.URL.Path+" not found")
		return
	}
	if project != "proj-1" || q.Get("environment") != "prod" {
		fail(http.StatusForbidden, "You are not allowed to access this project")
		return
	}
	v, ok := f.secrets[q.Get("secretPath")+"|"+name]
	if !ok {
		fail(http.StatusNotFound, "Secret with name '"+name+"' not found")
		return
	}
	sec := map[string]any{"secretKey": name, "secretValue": v, "secretValueHidden": f.hidden}
	if f.hidden {
		sec["secretValue"] = "<hidden-by-infisical>"
	}
	json.NewEncoder(w).Encode(map[string]any{"secret": sec})
}

func infisicalStore(url string) model.SecretStore {
	s := model.SecretStore{Name: "inf", Type: model.SecretStoreInfisical, URL: url,
		Infisical: &model.InfisicalStore{ClientID: "client-1", ClientSecret: "secret-1", ProjectID: "proj-1", Environment: "prod"}}
	s.ApplyDefaults()
	return s
}

func TestInfisicalReadsSecrets(t *testing.T) {
	f := newFakeInfisical()
	srv := httptest.NewServer(f)
	defer srv.Close()
	clock := newClock()
	c := newInfisical(infisicalStore(srv.URL), srv.Client(), clock.Now)

	res := c.fetch(context.Background(), []string{"DB_PASSWORD", "/backend/API_KEY", "NOPE"})
	if res[0].value != "root-pw" || res[1].value != "key-123" || res[0].err != nil || res[1].err != nil {
		t.Fatalf("results = %+v", res)
	}
	if !IsNotFound(res[2].err) {
		t.Errorf("missing secret: %v", res[2].err)
	}
	if f.logins.Load() != 1 {
		t.Errorf("%d logins", f.logins.Load())
	}
	if q := f.queries[1]; !strings.Contains(q, "secretPath=%2Fbackend") || !strings.Contains(q, "expandSecretReferences=true") {
		t.Errorf("query = %s", q)
	}

	// The token is reused until shortly before it expires.
	clock.Add(time.Hour)
	fetchOne(t, c, "DB_PASSWORD")
	if f.logins.Load() != 1 {
		t.Fatalf("signed in again before expiry")
	}
	clock.Add(59 * time.Minute)
	fetchOne(t, c, "DB_PASSWORD")
	if f.logins.Load() != 2 {
		t.Fatalf("logins = %d, want a new sign-in near expiry", f.logins.Load())
	}

	// A token the server no longer accepts: sign in again once.
	f.mu.Lock()
	f.token = "rotated"
	f.mu.Unlock()
	if v, err := fetchOne(t, c, "DB_PASSWORD"); v != "root-pw" || err != nil {
		t.Fatalf("after 401 = %q, %v", v, err)
	}

	detail, err := c.test(context.Background())
	if err != nil || !strings.Contains(detail, "1 secrets") {
		t.Errorf("test = %q, %v", detail, err)
	}
}

func TestInfisicalErrorsAndOldServers(t *testing.T) {
	f := newFakeInfisical()
	srv := httptest.NewServer(f)
	defer srv.Close()

	s := infisicalStore(srv.URL)
	s.Infisical.ClientSecret = "wrong"
	if _, err := newInfisical(s, srv.Client(), time.Now).test(context.Background()); err == nil || !strings.Contains(err.Error(), "Invalid credentials") {
		t.Errorf("wrong secret: %v", err)
	}

	s = infisicalStore(srv.URL)
	s.Infisical.Environment = "dev"
	if _, err := fetchOne(t, newInfisical(s, srv.Client(), time.Now), "DB_PASSWORD"); err == nil || IsNotFound(err) || !strings.Contains(err.Error(), "no access") {
		t.Errorf("forbidden environment: %v", err)
	}

	f.mu.Lock()
	f.hidden = true
	f.mu.Unlock()
	if _, err := fetchOne(t, newInfisical(infisicalStore(srv.URL), srv.Client(), time.Now), "DB_PASSWORD"); err == nil || !strings.Contains(err.Error(), "hides") {
		t.Errorf("hidden value: %v", err)
	}
	f.mu.Lock()
	f.hidden = false
	f.mu.Unlock()

	// A server without the v4 API: v3 raw is used from then on.
	f.mu.Lock()
	f.v3only = true
	f.mu.Unlock()
	c := newInfisical(infisicalStore(srv.URL), srv.Client(), time.Now)
	if v, err := fetchOne(t, c, "/backend/API_KEY"); v != "key-123" || err != nil {
		t.Fatalf("v3 fallback = %q, %v", v, err)
	}
	if !c.v3 {
		t.Error("the store did not remember the v3 API")
	}
	if _, err := fetchOne(t, c, "NOPE"); !IsNotFound(err) {
		t.Errorf("v3 missing secret: %v", err)
	}
	detail, err := c.test(context.Background())
	if err != nil || !strings.Contains(detail, "too old") {
		t.Errorf("test on an old server = %q, %v", detail, err)
	}
}
