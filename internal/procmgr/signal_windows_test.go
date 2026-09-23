//go:build windows

package procmgr

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func signalZero(p *os.Process) error {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(p.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return err
	}
	if code != 259 { // STILL_ACTIVE
		return errors.New("exited")
	}
	return nil
}
