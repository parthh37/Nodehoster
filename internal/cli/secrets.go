package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

func init() {
	register(
		&Command{Name: "secrets list", Summary: "List the secret stores and their state",
			Setup: func(*flag.FlagSet) Runner { return secretsList }},
		&Command{Name: "secrets test", Args: "<store>", MinArgs: 1, MaxArgs: 1,
			Summary: "Sign in to a secret store (and read a reference with --ref) without showing any value",
			Setup:   secretsTestCmd},
		&Command{Name: "secrets check", Args: "<site>", MinArgs: 1, MaxArgs: 1,
			Summary: "Read every secret store reference of a site now (exit 1 if one fails)",
			Setup:   func(*flag.FlagSet) Runner { return secretsCheck }},
	)
}

func secretsList(e *Env, _ []string) error {
	var list []model.SecretStoreStatus
	raw, err := e.get("/api/secret-stores", &list)
	if err != nil || e.JSON {
		if err == nil {
			err = e.printJSON(raw)
		}
		return err
	}
	if len(list) == 0 {
		e.printf("No secret stores are configured (web console: Settings → Secret stores).\n")
		return nil
	}
	rows := make([][]string, 0, len(list))
	for _, s := range list {
		ok, failed := "-", "-"
		if s.LastSuccess != nil {
			ok = localTime(*s.LastSuccess)
		}
		if s.LastErrorAt != nil && (s.LastSuccess == nil || s.LastErrorAt.After(*s.LastSuccess)) {
			failed = localTime(*s.LastErrorAt) + ": " + truncate(s.LastError, 60)
		}
		rows = append(rows, []string{s.Name, s.Type, fmt.Sprint(s.References), fmt.Sprint(s.Cached), ok, failed})
	}
	e.table([]string{"NAME", "TYPE", "REFERENCES", "IN MEMORY", "LAST READ", "LAST ERROR"}, rows)
	return nil
}

func secretsTestCmd(fs *flag.FlagSet) Runner {
	ref := fs.String("ref", "", "a reference to read with the store, e.g. app/prod#DB_PASSWORD (the value is not shown)")
	return func(e *Env, args []string) error {
		var settings model.Settings
		if _, err := e.get("/api/settings", &settings); err != nil {
			return err
		}
		var store *model.SecretStore
		var names []string
		for i, s := range settings.SecretStores {
			names = append(names, s.Name)
			if strings.EqualFold(s.Name, args[0]) {
				store = &settings.SecretStores[i]
			}
		}
		if store == nil {
			return fmt.Errorf("no secret store is named %q (stores: %s)", args[0], orDash(strings.Join(names, ", ")))
		}
		var res model.SecretTestResult
		var raw json.RawMessage
		if err := e.Client.Post(e.Ctx, "/api/secret-stores/test", model.SecretStoreTest{Store: *store, Ref: *ref}, &raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return err
		}
		if e.JSON {
			if err := e.printJSON(raw); err != nil {
				return err
			}
		} else if res.OK {
			e.printf("%s works: %s.\n", store.Name, res.Detail)
		}
		if !res.OK {
			if res.Detail != "" {
				return fmt.Errorf("%s: %s; then: %s", store.Name, res.Detail, res.Error)
			}
			return fmt.Errorf("%s: %s", store.Name, res.Error)
		}
		return nil
	}
}

func secretsCheck(e *Env, args []string) error {
	s, err := e.resolveSite(args[0])
	if err != nil {
		return err
	}
	var list []model.SecretRefCheck
	var raw json.RawMessage
	if err := e.Client.Post(e.Ctx, sitePath(s)+"/secrets/check", nil, &raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	failed := 0
	for _, c := range list {
		if !c.OK {
			failed++
		}
	}
	if e.JSON {
		if err := e.printJSON(raw); err != nil {
			return err
		}
	} else if len(list) == 0 {
		e.printf("%s takes nothing from secret stores.\n", s.Name)
	} else {
		rows := make([][]string, 0, len(list))
		for _, c := range list {
			what := c.Variable
			switch {
			case c.Task != "":
				what += " (task " + c.Task + ")"
			case c.Slot != "":
				what += " (slot " + c.Slot + ")"
			case c.Preview:
				what += " (previews)"
			case what == "":
				what = "git token"
			}
			result := "ok"
			if !c.OK {
				result = "FAILED: " + c.Error
			}
			rows = append(rows, []string{what, c.Ref.String(), result})
		}
		e.table([]string{"VARIABLE", "REFERENCE", "RESULT"}, rows)
	}
	if failed > 0 {
		return errors.New(plural(failed, "reference") + " of " + s.Name + " cannot be read")
	}
	return nil
}
