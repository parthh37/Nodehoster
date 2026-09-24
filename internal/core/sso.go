package core

import (
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
)

// prepareSSO validates the single sign-on settings and seals the client
// secret, keeping the stored one when the console sends the mask back.
func (c *Core) prepareSSO(in *model.SSOSettings, cur model.SSOSettings) error {
	in.Normalize()
	switch in.ClientSecret {
	case secrets.Mask:
		in.ClientSecret = cur.ClientSecret
	case "":
	default:
		sealed, err := c.Box.Seal(in.ClientSecret)
		if err != nil {
			return err
		}
		in.ClientSecret = sealed
	}
	return in.Validate()
}

// maskSSO hides the client secret and gives the lists a JSON value.
func maskSSO(s *model.SSOSettings) {
	if s.ClientSecret != "" {
		s.ClientSecret = secrets.Mask
	}
	if s.Scopes == nil {
		s.Scopes = []string{}
	}
	if s.RoleMap == nil {
		s.RoleMap = []model.SSORoleRule{}
	}
	if s.UsernameClaim == "" {
		s.UsernameClaim = model.DefaultUsernameClaim
	}
}
