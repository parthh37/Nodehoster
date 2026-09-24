//go:build !windows

package runtimes

import "os/exec"

func hideWindow(*exec.Cmd) {}
