package model

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestSecretRefText(t *testing.T) {
	r := SecretRef{Store: "vault", Ref: "app/prod#DB_PASSWORD"}
	if r.String() != "secretref:vault/app/prod#DB_PASSWORD" {
		t.Errorf("String = %q", r.String())
	}
	if got, ok := ParseSecretRefText(" " + r.String() + " "); !ok || got != r {
		t.Errorf("parse = %+v, %v", got, ok)
	}
	if got, ok := ParseSecretRefText("secretref:bw/3b3f5c1e-8f8a-4a3e-9c1e-2b7f0a6d4c10"); !ok || got.Store != "bw" || got.Ref != "3b3f5c1e-8f8a-4a3e-9c1e-2b7f0a6d4c10" {
		t.Errorf("parse bitwarden = %+v", got)
	}
	if _, ok := ParseSecretRefText("postgres://secretref:x"); ok {
		t.Error("a plain value was taken for a reference")
	}
}

func TestValidateSecretRef(t *testing.T) {
	for _, c := range []struct {
		typ, ref string
		ok       bool
	}{
		{SecretStoreVault, "app/prod#DB_PASSWORD", true},
		{SecretStoreVault, "app#key with space", true},
		{SecretStoreVault, "app/prod", false},
		{SecretStoreVault, "app/prod#", false},
		{SecretStoreVault, "/app#K", false},
		{SecretStoreVault, "app/../x#K", false},
		{SecretStoreVault, "app//x#K", false},
		{SecretStoreInfisical, "DB_PASSWORD", true},
		{SecretStoreInfisical, "/backend/api/DB_PASSWORD", true},
		{SecretStoreInfisical, "backend/DB_PASSWORD", false},
		{SecretStoreInfisical, "/backend/", false},
		{SecretStoreInfisical, "/../DB", false},
		{SecretStoreInfisical, "DB PASSWORD", false},
		{SecretStoreBitwarden, "3b3f5c1e-8f8a-4a3e-9c1e-2b7f0a6d4c10", true},
		{SecretStoreBitwarden, "DB_PASSWORD", false},
		{SecretStoreVault, "", false},
		{SecretStoreVault, "a\nb#c", false},
		{"other", "x", false},
	} {
		err := ValidateSecretRef(c.typ, c.ref)
		if (err == nil) != c.ok {
			t.Errorf("%s %q: %v", c.typ, c.ref, err)
		}
	}
}

