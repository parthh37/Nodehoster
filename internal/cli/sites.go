package cli

import (
	"flag"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

func init() {
	register(
		&Command{Name: "site list", Summary: "List the sites and their state", Setup: func(*flag.FlagSet) Runner { return siteList }},
		&Command{Name: "site show", Args: "<site>", Summary: "Show a site's settings and instances", MinArgs: 1, MaxArgs: 1,
			Setup: func(*flag.FlagSet) Runner { return siteShow }},
	)
	for _, a := range []struct{ name, summary, done string }{
		{"start", "Start a site", "Started"},
		{"stop", "Stop a site", "Stopped"},
		{"restart", "Restart a site (stop, then start)", "Restarted"},
		{"recycle", "Recycle a site without downtime (like an app pool)", "Recycled"},
	} {
		register(&Command{Name: "site " + a.name, Args: "<site>", Summary: a.summary, MinArgs: 1, MaxArgs: 1,
			Setup: func(*flag.FlagSet) Runner { return siteAction(a.name, a.done) }})
	}
}

// sites lists the sites, keeping the API's JSON.
func (e *Env) sites() ([]localapi.Site, error) {
	var list []localapi.Site
	_, err := e.get("/api/sites", &list)
	return list, err
}

// resolveSite finds a site by ID, or by name ignoring case (site names are
// unique regardless of case).
func (e *Env) resolveSite(ref string) (*localapi.Site, error) {
	list, err := e.sites()
	if err != nil {
		return nil, err
	}
	return findSite(list, ref)
}

func findSite(list []localapi.Site, ref string) (*localapi.Site, error) {
	for i := range list {
		if list[i].ID == ref {
			return &list[i], nil
		}
	}
	for i := range list {
		if strings.EqualFold(list[i].Name, ref) {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("no site is named %q (nodehoster site list shows them)", ref)
}

func sitePath(s *localapi.Site) string { return "/api/sites/" + url.PathEscape(s.ID) }

func siteList(e *Env, _ []string) error {
	var list []localapi.Site
	raw, err := e.get("/api/sites", &list)
	if err != nil || e.JSON {
		if err == nil {
			err = e.printJSON(raw)
		}
		return err
	}
	if len(list) == 0 {
		e.printf("There are no sites.\n")
		return nil
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	rows := make([][]string, 0, len(list))
	for _, s := range list {
		rows = append(rows, []string{s.Name, string(s.Type), stateText(s.Status), instancesText(s), bindingsText(s.Bindings), s.ID})
	}
	e.table([]string{"NAME", "TYPE", "STATE", "INSTANCES", "BINDINGS", "ID"}, rows)
	return nil
}

func stateText(st model.SiteStatus) string {
	if st.Message != "" {
		return string(st.State) + " (" + st.Message + ")"
	}
	return string(st.State)
}

// instancesText is "ready/configured" for Node.js sites.
func instancesText(s localapi.Site) string {
	if s.Type != model.SiteNode || s.Node == nil {
		return "-"
	}
	ready := 0
	for _, in := range s.Status.Instances {
		if in.State == "ready" {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, s.Node.Instances)
}

// bindingText is a binding as IIS Manager lists it: "http *:80:example.com".
func bindingText(b model.Binding) string {
	ip := b.IP
	if ip == "" {
		ip = "*"
	}
	if strings.Contains(ip, ":") {
		ip = "[" + ip + "]"
	}
	return fmt.Sprintf("%s %s:%d:%s", b.Protocol, ip, b.Port, b.Host)
}

func bindingsText(list []model.Binding) string {
	if len(list) == 0 {
		return "-"
	}
	out := make([]string, len(list))
	for i, b := range list {
		out[i] = bindingText(b)
	}
	return strings.Join(out, ", ")
}

func siteShow(e *Env, args []string) error {
	s, err := e.resolveSite(args[0])
	if err != nil {
		return err
	}
	if e.JSON {
		raw, err := e.get(sitePath(s), nil)
		if err != nil {
			return err
		}
		return e.printJSON(raw)
	}
	kv := [][]string{
		{"Name", s.Name},
		{"ID", s.ID},
		{"Type", string(s.Type)},
		{"State", stateText(s.Status)},
		{"Auto start", yesNo(s.AutoStart)},
	}
	for i, b := range s.Bindings {
		label := ""
		if i == 0 {
			label = "Bindings"
		}
		kv = append(kv, []string{label, bindingText(b)})
	}
	switch {
	case s.Node != nil:
		kv = append(kv, []string{"Application", s.Node.AppRoot}, []string{"Script", s.Node.Script}, []string{"Instances", instancesText(*s)})
	case s.Static != nil:
		kv = append(kv, []string{"Root", s.Static.Root})
	case s.Redirect != nil:
		kv = append(kv, []string{"Redirect to", s.Redirect.TargetURL})
	}
	if s.ActiveRelease != "" {
		kv = append(kv, []string{"Active release", s.ActiveRelease})
	}
	t := s.Status.Traffic
	kv = append(kv, []string{"Requests", fmt.Sprintf("%d (2xx %d, 3xx %d, 4xx %d, 5xx %d), %.1f/s", t.Requests, t.Status2xx, t.Status3xx, t.Status4xx, t.Status5xx, t.RPS)})
	for i := range kv {
		if kv[i][0] != "" {
			kv[i][0] += ":"
		}
	}
	e.tableNoHeader(kv)
	if len(s.Status.Instances) > 0 {
		fmt.Fprintln(e.Stdout)
		rows := make([][]string, 0, len(s.Status.Instances))
		for _, in := range s.Status.Instances {
			up := "-"
			if in.StartedAt != nil {
				up = time.Since(*in.StartedAt).Round(time.Second).String()
			}
			rows = append(rows, []string{fmt.Sprint(in.Index), fmt.Sprint(in.PID), fmt.Sprint(in.Port), in.State,
				fmt.Sprintf("%.1f%%", in.CPUPercent), formatBytes(in.MemoryBytes), fmt.Sprint(in.Restarts), up})
		}
		e.table([]string{"INSTANCE", "PID", "PORT", "STATE", "CPU", "MEMORY", "RESTARTS", "UPTIME"}, rows)
	}
	return nil
}

func siteAction(action, done string) Runner {
	return func(e *Env, args []string) error {
		s, err := e.resolveSite(args[0])
		if err != nil {
			return err
		}
		var st model.SiteStatus
		raw, err := e.post(sitePath(s)+"/"+action, &st)
		if err != nil {
			return err
		}
		if e.JSON {
			return e.printJSON(raw)
		}
		e.printf("%s %s (%s).\n", done, s.Name, stateText(st))
		return nil
	}
}

// ---- formatting helpers

// tableNoHeader prints aligned rows (a key/value list).
func (e *Env) tableNoHeader(rows [][]string) {
	tw := newTabWriter(e.Stdout)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func formatBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// localTime formats a time for tables.
func localTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
