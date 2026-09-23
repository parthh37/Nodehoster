//go:build !windows

package procmgr

import (
	"net"
	"os"
	"path/filepath"
)

func listenAgent(dir string) (net.Listener, string, error) {
	path := filepath.Join(dir, "agent.sock")
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, "", err
	}
	os.Chmod(path, 0o666)
	return l, path, nil
}
