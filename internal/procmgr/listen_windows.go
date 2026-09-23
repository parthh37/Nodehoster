//go:build windows

package procmgr

import (
	"crypto/rand"
	"encoding/hex"
	"net"

	"github.com/Microsoft/go-winio"
)

// listenAgent opens the named pipe hosted applications connect back to.
// SYSTEM and Administrators get full control; authenticated users may read
// and write so that sites running under their own identity can connect too.
// A per-instance token authenticates each connection.
func listenAgent(_ string) (net.Listener, string, error) {
	b := make([]byte, 8)
	rand.Read(b)
	path := `\\.\pipe\nodehoster-agent-` + hex.EncodeToString(b)
	l, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;AU)",
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
	return l, path, err
}
