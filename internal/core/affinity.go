package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"github.com/parthh37/nodehoster/internal/store"
)

const affinityKeyDoc = "proxy.affinityKey"

// affinityKey returns the key that signs session affinity cookies, created
// on first use and kept (sealed with the master key) so that clients stay
// on their backend across service restarts. A key that cannot be read is
// replaced: the only cost is that clients are balanced afresh once.
func (c *Core) affinityKey(ctx context.Context) ([]byte, error) {
	var sealed string
	err := c.Store.GetDoc(ctx, affinityKeyDoc, &sealed)
	if err == nil {
		if plain, err := c.Box.Unseal(sealed); err == nil {
			if key, err := base64.StdEncoding.DecodeString(plain); err == nil && len(key) == 32 {
				return key, nil
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	sealed, err = c.Box.Seal(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		return nil, err
	}
	if err := c.Store.PutDoc(ctx, affinityKeyDoc, sealed); err != nil {
		return nil, err
	}
	return key, nil
}
