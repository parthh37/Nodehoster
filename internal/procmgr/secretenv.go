package procmgr

import (
	"errors"

	"github.com/parthh37/nodehoster/internal/model"
)

// setSecretEnv sets the variables of vars that come from secret stores,
// read when the process starts (task "" is an instance of the site). A
// value that cannot be read fails the start: running without it would
// fail later and less clearly.
func (m *Manager) setSecretEnv(e *env, site *model.Site, vars []model.EnvVar, task string) error {
	has := false
	for _, v := range vars {
		if v.From != nil {
			has = true
			break
		}
	}
	if !has {
		return nil
	}
	if m.opts.ResolveEnv == nil {
		return errors.New("variables from secret stores cannot be read here")
	}
	vals, err := m.opts.ResolveEnv(site, vars, task)
	if err != nil {
		return err
	}
	for _, v := range vars {
		if v.From != nil {
			e.set(v.Name, vals[v.Name])
		}
	}
	return nil
}
