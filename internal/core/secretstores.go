package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/secretstore"
)

// openSecretStores creates the secret store manager with the saved stores.
func (c *Core) openSecretStores() {
	c.Secrets = secretstore.New(secretstore.Options{
		Log:        c.Log,
		Unseal:     c.Box.Unseal,
		Event:      c.Bus.Emit,
		Watched:    c.watchedSecretRefs,
		Changed:    c.secretsChanged,
		References: c.allSecretRefs,
	})
	c.Secrets.Apply(c.settings.SecretStores)
}

// secretsCtx ends at shutdown, so that a start waiting on an unreachable
// store does not hold it up.
func (c *Core) secretsCtx() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

// allSecretRefs lists every reference of every site.
func (c *Core) allSecretRefs() []model.SecretRef {
	var out []model.SecretRef
	for _, s := range c.Sites() {
		for _, r := range s.SecretRefs() {
			out = append(out, r.Ref)
		}
	}
	return out
}

// watchedSecretRefs lists the variables from secret stores of the sites
// whose processes are running: a change of one recycles its site.
func (c *Core) watchedSecretRefs() map[string][]model.SecretRef {
	out := map[string][]model.SecretRef{}
	for _, s := range c.Sites() {
		if !s.RunsNode() || s.Node == nil || !c.Procs.Running(s.ID) {
			continue
		}
		var refs []model.SecretRef
		for _, e := range s.Node.Env {
			if e.From != nil {
				refs = append(refs, *e.From)
			}
		}
		if len(refs) > 0 {
			out[s.ID] = refs
		}
	}
	return out
}

// secretsChanged recycles a site whose secrets changed in their store:
// instances are replaced one at a time, so it keeps serving. It runs from
// the manager's goroutine, which c.wg tracks, hence c.wg.Go is safe here.
func (c *Core) secretsChanged(siteID string, refs []string) {
	s, err := c.Site(siteID)
	if err != nil {
		return
	}
	c.Bus.Info(events.SecretRotated, siteID, "%s: %s changed in the secret store; recycling", s.Name, strings.Join(refs, ", "))
	c.wg.Go(func() {
		if err := c.Procs.Recycle(siteID, "a secret changed"); err != nil {
			c.Log.Warn("recycle after a secret changed", "site", s.Name, "err", err)
		}
	})
}

// secretEnv reads the variables of vars that come from secret stores,
// by name. what names what is starting, for the failure event ("an
// instance", "task Nightly", "the deployment"); record marks the values as
// those the site's instances run with (see Options.Watched).
func (c *Core) secretEnv(site *model.Site, vars []model.EnvVar, what string, record bool) (map[string]string, error) {
	var refs []model.SecretRef
	for _, v := range vars {
		if v.From != nil {
			refs = append(refs, *v.From)
		}
	}
	if len(refs) == 0 {
		return map[string]string{}, nil
	}
	vals, err := c.Secrets.Resolve(c.secretsCtx(), refs, secretstore.ResolveOptions{Record: record, SiteID: site.ID})
	if err != nil {
		name := ""
		var re *secretstore.ResolveError
		if errors.As(err, &re) {
			for _, v := range vars {
				if v.From != nil && *v.From == re.Ref {
					name = v.Name
				}
			}
		}
		err = fmt.Errorf("variable %s from secret store: %w", name, err)
		if c.Secrets.AllowEvent("failed\x00" + site.ID + "\x00" + what) {
			c.Bus.Error(events.SecretFailed, site.ID, "%s: %s could not start: %v", site.Name, what, err)
		}
		return nil, err
	}
	out := make(map[string]string, len(refs))
	for _, v := range vars {
		if v.From != nil {
			out[v.Name] = vals[*v.From]
		}
	}
	return out, nil
}

// secretToken reads a deploy token from its secret store.
func (c *Core) secretToken(site *model.Site, ref model.SecretRef) (string, error) {
	vals, err := c.Secrets.Resolve(c.secretsCtx(), []model.SecretRef{ref}, secretstore.ResolveOptions{SiteID: site.ID})
	if err != nil {
		err = fmt.Errorf("git token from secret store: %w", err)
		if c.Secrets.AllowEvent("failed\x00" + site.ID + "\x00deploy") {
			c.Bus.Error(events.SecretFailed, site.ID, "%s: the deployment could not start: %v", site.Name, err)
		}
		return "", err
	}
	return vals[ref], nil
}

