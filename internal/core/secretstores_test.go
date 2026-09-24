package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	vals, err := c.procSecretEnv(site, site.Node.Env, site.ID, "")
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
	_, err = c.procSecretEnv(site, bad, site.ID, "nightly")
	if err == nil || !strings.Contains(err.Error(), "variable MISSING") || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("missing key: %v", err)
	}
	c.procSecretEnv(site, bad, site.ID, "nightly") // the second failure within minutes: no second event
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

// fakeVault answers KV v2 reads of secret/app and token lookups for token
// "tok". Every request is counted.
type fakeVault struct {
	*httptest.Server
	mu       sync.Mutex
	values   map[string]string
	requests atomic.Int32
}

func newFakeVault(t *testing.T, values map[string]string) *fakeVault {
	f := &fakeVault{values: values}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.Header.Get("X-Vault-Token") != "tok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/v1/auth/token/lookup-self":
			w.Write([]byte(`{"data":{"ttl":0}}`))
		case "/v1/secret/data/app":
			f.mu.Lock()
			raw, _ := json.Marshal(map[string]any{"data": map[string]any{"data": f.values}})
			f.mu.Unlock()
			w.Write(raw)
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errors":[]}`))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func vaultAt(name, url string) model.SecretStore {
	return model.SecretStore{Name: name, Type: model.SecretStoreVault, URL: url, Vault: &model.VaultStore{Token: "tok"}}
}

func fieldOf(err error) string {
	var ve *model.ValidationError
	if errors.As(err, &ve) {
		return ve.Field
	}
	return ""
}

// References in a deployment slot's own variables and in the variables
// previews are given are checked like the site's own, keep their store
// from being removed or renamed, and are part of `secrets check`.
func TestSecretRefsInSlotsAndPreviews(t *testing.T) {
	c := openTestCore(t)
	t.Cleanup(c.Shutdown)
	ctx := context.Background()
	f := newFakeVault(t, map[string]string{"DB": "x"})
	s := c.Settings()
	s.SecretStores = []model.SecretStore{vaultAt("vault", f.URL), vaultAt("spare", f.URL)}
	if _, err := c.UpdateSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	app := t.TempDir()
	site := func() *model.Site {
		in := &model.Site{Name: "shop", Type: model.SiteWorker, Node: &model.NodeConfig{AppRoot: app, Script: "w.js"},
			Slots: []model.DeploymentSlot{{Name: "staging", Env: []model.EnvVar{{Name: "SLOT_DB", From: &model.SecretRef{Store: "spare", Ref: "app#DB"}}}}}}
		in.Deploy.Previews.Env = []model.EnvVar{{Name: "PREVIEW_DB", From: &model.SecretRef{Store: "vault", Ref: "app#DB"}}}
		return in
	}
	in := site()
	in.Slots[0].Env[0].From.Store = "nope"
	if _, err := c.CreateSite(ctx, in); fieldOf(err) != "slots[0].env[0].from.store" {
		t.Fatalf("slot reference to a missing store: %v", err)
	}
	in = site()
	in.Deploy.Previews.Env[0].From.Ref = "no-key"
	if _, err := c.CreateSite(ctx, in); fieldOf(err) != "deploy.previews.env[0].from.ref" {
		t.Fatalf("preview reference a Vault store cannot read: %v", err)
	}
	in = site()
	in.Slots[0].Env[0].Value = "also"
	if _, err := c.CreateSite(ctx, in); fieldOf(err) != "slots[0].env[0].value" {
		t.Fatalf("slot variable with a value and a reference: %v", err)
	}
	created, err := c.CreateSite(ctx, site())
	if err != nil {
		t.Fatal(err)
	}

	checks, err := c.CheckSiteSecrets(ctx, created.ID)
	if err != nil || len(checks) != 2 || checks[0].Slot != "staging" || !checks[0].OK || !checks[1].Preview || !checks[1].OK {
		t.Fatalf("checks = %+v, %v", checks, err)
	}
	if refs := c.allSecretRefs(); !slices.Contains(refs, model.SecretRef{Store: "spare", Ref: "app#DB"}) {
		t.Errorf("all references = %v", refs)
	}

	// "spare" is only used by the slot, "vault" only by previews.
	s = c.Settings()
	s.SecretStores = s.SecretStores[:1]
	if _, err := c.UpdateSettings(ctx, s); err == nil || !strings.Contains(err.Error(), "slot staging") {
		t.Errorf("removing a store a slot uses: %v", err)
	}
	s = c.Settings()
	s.SecretStores[0].Name = "vault2"
	if _, err := c.UpdateSettings(ctx, s); err == nil || !strings.Contains(err.Error(), "preview settings") {
		t.Errorf("renaming a store previews use: %v", err)
	}
}

