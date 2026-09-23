package main

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// runElevated starts this program again with the given arguments, through
// the UAC prompt. It returns ERROR_CANCELLED if the user declines.
func runElevated(args ...string) error {
	return shellExecute("runas", exePath(), quoteArgs(args))
}

// shellOpen opens a URL in the default browser or a folder in Explorer.
func shellOpen(target string) error {
	return shellExecute("open", target, "")
}

// startManager opens the manager, which elevates itself.
func startManager(args ...string) error {
	return shellExecute("open", exePath(), quoteArgs(args))
}

func shellExecute(verb, file, args string) error {
	v, _ := windows.UTF16PtrFromString(verb)
	f, _ := windows.UTF16PtrFromString(file)
	var a *uint16
	if args != "" {
		a, _ = windows.UTF16PtrFromString(args)
	}
	return windows.ShellExecute(0, v, f, a, nil, windows.SW_SHOWNORMAL)
}

func exePath() string {
	p, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return p
}

func quoteArgs(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = windows.EscapeArg(a)
	}
	return strings.Join(q, " ")
}

// The installer starts the status icon at every sign-in (HKLM Run, with
// --autostart). Each user can opt out; the choice is theirs, in HKCU.
const trayKey = `Software\NodeHoster\StatusIcon`

func trayAutostartDisabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, trayKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("DisableAtSignIn")
	return err == nil && v != 0
}

func setTrayAutostartDisabled(disabled bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, trayKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	var v uint32
	if disabled {
		v = 1
	}
	return k.SetDWordValue("DisableAtSignIn", v)
}

// singleInstance reports whether this is the only process holding name in
// the current session. The mutex lives as long as the process.
func singleInstance(name string) bool {
	n, _ := windows.UTF16PtrFromString(`Local\` + name)
	_, err := windows.CreateMutex(nil, false, n)
	return err != windows.ERROR_ALREADY_EXISTS
}
