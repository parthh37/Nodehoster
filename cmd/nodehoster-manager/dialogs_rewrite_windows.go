package main

import (
	"context"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- URL Rewrite, as the IIS URL Rewrite module's feature page

var (
	ruleActions     = []string{"rewrite", "redirect", "respond", "block", "none"}
	ruleActionNames = []string{"Rewrite", "Redirect", "Custom response", "Abort request (block)", "None"}

	queryModes     = []string{"", "append", "discard"}
	queryModeNames = []string{"Keep, unless the target has a query string", "Append the original query string", "Discard the original query string"}

	conditionInputs = []string{"{HTTP_HOST}", "{QUERY_STRING}", "{REQUEST_URI}", "{URL}", "{REQUEST_METHOD}", "{REMOTE_ADDR}",
		"{HTTPS}", "{SERVER_PORT}", "{REQUEST_FILENAME}", "{HTTP_USER_AGENT}", "{HTTP_REFERER}"}
	matchTypes     = []string{"pattern", "isFile", "isDirectory"}
	matchTypeNames = []string{"Matches the pattern", "Is a file", "Is a directory"}

	outboundScopes     = []string{"header", "tags", "body"}
	outboundScopeNames = []string{"An HTTP response header", "URLs in HTML tags", "Text in the response body"}
)

func actionName(a string) string {
	if i := slices.Index(ruleActions, a); i >= 0 {
		return ruleActionNames[i]
	}
	return a
}

func negated(neg bool, pattern string) string {
	if neg {
		return "not " + pattern
	}
	return pattern
}

func cloneRule(r model.RewriteRule) model.RewriteRule {
	r.Conditions = slices.Clone(r.Conditions)
	return r
}

func cloneOutbound(o model.OutboundRule) model.OutboundRule {
	o.Conditions = slices.Clone(o.Conditions)
	o.Tags = slices.Clone(o.Tags)
	return o
}

func cloneMap(m model.RewriteMap) model.RewriteMap {
	m.Entries = maps.Clone(m.Entries)
	if m.Entries == nil {
		m.Entries = map[string]string{}
	}
	return m
}

func rewriteDialog(m *manager, siteID string) {
	site, err := m.fetchSite(siteID)
	if err != nil {
		m.errorBox("URL Rewrite", err)
		return
	}
	var rules []model.RewriteRule
	for _, r := range site.Routing.Rewrites {
		rules = append(rules, cloneRule(r))
	}
	var outbound []model.OutboundRule
	for _, o := range site.Routing.OutboundRules {
		outbound = append(outbound, cloneOutbound(o))
	}
	var rmaps []model.RewriteMap
	for _, x := range site.Routing.RewriteMaps {
		rmaps = append(rmaps, cloneMap(x))
	}

	var dlg *walk.Dialog
	var toggleIn, toggleOut *walk.PushButton

	// Inbound rules
	in := newEditList(&rules, func(r model.RewriteRule) []string {
		target := r.Target
		switch r.Action {
		case "respond":
			target = strings.TrimSpace(fmt.Sprintf("%s %s", statusText(r.StatusCode), firstLine(r.Body)))
		case "block":
			target = statusText(r.StatusCode)
		}
		return []string{r.Name, negated(r.Negate, r.Match), actionName(r.Action), target, yesNo(r.Enabled)}
	})
	in.numbered = true
	in.t.color = func(row, col int) (walk.Color, bool) {
		if row < len(rules) && !rules[row].Enabled {
			return colorMuted, true
		}
		return 0, false
	}
	in.t.onSelect = func() { toggleText(toggleIn, in.current() != nil && in.current().Enabled) }
	in.refresh()
	editIn := func() {
		in.edit(func(r *model.RewriteRule) bool { return ruleEditDialog(dlg, "Edit inbound rule", r) })
	}

	// Outbound rules
	out := newEditList(&outbound, func(o model.OutboundRule) []string {
		scope := "Body"
		switch o.Scope {
		case "header":
			scope = "Header " + o.Header
		case "tags":
			scope = "Tags: " + strings.Join(o.Tags, ", ")
		}
		value := o.Value
		if o.Action == "none" {
			value = "(none)"
		}
		return []string{o.Name, scope, negated(o.Negate, o.Match), value, yesNo(o.Enabled)}
	})
	out.numbered = true
	out.t.color = func(row, col int) (walk.Color, bool) {
		if row < len(outbound) && !outbound[row].Enabled {
			return colorMuted, true
		}
		return 0, false
	}
	out.t.onSelect = func() { toggleText(toggleOut, out.current() != nil && out.current().Enabled) }
	out.refresh()
	editOut := func() {
		out.edit(func(o *model.OutboundRule) bool { return outboundEditDialog(dlg, "Edit outbound rule", o) })
	}

	// Rewrite maps
	mp := newEditList(&rmaps, func(x model.RewriteMap) []string {
		return []string{x.Name, x.DefaultValue, strconv.Itoa(len(x.Entries))}
	})
	mp.refresh()
	editMap := func() {
		mp.edit(func(x *model.RewriteMap) bool { return rewriteMapDialog(dlg, "Edit rewrite map", x, rmaps) })
	}

	importRules := func() {
		res, ok := rewriteImportDialog(m, dlg)
		if !ok {
			return
		}
		in.add(res.Rules...)
		out.add(res.OutboundRules...)
		// A map with the name of an existing one replaces it: two maps of
		// the same name would not save.
		for _, x := range res.RewriteMaps {
			replaced := false
			for i := range *mp.items {
				if strings.EqualFold((*mp.items)[i].Name, x.Name) {
					(*mp.items)[i], replaced = cloneMap(x), true
				}
			}
			if replaced {
				mp.refresh()
			} else {
				mp.add(cloneMap(x))
			}
		}
		msg := fmt.Sprintf("Imported %d inbound rules, %d outbound rules and %d rewrite maps. Review them, then choose OK to save.",
			len(res.Rules), len(res.OutboundRules), len(res.RewriteMaps))
		icon, details := walk.TaskDialogSystemIconInformation, ""
		if len(res.Warnings) > 0 {
			msg += "\n\nSome of the input could not be converted: see the details."
			details = "• " + strings.Join(res.Warnings, "\n• ")
			icon = walk.TaskDialogSystemIconWarning
		}
		notify(dlg, "Import rules", "The rules are imported", msg, details, icon)
	}

	runDialogAs(&dlg, m.mw, "URL Rewrite — "+site.Name, Size{Width: 860, Height: 520}, []Widget{
		intro(desktop.IconRoute, "Rewrite, redirect or block requests with ordered rules, and rewrite responses, like the IIS URL Rewrite module. Nothing is saved until you choose OK."),
		TabWidget{Pages: []TabPage{
			{Title: "Inbound rules", Image: img(desktop.IconImport), Layout: VBox{}, Children: []Widget{
				in.view(editIn, []TableViewColumn{col("#", 30), col("Name", 150), col("Match", 170), col("Action", 110), col("Target", 200), col("Enabled", 60)},
					button("Add…", func() {
						r := model.RewriteRule{Enabled: true, Match: "^(.*)$", IgnoreCase: true, Action: "rewrite"}
						if ruleEditDialog(dlg, "Add inbound rule", &r) {
							in.add(r)
						}
					}),
					button("Edit…", editIn),
					button("Remove", in.remove),
					button("Move up", func() { in.move(-1) }),
					button("Move down", func() { in.move(1) }),
					PushButton{AssignTo: &toggleIn, Text: "Disable", OnClicked: func() {
						if r := in.current(); r != nil {
							r.Enabled = !r.Enabled
							in.refresh()
							in.t.onSelect()
						}
					}},
					VSpacer{Size: 8},
					button("Import…", importRules),
				),
				hint("Rules run in this order. Targets may use {R:1} (or $1), {C:1}, server variables such as {HTTP_HOST}, and maps as {MapName:key}. A rewrite to an absolute URL proxies the request there."),
			}},
			{Title: "Outbound rules", Image: img(desktop.IconSend), Layout: VBox{}, Children: []Widget{
				out.view(editOut, []TableViewColumn{col("#", 30), col("Name", 150), col("Scope", 160), col("Match", 160), col("Value", 170), col("Enabled", 60)},
					button("Add…", func() {
						o := model.OutboundRule{Enabled: true, Scope: "tags", Tags: []string{"a", "form", "img"}, IgnoreCase: true, Action: "rewrite"}
						if outboundEditDialog(dlg, "Add outbound rule", &o) {
							out.add(o)
						}
					}),
					button("Edit…", editOut),
					button("Remove", out.remove),
					button("Move up", func() { out.move(-1) }),
					button("Move down", func() { out.move(1) }),
					PushButton{AssignTo: &toggleOut, Text: "Disable", OnClicked: func() {
						if o := out.current(); o != nil {
							o.Enabled = !o.Enabled
							out.refresh()
							out.t.onSelect()
						}
					}},
				),
				hint("Outbound rules rewrite responses: a header such as Location, URLs in HTML, or text in the body."),
			}},
			{Title: "Rewrite maps", Image: img(desktop.IconTag), Layout: VBox{}, Children: []Widget{
				mp.view(editMap, []TableViewColumn{col("Name", 180), col("Default value", 260), col("Entries", 70)},
					button("Add…", func() {
						x := model.RewriteMap{Entries: map[string]string{}}
						if rewriteMapDialog(dlg, "Add rewrite map", &x, rmaps) {
							mp.add(x)
						}
					}),
					button("Edit…", editMap),
					button("Remove", mp.remove),
				),
				hint("Use a map in a target or condition as {MapName:{R:1}}. Keys match without regard to case."),
			}},
		}},
	}, func(dlg *walk.Dialog) bool {
		return m.updateSiteNow(dlg, siteID, "URL Rewrite", func(s *model.Site) {
			s.Routing.Rewrites, s.Routing.OutboundRules, s.Routing.RewriteMaps = rules, outbound, rmaps
		})
	})
}

func toggleText(b *walk.PushButton, enabled bool) {
	if b == nil {
		return
	}
	if enabled {
		b.SetText("Disable")
	} else {
		b.SetText("Enable")
	}
}

func statusText(code int) string {
	if code == 0 {
		return ""
	}
	return strconv.Itoa(code)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return strings.TrimSpace(s)
}

// ---- conditions, shared by inbound and outbound rules

func conditionsBox(owner **walk.Dialog, conds *[]model.RewriteCondition, grouping **walk.ComboBox, matchAny bool) GroupBox {
	l := newEditList(conds, func(c model.RewriteCondition) []string {
		kind := c.MatchType
		if i := slices.Index(matchTypes, kind); i >= 0 {
			kind = matchTypeNames[i]
		} else if kind == "" {
			kind = matchTypeNames[0]
		}
		if c.Negate {
			kind = "Not: " + kind
		}
		return []string{c.Input, kind, c.Pattern}
	})
	l.minHeight = 70
	l.refresh()
	edit := func() {
		l.edit(func(c *model.RewriteCondition) bool { return conditionDialog(*owner, "Edit condition", c) })
	}
	return GroupBox{Title: "Conditions", Layout: VBox{}, Children: []Widget{
		Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
			Label{Text: "Logical grouping:"},
			ComboBox{AssignTo: grouping, Model: []string{"Match all", "Match any"}, CurrentIndex: map[bool]int{true: 1}[matchAny]},
			HSpacer{},
		}},
		l.view(edit, []TableViewColumn{col("Input", 150), col("Type", 150), col("Pattern", 200)},
			button("Add…", func() {
				c := model.RewriteCondition{Input: "{HTTP_HOST}", MatchType: "pattern", IgnoreCase: true}
				if conditionDialog(*owner, "Add condition", &c) {
					l.add(c)
				}
			}),
			button("Edit…", edit),
			button("Remove", l.remove),
		),
	}}
}

