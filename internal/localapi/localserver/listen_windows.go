//go:build windows

package localserver

import (
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/localapi"
	"golang.org/x/sys/windows"
)

// Protected DACLs (P: inherited entries are ignored).
//
// The admin pipe admits SYSTEM and Administrators only. An administrator's
// unelevated processes carry the Administrators group as deny-only, so UAC
// applies: like IIS Manager, the desktop manager runs elevated.
//
// The status pipe also admits interactive users (sessions at the console
// or over Remote Desktop, not services or network logons); read and write
// are both needed to send a request.
const (
	adminSDDL  = "D:P(A;;GA;;;SY)(A;;GA;;;BA)"
	statusSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"
)

// listen creates the endpoint's pipe. go-winio opens the first instance
// with FILE_FLAG_FIRST_PIPE_INSTANCE, so this fails if another process
// already owns the name rather than sharing it with an impostor.
func listen(e localapi.Endpoint, _ config.Paths) (net.Listener, string, error) {
	path, sddl := localapi.AdminPipe, adminSDDL
	if e == localapi.Status {
		path, sddl = localapi.StatusPipe, statusSDDL
	}
	l, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
	return l, path, err
}

// clientAccount names the Windows account of the process at the other end
// of a pipe connection, for the audit log.
func clientAccount(conn net.Conn) (string, error) {
	f, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return "", fmt.Errorf("%T is not a pipe", conn)
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(f.Fd()), &pid); err != nil {
		return "", err
	}
	tok, err := processToken(pid)
	if err != nil {
		return "", err
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	account, domain, _, err := tu.User.Sid.LookupAccount("")
	if err != nil {
		return tu.User.Sid.String(), nil
	}
	return domain + `\` + account, nil
}

func processToken(pid uint32) (windows.Token, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return 0, fmt.Errorf("open token of process %d: %w", pid, err)
	}
	return tok, nil
}