// A saved store's credentials are only sent to the server they were
// entered for: pointing the store, or a connection test, at another one
// needs them entered again. Encrypted values are never accepted as
// credentials (they would be decrypted and sent).
func TestSecretStoreCredentialsStayWithTheirServer(t *testing.T) {
	c := openTestCore(t)
	t.Cleanup(c.Shutdown)
	ctx := context.Background()
	good := newFakeVault(t, map[string]string{})
	evil := newFakeVault(t, map[string]string{})
	s := c.Settings()
	s.SecretStores = []model.SecretStore{vaultAt("vault", good.URL)}
	if _, err := c.UpdateSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	saved := c.MaskedSettings().SecretStores[0]

	moved := saved
	moved.URL = evil.URL
	if _, err := c.TestSecretStore(ctx, model.SecretStoreTest{Store: moved}); fieldOf(err) != "store.url" {
		t.Fatalf("test at another server: %v", err)
	}
	s = c.MaskedSettings()
	s.SecretStores[0].URL = evil.URL
	if _, err := c.UpdateSettings(ctx, s); fieldOf(err) != "secretStores[0].url" {
		t.Fatalf("save with another server: %v", err)
	}
	sealed := c.Settings().SecretStores[0].Vault.Token
	other, _ := c.Box.Seal("tok")
	for _, v := range []string{sealed, other} {
		st := vaultAt("new", evil.URL)
		st.Vault.Token = v
		if _, err := c.TestSecretStore(ctx, model.SecretStoreTest{Store: st}); err == nil || !strings.Contains(err.Error(), "encrypted") {
			t.Fatalf("test with an encrypted credential: %v", err)
		}
		s = c.MaskedSettings()
		s.SecretStores = append(s.SecretStores, st)
		if _, err := c.UpdateSettings(ctx, s); err == nil || !strings.Contains(err.Error(), "encrypted") {
			t.Fatalf("save with an encrypted credential: %v", err)
		}
	}
	if n := evil.requests.Load(); n != 0 {
		t.Fatalf("the other server was asked %d time(s)", n)
	}

	// The same server keeps the saved credentials; so do settings read
	// inside the server and saved again (sealed as they were).
	if res, err := c.TestSecretStore(ctx, model.SecretStoreTest{Store: saved}); err != nil || !res.OK {
		t.Fatalf("test of the saved store = %+v, %v", res, err)
	}
	if _, err := c.UpdateSettings(ctx, c.Settings()); err != nil {
		t.Fatalf("settings saved again: %v", err)
	}
	if _, err := c.UpdateSettings(ctx, c.MaskedSettings()); err != nil {
		t.Fatalf("masked settings saved again: %v", err)
	}
	if c.Box.MustUnseal(c.Settings().SecretStores[0].Vault.Token) != "tok" {
		t.Fatal("the token was lost")
	}
	// Entered again, a new server is fine.
	s = c.MaskedSettings()
	s.SecretStores[0].URL, s.SecretStores[0].Vault.Token = evil.URL, "tok"
	if _, err := c.UpdateSettings(ctx, s); err != nil {
		t.Fatalf("new server with the token entered: %v", err)
	}
}

// Each deployment slot's instances record the secrets they started with
// under the slot's key and are watched for their own variables; a change
// recycles the slot, not production.
func TestSecretRotationPerSlot(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep program")
	}
	c := testCore(t)
	ctx := context.Background()
	f := newFakeVault(t, map[string]string{"PROD": "p1", "SHARED": "s1", "SLOT": "t1"})
	s := c.Settings()
	s.SecretStores = []model.SecretStore{vaultAt("vault", f.URL)}
	if _, err := c.UpdateSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	from := func(k string) *model.SecretRef { return &model.SecretRef{Store: "vault", Ref: "app#" + k} }
	site, err := c.CreateSite(ctx, &model.Site{Name: "queue", Type: model.SiteWorker,
		Node: &model.NodeConfig{AppRoot: t.TempDir(), Runtime: model.RuntimeCustom, Script: sleep, Args: []string{"600"}, Env: []model.EnvVar{
			{Name: "PROD_DB", From: from("PROD"), SlotSetting: true},
			{Name: "SHARED", From: from("SHARED")},
		}},
		Slots: []model.DeploymentSlot{{Name: "staging", Env: []model.EnvVar{{Name: "SLOT_DB", From: from("SLOT")}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	key := model.SlotKey(site.ID, "staging")
	if err := c.Procs.Start(site.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Procs.StartSlot(site.ID, "staging"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Procs.StopSlot(site.ID, "staging"); c.Procs.Stop(site.ID) })
	pid := func(k string) int {
		st, _ := c.Procs.Status(k)
		if len(st.Instances) == 0 {
			return 0
		}
		return st.Instances[0].PID
	}
	poll := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	poll("instances", func() bool { return pid(site.ID) != 0 && pid(key) != 0 })

	w := c.watchedSecretRefs()
	if !slices.Equal(w[site.ID], []model.SecretRef{*from("PROD"), *from("SHARED")}) || !slices.Equal(w[key], []model.SecretRef{*from("SHARED"), *from("SLOT")}) {
		t.Fatalf("watched = %v", w)
	}

	prodPID, slotPID := pid(site.ID), pid(key)
	c.secretsChanged(key, []string{from("SLOT").String()})
	poll("the slot's recycle", func() bool { p := pid(key); return p != 0 && p != slotPID })
	if pid(site.ID) != prodPID {
		t.Error("production was recycled for a slot's secret")
	}
	evs, _ := c.Store.ListEvents(ctx, site.ID, 50)
	if !slices.ContainsFunc(evs, func(e model.Event) bool {
		return e.Type == events.SecretRotated && strings.Contains(e.Message, "queue [staging]")
	}) {
		t.Errorf("no rotation event for the slot: %+v", evs)
	}
}