func conditionDialog(owner walk.Form, title string, c *model.RewriteCondition) bool {
	var input, kind *walk.ComboBox
	var pattern *walk.LineEdit
	var negate, ignoreCase *walk.CheckBox
	isPattern := c.MatchType == "" || c.MatchType == "pattern"
	return runDialog(owner, title, Size{Width: 460}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Condition input:"}, ComboBox{AssignTo: &input, Editable: true, Model: conditionInputs, Value: c.Input},
			Label{Text: "Check if input string:"}, ComboBox{AssignTo: &kind, Model: matchTypeNames, CurrentIndex: max(slices.Index(matchTypes, c.MatchType), 0),
				OnCurrentIndexChanged: func() {
					if pattern != nil && ignoreCase != nil {
						on := kind.CurrentIndex() <= 0
						pattern.SetEnabled(on)
						ignoreCase.SetEnabled(on)
					}
				}},
			Label{Text: "Pattern:"}, LineEdit{AssignTo: &pattern, Text: c.Pattern, Enabled: isPattern, CueBanner: `^www\.example\.com$`},
			Label{}, CheckBox{AssignTo: &ignoreCase, Text: "Ignore case", Checked: c.IgnoreCase, Enabled: isPattern},
			Label{}, CheckBox{AssignTo: &negate, Text: "Negate (the condition holds when this does not match)", Checked: c.Negate},
		}},
		hint("The input may combine text and server variables, e.g. {HTTP_HOST}{REQUEST_URI}."),
	}, func(dlg *walk.Dialog) bool {
		inp := strings.TrimSpace(input.Text())
		if inp == "" {
			return invalid(dlg, "Enter the condition input, such as {HTTP_HOST}.")
		}
		mt := matchTypes[max(kind.CurrentIndex(), 0)]
		c.Input, c.MatchType, c.Negate = inp, mt, negate.Checked()
		c.Pattern, c.IgnoreCase = "", false
		if _, err := regexp.Compile(pattern.Text()); mt == "pattern" && err != nil {
			return invalid(dlg, "The pattern is not a valid regular expression: "+err.Error())
		}
		if mt == "pattern" {
			c.Pattern, c.IgnoreCase = pattern.Text(), ignoreCase.Checked()
		}
		return true
	})
}

