package model

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Secret store types.
const (
	SecretStoreVault     = "vault"     // HashiCorp Vault or OpenBao, KV version 1 or 2
	SecretStoreInfisical = "infisical" // Infisical Cloud or self-hosted
	SecretStoreBitwarden = "bitwarden" // Bitwarden Secrets Manager, cloud or self-hosted
)

// Vault authentication methods.
const (
	VaultAuthToken   = "token"
	VaultAuthAppRole = "approle"
)

// Bitwarden cloud regions. A self-hosted server is given by its URL.
const (
	BitwardenUS = "us"
	BitwardenEU = "eu"
)

// SecretRefPrefix starts the text form of a secret reference,
// "secretref:<store>/<ref>", which NodeHoster Manager and the command line
// accept where a variable's value is typed.
const SecretRefPrefix = "secretref:"

// SecretStore is an external secret manager that environment variables and
// deploy tokens can take their values from, the way an Azure App Service
// setting can be a Key Vault reference. Values are read when a process
// starts (every instance start and recycle), a task runs or a deployment
// builds, and are never stored: only the reference is. Only the section
// matching Type is used; its credentials are secrets.
type SecretStore struct {
	ID string `json:"id"`
	// Name is what references use ("vault" in vault/app#DB_PASSWORD). A store
	// that references point to cannot be renamed or removed.
	Name string `json:"name"`
	Type string `json:"type"` // vault | infisical | bitwarden
	// URL is the server: Vault's address (https://vault.example.com:8200);
	// for Infisical and Bitwarden "" means their cloud, else the base URL
	// of a self-hosted server.
	URL string `json:"url"`
	// CACert, PEM, is trusted for this store in addition to the Windows
	// certificate store: a self-hosted server with a private CA.
	// Certificate verification cannot be turned off.
	CACert string `json:"caCert,omitempty"`
	// CacheTTLSec is how long a value read from the store is reused before
	// it is read again (recycles and instance restarts within it do not
	// ask the store). When the store cannot be reached, the last value read
	// is used whatever its age.
	CacheTTLSec int `json:"cacheTtlSec"`
	// WatchIntervalSec, when set, checks the secrets referenced by running
	// sites this often and recycles a site (without downtime) when one of
	// its variables changed in the store. 0 = off.
	WatchIntervalSec int `json:"watchIntervalSec"`

	Vault     *VaultStore     `json:"vault,omitempty"`
	Infisical *InfisicalStore `json:"infisical,omitempty"`
	Bitwarden *BitwardenStore `json:"bitwarden,omitempty"`
}

// VaultStore reads a KV secrets engine of HashiCorp Vault or OpenBao.
// References are "<path>#<key>" relative to Mount: "app/prod#DB_PASSWORD"
// reads key DB_PASSWORD of secret app/prod.
type VaultStore struct {
	Auth  string `json:"auth"`            // token | approle
	Token string `json:"token,omitempty"` // secret; auth token
	// AppRole: NodeHoster signs in with the role and secret IDs, renews
	// the token it gets and signs in again when it cannot.
	RoleID    string `json:"roleId,omitempty"`
	SecretID  string `json:"secretId,omitempty"`  // secret
	AuthMount string `json:"authMount,omitempty"` // where AppRole is enabled, default "approle"
	Namespace string `json:"namespace,omitempty"` // Vault Enterprise / OpenBao namespace
	Mount     string `json:"mount"`               // the KV engine's path, default "secret"
	KVVersion int    `json:"kvVersion"`           // 1 or 2 (default)
}

// InfisicalStore reads one environment of an Infisical project with a
// machine identity's Universal Auth credentials. References are a secret
// name, optionally in a folder: "DB_PASSWORD" or "/backend/DB_PASSWORD".
type InfisicalStore struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"` // secret
	ProjectID    string `json:"projectId"`
	Environment  string `json:"environment"` // the environment's slug: dev, staging, prod…
}

