package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
)

// SSO sign-in refusals. The login page shows a short message for each; the
// audit log has the details.
var (
	ErrSSOUnknownUser = errors.New("there is no NodeHoster user with this name")
	ErrSSONoRole      = errors.New("no role mapping rule matches this user")
	ErrSSOBadUsername = errors.New("the user name from the identity provider is not usable (2-64 characters)")
)

// SSOIdentity is who the identity provider says signed in.
type SSOIdentity struct {
	Username string
	// RoleValues are the values of the configured role claim (group IDs,
	// app role names); nil when roles are not mapped.
	RoleValues []string
}

// SSOResult describes what a sign-in changed, for the audit log.
type SSOResult struct {
	Created     bool
	RoleChanged string // "operator -> admin"
	KeptAdmin   bool   // the mapping would have removed the last administrator
}

// ssoFailureKey throttles SSO failures that happen before the user is
// known (a bad state or ID token). It cannot collide with a user name
// key: those have no "#".
func ssoFailureKey(ip string) string { return ip + "|#sso" }

// SSOThrottled reports whether an address has failed too often. The limit
// is the password sign-in's: five failures in fifteen minutes.
func (s *Service) SSOThrottled(ip string) bool { return s.throttled(ssoFailureKey(ip)) }

// SSOFailed counts a failure that happened before the user was known.
func (s *Service) SSOFailed(ip string) { s.fail(ssoFailureKey(ip)) }

// mappedRole is the role of the first rule matching one of the values, ""
// if none does.
func mappedRole(cfg *model.SSOSettings, values []string) model.Role {
	for _, rule := range cfg.RoleMap {
		for _, v := range values {
			if strings.EqualFold(v, rule.Value) {
				return rule.Role
			}
		}
	}
	return ""
}

// SSOLogin signs in a user the identity provider vouched for and creates a
// session, as Login does for a password. Local two-factor authentication
// is skipped: multi-factor is the provider's job.
//
// Provisioning follows cfg:
//   - an existing user (matched case-insensitively) signs in; a disabled
//     one is refused.
//   - an unknown user is refused, unless AutoCreate is on: then they are
//     created as an SSO user (no password) with the mapped or default role.
//   - with a role mapping, the role is worked out at every sign-in: the
//     first matching rule; else a site-scoped user keeps the grants an
//     administrator gave them; else the default role; else the sign-in is
//     refused. The mapping never removes the last enabled administrator.
func (s *Service) SSOLogin(ctx context.Context, cfg model.SSOSettings, id SSOIdentity, ip, ua string) (*store.UserRecord, string, SSOResult, error) {
	var res SSOResult
	key := ip + "|" + strings.ToLower(id.Username)
	if s.throttled(key) {
		return nil, "", res, ErrLocked
	}
	u, err := s.store.GetUserByName(ctx, id.Username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if !cfg.AutoCreate {
			s.fail(key)
			return nil, "", res, ErrSSOUnknownUser
		}
		role := model.Role("")
		if cfg.MapsRoles() {
			role = mappedRole(&cfg, id.RoleValues)
		}
		if role == "" {
			role = cfg.DefaultRole
		}
		if role == "" {
			s.fail(key)
			return nil, "", res, ErrSSONoRole
		}
		if n := len([]rune(id.Username)); n < 2 || n > 64 {
			return nil, "", res, ErrSSOBadUsername
		}
		u = &store.UserRecord{User: model.User{ID: uuid.NewString(), Username: id.Username, Role: role, SSO: true, CreatedAt: time.Now()}}
		if err := s.store.PutUser(ctx, u); err != nil {
			return nil, "", res, err
		}
		res.Created = true
	case err != nil:
		return nil, "", res, err
	default:
		if u.Disabled {
			return nil, "", res, ErrDisabled
		}
		if cfg.MapsRoles() {
			role := mappedRole(&cfg, id.RoleValues)
			if role == "" && u.Role != model.RoleSites {
				role = cfg.DefaultRole
			}
			if role == "" && u.Role != model.RoleSites {
				s.fail(key)
				return nil, "", res, ErrSSONoRole
			}
			if role != "" && role != u.Role {
				if u.Role == model.RoleAdmin && s.lastAdmin(ctx, u.ID) {
					res.KeptAdmin = true
				} else {
					res.RoleChanged = fmt.Sprintf("%s -> %s", u.Role, role)
					u.Role, u.Sites = role, nil
				}
			}
		}
	}
	s.mu.Lock()
	delete(s.failures, key)
	s.mu.Unlock()
	token, err := s.newSession(ctx, u, ip, ua)
	if err != nil {
		return nil, "", res, err
	}
	return u, token, res, nil
}

// lastAdmin reports whether id is the only enabled administrator.
func (s *Service) lastAdmin(ctx context.Context, id string) bool {
	list, err := s.store.ListUsers(ctx)
	if err != nil {
		return true // fail safe: keep the role
	}
	for _, u := range list {
		if u.ID != id && u.Role == model.RoleAdmin && !u.Disabled {
			return false
		}
	}
	return true
}
