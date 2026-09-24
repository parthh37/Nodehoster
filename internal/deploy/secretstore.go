package deploy

import (
	"errors"

	"github.com/parthh37/nodehoster/internal/model"
)

var errNoSecretStores = errors.New("values from secret stores cannot be read here")

// secretEnv reads the variables of vars that come from secret stores, for
// install and build commands.
func (d *Deployer) secretEnv(site *model.Site, vars []model.EnvVar) (map[string]string, error) {
	for _, v := range vars {
		if v.From == nil {
			continue
		}
		if d.opts.SecretEnv == nil {
			return nil, errNoSecretStores
		}
		return d.opts.SecretEnv(site, vars)
	}
	return nil, nil
}

// secretToken reads a git token from its secret store, at each deployment.
func (d *Deployer) secretToken(site *model.Site, ref model.SecretRef) (string, error) {
	if d.opts.SecretToken == nil {
		return "", errNoSecretStores
	}
	return d.opts.SecretToken(site, ref)
}
