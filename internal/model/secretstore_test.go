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

func testCAPEM(t *testing.T) string {
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

	ca := testCAPEM(t)
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
}

func TestSiteSecretRefs(t *testing.T) {
	site := &Site{Name: "shop", Type: SiteWorker, Node: &NodeConfig{AppRoot: `C:\apps\shop`, Script: "w.js", Env: []EnvVar{
		{Name: "PLAIN", Value: "1"},
		{Name: "DB_PASSWORD", From: &SecretRef{Store: "vault", Ref: "app#DB"}},
	}}, Tasks: []ScheduledTask{{Name: "nightly", Schedule: "@daily", Script: "n.js", Env: []EnvVar{{Name: "K", From: &SecretRef{Store: "bw", Ref: "id"}}}}}}
	site.Deploy.Git = GitSource{Repo: "https://example.com/r.git", TokenFrom: &SecretRef{Store: "vault", Ref: "ci#token"}}
	site.ApplyDefaults()
	if err := site.Validate(); err != nil {
		t.Fatal(err)
	}
	refs := site.SecretRefs()
	if len(refs) != 3 || refs[0].Field != "node.env[1]" || refs[0].Variable != "DB_PASSWORD" || refs[1].Task != "nightly" || refs[1].Field != "tasks[0].env[0]" || refs[2].Field != "deploy.git.tokenFrom" {
		t.Fatalf("refs = %+v", refs)
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
		{"tasks[0].env[0].from.ref", func(s *Site) { s.Tasks[0].Env[0].From.Ref = "" }},
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
	c := *s
	n := *s.Node
	n.Env = append([]EnvVar(nil), s.Node.Env...)
	for i, e := range n.Env {
		if e.From != nil {
			f := *e.From
			n.Env[i].From = &f
		}
	}
	c.Node = &n
	c.Tasks = append([]ScheduledTask(nil), s.Tasks...)
	for i := range c.Tasks {
		c.Tasks[i].Env = append([]EnvVar(nil), s.Tasks[i].Env...)
		for j, e := range c.Tasks[i].Env {
			if e.From != nil {
				f := *e.From
				c.Tasks[i].Env[j].From = &f
			}
		}
	}
	if s.Deploy.Git.TokenFrom != nil {
		f := *s.Deploy.Git.TokenFrom
		c.Deploy.Git.TokenFrom = &f
	}
	return &c
}