// ---- settings

// prepareSecretStores validates the stores and seals their credentials,
// keeping masked ones from the saved store with the same ID and type. A
// store that sites reference cannot be removed or renamed, and its type
// must still understand their references.
func (c *Core) prepareSecretStores(in *[]model.SecretStore, cur []model.SecretStore) error {
	if *in == nil {
		*in = []model.SecretStore{}
	}
	list := *in
	seen := map[string]int{}
	for i := range list {
		s := &list[i]
		*s = secretstore.Clone(*s)
		if s.ID == "" {
			s.ID = uuid.NewString()
		}
		s.ApplyDefaults()
		f := fmt.Sprintf("secretStores[%d]", i)
		mergeStoreSecrets(s, cur)
		// Only the credentials of the chosen authentication are kept.
		if v := s.Vault; v != nil {
			if v.Auth == model.VaultAuthAppRole {
				v.Token = ""
			} else {
				v.RoleID, v.SecretID = "", ""
			}
		}
		if s.Type != model.SecretStoreVault {
			s.Vault = nil
		}
		if s.Type != model.SecretStoreInfisical {
			s.Infisical = nil
		}
		if s.Type != model.SecretStoreBitwarden {
			s.Bitwarden = nil
		}
		if err := s.Validate(f); err != nil {
			return err
		}
		if j, dup := seen[strings.ToLower(s.Name)]; dup {
			return &model.ValidationError{Field: f + ".name", Message: fmt.Sprintf("secret store %q is already defined (secretStores[%d])", s.Name, j)}
		}
		seen[strings.ToLower(s.Name)] = i
		if b := s.Bitwarden; b != nil && !secrets.IsSealed(b.AccessToken) {
			if err := secretstore.ValidateAccessToken(b.AccessToken); err != nil {
				return &model.ValidationError{Field: f + ".bitwarden.accessToken", Message: fmt.Sprintf("secret store %q: %v", s.Name, err)}
			}
		}
	}
	byName := map[string]model.SecretStore{}
	for _, s := range list {
		byName[s.Name] = s
	}
	for _, site := range c.Sites() {
		for _, r := range site.SecretRefs() {
			s, ok := byName[r.Ref.Store]
			if !ok {
				return &model.ValidationError{Field: "secretStores", Message: fmt.Sprintf("site %q uses secret store %q (%s): change it before removing or renaming the store", site.Name, r.Ref.Store, refPlace(r))}
			}
			if err := model.ValidateSecretRef(s.Type, r.Ref.Ref); err != nil {
				return &model.ValidationError{Field: "secretStores", Message: fmt.Sprintf("site %q uses %s (%s), which a %s store cannot read: %v", site.Name, r.Ref, refPlace(r), s.Type, err)}
			}
		}
	}
	for i := range list {
		for _, p := range secretstore.Credentials(&list[i]) {
			v, err := c.Box.Seal(*p)
			if err != nil {
				return err
			}
			*p = v
		}
	}
	return nil
}

// refPlace names where a reference is, for an administrator.
func refPlace(r model.SiteSecretRef) string {
	switch {
	case r.Task != "":
		return fmt.Sprintf("variable %s of task %s", r.Variable, r.Task)
	case r.Variable != "":
		return "variable " + r.Variable
	}
	return "git token"
}

// mergeStoreSecrets replaces masked credentials of s with the stored ones
// of the store with the same ID and type (never the mask itself).
func mergeStoreSecrets(s *model.SecretStore, stored []model.SecretStore) {
	var olds []*string
	for _, o := range stored {
		if s.ID != "" && o.ID == s.ID && o.Type == s.Type {
			o = secretstore.Clone(o)
			olds = secretstore.Credentials(&o)
		}
	}
	for i, p := range secretstore.Credentials(s) {
		if *p != secrets.Mask {
			continue
		}
		*p = ""
		if i < len(olds) {
			*p = *olds[i]
		}
	}
}

// maskSecretStores hides the stores' credentials for the API.
func maskSecretStores(list *[]model.SecretStore) {
	out := make([]model.SecretStore, len(*list))
	for i, s := range *list {
		s = secretstore.Clone(s)
		for _, p := range secretstore.Credentials(&s) {
			if *p != "" {
				*p = secrets.Mask
			}
		}
		out[i] = s
	}
	*list = out
}