// BitwardenStore reads Bitwarden Secrets Manager with a machine account's
// access token. References are secret IDs (UUIDs). Vaultwarden does not
// implement Secrets Manager.
type BitwardenStore struct {
	AccessToken string `json:"accessToken"` // secret
	// Region picks the cloud when the store has no URL: us or eu.
	Region string `json:"region,omitempty"`
	// APIURL and IdentityURL override the URLs derived from the region or
	// the base URL (<url>/api and <url>/identity), for unusual setups.
	APIURL      string `json:"apiUrl,omitempty"`
	IdentityURL string `json:"identityUrl,omitempty"`
}

// SecretRef takes a value from a secret store instead of the
// configuration: Store is a store's name, Ref the secret in it (see the
// store types for the syntax).
type SecretRef struct {
	Store string `json:"store"`
	Ref   string `json:"ref"`
}

// String is the reference's text form, secretref:<store>/<ref>.
func (r SecretRef) String() string { return SecretRefPrefix + r.Store + "/" + r.Ref }

// ParseSecretRefText reads the text form of a reference. ok is false for
// any other text (a plain value).
func ParseSecretRefText(s string) (ref SecretRef, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(s), SecretRefPrefix)
	if !found {
		return SecretRef{}, false
	}
	store, r, _ := strings.Cut(rest, "/")
	return SecretRef{Store: strings.TrimSpace(store), Ref: strings.TrimSpace(r)}, true
}