// ---- inbound rule

func ruleEditDialog(owner walk.Form, title string, r *model.RewriteRule) bool {
	var dlg *walk.Dialog
	var name, match, host, target, contentType *walk.LineEdit
	var enabled, negate, ignoreCase, preserveHost, stop *walk.CheckBox
	var grouping, action, query *walk.ComboBox
	var status *walk.NumberEdit
	var body *walk.TextEdit
	conds := slices.Clone(r.Conditions)

	// Which fields an action uses.
	uses := func(a string) (target, query, preserve, status, respond bool) {
		return a == "rewrite" || a == "redirect", a == "rewrite" || a == "redirect", a == "rewrite",
			a == "redirect" || a == "block" || a == "respond", a == "respond"
	}
	t0, q0, p0, s0, r0 := uses(r.Action)
	onAction := func() {
		if body == nil {
			return
		}
		t, q, p, s, resp := uses(ruleActions[max(action.CurrentIndex(), 0)])
		target.SetEnabled(t)
		query.SetEnabled(q)
		preserveHost.SetEnabled(p)
		status.SetEnabled(s)
		contentType.SetEnabled(resp)
		body.SetEnabled(resp)
	}

	return runDialogAs(&dlg, owner, title, Size{Width: 640, Height: 600}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Name:"}, LineEdit{AssignTo: &name, Text: r.Name},
			Label{}, CheckBox{AssignTo: &enabled, Text: "Enabled", Checked: r.Enabled},
		}},
		GroupBox{Title: "Match URL", Layout: Grid{Columns: 2}, Children: []Widget{
			Label{Text: "Pattern (regular expression):"}, LineEdit{AssignTo: &match, Text: r.Match, CueBanner: "^/blog/(.*)$ (the path, including its leading /)"},
			Label{}, Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				CheckBox{AssignTo: &ignoreCase, Text: "Ignore case", Checked: r.IgnoreCase},
				CheckBox{AssignTo: &negate, Text: "Negate (the rule applies when the URL does not match)", Checked: r.Negate},
				HSpacer{},
			}},
			Label{Text: "Host name pattern:"}, LineEdit{AssignTo: &host, Text: r.Host, CueBanner: "(any host) e.g. ^www\\.example\\.com$"},
		}},
		conditionsBox(&dlg, &conds, &grouping, r.MatchAny),
		GroupBox{Title: "Action", Layout: Grid{Columns: 2}, Children: []Widget{
			Label{Text: "Action type:"}, ComboBox{AssignTo: &action, Model: ruleActionNames, CurrentIndex: max(slices.Index(ruleActions, r.Action), 0), OnCurrentIndexChanged: onAction},
			Label{Text: "Target URL:"}, LineEdit{AssignTo: &target, Text: r.Target, Enabled: t0, CueBanner: "/index.php?page={R:1} or https://backend.local/{R:1}"},
			Label{Text: "Query string:"}, ComboBox{AssignTo: &query, Model: queryModeNames, CurrentIndex: max(slices.Index(queryModes, r.QueryString), 0), Enabled: q0},
			Label{}, CheckBox{AssignTo: &preserveHost, Text: "Preserve the original Host header (rewrite to an absolute URL)", Checked: r.PreserveHost, Enabled: p0},
			Label{Text: "Status code:"}, Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				NumberEdit{AssignTo: &status, Value: float64(r.StatusCode), MinValue: 0, MaxValue: 599, Enabled: s0, MaxSize: Size{Width: 80}},
				hint("0 = default. Redirect: 301, 302, 303, 307, 308. Abort: 400–599."),
			}},
			Label{Text: "Content type:"}, LineEdit{AssignTo: &contentType, Text: r.ContentType, Enabled: r0, CueBanner: "text/plain"},
			Label{Text: "Response body:"}, TextEdit{AssignTo: &body, Text: strings.ReplaceAll(r.Body, "\n", "\r\n"), Enabled: r0, VScroll: true, MinSize: Size{Height: 50}},
			Label{}, CheckBox{AssignTo: &stop, Text: "Stop processing of subsequent rules", Checked: r.Stop},
		}},
	}, func(dlg *walk.Dialog) bool {
		if _, err := regexp.Compile(match.Text()); err != nil {
			return invalid(dlg, "The pattern is not a valid regular expression: "+err.Error())
		}
		if _, err := regexp.Compile(strings.TrimSpace(host.Text())); err != nil {
			return invalid(dlg, "The host name pattern is not a valid regular expression: "+err.Error())
		}
		a := ruleActions[max(action.CurrentIndex(), 0)]
		tg := strings.TrimSpace(target.Text())
		if (a == "rewrite" || a == "redirect") && tg == "" {
			return invalid(dlg, "Enter the URL to "+a+" to.")
		}
		code := int(status.Value())
		switch {
		case a == "redirect" && code != 0 && !slices.Contains([]int{301, 302, 303, 307, 308}, code):
			return invalid(dlg, "A redirect's status code is 301, 302, 303, 307 or 308.")
		case a == "block" && code != 0 && code < 400:
			return invalid(dlg, "An aborted request's status code is between 400 and 599.")
		case a == "respond" && code != 0 && code < 200:
			return invalid(dlg, "A custom response's status code is between 200 and 599.")
		}
		t, q, p, s, resp := uses(a)
		*r = model.RewriteRule{
			Name:       strings.TrimSpace(name.Text()),
			Enabled:    enabled.Checked(),
			Match:      match.Text(),
			Negate:     negate.Checked(),
			IgnoreCase: ignoreCase.Checked(),
			Host:       strings.TrimSpace(host.Text()),
			Conditions: conds,
			MatchAny:   grouping.CurrentIndex() == 1,
			Action:     a,
			Stop:       stop.Checked(),
		}
		if t {
			r.Target = tg
		}
		if q {
			r.QueryString = queryModes[max(query.CurrentIndex(), 0)]
		}
		if p {
			r.PreserveHost = preserveHost.Checked()
		}
		if s {
			r.StatusCode = code
		}
		if resp {
			r.ContentType = strings.TrimSpace(contentType.Text())
			r.Body = strings.ReplaceAll(body.Text(), "\r\n", "\n")
		}
		return true
	})
}

