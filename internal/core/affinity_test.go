package core

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/config"
)

// TestAffinityKeyPersists: clients keep their backend across a service
// restart, so the cookie key must be the same after reopening.
func TestAffinityKeyPersists(t *testing.T) {
	root := t.TempDir()
	open := func() *Core {
		boot := config.DefaultBootstrap()
		boot.Admin.Listen = "127.0.0.1:0"
		c, err := Open(config.NewPaths(root), boot, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := open()
	k1, err := c.affinityKey(context.Background())
	if err != nil || len(k1) != 32 {
		t.Fatalf("key %x, %v", k1, err)
	}
	var sealed string
	if err := c.Store.GetDoc(context.Background(), affinityKeyDoc, &sealed); err != nil || !strings.HasPrefix(sealed, "enc:") {
		t.Fatalf("stored as %q (%v)", sealed, err)
	}
	c.Shutdown()
	c = open()
	defer c.Shutdown()
	k2, _ := c.affinityKey(context.Background())
	if !bytes.Equal(k1, k2) {
		t.Fatal("affinity key changed across a restart")
	}
}