var (
	secretStoreNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	uuidRe            = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	infisicalNameRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// Secret store limits.
const (
	DefaultSecretCacheTTLSec = 300
	MinSecretCacheTTLSec     = 10
	MinSecretWatchSec        = 60
)

// ApplyDefaults fills a store's zero values.
func (s *SecretStore) ApplyDefaults() {
	s.Name = strings.TrimSpace(s.Name)
	s.Type = strings.ToLower(strings.TrimSpace(s.Type))
	s.URL = strings.TrimRight(strings.TrimSpace(s.URL), "/")
	if s.CacheTTLSec == 0 {
		s.CacheTTLSec = DefaultSecretCacheTTLSec
	}
	switch s.Type {
	case SecretStoreVault:
		if s.Vault == nil {
			s.Vault = &VaultStore{}
		}
		v := s.Vault
		if v.Auth == "" {
			v.Auth = VaultAuthToken
		}
		if v.AuthMount == "" {
			v.AuthMount = "approle"
		}
		v.Mount = strings.Trim(strings.TrimSpace(v.Mount), "/")
		if v.Mount == "" {
			v.Mount = "secret"
		}
		v.AuthMount = strings.Trim(strings.TrimSpace(v.AuthMount), "/")
		v.Namespace = strings.Trim(strings.TrimSpace(v.Namespace), "/")
		if v.KVVersion == 0 {
			v.KVVersion = 2
		}
	case SecretStoreInfisical:
		if s.Infisical == nil {
			s.Infisical = &InfisicalStore{}
		}
		i := s.Infisical
		i.ClientID = strings.TrimSpace(i.ClientID)
		i.ProjectID = strings.TrimSpace(i.ProjectID)
		i.Environment = strings.TrimSpace(i.Environment)
	case SecretStoreBitwarden:
		if s.Bitwarden == nil {
			s.Bitwarden = &BitwardenStore{}
		}
		b := s.Bitwarden
		b.Region = strings.ToLower(strings.TrimSpace(b.Region))
		if b.Region == "" && s.URL == "" {
			b.Region = BitwardenUS
		}
		b.APIURL = strings.TrimRight(strings.TrimSpace(b.APIURL), "/")
		b.IdentityURL = strings.TrimRight(strings.TrimSpace(b.IdentityURL), "/")
	}
}

// Validate checks a store after ApplyDefaults; field is its path, such as
// "secretStores[0]". Credentials are only checked for presence: whether
// they work is what the connection test is for.
func (s *SecretStore) Validate(field string) error {
	if !secretStoreNameRe.MatchString(s.Name) {
		return verr(field+".name", "1-64 characters: letters, digits, '.', '_' or '-'")
	}
	named := fmt.Sprintf("secret store %q: ", s.Name)
	if s.URL != "" {
		if err := validURL(s.URL); err != nil {
			return verr(field+".url", "%s%v", named, err)
		}
	}
	if s.CACert != "" {
		if err := checkCAPEM(s.CACert); err != nil {
			return verr(field+".caCert", "%s%v", named, err)
		}
	}
	if s.CacheTTLSec < MinSecretCacheTTLSec || s.CacheTTLSec > 86400 {
		return verr(field+".cacheTtlSec", "%smust be between %d and 86400 seconds", named, MinSecretCacheTTLSec)
	}
	if s.WatchIntervalSec != 0 && (s.WatchIntervalSec < MinSecretWatchSec || s.WatchIntervalSec > 86400) {
		return verr(field+".watchIntervalSec", "%smust be 0 (off) or between %d and 86400 seconds", named, MinSecretWatchSec)
	}
	switch s.Type {
	case SecretStoreVault:
		v := s.Vault
		f := field + ".vault"
		if s.URL == "" {
			return verr(field+".url", "%senter the Vault or OpenBao address, e.g. https://vault.example.com:8200", named)
		}
		switch v.Auth {
		case VaultAuthToken:
			if v.Token == "" {
				return verr(f+".token", "%senter the token", named)
			}
		case VaultAuthAppRole:
			if strings.TrimSpace(v.RoleID) == "" {
				return verr(f+".roleId", "%senter the role ID", named)
			}
			if v.SecretID == "" {
				return verr(f+".secretId", "%senter the secret ID", named)
			}
			if !vaultPathOK(v.AuthMount) {
				return verr(f+".authMount", "%snot a valid mount path", named)
			}
		default:
			return verr(f+".auth", "%smust be token or approle", named)
		}
		if !vaultPathOK(v.Mount) {
			return verr(f+".mount", "%snot a valid mount path", named)
		}
		if v.Namespace != "" && !vaultPathOK(v.Namespace) {
			return verr(f+".namespace", "%snot a valid namespace", named)
		}
		if v.KVVersion != 1 && v.KVVersion != 2 {
			return verr(f+".kvVersion", "%smust be 1 or 2", named)
		}
	case SecretStoreInfisical:
		i := s.Infisical
		f := field + ".infisical"
		if i.ClientID == "" {
			return verr(f+".clientId", "%senter the machine identity's client ID", named)
		}
		if i.ClientSecret == "" {
			return verr(f+".clientSecret", "%senter the client secret", named)
		}
		if i.ProjectID == "" {
			return verr(f+".projectId", "%senter the project ID", named)
		}
		if i.Environment == "" || strings.ContainsAny(i.Environment, "/?#& ") {
			return verr(f+".environment", "%senter the environment's slug (dev, staging, prod…)", named)
		}
	case SecretStoreBitwarden:
		b := s.Bitwarden
		f := field + ".bitwarden"
		if b.AccessToken == "" {
			return verr(f+".accessToken", "%senter the machine account's access token", named)
		}
		if s.URL == "" && b.Region != BitwardenUS && b.Region != BitwardenEU && (b.APIURL == "" || b.IdentityURL == "") {
			return verr(f+".region", "%schoose the US or EU cloud, or enter the URL of a self-hosted server", named)
		}
		for _, u := range []struct{ f, v string }{{"apiUrl", b.APIURL}, {"identityUrl", b.IdentityURL}} {
			if u.v != "" {
				if err := validURL(u.v); err != nil {
					return verr(f+"."+u.f, "%s%v", named, err)
				}
			}
		}
	default:
		return verr(field+".type", "must be vault, infisical or bitwarden")
	}
	return nil
}

// vaultPathOK accepts a mount path or namespace: segments of letters,
// digits, '-', '_' and '.', without "." or ".." segments.
func vaultPathOK(p string) bool {
	if p == "" {
		return false
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." || !infisicalNameRe.MatchString(seg) {
			return false
		}
	}
	return true
}

