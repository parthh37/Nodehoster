package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

func init() {
	register(
		&Command{Name: "waf list", MinArgs: 0, MaxArgs: 0,
			Summary: "List each site's web application firewall: mode, paranoia, threshold, exclusions, blocks",
			Setup:   func(*flag.FlagSet) Runner { return wafList }},
		&Command{Name: "waf mode", Args: "<site> <off|detect|block>", MinArgs: 2, MaxArgs: 2,
			Summary: "Set a site's firewall mode (--paranoia 1-3, --threshold N)",
			Setup:   wafModeCmd},
		&Command{Name: "waf events", Args: "[<site>]", MinArgs: 0, MaxArgs: 1,
			Summary: "List blocked (and detected) requests, newest first (-n, --action, --ip, --rule, --category, --id)",
			Setup:   wafEventsCmd},
		&Command{Name: "waf exclude", Args: "<site>", MinArgs: 1, MaxArgs: 1,
			Summary: "Add a firewall exclusion (--path, --rule, --category, --arg, --cookie, --header, --comment)",
			Setup:   wafExcludeCmd},
		&Command{Name: "waf rules", MinArgs: 0, MaxArgs: 0,
			Summary: "List the firewall's rules (--category)",
			Setup:   wafRulesCmd},
	)
}

// siteWAF is GET/PUT /api/sites/{id}/waf.
type siteWAF struct {
	Config model.WAFConfig `json:"config"`
	Stats  struct {
		Inspected int64 `json:"inspected"`
		Blocked   int64 `json:"blocked"`
		Detected  int64 `json:"detected"`
	} `json:"stats"`
}

func wafPath(s *localapi.Site) string { return sitePath(s) + "/waf" }

func wafList(e *Env, _ []string) error {
	list, err := e.sites()
	if err != nil {
		return err
	}
	type entry struct {
		Site string `json:"site"`
		ID   string `json:"id"`
		siteWAF
	}
	var out []entry
	for i := range list {
		s := &list[i]
		if s.Type == model.SiteWorker {
			continue
		}
		var w siteWAF
		if _, err := e.get(wafPath(s), &w); err != nil {
			return err
		}
		out = append(out, entry{Site: s.Name, ID: s.ID, siteWAF: w})
	}
	if e.JSON {
		if out == nil {
			out = []entry{}
		}
		return e.printJSON(out)
	}
	if len(out) == 0 {
		e.printf("No sites serve HTTP.\n")
		return nil
	}
	rows := make([][]string, 0, len(out))
	for _, x := range out {
		c := x.Config
		mode := c.Mode
		if mode == "" {
			mode = model.WAFOff
		}
		rows = append(rows, []string{x.Site, mode, strconv.Itoa(c.Paranoia()), strconv.Itoa(c.Threshold()), strconv.Itoa(len(c.Exclusions)),
			strconv.FormatInt(x.Stats.Blocked, 10), strconv.FormatInt(x.Stats.Detected, 10)})
	}
	e.table([]string{"SITE", "MODE", "PARANOIA", "THRESHOLD", "EXCLUSIONS", "BLOCKED", "DETECTED"}, rows)
	return nil
}

func wafModeCmd(fs *flag.FlagSet) Runner {
	paranoia := fs.Int("paranoia", 0, "paranoia level, 1-3 (unchanged if not given)")
	threshold := fs.Int("threshold", 0, "anomaly score that blocks (unchanged if not given)")
	return func(e *Env, args []string) error {
		mode := strings.ToLower(args[1])
		if !slices.Contains([]string{model.WAFOff, model.WAFDetect, model.WAFBlock}, mode) {
			return usagef("the mode is off, detect or block, not %q", args[1])
		}
		if *paranoia < 0 || *paranoia > 3 {
			return usagef("--paranoia must be 1, 2 or 3")
		}
		s, err := e.resolveSite(args[0])
		if err != nil {
			return err
		}
		var cur siteWAF
		if _, err := e.get(wafPath(s), &cur); err != nil {
			return err
		}
		cfg := cur.Config
		cfg.Mode = mode
		if *paranoia > 0 {
			cfg.ParanoiaLevel = *paranoia
		}
		if *threshold > 0 {
			cfg.AnomalyThreshold = *threshold
		}
		var raw json.RawMessage
		if err := e.Client.Put(e.Ctx, wafPath(s), cfg, &raw); err != nil {
			return err
		}
		if e.JSON {
			return e.printJSON(raw)
		}
		e.printf("The firewall of %s is now %s (paranoia level %d, threshold %d).\n", s.Name, describeMode(mode), cfg.Paranoia(), cfg.Threshold())
		return nil
	}
}

func describeMode(mode string) string {
	switch mode {
	case model.WAFDetect:
		return "in detect mode: it logs what it would block"
	case model.WAFBlock:
		return "blocking attacks"
	}
	return "off"
}

