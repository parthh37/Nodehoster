package procmgr

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

// Variables from secret stores are read at every instance start, set in
// the process's environment over nothing else, and a value that cannot be
// read fails the start with the reason in the site's log. A slot's
// instances are resolved under the slot's key, and a variable NodeHoster
// sets itself is never taken from a store.
func TestSecretEnvAtInstanceStart(t *testing.T) {
	m, app := newRecoveryManager(t)
	os.WriteFile(filepath.Join(app, "worker.js"), []byte(`console.log('secret=' + process.env.DB_PASSWORD + ' plain=' + process.env.PLAIN + ' site=' + process.env.NODEHOSTER_SITE); setInterval(() => {}, 1000);`), 0o644)
	var mu sync.Mutex
	var calls []string
	fail := false
	m.opts.ResolveEnv = func(site *model.Site, vars []model.EnvVar, key, task string) (map[string]string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, site.ID+" "+key+"/"+task)
		if fail {
			return nil, errors.New("variable DB_PASSWORD from secret store: store \"vault\" could not be read")
		}
		out := map[string]string{}
		for _, v := range vars {
			if model.ReservedEnvName(v.Name) {
				t.Errorf("%s was asked of the store", v.Name)
			}
			if v.From != nil {
				out[v.Name] = "from-" + v.From.Ref
			}
		}
		return out, nil
	}
	// A configuration saved before validation refused NODEHOSTER_SITE
	// from a store: the store's value does not replace NodeHoster's.
	site := &model.Site{ID: "w1", Name: "queue", Type: model.SiteWorker,
		Node: &model.NodeConfig{AppRoot: app, Script: "worker.js", Instances: 1, MaxRestarts: 1, RestartWindowSec: 60, RapidFailAction: "stop", Env: []model.EnvVar{
			{Name: "PLAIN", Value: "p"},
			{Name: "DB_PASSWORD", From: &model.SecretRef{Store: "vault", Ref: "app#DB"}},
			{Name: "NODEHOSTER_SITE", From: &model.SecretRef{Store: "vault", Ref: "app#SITE"}},
		}},
		Slots: []model.DeploymentSlot{{Name: "staging", Env: []model.EnvVar{{Name: "DB_PASSWORD", From: &model.SecretRef{Store: "vault", Ref: "staging#DB"}}}}},
	}
	site.ApplyDefaults()
	m.Apply(site)
	if err := m.Start("w1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "output", func() bool { return strings.Contains(logText(m, "w1"), "secret=from-app#DB plain=p site=queue") })
	mu.Lock()
	if len(calls) != 1 || calls[0] != "w1 w1/" {
		t.Errorf("calls = %v", calls)
	}
	mu.Unlock()

	if err := m.StartSlot("w1", "staging"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "slot output", func() bool { return strings.Contains(logText(m, "w1"), "secret=from-staging#DB plain=p site=queue") })
	mu.Lock()
	if !slices.Contains(calls, "w1 w1@staging/") {
		t.Errorf("the slot's values were not resolved under its key: %v", calls)
	}
	fail = true
	mu.Unlock()
	m.StopSlot("w1", "staging")

	// A recycle that cannot read the value keeps the running instance.
	m.Recycle("w1", "test")
	waitFor(t, "failure logged", func() bool { return strings.Contains(logText(m, "w1"), `store "vault" could not be read`) })
	st, _ := m.Status("w1")
	if st.State != model.StateRunning {
		t.Errorf("state after a failed recycle = %s", st.State)
	}
	m.Stop("w1")
}
