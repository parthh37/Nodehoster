package procmgr

import (
	"errors"

	"github.com/parthh37/nodehoster/internal/model"
)

// setSecretEnv sets the variables of vars that come from secret stores,
// read when the process starts. key is where the process belongs (a
// site's production or one of its slots, model.SlotKey) and task is ""
// for an instance, whose values are recorded under key for rotation. A
// value that cannot be read fails the start: running without it would
// fail later and less clearly. Variables NodeHoster sets itself (PORT,
// NODEHOSTER_*, ...) are never taken from a store, even from a
// configuration saved before validation refused them.
func (m *Manager) setSecretEnv(e *env, site *model.Site, vars []model.EnvVar, key, task string) error {
	var from []model.EnvVar
	for _, v := range vars {
		if v.From != nil && !model.ReservedEnvName(v.Name) {
			from = append(from, v)
		}
	}
	if len(from) == 0 {
		return nil
	}
	if m.opts.ResolveEnv == nil {
		return errors.New("variables from secret stores cannot be read here")
	}
	vals, err := m.opts.ResolveEnv(site, from, key, task)
	if err != nil {
		return err
	}
	for _, v := range from {
		e.set(v.Name, vals[v.Name])
	}
	return nil
}
