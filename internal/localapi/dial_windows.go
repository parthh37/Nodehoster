//go:build windows

package localapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const (
	AdminPipe  = `\\.\pipe\NodeHoster.Admin`
	StatusPipe = `\\.\pipe\NodeHoster.Status`
)

func dial(ctx context.Context, e Endpoint, _ string) (net.Conn, error) {
	path := AdminPipe
	if e == Status {
		path = StatusPipe
	}
	conn, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return nil, err
	}
	if e == Admin {
		if err := verifyServer(conn); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

// verifyServer guards against pipe squatting. While the service is stopped
// any user can create a pipe with the admin pipe's name, and would then
// receive the administrator's requests, secrets included. The real server
// runs as LocalSystem, or elevated when started from a console.
func verifyServer(conn net.Conn) error {
	f, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("%T is not a pipe", conn)
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &pid); err != nil {
		return err
	}
	tok, err := processToken(pid)
	if err != nil {
		return err
	}
	defer tok.Close()
	if tok.IsElevated() {
		return nil
	}
	if tu, err := tok.GetTokenUser(); err == nil && tu.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return nil
	}
	return fmt.Errorf("refusing to use %s: process %d serving it does not run as an administrator", AdminPipe, pid)
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
