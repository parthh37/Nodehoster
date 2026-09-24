package desktop

import (
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
)

func TestEnvText(t *testing.T) {
	for _, c := range []struct {
		e             model.EnvVar
		value, source string
	}{
		{model.EnvVar{Name: "A", Value: "1"}, "1", EnvPlain},
		{model.EnvVar{Name: "S", Value: secrets.Mask, Secret: true}, "••••••••", EnvSecret},
		{model.EnvVar{Name: "R", From: &model.SecretRef{Store: "vault", Ref: "app#DB"}}, "secretref:vault/app#DB", EnvStore},
	} {
		v, src := EnvText(c.e)
		if v != c.value || src != c.source {
			t.Errorf("%s: %q, %q", c.e.Name, v, src)
		}
	}
}

func TestEnvFromText(t *testing.T) {
	// A reference typed as text.
	e := model.EnvVar{Name: "DB", Value: "old"}
	if err := EnvFromText(&e, "DB", " secretref:vault/app/prod#DB_PASSWORD ", false); err != nil {
		t.Fatal(err)
	}
	if e.From == nil || *e.From != (model.SecretRef{Store: "vault", Ref: "app/prod#DB_PASSWORD"}) || e.Value != "" || e.Secret {
		t.Fatalf("reference = %+v", e)
	}
	// And back to a value.
	if err := EnvFromText(&e, "DB", "plain", false); err != nil || e.From != nil || e.Value != "plain" {
		t.Fatalf("plain = %+v, %v", e, err)
	}
	for _, bad := range []struct {
		text   string
		secret bool
		want   string
	}{
		{"secretref:vault/app#K", true, "not a NodeHoster secret"},
		{"secretref:vault", false, "secretref:<store>/<secret>"},
	} {
		e := model.EnvVar{Name: "X"}
		if err := EnvFromText(&e, "X", bad.text, bad.secret); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%q: %v", bad.text, err)
		}
	}

	// A stored secret: kept when left empty, refused when renamed or made plain.
	stored := model.EnvVar{Name: "S", Value: secrets.Mask, Secret: true}
	e = stored
	if err := EnvFromText(&e, "S", "", true); err != nil || e.Value != secrets.Mask {
		t.Errorf("kept = %+v, %v", e, err)
	}
	e = stored
	if err := EnvFromText(&e, "T", "", true); err == nil {
		t.Error("a stored secret moved to a new name")
	}
	e = stored
	if err := EnvFromText(&e, "S", "", false); err == nil {
		t.Error("a stored secret became plain without a value")
	}
	// A stored secret can become a reference: its value is dropped.
	e = stored
	if err := EnvFromText(&e, "S", "secretref:bw/3b3f5c1e-8f8a-4a3e-9c1e-2b7f0a6d4c10", false); err != nil || e.Value != "" || e.Secret || e.From == nil {
		t.Errorf("secret to reference = %+v, %v", e, err)
	}
}