// checkSiteSecretRefs verifies that a site's references name existing
// stores in their syntax.
func (c *Core) checkSiteSecretRefs(in *model.Site) error {
	refs := in.SecretRefs()
	if len(refs) == 0 {
		return nil
	}
	stores := map[string]model.SecretStore{}
	for _, s := range c.Settings().SecretStores {
		stores[s.Name] = s
	}
	for _, r := range refs {
		f := r.Field
		if !strings.HasSuffix(f, ".tokenFrom") {
			f += ".from"
		}
		s, ok := stores[r.Ref.Store]
		if !ok {
			return &model.ValidationError{Field: f + ".store", Message: fmt.Sprintf("secret store %q does not exist (Settings → Secret stores)", r.Ref.Store)}
		}
		if err := model.ValidateSecretRef(s.Type, r.Ref.Ref); err != nil {
			return &model.ValidationError{Field: f + ".ref", Message: err.Error()}
		}
	}
	return nil
}

// ---- API

// SecretStoreStatus describes every store.
func (c *Core) SecretStoreStatus() []model.SecretStoreStatus {
	return c.Secrets.Status()
}

// TestSecretStore signs in to a store as edited (masked credentials are
// the saved store's with the same ID) and reads in.Ref if given.
func (c *Core) TestSecretStore(ctx context.Context, in model.SecretStoreTest) (model.SecretTestResult, error) {
	s := secretstore.Clone(in.Store)
	s.ApplyDefaults()
	mergeStoreSecrets(&s, c.Settings().SecretStores)
	if s.Name == "" {
		s.Name = "test"
	}
	if err := s.Validate("store"); err != nil {
		return model.SecretTestResult{}, err
	}
	if b := s.Bitwarden; b != nil && !secrets.IsSealed(b.AccessToken) {
		if err := secretstore.ValidateAccessToken(b.AccessToken); err != nil {
			return model.SecretTestResult{}, &model.ValidationError{Field: "store.bitwarden.accessToken", Message: err.Error()}
		}
	}
	return c.Secrets.Test(ctx, s, strings.TrimSpace(in.Ref)), nil
}

// ResolveSecretRef reads a reference from its saved store now, to show
// that it resolves. The value is never returned.
func (c *Core) ResolveSecretRef(ctx context.Context, in model.SecretResolveRequest) (model.SecretTestResult, error) {
	ref := model.SecretRef{Store: strings.TrimSpace(in.Store), Ref: strings.TrimSpace(in.Ref)}
	var st *model.SecretStore
	for _, s := range c.Settings().SecretStores {
		if s.Name == ref.Store {
			st = &s
		}
	}
	if st == nil {
		return model.SecretTestResult{}, &model.ValidationError{Field: "store", Message: fmt.Sprintf("secret store %q does not exist", ref.Store)}
	}
	if err := model.ValidateSecretRef(st.Type, ref.Ref); err != nil {
		return model.SecretTestResult{}, &model.ValidationError{Field: "ref", Message: err.Error()}
	}
	if err := c.Secrets.Check(ctx, []model.SecretRef{ref})[0]; err != nil {
		return model.SecretTestResult{Error: err.Error()}, nil
	}
	return model.SecretTestResult{OK: true, Detail: fmt.Sprintf("%s resolves", ref)}, nil
}

// CheckSiteSecrets reads every reference of a site from its store now.
func (c *Core) CheckSiteSecrets(ctx context.Context, id string) ([]model.SecretRefCheck, error) {
	s, err := c.Site(id)
	if err != nil {
		return nil, err
	}
	refs := s.SecretRefs()
	out := make([]model.SecretRefCheck, len(refs))
	list := make([]model.SecretRef, len(refs))
	for i, r := range refs {
		list[i] = r.Ref
	}
	errs := c.Secrets.Check(ctx, list)
	for i, r := range refs {
		out[i] = model.SecretRefCheck{SiteSecretRef: r, OK: errs[i] == nil}
		if errs[i] != nil {
			out[i].Error = errs[i].Error()
		}
	}
	return out, nil
}

// procSecretEnv is the process manager's ResolveEnv: task "" is an
// instance of the site, whose values are recorded for rotation.
func (c *Core) procSecretEnv(site *model.Site, vars []model.EnvVar, task string) (map[string]string, error) {
	if task == "" {
		return c.secretEnv(site, vars, "an instance", true)
	}
	return c.secretEnv(site, vars, "task "+task, false)
}