// ---- outbound rule

func outboundEditDialog(owner walk.Form, title string, o *model.OutboundRule) bool {
	var dlg *walk.Dialog
	var name, header, match, value *walk.LineEdit
	var enabled, negate, ignoreCase, stop *walk.CheckBox
	var grouping, scope, action *walk.ComboBox
	conds := slices.Clone(o.Conditions)

	tags := slices.Sorted(maps.Keys(model.OutboundTags))
	boxes := make([]*walk.CheckBox, len(tags))
	var tagWidgets []Widget
	for i, t := range tags {
		tagWidgets = append(tagWidgets, CheckBox{AssignTo: &boxes[i], Text: t, Checked: slices.Contains(o.Tags, t), Enabled: o.Scope == "tags"})
	}
	onScope := func() {
		if boxes[len(boxes)-1] == nil || header == nil {
			return
		}
		sc := outboundScopes[max(scope.CurrentIndex(), 0)]
		header.SetEnabled(sc == "header")
		for _, b := range boxes {
			b.SetEnabled(sc == "tags")
		}
	}

	return runDialogAs(&dlg, owner, title, Size{Width: 620, Height: 540}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Name:"}, LineEdit{AssignTo: &name, Text: o.Name},
			Label{}, CheckBox{AssignTo: &enabled, Text: "Enabled", Checked: o.Enabled},
		}},
		GroupBox{Title: "Match", Layout: Grid{Columns: 2}, Children: []Widget{
			Label{Text: "Matching scope:"}, ComboBox{AssignTo: &scope, Model: outboundScopeNames, CurrentIndex: max(slices.Index(outboundScopes, o.Scope), 0), OnCurrentIndexChanged: onScope},
			Label{Text: "Header name:"}, LineEdit{AssignTo: &header, Text: o.Header, Enabled: o.Scope == "header", CueBanner: "Location"},
			Label{Text: "Tags:"}, Composite{Layout: Grid{Columns: 6, MarginsZero: true}, Children: tagWidgets},
			Label{Text: "Pattern (regular expression):"}, LineEdit{AssignTo: &match, Text: o.Match, CueBanner: `^http://backend\.local/(.*)`},
			Label{}, Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				CheckBox{AssignTo: &ignoreCase, Text: "Ignore case", Checked: o.IgnoreCase},
				CheckBox{AssignTo: &negate, Text: "Negate", Checked: o.Negate},
				HSpacer{},
			}},
		}},
		conditionsBox(&dlg, &conds, &grouping, o.MatchAny),
		GroupBox{Title: "Action", Layout: Grid{Columns: 2}, Children: []Widget{
			Label{Text: "Action type:"}, ComboBox{AssignTo: &action, Model: []string{"Rewrite", "None"}, CurrentIndex: map[bool]int{true: 1}[o.Action == "none"],
				OnCurrentIndexChanged: func() {
					if value != nil {
						value.SetEnabled(action.CurrentIndex() == 0)
					}
				}},
			Label{Text: "Value:"}, LineEdit{AssignTo: &value, Text: o.Value, Enabled: o.Action != "none", CueBanner: "https://www.example.com/{R:1}"},
			Label{}, CheckBox{AssignTo: &stop, Text: "Stop processing of subsequent rules", Checked: o.Stop},
		}},
	}, func(dlg *walk.Dialog) bool {
		if _, err := regexp.Compile(match.Text()); err != nil {
			return invalid(dlg, "The pattern is not a valid regular expression: "+err.Error())
		}
		sc := outboundScopes[max(scope.CurrentIndex(), 0)]
		res := model.OutboundRule{
			Name:       strings.TrimSpace(name.Text()),
			Enabled:    enabled.Checked(),
			Scope:      sc,
			Match:      match.Text(),
			Negate:     negate.Checked(),
			IgnoreCase: ignoreCase.Checked(),
			Conditions: conds,
			MatchAny:   grouping.CurrentIndex() == 1,
			Action:     []string{"rewrite", "none"}[max(action.CurrentIndex(), 0)],
			Stop:       stop.Checked(),
		}
		switch sc {
		case "header":
			if res.Header = strings.TrimSpace(header.Text()); res.Header == "" {
				return invalid(dlg, "Enter the name of the response header to rewrite, such as Location.")
			}
		case "tags":
			for i, b := range boxes {
				if b.Checked() {
					res.Tags = append(res.Tags, tags[i])
				}
			}
			if len(res.Tags) == 0 {
				return invalid(dlg, "Choose at least one HTML tag.")
			}
		}
		if res.Action == "rewrite" {
			res.Value = value.Text()
		}
		*o = res
		return true
	})
}

