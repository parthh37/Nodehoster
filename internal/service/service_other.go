//go:build !windows

// Package service integrates with the Windows Service Control Manager. On
// other systems run NodeHoster under systemd or launchd instead.
package service

import "errors"

var errUnsupported = errors.New("Windows services are only available on Windows; use systemd or launchd here")

func IsService() bool                               { return false }
func Run(fn func(stop <-chan struct{}) error) error { return errUnsupported }
func Install(exe string, args ...string) error      { return errUnsupported }
func Uninstall() error                              { return errUnsupported }
func Start() error                                  { return errUnsupported }
func Stop() error                                   { return errUnsupported }
func Status() (string, error)                       { return "", errUnsupported }
func ProcessID() (uint32, error)                    { return 0, errUnsupported }
