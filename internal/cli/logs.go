package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"slices"
	"strconv"

	"github.com/parthh37/nodehoster/internal/model"
)

func init() {
	register(
		&Command{Name: "logs", Args: "<site>", MinArgs: 1, MaxArgs: 1,
			Summary: "Show a site's output (-n lines, -f to follow, --access)",
			Setup:   logsCmd},
		&Command{Name: "events", MaxArgs: 0,
			Summary: "Show recent events (-n count, --site)",
			Setup:   eventsCmd},
	)
}

func logsCmd(fs *flag.FlagSet) Runner {
	n := fs.Int("n", 100, "number of recent lines")
	follow := fs.Bool("f", false, "keep following new lines until Ctrl+C")
	access := fs.Bool("access", false, "the access log instead of the application's output")
	slot := fs.String("slot", "", "only this deployment slot's lines (production for the site's own)")
	return func(e *Env, args []string) error {
		if *n < 0 {
			return usagef("-n must be 0 or more")
		}
		s, err := e.resolveSite(args[0])
		if err != nil {
			return err
		}
		q := url.Values{}
		if *access {
			q.Set("type", "access")
		}
		if *slot != "" {
			if _, err := checkSlot(s, *slot); err != nil {
				return err
			}
			q.Set("slot", *slot)
		}
		var lines []model.LogLine
		if *n > 0 {
			q.Set("lines", strconv.Itoa(*n))
			raw, err := e.get(sitePath(s)+"/logs?"+q.Encode(), &lines)
			if err != nil {
				return err
			}
			if e.JSON && !*follow {
				return e.printJSON(raw)
			}
		} else if e.JSON && !*follow {
			return e.printJSON([]model.LogLine{})
		}
		for _, l := range lines {
			e.printLogLine(l)
		}
		if !*follow {
			return nil
		}
		q.Del("lines")
		path := sitePath(s) + "/logs/stream"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		err = e.Client.Stream(e.Ctx, path, func(event string, data []byte) {
			var l model.LogLine
			if event == "log" && json.Unmarshal(data, &l) == nil {
				e.printLogLine(l)
			}
		})
		if e.Ctx.Err() != nil {
			return nil // Ctrl+C
		}
		return fmt.Errorf("the log stream ended: %w", err)
	}
}

// printLogLine writes one line: "time stream[instance] text", or with
// --json one JSON object per line (NDJSON) when following.
func (e *Env) printLogLine(l model.LogLine) {
	if e.JSON {
		data, _ := json.Marshal(l)
		fmt.Fprintf(e.Stdout, "%s\n", data)
		return
	}
	if l.Stream == "access" || l.Instance < 0 {
		fmt.Fprintln(e.Stdout, l.Text)
		return
	}
	if l.Slot != "" {
		fmt.Fprintf(e.Stdout, "%s %s:%s[%d] %s\n", localTime(l.Time), l.Slot, l.Stream, l.Instance, l.Text)
		return
	}
	fmt.Fprintf(e.Stdout, "%s %s[%d] %s\n", localTime(l.Time), l.Stream, l.Instance, l.Text)
}

func eventsCmd(fs *flag.FlagSet) Runner {
	n := fs.Int("n", 50, "number of events")
	site := fs.String("site", "", "only this site's events (name or ID)")
	return func(e *Env, _ []string) error {
		if *n <= 0 {
			return usagef("-n must be 1 or more")
		}
		q := url.Values{"limit": {strconv.Itoa(*n)}}
		names := map[string]string{}
		if *site != "" || !e.JSON {
			list, err := e.sites()
			if err != nil {
				return err
			}
			for _, s := range list {
				names[s.ID] = s.Name
			}
			if *site != "" {
				s, err := findSite(list, *site)
				if err != nil {
					return err
				}
				q.Set("siteId", s.ID)
			}
		}
		var list []model.Event
		raw, err := e.get("/api/events?"+q.Encode(), &list)
		if err != nil || e.JSON {
			if err == nil {
				err = e.printJSON(raw)
			}
			return err
		}
		if len(list) == 0 {
			e.printf("No events.\n")
			return nil
		}
		slices.Reverse(list) // oldest first, the latest at the bottom, like logs
		rows := make([][]string, 0, len(list))
		for _, ev := range list {
			site := names[ev.SiteID]
			if site == "" {
				site = ev.SiteID
			}
			if site == "" {
				site = "-"
			}
			rows = append(rows, []string{localTime(ev.Time), ev.Level, ev.Type, site, ev.Message})
		}
		e.table([]string{"TIME", "LEVEL", "TYPE", "SITE", "MESSAGE"}, rows)
		return nil
	}
}
