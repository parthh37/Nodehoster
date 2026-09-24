//go:build windows

package procmgr

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/winacl"
	"golang.org/x/sys/windows"
)

// Every instance runs inside its own Job Object. The job gives three things
// IIS gets from its worker process model:
//
//   - KILL_ON_JOB_CLOSE: if NodeHoster dies, Windows kills the whole process
//     tree with it, so there are never orphaned node.exe processes holding
//     ports after a crash or an upgrade.
//   - A single handle to terminate the tree (npm -> cmd -> node) at once.
//   - Optional hard CPU and memory caps, like application pool limits.
//
// The process is created suspended and only resumed after it is inside the
// job, so not even a grandchild spawned in the first microseconds can escape.

type osProc struct {
	job windows.Handle
}

const jobObjectCpuRateControlInformation = 15

type jobCPURateControl struct {
	ControlFlags uint32
	Value        uint32
}

const (
	cpuRateControlEnable  = 0x1
	cpuRateControlHardCap = 0x4
)

var (
	advapi32       = windows.NewLazySystemDLL("advapi32.dll")
	procLogonUserW = advapi32.NewProc("LogonUserW")
)

const (
	logon32LogonBatch   = 4
	logon32LogonService = 5
	logon32ProviderDef  = 0
)

func logonUser(username, password string) (windows.Token, error) {
	domain := "."
	if i := strings.IndexByte(username, '\\'); i >= 0 {
		domain, username = username[:i], username[i+1:]
	} else if strings.Contains(username, "@") {
		domain = "" // UPN format
	}
	u, _ := windows.UTF16PtrFromString(username)
	p, _ := windows.UTF16PtrFromString(password)
	var d *uint16
	if domain != "" {
		d, _ = windows.UTF16PtrFromString(domain)
	}
	var lastErr error
	// Service logon needs "Log on as a service"; batch needs "Log on as a
	// batch job". Try both so either right is enough.
	for _, kind := range []uintptr{logon32LogonService, logon32LogonBatch} {
		var tok windows.Token
		r, _, err := procLogonUserW.Call(uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(p)),
			kind, logon32ProviderDef, uintptr(unsafe.Pointer(&tok)))
		if r != 0 {
			return tok, nil
		}
		lastErr = err
	}
	return 0, fmt.Errorf("log on as %s: %w", username, lastErr)
}

// prepare configures the command before it starts. siteDir is the site's
// folder under the data directory (sites\<id>).
func prepare(cmd *exec.Cmd, runAs model.RunAsConfig, password, siteDir string) (func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	cleanup := func() {}
	if !runAs.Enabled {
		return cleanup, siteAccess(siteDir, nil)
	}
	tok, err := logonUser(runAs.Username, password)
	if err != nil {
		return cleanup, err
	}
	u, err := tok.GetTokenUser()
	if err == nil {
		err = siteAccess(siteDir, u.User.Sid)
	}
	if err != nil {
		tok.Close()
		return cleanup, fmt.Errorf("give %s access to the site folder: %w", runAs.Username, err)
	}
	cmd.SysProcAttr.Token = syscall.Token(tok)
	return func() { tok.Close() }, nil
}

// siteAccess gives a site's run-as account Modify access to the site's own
// folder, like an application pool identity on its site: it reads its
// releases and writes its shared files and npm cache there. The data
// directory above admits only SYSTEM and Administrators (config.Paths), so
// the account cannot see other sites or the server's own files.
//
// NodeHoster owns the folder's explicit permissions: when the site's
// identity changes or is turned off, the previous account's access goes
// with it. With no run-as account, node runs as the service and needs no
// grant. An application folder outside the data directory is the
// administrator's to share, as in IIS.
func siteAccess(siteDir string, sid *windows.SID) error {
	if sid == nil {
		if _, err := os.Stat(siteDir); err != nil {
			return nil // nothing was ever granted
		}
		return winacl.Set(siteDir, winacl.Inherited)
	}
	// It may not exist yet when the application lives elsewhere; npm
	// still puts its cache here.
	if err := os.MkdirAll(siteDir, 0o750); err != nil {
		return err
	}
	return winacl.Set(siteDir, winacl.Modify(sid))
}

// afterStart places the suspended process in a new job object and resumes it.
func afterStart(pid int, limits model.ProcessLimits) (*osProc, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if limits.MemoryLimitMB > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		info.JobMemoryLimit = uintptr(limits.MemoryLimitMB) << 20
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("configure job object: %w", err)
	}
	if limits.CPUPercent > 0 && limits.CPUPercent < 100 {
		cpu := jobCPURateControl{
			ControlFlags: cpuRateControlEnable | cpuRateControlHardCap,
			Value:        uint32(limits.CPUPercent * 100), // in 1/100ths of a percent
		}
		if _, err := windows.SetInformationJobObject(job, jobObjectCpuRateControlInformation,
			uintptr(unsafe.Pointer(&cpu)), uint32(unsafe.Sizeof(cpu))); err != nil {
			windows.CloseHandle(job)
			return nil, fmt.Errorf("set CPU limit: %w", err)
		}
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("open process: %w", err)
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("assign job object: %w", err)
	}
	if err := resumeProcess(uint32(pid)); err != nil {
		windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
		return nil, err
	}
	return &osProc{job: job}, nil
}

// resumeProcess resumes every thread of a process created suspended. Go's
// os/exec does not expose the primary thread handle, so the threads are
// found through a Toolhelp snapshot.
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("thread snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)
	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	resumed := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			continue
		}
		windows.ResumeThread(th)
		windows.CloseHandle(th)
		resumed++
	}
	if resumed == 0 {
		return errors.New("could not resume process: no threads found")
	}
	return nil
}

// signalStop asks a Node.js process to stop when no agent is connected.
// On Windows there is nothing gentler than the agent for Node.js, which
// has no default Ctrl+Break handling worth sending (other runtimes get
// one: interrupt, in ctrlbreak_windows.go); requests have already been
// drained by the proxy by the time this is called.
func (p *osProc) signalStop(pid int) error { return errors.New("not supported") }

func (p *osProc) kill(pid int) error {
	if p == nil || p.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(p.job, 1)
}

func (p *osProc) release() {
	if p != nil && p.job != 0 {
		windows.CloseHandle(p.job)
		p.job = 0
	}
}
