// Package secrets encrypts sensitive configuration values (environment
// variables marked secret, DNS API keys, git tokens, run-as passwords) before
// they reach the database.
//
// Values are sealed with AES-256-GCM under a random master key. On Windows the
// master key file is itself protected with DPAPI in machine scope, so copying
// the database and key file to another machine does not reveal the secrets.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/parthh37/nodehoster/internal/config"
)

const prefix = "enc:v1:"

// Mask is what the API returns in place of a stored secret, and what a client
// sends back to mean "leave it unchanged".
const Mask = "__SECRET__"

type Box struct{ aead cipher.AEAD }

// Open loads the master key from path, creating it on first use.
func Open(path string) (*Box, error) {
	key, err := loadKey(path)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func loadKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		sealed, err := protect(key)
		if err != nil {
			return nil, fmt.Errorf("protect master key: %w", err)
		}
		if err := config.WriteFileAtomic(path, sealed, 0o600); err != nil {
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	key, err := unprotect(raw)
	if err != nil {
		return nil, fmt.Errorf("unprotect master key %s: %w", path, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("master key %s is corrupt", path)
	}
	return key, nil
}

// Seal encrypts plaintext. Empty strings and already-sealed values pass through.
func (b *Box) Seal(plain string) (string, error) {
	if plain == "" || strings.HasPrefix(plain, prefix) {
		return plain, nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return prefix + base64.RawStdEncoding.EncodeToString(out), nil
}

// Unseal decrypts a value produced by Seal. Values without the prefix are
// returned unchanged (plaintext written before encryption was enabled).
func (b *Box) Unseal(v string) (string, error) {
	if !strings.HasPrefix(v, prefix) {
		return v, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(v, prefix))
	if err != nil {
		return "", err
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("sealed value too short")
	}
	out, err := b.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", errors.New("secret cannot be decrypted with this server's master key")
	}
	return string(out), nil
}

// IsSealed reports whether v is a value produced by Seal.
func IsSealed(v string) bool { return strings.HasPrefix(v, prefix) }

// MustUnseal returns "" on failure; used where a missing secret is handled
// downstream (for example an auth failure against a git remote).
func (b *Box) MustUnseal(v string) string {
	s, _ := b.Unseal(v)
	return s
}