func checkCAPEM(s string) error {
	rest := []byte(s)
	n := 0
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(b.Bytes); err != nil {
			return fmt.Errorf("a certificate in the CA PEM cannot be read: %v", err)
		}
		n++
	}
	if n == 0 {
		return fmt.Errorf("the CA must be one or more PEM certificates (-----BEGIN CERTIFICATE-----)")
	}
	return nil
}

// ValidateSecretRef checks a reference's syntax for a store type.
func ValidateSecretRef(storeType, ref string) error {
	if ref == "" {
		return fmt.Errorf("enter the secret to use")
	}
	if strings.IndexFunc(ref, unicode.IsControl) >= 0 || len(ref) > 512 {
		return fmt.Errorf("not a valid reference")
	}
	switch storeType {
	case SecretStoreVault:
		path, key, ok := strings.Cut(ref, "#")
		if !ok || key == "" {
			return fmt.Errorf("use <path>#<key>, e.g. app/prod#DB_PASSWORD")
		}
		if strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") || path == "" {
			return fmt.Errorf("the path is relative to the store's mount, without leading or trailing '/': app/prod#DB_PASSWORD")
		}
		for seg := range strings.SplitSeq(path, "/") {
			if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "?#%\\") {
				return fmt.Errorf("%q is not a valid secret path", path)
			}
		}
	case SecretStoreInfisical:
		dir, name := "/", ref
		if i := strings.LastIndex(ref, "/"); i >= 0 {
			dir, name = ref[:i+1], ref[i+1:]
			if !strings.HasPrefix(dir, "/") {
				return fmt.Errorf("a folder starts with '/': /backend/DB_PASSWORD")
			}
		}
		if !infisicalNameRe.MatchString(name) {
			return fmt.Errorf("use the secret's name, optionally in a folder: DB_PASSWORD or /backend/DB_PASSWORD")
		}
		for seg := range strings.SplitSeq(strings.Trim(dir, "/"), "/") {
			if seg == "." || seg == ".." || strings.ContainsAny(seg, "?#%\\") {
				return fmt.Errorf("%q is not a valid folder", dir)
			}
		}
	case SecretStoreBitwarden:
		if !uuidRe.MatchString(ref) {
			return fmt.Errorf("use the secret's ID, a UUID such as 3b3f5c1e-8f8a-4a3e-9c1e-2b7f0a6d4c10")
		}
	default:
		return fmt.Errorf("unknown secret store type %q", storeType)
	}
	return nil
}

// validateRef checks the parts of a reference that do not depend on the
// store (which the site's validation cannot see).
func (r *SecretRef) validate(field string) error {
	r.Store, r.Ref = strings.TrimSpace(r.Store), strings.TrimSpace(r.Ref)
	if r.Store == "" {
		return verr(field+".store", "choose a secret store")
	}
	if r.Ref == "" {
		return verr(field+".ref", "enter the secret to use")
	}
	if strings.IndexFunc(r.Ref, unicode.IsControl) >= 0 || len(r.Ref) > 512 {
		return verr(field+".ref", "not a valid reference")
	}
	return nil
}

// SiteSecretRef is a reference found in a site's configuration.
type SiteSecretRef struct {
	Field    string    `json:"field"`              // node.env[3], tasks[1].env[0], deploy.git.tokenFrom
	Variable string    `json:"variable,omitempty"` // the environment variable, if it is one
	Task     string    `json:"task,omitempty"`     // the task's name, for a task variable
	Ref      SecretRef `json:"ref"`
}

