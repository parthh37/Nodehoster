//go:build !windows

package procmgr

import (
	"fmt"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"

	"github.com/parthh37/nodehoster/internal/model"
)

// On Unix the instance runs in its own process group so the whole tree can
// be signalled at once. Resource limits are Windows-only (Job Objects).
type osProc struct{}

func prepare(cmd *exec.Cmd, runAs model.RunAsConfig, _ string) (func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if runAs.Enabled {
		u, err := user.Lookup(runAs.Username)
		if err != nil {
			return func() {}, fmt.Errorf("run as %s: %w", runAs.Username, err)
		}
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}
	return func() {}, nil
}

func afterStart(pid int, _ model.ProcessLimits) (*osProc, error) { return &osProc{}, nil }

func (p *osProc) signalStop(pid int) error { return syscall.Kill(-pid, syscall.SIGTERM) }

func (p *osProc) kill(pid int) error { return syscall.Kill(-pid, syscall.SIGKILL) }

func (p *osProc) release() {}
