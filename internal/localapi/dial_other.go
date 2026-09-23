//go:build !windows

package localapi

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// SocketPath is where an endpoint listens outside Windows (development and
// tests): a Unix socket in the data directory.
func SocketPath(e Endpoint, dataDir string) string {
	return filepath.Join(dataDir, e.String()+".sock")
}

func dial(ctx context.Context, e Endpoint, dataDir string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", SocketPath(e, dataDir))
}

func notListening(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist)
}
