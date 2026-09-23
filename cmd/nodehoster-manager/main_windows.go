// Command nodehoster-manager is NodeHoster's desktop manager, in the style
// of IIS Manager, and its notification-area status icon.
//
//	nodehoster-manager                 open the manager (asks for elevation)
//	nodehoster-manager --site ID       open the manager at a site
//	nodehoster-manager --tray          run the status icon (unelevated)
//	nodehoster-manager --service start|stop|restart
//	                                   control the service, then exit (the
//	                                   status icon runs this elevated)
//
// The manager talks to the server over the local admin pipe
// (internal/localapi), so it works when the web console does not: no port,
// certificate or NodeHoster account is involved, only Windows
// administrator rights. It also controls the Windows service directly,
// which the web console cannot do once the service is stopped.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/parthh37/nodehoster/internal/service"
	"github.com/tailscale/walk"
	"golang.org/x/sys/windows"
)

func main() {
	tray := flag.Bool("tray", false, "run the notification-area icon")
	autostart := flag.Bool("autostart", false, "started at sign-in (the icon honors its per-user opt-out)")
	svcCmd := flag.String("service", "", "start, stop or restart the NodeHoster service, then exit")
	site := flag.String("site", "", "open the manager at this site")
	flag.Parse()

	if *tray {
		runTray(*autostart)
		return
	}
	// Like IIS Manager, the manager needs an elevated token: the admin pipe
	// admits Administrators only, and UAC strips that group from
	// unelevated processes.
	if !windows.GetCurrentProcessToken().IsElevated() {
		if err := runElevated(os.Args[1:]...); err != nil && !errors.Is(err, windows.ERROR_CANCELLED) {
			fatal(fmt.Errorf("NodeHoster Manager needs administrator rights: %w", err))
		}
		return
	}
	if *svcCmd != "" {
		if err := controlService(*svcCmd); err != nil {
			fatal(err)
		}
		return
	}
	runManager(*site)
}

// controlService starts, stops or restarts the service and waits for it.
func controlService(cmd string) error {
	switch cmd {
	case "start":
		return service.Start()
	case "stop":
		return service.Stop()
	case "restart":
		if st, _ := service.Status(); st == "running" {
			if err := service.Stop(); err != nil {
				return err
			}
		}
		return service.Start()
	}
	return fmt.Errorf("unknown service command %q", cmd)
}

func fatal(err error) {
	walk.MsgBox(nil, "NodeHoster Manager", err.Error(), walk.MsgBoxIconError)
	os.Exit(1)
}
