//go:build windows

package localapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/Microsoft/go-winio"
	"github.com/parthh37/nodehoster/internal/service"
	"golang.org/x/sys/windows"
)

const (
	AdminPipe  = `\\.\pipe\NodeHoster.Admin`
	StatusPipe = `\\.\pipe\NodeHoster.Status`
)

// pipeAccess is what a client needs: read, and write data. Not
// GENERIC_WRITE: it includes FILE_APPEND_DATA, which on a pipe is the right
// to create server instances of it (see localserver's descriptors).
const pipeAccess = windows.GENERIC_READ | windows.FILE_WRITE_DATA

func dial(ctx context.Context, e Endpoint, _ string) (net.Conn, error) {
	path := AdminPipe
	if e == Status {
		path = StatusPipe
	}
	conn, err := winio.DialPipeAccess(ctx, path, pipeAccess)
	if err != nil {
		return nil, err
	}
	if err := verifyServer(conn, path); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// verifyServer guards against pipe squatting. While the service is stopped
// any user can create a pipe with one of these names; the admin pipe's
// impostor would receive the administrator's requests, secrets included,
// and the status pipe's would feed the notification-area icon.
//
// When the NodeHoster service runs, the pipe must be served by its
// process. Otherwise (NodeHoster started from a console, for development)
// the server must run as LocalSystem or with the Administrators group
// enabled: service accounts such as NetworkService or an IIS application
// pool identity do not qualify.
func verifyServer(conn net.Conn, path string) error {
	f, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("%T is not a pipe", conn)
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &pid); err != nil {
		return err
	}
	if svcPID, err := service.ProcessID(); err == nil && svcPID != 0 {
		if pid == svcPID {
			return nil
		}
		return fmt.Errorf("refusing to use %s: process %d serves it, not the NodeHoster service (process %d)", path, pid, svcPID)
	}
	tok, err := processToken(pid)
	if err != nil {
		return fmt.Errorf("refusing to use %s: %w", path, err)
	}
	defer tok.Close()
	if ok, err := isAdministrator(tok); err != nil || !ok {
		return fmt.Errorf("refusing to use %s: process %d serving it does not run as an administrator", path, pid)
	}
	return nil
}

// isAdministrator reports whether tok is LocalSystem's, or has the
// Administrators group enabled (not deny-only, as in a UAC-filtered token).
func isAdministrator(tok windows.Token) (bool, error) {
	tu, err := tok.GetTokenUser()
	if err != nil {
		return false, err
	}
	if tu.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return true, nil
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, err
	}
	groups, err := tok.GetTokenGroups()
	if err != nil {
		return false, err
	}
	for _, g := range groups.AllGroups() {
		if g.Sid.Equals(admins) {
			return g.Attributes&windows.SE_GROUP_ENABLED != 0 && g.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0, nil
		}
	}
	return false, nil
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

func notListening(err error) bool {
	return errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, os.ErrNotExist)
}