func secretStoreCAPEM(t *testing.T) string {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestSecretStoreDefaultsAndValidation(t *testing.T) {
	v := SecretStore{Name: " vault ", Type: "Vault", URL: "https://vault.example.com:8200/", Vault: &VaultStore{Token: "t"}}
	v.ApplyDefaults()
	if v.Name != "vault" || v.Type != SecretStoreVault || v.URL != "https://vault.example.com:8200" || v.CacheTTLSec != DefaultSecretCacheTTLSec ||
		v.Vault.Auth != VaultAuthToken || v.Vault.Mount != "secret" || v.Vault.KVVersion != 2 || v.Vault.AuthMount != "approle" {
		t.Errorf("defaults = %+v %+v", v, *v.Vault)
	}
	if err := v.Validate("secretStores[0]"); err != nil {
		t.Fatal(err)
	}
	b := SecretStore{Name: "bw", Type: SecretStoreBitwarden, Bitwarden: &BitwardenStore{AccessToken: "x"}}
	b.ApplyDefaults()
	if b.Bitwarden.Region != BitwardenUS {
		t.Errorf("bitwarden region = %q", b.Bitwarden.Region)
	}
	i := SecretStore{Name: "inf", Type: SecretStoreInfisical, Infisical: &InfisicalStore{ClientID: "c", ClientSecret: "s", ProjectID: "p", Environment: "prod"}}
	i.ApplyDefaults()
	if err := i.Validate("secretStores[1]"); err != nil {
		t.Fatal(err)
	}

	ca := secretStoreCAPEM(t)
	for _, c := range []struct {
		field string
		edit  func(s *SecretStore)
	}{
		{"x.name", func(s *SecretStore) { s.Name = "has/slash" }},
		{"x.type", func(s *SecretStore) { s.Type = "aws" }},
		{"x.url", func(s *SecretStore) { s.URL = "" }},
		{"x.url", func(s *SecretStore) { s.URL = "ftp://vault" }},
		{"x.caCert", func(s *SecretStore) { s.CACert = "not pem" }},
		{"x.cacheTtlSec", func(s *SecretStore) { s.CacheTTLSec = 5 }},
		{"x.watchIntervalSec", func(s *SecretStore) { s.WatchIntervalSec = 30 }},
		{"x.vault.token", func(s *SecretStore) { s.Vault.Token = "" }},
		{"x.vault.roleId", func(s *SecretStore) { s.Vault.Auth = VaultAuthAppRole }},
		{"x.vault.auth", func(s *SecretStore) { s.Vault.Auth = "ldap" }},
		{"x.vault.mount", func(s *SecretStore) { s.Vault.Mount = "a/../b" }},
		{"x.vault.kvVersion", func(s *SecretStore) { s.Vault.KVVersion = 3 }},
	} {
		s := v
		vv := *v.Vault
		s.Vault = &vv
		c.edit(&s)
		var ve *ValidationError
		if err := s.Validate("x"); !errors.As(err, &ve) || ve.Field != c.field {
			t.Errorf("%s: %v", c.field, err)
		}
	}
	s := v
	s.CACert = ca
	s.WatchIntervalSec = 300
	if err := s.Validate("x"); err != nil {
		t.Errorf("a CA and a watch interval: %v", err)
	}
	b.Bitwarden.Region = ""
	if err := b.Validate("x"); err == nil || !strings.Contains(err.Error(), "self-hosted") {
		t.Errorf("bitwarden without region or URL: %v", err)
	}
	i.Infisical.Environment = "prod/x"
	if err := i.Validate("x"); err == nil {
		t.Error("an environment with a slash was accepted")
	}

	// Credentials go to the store: https, except on this machine.
	for u, ok := range map[string]bool{
		"https://vault.example.com": true, "http://127.0.0.1:8200": true, "http://localhost:8200": true, "http://[::1]:8200": true,
		"http://vault.example.com:8200": false, "http://10.0.0.5:8200": false,
	} {
		s := v
		s.URL = u
		if err := s.Validate("x"); (err == nil) != ok {
			t.Errorf("%s: %v", u, err)
		}
	}
	bw := b
	bw.Bitwarden = &BitwardenStore{AccessToken: "x", APIURL: "http://bw.example.com/api", IdentityURL: "https://bw.example.com/identity"}
	var ve *ValidationError
	if err := bw.Validate("x"); !errors.As(err, &ve) || ve.Field != "x.bitwarden.apiUrl" {
		t.Errorf("bitwarden http API URL: %v", err)
	}
}

// SameStoreServer: saved credentials are only reused for the servers
// they were entered for.
func TestSameStoreServer(t *testing.T) {
	v := SecretStore{Name: "v", Type: SecretStoreVault, URL: "https://vault.example.com", Vault: &VaultStore{}}
	b := SecretStore{Name: "b", Type: SecretStoreBitwarden, Bitwarden: &BitwardenStore{Region: BitwardenUS}}
	for _, c := range []struct {
		edit func(s *SecretStore)
		same bool
	}{
		{func(s *SecretStore) {}, true},
		{func(s *SecretStore) { s.URL = "https://VAULT.example.com:443" }, true},
		{func(s *SecretStore) { s.CacheTTLSec, s.Vault.Mount = 60, "kv" }, true},
		{func(s *SecretStore) { s.URL = "https://evil.example.com" }, false},
		{func(s *SecretStore) { s.URL = "https://vault.example.com:8200" }, false},
		{func(s *SecretStore) { s.URL = "http://vault.example.com" }, false},
		{func(s *SecretStore) { s.Type = SecretStoreInfisical }, false},
	} {
		s := v
		vv := *v.Vault
		s.Vault = &vv
		c.edit(&s)
		if SameStoreServer(v, s) != c.same {
			t.Errorf("%+v: same = %v", s, !c.same)
		}
	}
	for _, c := range []struct {
		edit func(s *BitwardenStore)
		same bool
	}{
		{func(s *BitwardenStore) {}, true},
		{func(s *BitwardenStore) { s.Region = BitwardenEU }, false},
		{func(s *BitwardenStore) { s.APIURL = "https://evil.example.com/api" }, false},
		{func(s *BitwardenStore) { s.IdentityURL = "https://evil.example.com/identity" }, false},
	} {
		s := b
		bb := *b.Bitwarden
		s.Bitwarden = &bb
		c.edit(s.Bitwarden)
		if SameStoreServer(b, s) != c.same {
			t.Errorf("%+v: same = %v", *s.Bitwarden, !c.same)
		}
	}
}

func TestSiteSecretRefs(t *testing.T) {
	site := &Site{Name: "shop", Type: SiteWorker, Node: &NodeConfig{AppRoot: `C:\apps\shop`, Script: "w.js", Env: []EnvVar{
		{Name: "PLAIN", Value: "1"},
		{Name: "DB_PASSWORD", From: &SecretRef{Store: "vault", Ref: "app#DB"}},
	}}, Tasks: []ScheduledTask{{Name: "nightly", Schedule: "@daily", Script: "n.js", Env: []EnvVar{{Name: "K", From: &SecretRef{Store: "bw", Ref: "id"}}}}},
		Slots: []DeploymentSlot{{Name: "staging", Env: []EnvVar{{Name: "X", Value: "1"}, {Name: "DB_PASSWORD", From: &SecretRef{Store: "vault", Ref: "staging#DB"}}}}}}
	site.Deploy.Git = GitSource{Repo: "https://example.com/r.git", TokenFrom: &SecretRef{Store: "vault", Ref: "ci#token"}}
	site.Deploy.Previews.Env = []EnvVar{{Name: "DB_PASSWORD", From: &SecretRef{Store: "vault", Ref: "preview#DB"}}}
	site.ApplyDefaults()
	if err := site.Validate(); err != nil {
		t.Fatal(err)
	}
	refs := site.SecretRefs()
	var got []string
	for _, r := range refs {
		got = append(got, r.Field+" "+r.Ref.Ref+" = "+r.Place())
	}
	want := []string{
		"node.env[1] app#DB = variable DB_PASSWORD",
		"tasks[0].env[0] id = variable K of task nightly",
		"slots[0].env[1] staging#DB = variable DB_PASSWORD of slot staging",
		"deploy.previews.env[0] preview#DB = variable DB_PASSWORD of the preview settings",
		"deploy.git.tokenFrom ci#token = git token",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("refs:\n%s", strings.Join(got, "\n"))
	}

	for _, c := range []struct {
		field string
		edit  func(s *Site)
	}{
		{"node.env[1].value", func(s *Site) { s.Node.Env[1].Value = "also" }},
		{"node.env[1].value", func(s *Site) { s.Node.Env[1].Secret = true }},
		{"node.env[1].from.store", func(s *Site) { s.Node.Env[1].From.Store = " " }},
		{"node.env[1].from.ref", func(s *Site) { s.Node.Env[1].From.Ref = "" }},
		{"node.env[1].from", func(s *Site) { s.Node.Env[1].Name = "NODE_OPTIONS" }},
		{"node.env[1].from", func(s *Site) { s.Node.Env[1].Name = "PORT" }},
		{"node.env[1].from", func(s *Site) { s.Node.Env[1].Name = "ASPNETCORE_URLS" }},
		{"node.env[1].from", func(s *Site) { s.Node.Env[1].Name = "NODEHOSTER_AGENT_TOKEN" }},
		{"tasks[0].env[0].from.ref", func(s *Site) { s.Tasks[0].Env[0].From.Ref = "" }},
		{"tasks[0].env[0].from", func(s *Site) { s.Tasks[0].Env[0].Name = "NODE_APP_INSTANCE" }},
		{"slots[0].env[1].value", func(s *Site) { s.Slots[0].Env[1].Value = "also" }},
		{"slots[0].env[1].from.ref", func(s *Site) { s.Slots[0].Env[1].From.Ref = "" }},
		{"slots[0].env[1].from", func(s *Site) { s.Slots[0].Env[1].Name = "NODE_OPTIONS" }},
		{"deploy.previews.env[0].value", func(s *Site) { s.Deploy.Previews.Env[0].Secret = true }},
		{"deploy.previews.env[0].from.store", func(s *Site) { s.Deploy.Previews.Env[0].From.Store = "" }},
		{"deploy.previews.env[0].from", func(s *Site) { s.Deploy.Previews.Env[0].Name = "Port" }},
		{"deploy.git.token", func(s *Site) { s.Deploy.Git.Token = "stored" }},
		{"deploy.git.tokenFrom.store", func(s *Site) { s.Deploy.Git.TokenFrom.Store = "" }},
	} {
		s := cloneRefSite(site)
		c.edit(s)
		var ve *ValidationError
		if err := s.Validate(); !errors.As(err, &ve) || ve.Field != c.field {
			t.Errorf("%s: %v", c.field, err)
		}
	}
}

func cloneRefSite(s *Site) *Site {
	cloneEnv := func(env []EnvVar) []EnvVar {
		out := append([]EnvVar(nil), env...)
		for i, e := range out {
			if e.From != nil {
				f := *e.From
				out[i].From = &f
			}
		}
		return out
	}
	c := *s
	n := *s.Node
	n.Env = cloneEnv(s.Node.Env)
	c.Node = &n
	c.Tasks = append([]ScheduledTask(nil), s.Tasks...)
	for i := range c.Tasks {
		c.Tasks[i].Env = cloneEnv(s.Tasks[i].Env)
	}
	c.Slots = append([]DeploymentSlot(nil), s.Slots...)
	for i := range c.Slots {
		c.Slots[i].Env = cloneEnv(s.Slots[i].Env)
	}
	c.Deploy.Previews.Env = cloneEnv(s.Deploy.Previews.Env)
	if s.Deploy.Git.TokenFrom != nil {
		f := *s.Deploy.Git.TokenFrom
		c.Deploy.Git.TokenFrom = &f
	}
	return &c
}
