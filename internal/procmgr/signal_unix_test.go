//go:build !windows

package procmgr

import (
	"os"
	"syscall"
)

func signalZero(p *os.Process) error { return p.Signal(syscall.Signal(0)) }