// SecretRefs lists every secret store reference of the site.
func (s *Site) SecretRefs() []SiteSecretRef {
	var out []SiteSecretRef
	if s.Node != nil {
		for i, e := range s.Node.Env {
			if e.From != nil {
				out = append(out, SiteSecretRef{Field: fmt.Sprintf("node.env[%d]", i), Variable: e.Name, Ref: *e.From})
			}
		}
	}
	for i, t := range s.Tasks {
		for j, e := range t.Env {
			if e.From != nil {
				out = append(out, SiteSecretRef{Field: fmt.Sprintf("tasks[%d].env[%d]", i, j), Variable: e.Name, Task: t.Name, Ref: *e.From})
			}
		}
	}
	if r := s.Deploy.Git.TokenFrom; r != nil {
		out = append(out, SiteSecretRef{Field: "deploy.git.tokenFrom", Ref: *r})
	}
	return out
}

// validateSecretRefs checks the syntax of the site's references: a
// variable taken from a secret store has no value of its own and is not a
// NodeHoster secret (nothing of it is stored), and a git token is either
// stored or referenced.
func (s *Site) validateSecretRefs() error {
	check := func(env []EnvVar, f string) error {
		for i := range env {
			e := &env[i]
			if e.From == nil {
				continue
			}
			ef := fmt.Sprintf("%s[%d]", f, i)
			if err := e.From.validate(ef + ".from"); err != nil {
				return err
			}
			if e.Secret || e.Value != "" {
				return verr(ef+".value", "%s comes from a secret store: it has no value of its own", e.Name)
			}
			// NodeHoster adds the agent to NODE_OPTIONS when it builds
			// the environment, before stores are read.
			if strings.EqualFold(e.Name, "NODE_OPTIONS") {
				return verr(ef+".from", "NODE_OPTIONS cannot come from a secret store")
			}
		}
		return nil
	}
	if s.Node != nil {
		if err := check(s.Node.Env, "node.env"); err != nil {
			return err
		}
	}
	for i := range s.Tasks {
		if err := check(s.Tasks[i].Env, fmt.Sprintf("tasks[%d].env", i)); err != nil {
			return err
		}
	}
	if r := s.Deploy.Git.TokenFrom; r != nil {
		if err := r.validate("deploy.git.tokenFrom"); err != nil {
			return err
		}
		if s.Deploy.Git.Token != "" {
			return verr("deploy.git.token", "the token comes from a secret store: remove the stored one")
		}
	}
	return nil
}

// SecretStoreStatus is the live state of a store, for the settings page
// and `nodehoster secrets list`. It never carries a value.
type SecretStoreStatus struct {
	Name        string     `json:"name"`
	Type        string     `json:"type"`
	Cached      int        `json:"cached"`     // values held in memory
	References  int        `json:"references"` // references to this store in the sites' configuration
	LastSuccess *time.Time `json:"lastSuccess,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
	LastErrorAt *time.Time `json:"lastErrorAt,omitempty"`
	// TokenExpires is when the store's current session ends (renewed
	// before then), when the store says.
	TokenExpires *time.Time `json:"tokenExpires,omitempty"`
}

// SecretStoreTest is the body of POST /api/secret-stores/test: a store as
// edited (masked credentials are those of the saved store with the same
// ID) and optionally a reference to read with it.
type SecretStoreTest struct {
	Store SecretStore `json:"store"`
	Ref   string      `json:"ref,omitempty"`
}

// SecretResolveRequest is the body of POST /api/secret-stores/resolve.
type SecretResolveRequest struct {
	Store string `json:"store"`
	Ref   string `json:"ref"`
}

// SecretTestResult is the outcome of a connection test or of resolving a
// reference. The value itself is never returned.
type SecretTestResult struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"` // what was checked: "signed in with AppRole; token valid for 1h"
}

// SecretRefCheck is the outcome of reading one of a site's references.
type SecretRefCheck struct {
	SiteSecretRef
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}
