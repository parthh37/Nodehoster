package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

func init() {
	register(
		&Command{Name: "update", MaxArgs: 0,
			Summary: "Show the installed and the newest version, and the automatic update settings",
			Setup:   func(*flag.FlagSet) Runner { return updateStatus("/api/updates", false) }},
		&Command{Name: "update check", MaxArgs: 0,
			Summary: "Check the release feed for a newer version now",
			Setup:   func(*flag.FlagSet) Runner { return updateStatus("/api/updates/check", true) }},
		&Command{Name: "update install", MaxArgs: 0,
			Summary: "Install the newest version now; the service restarts (asks first unless --yes)",
			Setup:   updateInstallCmd},
		&Command{Name: "update auto", Args: "on|off", MinArgs: 1, MaxArgs: 1,
			Summary: "Turn automatic updates on or off (--time HH:MM, --days 0,6 with 0 = Sunday)",
			Setup:   updateAutoCmd},
	)
}

// OfflineUpdates changes the updates settings in the database when the
// service is not running, so that setup's choice is kept even when the
// service did not start. main sets it: it owns the database.
var OfflineUpdates func(fn func(*model.UpdateSettings)) error

// ServiceUp reports whether the Windows service is running or starting,
// even though its admin pipe may not answer yet (setup runs `update auto`
// right after starting it). main sets it.
var ServiceUp func() bool

func updateStatus(path string, post bool) Runner {
	return func(e *Env, _ []string) error {
		var st model.UpdateStatus
		var raw json.RawMessage
		var err error
		if post {
			raw, err = e.post(path, &st)
		} else {
			raw, err = e.get(path, &st)
		}
		if err != nil {
			return err
		}
		if e.JSON {
			return e.printJSON(raw)
		}
		printUpdateStatus(e, st)
		return nil
	}
}

func printUpdateStatus(e *Env, st model.UpdateStatus) {
	at := func(t *time.Time) string {
		if t == nil {
			return "—"
		}
		return t.Local().Format("2006-01-02 15:04")
	}
	rows := [][]string{{"Installed", st.Current}}
	switch {
	case !st.Supported:
		rows = append(rows, []string{"Updates", "not available: " + st.Reason})
	case st.Available != nil:
		a := st.Available
		note := a.Notes
		switch {
		case a.Failed:
			note = "installing it failed before; not retried automatically. " + note
		case a.Manual:
			note = "not installed automatically: use nodehoster update install. " + note
		}
		rows = append(rows, []string{"Available", a.Version + " — " + note})
	default:
		rows = append(rows, []string{"Available", "none, up to date"})
	}
	auto := "off"
	if st.Auto {
		auto = "on, at " + st.Time + " " + weekdayNames(st.Weekdays)
	}
	rows = append(rows, []string{"Automatic updates", auto})
	if st.NextInstall != nil {
		rows = append(rows, []string{"Next install", at(st.NextInstall)})
	}
	if st.State != model.UpdateIdle {
		rows = append(rows, []string{"State", st.State})
	}
	rows = append(rows, []string{"Last check", at(st.LastCheck)})
	if st.LastError != "" {
		rows = append(rows, []string{"Last error", st.LastError})
	}
	if r := st.LastResult; r != nil {
		res := "succeeded"
		if !r.OK {
			res = "failed: " + r.Error
		}
		rows = append(rows, []string{"Last update", fmt.Sprintf("%s → %s %s (%s)", r.From, r.To, res, r.StartedAt.Local().Format("2006-01-02 15:04"))})
	}
	tw := newTabWriter(e.Stdout)
	for _, r := range rows {
		fmt.Fprintf(tw, "%s:\t%s\n", r[0], r[1])
	}
	tw.Flush()
}

func weekdayNames(days []int) string {
	if len(days) == 0 {
		return "every day"
	}
	names := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	var out []string
	for _, d := range days {
		if d >= 0 && d < 7 {
			out = append(out, names[d])
		}
	}
	return "on " + strings.Join(out, ", ")
}

func updateInstallCmd(fs *flag.FlagSet) Runner {
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	return func(e *Env, _ []string) error {
		var st model.UpdateStatus
		if _, err := e.post("/api/updates/check", &st); err != nil {
			return err
		}
		if st.Available == nil {
			if e.JSON {
				return e.printJSON(st)
			}
			fmt.Fprintf(e.Stdout, "NodeHoster %s is up to date.\n", st.Current)
			return nil
		}
		if !*yes {
			if !e.Interactive {
				return usagef("installing restarts the service; add --yes to confirm")
			}
			fmt.Fprintf(e.Stdout, "Install NodeHoster %s over %s? The service restarts: sites are offline for a few seconds. [y/N] ", st.Available.Version, st.Current)
			answer, _ := bufio.NewReader(e.Stdin).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
				return errors.New("cancelled; nothing was installed")
			}
		}
		raw, err := e.post("/api/updates/install", &st)
		if err != nil {
			return err
		}
		if e.JSON {
			return e.printJSON(raw)
		}
		fmt.Fprintf(e.Stdout, "Installing NodeHoster %s. The service restarts in a moment; `nodehoster update` shows the result once it is back.\n", st.Available.Version)
		return nil
	}
}

func updateAutoCmd(fs *flag.FlagSet) Runner {
	at := fs.String("time", "", "when to install updates, HH:MM in local time (default: unchanged, initially 03:00)")
	days := fs.String("days", "", "days to install on: 0 (Sunday) to 6, comma-separated; \"all\" = every day (default: unchanged)")
	return func(e *Env, args []string) error {
		var on bool
		switch strings.ToLower(args[0]) {
		case "on":
			on = true
		case "off":
		default:
			return usagef("expected on or off, not %q", args[0])
		}
		var weekdays []int
		if *days != "" && *days != "all" {
			for _, d := range strings.Split(*days, ",") {
				var n int
				if _, err := fmt.Sscanf(strings.TrimSpace(d), "%d", &n); err != nil {
					return usagef("--days: %q is not a day number", d)
				}
				weekdays = append(weekdays, n)
			}
		}
		apply := func(u *model.UpdateSettings) {
			u.Auto = on
			if *at != "" {
				u.Time = *at
			}
			if *days == "all" {
				u.Weekdays = []int{}
			} else if weekdays != nil {
				u.Weekdays = weekdays
			}
		}

		var cur model.UpdateStatus
		_, err := e.get("/api/updates", &cur)
		// A service that is starting opens its pipe a moment later. Its
		// database must not be changed behind its back: it would keep and
		// later save the settings it read.
		for deadline := time.Now().Add(60 * time.Second); errors.Is(err, localapi.ErrNotRunning) &&
			ServiceUp != nil && ServiceUp() && time.Now().Before(deadline); {
			time.Sleep(time.Second)
			_, err = e.get("/api/updates", &cur)
		}
		if errors.Is(err, localapi.ErrNotRunning) && OfflineUpdates != nil {
			if err := OfflineUpdates(apply); err != nil {
				return err
			}
			fmt.Fprintln(e.Stdout, "The service is not running; the setting was saved and applies when it starts.")
			return nil
		}
		if err != nil {
			return err
		}
		u := cur.UpdateSettings
		apply(&u)
		var st model.UpdateStatus
		if err := e.Client.Put(e.Ctx, "/api/updates", u, &st); err != nil {
			return err
		}
		if e.JSON {
			return e.printJSON(st)
		}
		printUpdateStatus(e, st)
		return nil
	}
}
