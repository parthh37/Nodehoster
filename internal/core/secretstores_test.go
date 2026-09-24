package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
)

// The environment of an instance, a task and a deployment gets the value
// from the store; a value that cannot be read fails with the variable's
// name and a secret.failed event, which never contains a value.
func TestSecretEnvResolution(t *testing.T) {
	c := openTestCore(t)
	t.Cleanup(c.Shutdown)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "tok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == "/v1/secret/data/app" {
			w.Write([]byte(`{"data":{"data":{"DB":"s3cr3t-value","TOKEN":"git-token"}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"errors":[]}`))
	}))
	defer srv.Close()
	s := c.Settings()
	s.SecretStores = []model.SecretStore{{Name: "vault", Type: model.SecretStoreVault, URL: srv.URL, Vault: &model.VaultStore{Token: "tok"}}}
	if _, err := c.UpdateSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	site, err := c.CreateSite(context.Background(), &model.Site{Name: "shop", Type: model.SiteWorker, Node: &model.NodeConfig{
		AppRoot: t.TempDir(), Script: "w.js", Env: []model.EnvVar{
			{Name: "DB_PASSWORD", From: &model.SecretRef{Store: "vault", Ref: "app#DB"}},
			{Name: "PLAIN", Value: "x"},
		}}})
	if err != nil {
		t.Fatal(err)
	}
	vals, err := c.procSecretEnv(site, site.Node.Env, "")
	if err != nil || len(vals) != 1 || vals["DB_PASSWORD"] != "s3cr3t-value" {
		t.Fatalf("instance env = %v, %v", vals, err)
	}
	if tok, err := c.secretToken(site, model.SecretRef{Store: "vault", Ref: "app#TOKEN"}); tok != "git-token" || err != nil {
		t.Errorf("token = %q, %v", tok, err)
	}
	if refs := c.watchedSecretRefs(); len(refs) != 0 {
		t.Errorf("a stopped site is watched: %v", refs)
	}

	bad := []model.EnvVar{{Name: "MISSING", From: &model.SecretRef{Store: "vault", Ref: "app#NOPE"}}}
	_, err = c.procSecretEnv(site, bad, "nightly")
	if err == nil || !strings.Contains(err.Error(), "variable MISSING") || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("missing key: %v", err)
	}
	c.procSecretEnv(site, bad, "nightly") // the second failure within minutes: no second event
	evs, _ := c.Store.ListEvents(context.Background(), site.ID, 50)
	n := 0
	for _, e := range evs {
		if e.Type == events.SecretFailed {
			n++
			if !strings.Contains(e.Message, "task nightly could not start") {
				t.Errorf("event = %q", e.Message)
			}
		}
		if strings.Contains(e.Message, "s3cr3t-value") {
			t.Errorf("an event contains the value: %q", e.Message)
		}
	}
	if n != 1 {
		t.Errorf("%d secret.failed events", n)
	}

	// Masked settings never show the token; the store stays referenced.
	if m := c.MaskedSettings().SecretStores[0].Vault.Token; m != "__SECRET__" {
		t.Errorf("masked token = %q", m)
	}
	s = c.Settings()
	s.SecretStores = nil
	if _, err := c.UpdateSettings(context.Background(), s); err == nil {
		t.Error("a referenced store was removed")
	}
}
