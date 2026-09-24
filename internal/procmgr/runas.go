package procmgr

import (
	"context"
	"os/exec"

	"github.com/parthh37/nodehoster/internal/model"
)

// RunAs runs cmd to completion as a site's run-as account (runAs, with its
// password unsealed), the way the site's instances run: logged on with
// LogonUser, the account given Modify access to the site's folder siteDir,
// and the process tree in a Job Object that dies with the command (or
// with NodeHoster). Deployments use it so that a site's install and build
// commands, which run the application's own code (package scripts, NuGet
// targets, setup.py), never run with more rights than the site itself.
//
// Cancelling ctx kills the whole tree, not only cmd's own process. cmd's
// SysProcAttr is replaced, keeping only a raw command line (CmdLine) that
// it sets on Windows. runAs must be enabled: with no run-as account the
// site's folder would be reset to what it inherits, as for an instance.
func RunAs(ctx context.Context, cmd *exec.Cmd, runAs model.RunAsConfig, password, siteDir string) error {
	cleanup, err := prepare(cmd, runAs, password, siteDir)
	defer cleanup()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	osp, err := afterStart(cmd.Process.Pid, model.ProcessLimits{})
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return err
	}
	defer osp.release()
	stop := context.AfterFunc(ctx, func() { osp.kill(cmd.Process.Pid) })
	err = cmd.Wait()
	stop()
	// What the command left running (a build server, a watcher) goes with
	// it: kill the job's remaining processes before releasing it.
	osp.kill(cmd.Process.Pid)
	return err
}