// ---- rewrite map

func rewriteMapDialog(owner walk.Form, title string, x *model.RewriteMap, all []model.RewriteMap) bool {
	var name, def *walk.LineEdit
	var entries *walk.TextEdit
	var text []string
	for _, k := range slices.Sorted(maps.Keys(x.Entries)) {
		text = append(text, k+"="+x.Entries[k])
	}
	return runDialog(owner, title, Size{Width: 520, Height: 420}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Name:"}, LineEdit{AssignTo: &name, Text: x.Name, CueBanner: "Redirects"},
			Label{Text: "Default value:"}, LineEdit{AssignTo: &def, Text: x.DefaultValue, CueBanner: "(empty) returned for keys not in the map"},
		}},
		Label{Text: "Entries, one per line as key=value (e.g. /old-page=/new-page):"},
		TextEdit{AssignTo: &entries, Text: joinLines(text), VScroll: true, HScroll: true, Font: Font{Family: "Consolas", PointSize: 9}},
	}, func(dlg *walk.Dialog) bool {
		n := strings.TrimSpace(name.Text())
		if n == "" || strings.ContainsAny(n, " :{}") {
			return invalid(dlg, "Enter a map name without spaces, such as Redirects.")
		}
		for _, o := range all {
			if strings.EqualFold(o.Name, n) && !strings.EqualFold(x.Name, n) {
				return invalid(dlg, "Another map is called "+o.Name+".")
			}
		}
		e := map[string]string{}
		for i, l := range lines(entries.Text()) {
			k, v, ok := strings.Cut(l, "=")
			if !ok || strings.TrimSpace(k) == "" {
				return invalid(dlg, fmt.Sprintf("Line %d is not key=value: %s", i+1, l))
			}
			e[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		x.Name, x.DefaultValue, x.Entries = n, def.Text(), e
		return true
	})
}

