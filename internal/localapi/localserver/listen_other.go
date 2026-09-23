//go:build !windows

package localserver

import (
	"net"
	"os"
	"os/user"

	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/localapi"
)

// listen opens the endpoint's socket. The admin socket is private to the
// server's user, standing in for the admin pipe's Administrators-only
// descriptor; anyone may read the status socket.
func listen(e localapi.Endpoint, paths config.Paths) (net.Listener, string, error) {
	path := localapi.SocketPath(e, paths.Data)
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, "", err
	}
	mode := os.FileMode(0o600)
	if e == localapi.Status {
		mode = 0o666
	}
	if err := os.Chmod(path, mode); err != nil {
		l.Close()
		return nil, "", err
	}
	return l, path, nil
}

// clientAccount: only the server's own user (or root) can open the admin
// socket, so that is who is calling.
func clientAccount(net.Conn) (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}
