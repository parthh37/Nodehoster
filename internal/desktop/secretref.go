package desktop

import (
	"fmt"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
)

// Where an environment variable's value comes from, for the Manager's
// "Source" column.
const (
	EnvPlain  = "Plain"
	EnvSecret = "Secret"
	EnvStore  = "Secret store"
)

// EnvText is how the Manager lists a variable: its value (a secret
// hidden, a secret store reference as secretref:<store>/<ref>) and where
// the value comes from.
func EnvText(e model.EnvVar) (value, source string) {
	switch {
	case e.From != nil:
		return e.From.String(), EnvStore
	case e.Secret:
		return "••••••••", EnvSecret
	}
	return e.Value, EnvPlain
}

// EnvFromText applies what was typed in the Manager's variable dialog to
// e: a value, a secret (secret set; an empty value keeps a stored one) or
// "secretref:<store>/<ref>", which makes the variable a reference. The
// server validates the store and the reference.
func EnvFromText(e *model.EnvVar, name, text string, secret bool) error {
	stored := e.Secret && e.Value == secrets.Mask
	if ref, ok := model.ParseSecretRefText(text); ok {
		if secret {
			return fmt.Errorf("a variable from a secret store is not a NodeHoster secret: clear “Secret” (nothing of it is stored here)")
		}
		if ref.Store == "" || ref.Ref == "" {
			return fmt.Errorf("write the reference as secretref:<store>/<secret>, e.g. secretref:vault/app/prod#DB_PASSWORD")
		}
		e.Name, e.Value, e.Secret, e.From = name, "", false, &ref
		return nil
	}
	if stored && text == "" && name != e.Name {
		// The server finds a stored secret by its name.
		return fmt.Errorf("enter the value again: a secret's stored value cannot move to a new name")
	}
	if stored && text == "" && !secret {
		return fmt.Errorf("enter the value: a secret's stored value is never shown, so it cannot become a plain variable as it is")
	}
	e.Name, e.Secret, e.From = name, secret, nil
	if stored && text == "" && secret {
		e.Value = secrets.Mask // keep the stored secret
	} else {
		e.Value = text
	}
	return nil
}