// ---- import from web.config or .htaccess

func rewriteImportDialog(m *manager, owner walk.Form) (model.RewriteImport, bool) {
	formats := []string{"webconfig", "htaccess"}
	var format *walk.ComboBox
	var text *walk.TextEdit
	var dlgp *walk.Dialog
	var res model.RewriteImport
	ok := runDialogAs(&dlgp, owner, "Import rules", Size{Width: 640, Height: 460}, []Widget{
		Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
			Label{Text: "Format:"},
			ComboBox{AssignTo: &format, Model: []string{"IIS web.config (<rewrite> section)", "Apache .htaccess (mod_rewrite)"}, CurrentIndex: 0},
			HSpacer{},
			button("Load from file…", func() {
				fd := walk.FileDialog{Title: "Import rewrite rules", Filter: "Configuration files (web.config;.htaccess;*.config;*.conf)|web.config;.htaccess;*.config;*.conf|All files (*.*)|*.*"}
				if ok, _ := fd.ShowOpen(dlgp); !ok {
					return
				}
				data, err := os.ReadFile(fd.FilePath)
				if err != nil {
					walk.MsgBox(dlgp, "Import rules", err.Error(), walk.MsgBoxIconError)
					return
				}
				if strings.Contains(strings.ToLower(fd.FilePath), "htaccess") {
					format.SetCurrentIndex(1)
				} else if strings.HasSuffix(strings.ToLower(fd.FilePath), ".config") {
					format.SetCurrentIndex(0)
				}
				s := strings.ReplaceAll(string(data), "\r\n", "\n")
				text.SetText(strings.ReplaceAll(s, "\n", "\r\n"))
			}),
		}},
		Label{Text: "Paste the rules to convert:"},
		TextEdit{AssignTo: &text, VScroll: true, HScroll: true, Font: Font{Family: "Consolas", PointSize: 9}},
		hint("The converted rules are added to the lists; nothing is saved until you choose OK in URL Rewrite."),
	}, func(dlg *walk.Dialog) bool {
		src := strings.ReplaceAll(text.Text(), "\r\n", "\n")
		if strings.TrimSpace(src) == "" {
			return invalid(dlg, "Paste the rules to import, or load them from a file.")
		}
		req := model.RewriteImportRequest{Format: formats[max(format.CurrentIndex(), 0)], Text: src}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var out model.RewriteImport
		if err := m.cl.Post(ctx, "/api/rewrite/import", req, &out); err != nil {
			m.errorBoxFor(dlg, "Import rules", err)
			return false
		}
		if len(out.Rules)+len(out.OutboundRules)+len(out.RewriteMaps) == 0 {
			msg := "No rules were found in the text."
			if len(out.Warnings) > 0 {
				msg += "\n\n• " + strings.Join(out.Warnings, "\n• ")
			}
			walk.MsgBox(dlg, "Import rules", msg, walk.MsgBoxIconWarning)
			return false
		}
		res = out
		return true
	})
	return res, ok
}