func wafEventsCmd(fs *flag.FlagSet) Runner {
	n := fs.Int("n", 50, "number of events, newest first")
	action := fs.String("action", "", "blocked or detected")
	ip := fs.String("ip", "", "only this client address")
	rule := fs.Int("rule", 0, "only events that matched this rule")
	category := fs.String("category", "", "only this category: "+strings.Join(model.WAFCategories, ", "))
	id := fs.String("id", "", "the event of this request ID (from a block page)")
	return func(e *Env, args []string) error {
		if *n < 1 || *n > 1000 {
			return usagef("-n must be between 1 and 1000")
		}
		q := url.Values{"limit": {strconv.Itoa(*n)}}
		for k, v := range map[string]string{"action": *action, "ip": *ip, "category": *category, "requestId": *id} {
			if v != "" {
				q.Set(k, v)
			}
		}
		if *rule > 0 {
			q.Set("rule", strconv.Itoa(*rule))
		}
		path := "/api/waf/events"
		names := map[string]string{}
		if len(args) > 0 {
			s, err := e.resolveSite(args[0])
			if err != nil {
				return err
			}
			path = wafPath(s) + "/events"
			names[s.ID] = s.Name
		} else if !e.JSON {
			if list, err := e.sites(); err == nil {
				for _, s := range list {
					names[s.ID] = s.Name
				}
			}
		}
		var list []model.WAFEvent
		raw, err := e.get(path+"?"+q.Encode(), &list)
		if err != nil || e.JSON {
			if err == nil {
				err = e.printJSON(raw)
			}
			return err
		}
		if len(list) == 0 {
			e.printf("No firewall events.\n")
			return nil
		}
		rows := make([][]string, 0, len(list))
		for _, ev := range list {
			rules := make([]string, len(ev.Matches))
			for i, m := range ev.Matches {
				rules[i] = strconv.Itoa(m.RuleID)
			}
			rows = append(rows, []string{localTime(ev.Time), ev.Action, ev.SiteName(names[ev.SiteID]), ev.ClientIP,
				truncate(ev.Method+" "+ev.Path, 50), strings.Join(rules, ","), fmt.Sprintf("%d/%d", ev.Score, ev.Threshold), ev.ID})
		}
		e.table([]string{"TIME", "ACTION", "SITE", "CLIENT", "REQUEST", "RULES", "SCORE", "REQUEST ID"}, rows)
		return nil
	}
}

// listFlag collects a repeatable, comma-separated flag.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

func wafExcludeCmd(fs *flag.FlagSet) Runner {
	path := fs.String("path", "", "path prefix (default: the whole site)")
	comment := fs.String("comment", "", "why the exclusion exists")
	var rules, cats, args, cookies, headers listFlag
	fs.Var(&rules, "rule", "rule IDs to turn off (repeatable, comma-separated)")
	fs.Var(&cats, "category", "categories to turn off (repeatable)")
	fs.Var(&args, "arg", "arguments not to inspect: form fields, query string, JSON keys (repeatable; trailing * = prefix)")
	fs.Var(&cookies, "cookie", "cookies not to inspect (repeatable)")
	fs.Var(&headers, "header", "headers not to inspect (repeatable)")
	return func(e *Env, pos []string) error {
		x := model.WAFExclusion{Path: *path, Categories: cats, Args: args, Cookies: cookies, Headers: headers, Comment: *comment}
		for _, r := range rules {
			id, err := strconv.Atoi(r)
			if err != nil || id <= 0 {
				return usagef("--rule takes rule IDs, not %q", r)
			}
			x.RuleIDs = append(x.RuleIDs, id)
		}
		if x.Path == "" && len(x.RuleIDs)+len(x.Categories)+len(x.Args)+len(x.Cookies)+len(x.Headers) == 0 {
			return usagef("give --rule, --category, --arg, --cookie or --header, or a --path under which the firewall is off")
		}
		s, err := e.resolveSite(pos[0])
		if err != nil {
			return err
		}
		var raw json.RawMessage
		if err := e.Client.Post(e.Ctx, wafPath(s)+"/exclusions", x, &raw); err != nil {
			return err
		}
		if e.JSON {
			return e.printJSON(raw)
		}
		e.printf("Added to %s's firewall: %s.\n", s.Name, x.String())
		return nil
	}
}

func wafRulesCmd(fs *flag.FlagSet) Runner {
	category := fs.String("category", "", "only this category")
	return func(e *Env, _ []string) error {
		var list []model.WAFRuleInfo
		raw, err := e.get("/api/waf/rules", &list)
		if err != nil {
			return err
		}
		if *category != "" {
			list = slices.DeleteFunc(list, func(r model.WAFRuleInfo) bool { return r.Category != *category })
			if e.JSON {
				return e.printJSON(list)
			}
		} else if e.JSON {
			return e.printJSON(raw)
		}
		rows := make([][]string, 0, len(list))
		for _, r := range list {
			rows = append(rows, []string{strconv.Itoa(r.ID), r.Category, r.Severity, strconv.Itoa(r.Paranoia), r.Message})
		}
		e.table([]string{"ID", "CATEGORY", "SEVERITY", "PARANOIA", "RULE"}, rows)
		return nil
	}
}
