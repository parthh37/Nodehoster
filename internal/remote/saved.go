package remote

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/model"
)

// Saved is a connection to a server saved for the current user, which
// NodeHoster Manager's "Connect to a server…" and `nodehoster server add`
// share. Unlike the connections of a server (model.ServerConnection) they
// belong to the Windows account: the token is protected with DPAPI in the
// user's scope, so another account on the computer, or the file copied
// elsewhere, cannot use it.
type Saved struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Token       string `json:"-"` // in memory only; on disk as ProtectedToken
	// ProtectedToken is the token as stored: "dpapi:" and a DPAPI blob in
	// base64 on Windows ("plain:" elsewhere, for development only, where
	// the file's permissions protect it).
	ProtectedToken string `json:"token"`
}

type savedFile struct {
	Servers []Saved `json:"servers"`
}

// SavedPath is where the current user's connections are kept:
// %APPDATA%\NodeHoster\connections.json on Windows.
func SavedPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "NodeHoster", "connections.json"), nil
}

// LoadSaved reads the current user's connections, tokens unprotected. A
// token that cannot be unprotected (the file comes from another account
// or computer) is left empty. No file is no connections.
func LoadSaved() ([]Saved, error) {
	p, err := SavedPath()
	if err != nil {
		return nil, err
	}
	return LoadSavedFrom(p)
}

// LoadSavedFrom is LoadSaved from another file (tests).
func LoadSavedFrom(p string) ([]Saved, error) {
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f savedFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	for i := range f.Servers {
		f.Servers[i].Token, _ = unprotectToken(f.Servers[i].ProtectedToken)
	}
	return f.Servers, nil
}

// StoreSaved replaces the current user's connections.
func StoreSaved(list []Saved) error {
	p, err := SavedPath()
	if err != nil {
		return err
	}
	return StoreSavedTo(p, list)
}

// StoreSavedTo is StoreSaved to another file (tests).
func StoreSavedTo(p string, list []Saved) error {
	f := savedFile{Servers: make([]Saved, 0, len(list))}
	for _, s := range list {
		pt, err := protectToken(s.Token)
		if err != nil {
			return fmt.Errorf("protect the token of %s: %w", s.Name, err)
		}
		s.ProtectedToken = pt
		f.Servers = append(f.Servers, s)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return config.WriteFileAtomic(p, data, 0o600)
}

// Validate normalizes a connection to save and checks it against the
// others (names are unique regardless of case).
func (s *Saved) Validate(others []Saved) error {
	s.Name = strings.TrimSpace(s.Name)
	s.Token = strings.TrimSpace(s.Token)
	u, err := model.NormalizeServerURL(s.URL)
	if err != nil {
		return err
	}
	s.URL = u
	if s.Name == "" {
		s.Name = hostOf(u)
	}
	fp, err := model.ParseFingerprint(s.Fingerprint)
	if err != nil {
		return err
	}
	s.Fingerprint = fp
	if s.Token == "" {
		return errors.New("enter an API token created on that server")
	}
	if strings.EqualFold(s.Name, "local") || strings.Contains(s.Name, "://") {
		return fmt.Errorf("%q cannot be the name of a connection", s.Name)
	}
	for _, o := range others {
		if strings.EqualFold(o.Name, s.Name) {
			return fmt.Errorf("a connection is already named %q", o.Name)
		}
	}
	return nil
}

// FindSaved finds a saved connection by name (ignoring case) or URL.
func FindSaved(list []Saved, ref string) (Saved, bool) {
	i := slices.IndexFunc(list, func(s Saved) bool { return strings.EqualFold(s.Name, ref) })
	if i < 0 {
		if u, err := model.NormalizeServerURL(ref); err == nil {
			i = slices.IndexFunc(list, func(s Saved) bool { return s.URL == u })
		}
	}
	if i < 0 {
		return Saved{}, false
	}
	return list[i], true
}

func hostOf(u string) string {
	if _, rest, ok := strings.Cut(u, "://"); ok {
		u = rest
	}
	host, _, _ := strings.Cut(u, "/")
	return host
}

func protectToken(tok string) (string, error) {
	if tok == "" {
		return "", nil
	}
	blob, scheme, err := protectUser([]byte(tok))
	if err != nil {
		return "", err
	}
	return scheme + ":" + base64.StdEncoding.EncodeToString(blob), nil
}

func unprotectToken(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	scheme, b64, ok := strings.Cut(v, ":")
	if !ok {
		return "", errors.New("unknown token format")
	}
	blob, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	plain, err := unprotectUser(scheme, blob)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
