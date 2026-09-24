package procmgr

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

// Variables from secret stores are read at every instance start, set in
// the process's environment over nothing else, and a value that cannot be
// read fails the start with the reason in the site's log.
func TestSecretEnvAtInstanceStart(t *testing.T) {
	m, app := newRecoveryManager(t)
	os.WriteFile(filepath.Join(app, "worker.js"), []byte(`console.log('secret=' + process.env.DB_PASSWORD + ' plain=' + process.env.PLAIN); setInterval(() => {}, 1000);`), 0o644)
	var mu sync.Mutex
	var calls []string
	fail := false
	m.opts.ResolveEnv = func(site *model.Site, vars []model.EnvVar, task string) (map[string]string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, site.ID+"/"+task)
		if fail {
			return nil, errors.New("variable DB_PASSWORD from secret store: store \"vault\" could not be read")
		}
		out := map[string]string{}
		for _, v := range vars {
			if v.From != nil {
				out[v.Name] = "from-" + v.From.Ref
			}
		}
		return out, nil
	}
	site := &model.Site{ID: "w1", Name: "queue", Type: model.SiteWorker,
		Node: &model.NodeConfig{AppRoot: app, Script: "worker.js", Instances: 1, MaxRestarts: 1, RestartWindowSec: 60, RapidFailAction: "stop", Env: []model.EnvVar{
			{Name: "PLAIN", Value: "p"},
			{Name: "DB_PASSWORD", From: &model.SecretRef{Store: "vault", Ref: "app#DB"}},
		}}}
	site.ApplyDefaults()
	m.Apply(site)
	if err := m.Start("w1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "output", func() bool { return strings.Contains(logText(m, "w1"), "secret=from-app#DB plain=p") })
	mu.Lock()
	if len(calls) != 1 || calls[0] != "w1/" {
		t.Errorf("calls = %v", calls)
	}
	fail = true
	mu.Unlock()

	// A recycle that cannot read the value keeps the running instance.
	m.Recycle("w1", "test")
	waitFor(t, "failure logged", func() bool { return strings.Contains(logText(m, "w1"), `store "vault" could not be read`) })
	st, _ := m.Status("w1")
	if st.State != model.StateRunning {
		t.Errorf("state after a failed recycle = %s", st.State)
	}
	m.Stop("w1")
}
