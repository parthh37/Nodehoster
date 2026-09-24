//go:build !windows

package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestSecretsCommands(t *testing.T) {
	s := newServer(t)
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "tok" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"errors":["permission denied"]}`))
			return
		}
		switch r.URL.Path {
		case "/v1/auth/token/lookup-self":
			w.Write([]byte(`{"data":{"ttl":0}}`))
		case "/v1/secret/data/app":
			w.Write([]byte(`{"data":{"data":{"DB":"cli-secret-value"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errors":[]}`))
		}
	}))
	defer vault.Close()

	s.run("secrets", "list").expect(t, ExitOK)
	st := s.c.Settings()
	st.SecretStores = []model.SecretStore{{Name: "vault", Type: model.SecretStoreVault, URL: vault.URL, Vault: &model.VaultStore{Token: "tok"}}}
	if _, err := s.c.UpdateSettings(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	site := &model.Site{Name: "shop", Type: model.SiteWorker, Node: &model.NodeConfig{AppRoot: t.TempDir(), Script: "w.js", Env: []model.EnvVar{
		{Name: "DB_PASSWORD", From: &model.SecretRef{Store: "vault", Ref: "app#DB"}},
		{Name: "OTHER", From: &model.SecretRef{Store: "vault", Ref: "app#NOPE"}},
	}}}
	s.createSite(site)

	out := s.run("secrets", "test", "vault", "--ref", "app#DB").expect(t, ExitOK).stdout
	if !strings.Contains(out, "vault works") || !strings.Contains(out, "16 characters") || strings.Contains(out, "cli-secret-value") {
		t.Errorf("test = %q", out)
	}
	r := s.run("secrets", "test", "vault", "--ref", "app#NOPE").expect(t, ExitError)
	if !strings.Contains(r.stderr, "NOPE") {
		t.Errorf("failed test = %q", r.stderr)
	}
	s.run("secrets", "test", "nope").expect(t, ExitError)

	r = s.run("secrets", "check", "shop").expect(t, ExitError)
	if !strings.Contains(r.stdout, "DB_PASSWORD") || !strings.Contains(r.stdout, "secretref:vault/app#DB") || !strings.Contains(r.stdout, "FAILED") ||
		!strings.Contains(r.stderr, "1 reference of shop cannot be read") || strings.Contains(r.stdout, "cli-secret-value") {
		t.Errorf("check = %q / %q", r.stdout, r.stderr)
	}

	out = s.run("secrets", "list").expect(t, ExitOK).stdout
	if !strings.Contains(out, "vault") || !strings.Contains(out, "vault  ") {
		t.Errorf("list = %q", out)
	}
	if out := s.run("--json", "secrets", "list").expect(t, ExitOK).stdout; !strings.Contains(out, `"references": 2`) {
		t.Errorf("list --json = %q", out)
	}
}
