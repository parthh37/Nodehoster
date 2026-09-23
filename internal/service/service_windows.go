//go:build windows

// Package service integrates with the Windows Service Control Manager.
package service

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	Name        = "NodeHoster"
	DisplayName = "NodeHoster Application Server"
	Description = "Hosts Node.js applications behind an HTTP/HTTPS reverse proxy with automatic certificates."
)

// IsService reports whether the process was started by the SCM.
func IsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

type handler struct {
	run func(stop <-chan struct{}) error
}

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- h.run(stop) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			if err != nil {
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				// Stopping drains connections and shuts down every
				// application, which can take a while; keep the SCM informed.
				status <- svc.Status{State: svc.StopPending, WaitHint: 60000}
				close(stop)
				select {
				case <-done:
				case <-time.After(55 * time.Second):
				}
				return false, 0
			}
		}
	}
}

// Run runs fn as the service's body until the SCM asks it to stop.
func Run(fn func(stop <-chan struct{}) error) error {
	return svc.Run(Name, &handler{run: fn})
}

// Install registers the service to start automatically (delayed) as
// LocalSystem, restarting on failure. It is idempotent so that installers
// can run it on every upgrade: an existing registration keeps its command
// line (and so its data folder) but gets the current start type and
// recovery settings.
func Install(exe string, args ...string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager (run as administrator): %w", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(Name); err == nil {
		defer s.Close()
		c, err := s.Config()
		if err != nil {
			return err
		}
		c.StartType, c.DelayedAutoStart = mgr.StartAutomatic, true
		c.DisplayName, c.Description = DisplayName, Description
		if err := s.UpdateConfig(c); err != nil {
			return err
		}
		return setRecovery(s)
	}
	s, err := m.CreateService(Name, exe, mgr.Config{
		DisplayName:      DisplayName,
		Description:      Description,
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
		ServiceStartName: "LocalSystem",
	}, args...)
	if err != nil {
		return err
	}
	defer s.Close()
	return setRecovery(s)
}

// setRecovery makes the SCM restart NodeHoster whenever it dies: after 5s,
// 15s, then every 60s (the last action repeats), with the failure count
// reset after a day without failures. Non-crash failures (the service
// stopping itself with an error) count too.
func setRecovery(s *mgr.Service) error {
	err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400)
	if err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}
	return s.SetRecoveryActionsOnNonCrashFailures(true)
}

func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return fmt.Errorf("service %s is not installed", Name)
	}
	defer s.Close()
	s.Control(svc.Stop)
	waitState(s, svc.Stopped, 60*time.Second)
	return s.Delete()
}

func Start() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Start(); err != nil {
		return err
	}
	return waitState(s, svc.Running, 60*time.Second)
}

func Stop() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return err
	}
	defer s.Close()
	if _, err := s.Control(svc.Stop); err != nil {
		return err
	}
	return waitState(s, svc.Stopped, 90*time.Second)
}

// Status returns the service state: running, stopped, starting, stopping,
// paused or "not installed". It asks only for the right to query status,
// which the SCM grants to interactive users, so unelevated processes such
// as the notification-area icon can call it.
func Status() (string, error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", err
	}
	defer windows.CloseServiceHandle(m)
	name, _ := windows.UTF16PtrFromString(Name)
	h, err := windows.OpenService(m, name, windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return "not installed", nil
	}
	if err != nil {
		return "", err
	}
	defer windows.CloseServiceHandle(h)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(h, &st); err != nil {
		return "", err
	}
	return map[svc.State]string{
		svc.Stopped: "stopped", svc.StartPending: "starting", svc.StopPending: "stopping",
		svc.Running: "running", svc.Paused: "paused", svc.ContinuePending: "starting", svc.PausePending: "stopping",
	}[svc.State(st.CurrentState)], nil
}

func waitState(s *mgr.Service, want svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for the service")
}
