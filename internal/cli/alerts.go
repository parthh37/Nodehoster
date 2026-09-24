package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

func init() {
	register(
		&Command{Name: "alert list", MaxArgs: 0,
			Summary: "Show the resource alerts firing and pending (--site)",
			Setup:   alertListCmd},
		&Command{Name: "alert history", MaxArgs: 0,
			Summary: "Show recent alerts, resolved ones included (-n count, --site)",
			Setup:   alertHistoryCmd},
		&Command{Name: "alert silence", Args: "<alert-id>", MinArgs: 1, MaxArgs: 1,
			Summary: "Silence an alert (--minutes, 60 by default; --note)",
			Setup:   alertSilenceCmd(false)},
		&Command{Name: "alert ack", Args: "<alert-id>", MinArgs: 1, MaxArgs: 1,
			Summary: "Acknowledge an alert: silence it until it resolves (--note)",
			Setup:   alertSilenceCmd(true)},
		&Command{Name: "alert unsilence", Args: "<alert-id>", MinArgs: 1, MaxArgs: 1,
			Summary: "Lift an alert's silence",
			Setup:   func(*flag.FlagSet) Runner { return alertUnsilence }},
	)
}

// alertSiteQuery turns --site into the siteId parameter.
func (e *Env) alertSiteQuery(q url.Values, site string) error {
	if site == "" {
		return nil
	}
	s, err := e.resolveSite(site)
	if err != nil {
		return err
	}
	q.Set("siteId", s.ID)
	return nil
}

func alertWho(a model.Alert) string {
	if a.SiteID == "" {
		return "(server)"
	}
	return a.SiteName
}

func silenceText(a model.Alert) string {
	s := a.Silence
	switch {
	case s == nil:
		return "-"
	case s.Until == nil:
		return "acknowledged by " + s.By
	}
	return "until " + localTime(*s.Until) + " by " + s.By
}

func alertListCmd(fs *flag.FlagSet) Runner {
	site := fs.String("site", "", "only this site's alerts (name or ID)")
	return func(e *Env, _ []string) error {
		q := url.Values{}
		if err := e.alertSiteQuery(q, *site); err != nil {
			return err
		}
		var l model.AlertList
		raw, err := e.get("/api/alerts?"+q.Encode(), &l)
		if err != nil || e.JSON {
			if err == nil {
				err = e.printJSON(raw)
			}
			return err
		}
		if !l.Enabled {
			e.printf("Alerts are off (Settings > Alerts in the web console).\n")
			return nil
		}
		if len(l.Firing)+len(l.Pending) == 0 {
			e.printf("No alert is firing.\n")
			return nil
		}
		var rows [][]string
		for _, a := range append(l.Firing, l.Pending...) {
			state := a.Severity
			if a.State == model.AlertPending {
				state = "pending"
			}
			rows = append(rows, []string{a.ID, state, alertWho(a), a.Message, localTime(a.Since), silenceText(a)})
		}
		e.table([]string{"ID", "STATE", "SITE", "ALERT", "SINCE", "SILENCED"}, rows)
		return nil
	}
}

func alertHistoryCmd(fs *flag.FlagSet) Runner {
	n := fs.Int("n", 50, "number of alerts")
	site := fs.String("site", "", "only this site's alerts (name or ID)")
	return func(e *Env, _ []string) error {
		if *n <= 0 || *n > 1000 {
			return usagef("-n must be between 1 and 1000")
		}
		q := url.Values{"limit": {strconv.Itoa(*n)}}
		if err := e.alertSiteQuery(q, *site); err != nil {
			return err
		}
		var list []model.Alert
		raw, err := e.get("/api/alerts/history?"+q.Encode(), &list)
		if err != nil || e.JSON {
			if err == nil {
				err = e.printJSON(raw)
			}
			return err
		}
		if len(list) == 0 {
			e.printf("No alerts.\n")
			return nil
		}
		var rows [][]string
		for _, a := range list {
			fired, resolved := "-", "firing"
			if a.FiredAt != nil {
				fired = localTime(*a.FiredAt)
			}
			if a.ResolvedAt != nil {
				resolved = localTime(*a.ResolvedAt)
			}
			rows = append(rows, []string{fired, alertWho(a), a.Severity, a.Message, resolved})
		}
		e.table([]string{"FIRED", "SITE", "SEVERITY", "ALERT", "RESOLVED"}, rows)
		return nil
	}
}

// resolveAlert finds an alert in progress by its ID or the start of it
// (the first 8 characters are enough in practice).
func (e *Env) resolveAlert(ref string) (string, error) {
	var l model.AlertList
	if _, err := e.get("/api/alerts", &l); err != nil {
		return "", err
	}
	var found []string
	for _, a := range append(l.Firing, l.Pending...) {
		if a.ID == ref {
			return a.ID, nil
		}
		if len(ref) >= 4 && strings.HasPrefix(a.ID, ref) {
			found = append(found, a.ID)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("no alert in progress has the ID %q (nodehoster alert list shows them)", ref)
	}
	return "", fmt.Errorf("%q is the start of %d alert IDs; give more of it", ref, len(found))
}

func alertSilenceCmd(ack bool) func(fs *flag.FlagSet) Runner {
	return func(fs *flag.FlagSet) Runner {
		minutes := new(int)
		if !ack {
			fs.IntVar(minutes, "minutes", 60, "how long; 0 = until the alert resolves")
		}
		note := fs.String("note", "", "why, for the others (audit log, consoles)")
		return func(e *Env, args []string) error {
			if *minutes < 0 {
				return usagef("--minutes must be 0 or more")
			}
			id, err := e.resolveAlert(args[0])
			if err != nil {
				return err
			}
			var raw json.RawMessage
			var a model.Alert
			body := model.AlertSilenceRequest{Minutes: *minutes, Note: *note}
			if err := e.Client.Post(e.Ctx, "/api/alerts/"+url.PathEscape(id)+"/silence", body, &raw); err != nil {
				return err
			}
			if e.JSON {
				return e.printJSON(raw)
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return err
			}
			if a.Silence != nil && a.Silence.Until != nil {
				e.printf("%s: %s\nSilenced until %s.\n", alertWho(a), a.Message, localTime(*a.Silence.Until))
			} else {
				e.printf("%s: %s\nAcknowledged: silent until it resolves.\n", alertWho(a), a.Message)
			}
			return nil
		}
	}
}

func alertUnsilence(e *Env, args []string) error {
	id, err := e.resolveAlert(args[0])
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := e.Client.Do(e.Ctx, http.MethodDelete, "/api/alerts/"+url.PathEscape(id)+"/silence", nil, &raw); err != nil {
		return err
	}
	if e.JSON {
		return e.printJSON(raw)
	}
	e.printf("The alert notifies again.\n")
	return nil
}
