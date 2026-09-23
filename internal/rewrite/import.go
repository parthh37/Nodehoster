package rewrite

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

// Import converts rules written for IIS (web.config) or Apache (.htaccess)
// into NodeHoster rules. What cannot be expressed is left out and
// explained in the warnings; nothing is saved.
func Import(req model.RewriteImportRequest) (model.RewriteImport, error) {
	var (
		out model.RewriteImport
		err error
	)
	switch req.Format {
	case "webconfig":
		out, err = importWebConfig(req.Text)
	case "htaccess":
		out = importHtaccess(req.Text)
	default:
		return out, &model.ValidationError{Field: "format", Message: "must be webconfig or htaccess"}
	}
	if err != nil {
		return out, &model.ValidationError{Field: "text", Message: err.Error()}
	}
	// Anything that still does not compile (RE2 has no look-around, for
	// example) is dropped with a warning rather than failing the import.
	kept := out.Rules[:0]
	for _, r := range out.Rules {
		cfg := model.RoutingConfig{Rewrites: []model.RewriteRule{r}, RewriteMaps: out.RewriteMaps}
		cfg.Rewrites[0].Enabled = true
		if err := cfg.ValidateRewrites(); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Rule %q was skipped: %s", r.Name, fieldMessage(err)))
			continue
		}
		if err := Validate(cfg); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Rule %q was skipped: %s", r.Name, fieldMessage(err)))
			continue
		}
		kept = append(kept, r)
	}
	out.Rules = kept
	keptOut := out.OutboundRules[:0]
	for _, r := range out.OutboundRules {
		cfg := model.RoutingConfig{OutboundRules: []model.OutboundRule{r}, RewriteMaps: out.RewriteMaps}
		if err := cfg.ValidateRewrites(); err == nil {
			err = Validate(cfg)
		}
		if err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Outbound rule %q was skipped: %s", r.Name, fieldMessage(err)))
			continue
		}
		keptOut = append(keptOut, r)
	}
	out.OutboundRules = keptOut
	if out.Rules == nil {
		out.Rules = []model.RewriteRule{}
	}
	if out.OutboundRules == nil {
		out.OutboundRules = []model.OutboundRule{}
	}
	if out.RewriteMaps == nil {
		out.RewriteMaps = []model.RewriteMap{}
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	if len(out.Rules)+len(out.OutboundRules)+len(out.RewriteMaps) == 0 && len(out.Warnings) == 0 {
		out.Warnings = append(out.Warnings, "No rewrite rules were found.")
	}
	return out, nil
}

func fieldMessage(err error) string {
	var ve *model.ValidationError
	if errors.As(err, &ve) {
		return ve.Message
	}
	return err.Error()
}

// ---- IIS web.config

type xBool string

func (b xBool) or(def bool) bool {
	switch strings.ToLower(strings.TrimSpace(string(b))) {
	case "true":
		return true
	case "false":
		return false
	}
	return def
}

type xCond struct {
	Input      string `xml:"input,attr"`
	Pattern    string `xml:"pattern,attr"`
	Negate     xBool  `xml:"negate,attr"`
	IgnoreCase xBool  `xml:"ignoreCase,attr"`
	MatchType  string `xml:"matchType,attr"`
}

type xConds struct {
	Grouping string  `xml:"logicalGrouping,attr"`
	TrackAll xBool   `xml:"trackAllCaptures,attr"`
	Add      []xCond `xml:"add"`
}

type xRule struct {
	Name          string `xml:"name,attr"`
	Enabled       xBool  `xml:"enabled,attr"`
	Stop          xBool  `xml:"stopProcessing,attr"`
	PatternSyntax string `xml:"patternSyntax,attr"`
	PreCondition  string `xml:"preCondition,attr"`
	Match         struct {
		URL            string `xml:"url,attr"`
		Pattern        string `xml:"pattern,attr"`
		ServerVariable string `xml:"serverVariable,attr"`
		FilterByTags   string `xml:"filterByTags,attr"`
		CustomTags     string `xml:"customTags,attr"`
		IgnoreCase     xBool  `xml:"ignoreCase,attr"`
		Negate         xBool  `xml:"negate,attr"`
	} `xml:"match"`
	Conditions      xConds `xml:"conditions"`
	ServerVariables struct {
		Set []struct {
			Name string `xml:"name,attr"`
		} `xml:"set"`
	} `xml:"serverVariables"`
	Action struct {
		Type              string `xml:"type,attr"`
		URL               string `xml:"url,attr"`
		Value             string `xml:"value,attr"`
		AppendQueryString xBool  `xml:"appendQueryString,attr"`
		RedirectType      string `xml:"redirectType,attr"`
		StatusCode        string `xml:"statusCode,attr"`
		StatusReason      string `xml:"statusReason,attr"`
		StatusDescription string `xml:"statusDescription,attr"`
	} `xml:"action"`
}

type xMap struct {
	Name         string `xml:"name,attr"`
	DefaultValue string `xml:"defaultValue,attr"`
	Add          []struct {
		Key   string `xml:"key,attr"`
		Value string `xml:"value,attr"`
	} `xml:"add"`
}

type xPre struct {
	Name string `xml:"name,attr"`
}

type xRewrite struct {
	Rules         []xRule   `xml:"rules>rule"`
	GlobalRules   []xRule   `xml:"globalRules>rule"`
	Outbound      []xRule   `xml:"outboundRules>rule"`
	PreConditions []xPre    `xml:"outboundRules>preConditions>preCondition"`
	Maps          []xMap    `xml:"rewriteMaps>rewriteMap"`
	Providers     *struct{} `xml:"providers"`
}

// importWebConfig accepts a whole web.config, a <rewrite> section, or a
// lone <rules>, <outboundRules> or <rewriteMaps> element.
func importWebConfig(text string) (model.RewriteImport, error) {
	var out model.RewriteImport
	var xr xRewrite
	d := xml.NewDecoder(strings.NewReader(text))
	d.Strict = false
	found := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, fmt.Errorf("not valid XML: %v", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var derr error
		switch se.Name.Local {
		case "rewrite":
			var r xRewrite
			derr = d.DecodeElement(&r, &se)
			xr.Rules = append(xr.Rules, r.Rules...)
			xr.GlobalRules = append(xr.GlobalRules, r.GlobalRules...)
			xr.Outbound = append(xr.Outbound, r.Outbound...)
			xr.PreConditions = append(xr.PreConditions, r.PreConditions...)
			xr.Maps = append(xr.Maps, r.Maps...)
			if r.Providers != nil {
				out.Warnings = append(out.Warnings, "Rewrite providers are not supported and were ignored.")
			}
		case "rules", "globalRules":
			var r struct {
				Rules []xRule `xml:"rule"`
			}
			derr = d.DecodeElement(&r, &se)
			xr.Rules = append(xr.Rules, r.Rules...)
		case "outboundRules":
			var r struct {
				Rules []xRule `xml:"rule"`
				Pre   []xPre  `xml:"preConditions>preCondition"`
			}
			derr = d.DecodeElement(&r, &se)
			xr.Outbound = append(xr.Outbound, r.Rules...)
			xr.PreConditions = append(xr.PreConditions, r.Pre...)
		case "rewriteMaps":
			var r struct {
				Maps []xMap `xml:"rewriteMap"`
			}
			derr = d.DecodeElement(&r, &se)
			xr.Maps = append(xr.Maps, r.Maps...)
		default:
			continue
		}
		if derr != nil {
			return out, fmt.Errorf("not valid XML: %v", derr)
		}
		found = true
	}
	if !found {
		return out, errors.New("no <rewrite> section was found")
	}

	for _, m := range xr.Maps {
		rm := model.RewriteMap{Name: m.Name, DefaultValue: m.DefaultValue, Entries: map[string]string{}}
		for _, a := range m.Add {
			rm.Entries[a.Key] = a.Value
		}
		out.RewriteMaps = append(out.RewriteMaps, rm)
	}
	for i, x := range append(xr.Rules, xr.GlobalRules...) {
		r, warns := convertIISRule(x, i)
		out.Warnings = append(out.Warnings, warns...)
		if r != nil {
			out.Rules = append(out.Rules, *r)
		}
	}
	for i, x := range xr.Outbound {
		r, warns := convertIISOutbound(x, i)
		out.Warnings = append(out.Warnings, warns...)
		if r != nil {
			out.OutboundRules = append(out.OutboundRules, *r)
		}
	}
	for _, p := range xr.PreConditions {
		if !strings.Contains(strings.ToLower(p.Name), "html") {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Precondition %q was not imported: tag rules apply to HTML responses and body rules to text responses.", p.Name))
		}
	}
	return out, nil
}

func ruleName(name string, i int) string {
	if strings.TrimSpace(name) == "" {
		return fmt.Sprintf("Imported rule %d", i+1)
	}
	return name
}

func convertIISRule(x xRule, i int) (*model.RewriteRule, []string) {
	name := ruleName(x.Name, i)
	var warns []string
	warn := func(f string, a ...any) { warns = append(warns, fmt.Sprintf("Rule %q: ", name)+fmt.Sprintf(f, a...)) }

	match, err := iisPattern(x.Match.URL, x.PatternSyntax)
	if err != nil {
		warn("%v; skipped.", err)
		return nil, warns
	}
	r := &model.RewriteRule{
		Name:       name,
		Enabled:    x.Enabled.or(true),
		Match:      match,
		Negate:     x.Match.Negate.or(false),
		IgnoreCase: x.Match.IgnoreCase.or(true),
		MatchAny:   strings.EqualFold(x.Conditions.Grouping, "MatchAny"),
		Stop:       x.Stop.or(false),
	}
	if x.Conditions.TrackAll.or(false) {
		warn("trackAllCaptures is not supported; {C:n} refers to the last matched condition.")
	}
	for _, c := range x.Conditions.Add {
		r.Conditions = append(r.Conditions, iisCondition(c))
	}
	if len(x.ServerVariables.Set) > 0 {
		var names []string
		for _, s := range x.ServerVariables.Set {
			names = append(names, s.Name)
		}
		warn("setting server variables (%s) is not supported; use the site's request header rules instead.", strings.Join(names, ", "))
	}
	appendQuery := "append"
	if !x.Action.AppendQueryString.or(true) {
		appendQuery = "discard"
	}
	switch strings.ToLower(x.Action.Type) {
	case "rewrite":
		r.Action, r.Target, r.QueryString = "rewrite", x.Action.URL, appendQuery
	case "redirect":
		r.Action, r.Target, r.QueryString = "redirect", siteRelative(x.Action.URL), appendQuery
		switch strings.ToLower(x.Action.RedirectType) {
		case "", "permanent":
			r.StatusCode = 301
		case "found":
			r.StatusCode = 302
		case "seeother":
			r.StatusCode = 303
		case "temporary":
			r.StatusCode = 307
		default:
			warn("redirect type %q is unknown; using 301.", x.Action.RedirectType)
			r.StatusCode = 301
		}
	case "customresponse":
		code, _ := strconv.Atoi(x.Action.StatusCode)
		if code >= 400 {
			r.Action, r.StatusCode = "block", code
		} else {
			if code == 0 {
				code = 200
			}
			r.Action, r.StatusCode = "respond", code
			r.Body = x.Action.StatusDescription
			if r.Body == "" {
				r.Body = x.Action.StatusReason
			}
		}
	case "abortrequest":
		r.Action, r.StatusCode = "block", 403
		warn("AbortRequest closes the connection in IIS; here the request is answered with 403.")
	case "none", "":
		r.Action = "none"
	default:
		warn("action type %q is not supported; skipped.", x.Action.Type)
		return nil, warns
	}
	return r, warns
}

func iisCondition(c xCond) model.RewriteCondition {
	rc := model.RewriteCondition{
		Input:      c.Input,
		MatchType:  "pattern",
		Pattern:    c.Pattern,
		Negate:     c.Negate.or(false),
		IgnoreCase: c.IgnoreCase.or(true),
	}
	switch strings.ToLower(c.MatchType) {
	case "isfile":
		rc.MatchType, rc.Pattern = "isFile", ""
	case "isdirectory":
		rc.MatchType, rc.Pattern = "isDirectory", ""
	}
	return rc
}

func convertIISOutbound(x xRule, i int) (*model.OutboundRule, []string) {
	name := ruleName(x.Name, i)
	var warns []string
	warn := func(f string, a ...any) {
		warns = append(warns, fmt.Sprintf("Outbound rule %q: ", name)+fmt.Sprintf(f, a...))
	}
	pattern := x.Match.Pattern
	switch strings.ToLower(x.PatternSyntax) {
	case "wildcard":
		pattern = wildcardToRegexp(pattern)
	case "exactmatch":
		pattern = "^" + regexp.QuoteMeta(pattern) + "$"
	}
	r := &model.OutboundRule{
		Name:       name,
		Enabled:    x.Enabled.or(true),
		Match:      pattern,
		Negate:     x.Match.Negate.or(false),
		IgnoreCase: x.Match.IgnoreCase.or(true),
		MatchAny:   strings.EqualFold(x.Conditions.Grouping, "MatchAny"),
		Stop:       x.Stop.or(false),
	}
	switch {
	case x.Match.ServerVariable != "":
		v := x.Match.ServerVariable
		if !strings.HasPrefix(strings.ToUpper(v), "RESPONSE_") {
			warn("only RESPONSE_ server variables can be rewritten; skipped.")
			return nil, warns
		}
		r.Scope, r.Header = "header", http.CanonicalHeaderKey(strings.ReplaceAll(v[len("RESPONSE_"):], "_", "-"))
	case strings.TrimSpace(x.Match.FilterByTags) != "":
		for _, t := range strings.Split(x.Match.FilterByTags, ",") {
			t = strings.ToLower(strings.TrimSpace(t))
			if t == "customtags" {
				warn("custom tags are not supported.")
				continue
			}
			if _, ok := model.OutboundTags[t]; ok && t != "" {
				r.Tags = append(r.Tags, t)
			}
		}
		if len(r.Tags) == 0 {
			warn("no supported tags; skipped.")
			return nil, warns
		}
		r.Scope = "tags"
	default:
		r.Scope = "body"
	}
	for _, c := range x.Conditions.Add {
		r.Conditions = append(r.Conditions, iisCondition(c))
	}
	switch strings.ToLower(x.Action.Type) {
	case "rewrite", "":
		r.Action, r.Value = "rewrite", x.Action.Value
	case "none":
		r.Action = "none"
	default:
		warn("action type %q is not supported; skipped.", x.Action.Type)
		return nil, warns
	}
	return r, warns
}

// iisPattern converts an IIS match url, which is tested against the path
// without its leading slash, into a NodeHoster pattern tested against the
// path with it.
func iisPattern(p, syntax string) (string, error) {
	switch strings.ToLower(syntax) {
	case "", "ecmascript":
		return slashPattern(p), nil
	case "wildcard":
		return "^/" + strings.TrimPrefix(wildcardToRegexp(p), "^"), nil
	case "exactmatch":
		return "^/" + regexp.QuoteMeta(strings.TrimPrefix(p, "/")) + "$", nil
	}
	return "", fmt.Errorf("pattern syntax %q is not supported", syntax)
}

// slashPattern adapts a regular expression written for a path without
// its leading slash. An anchored pattern gets the slash after "^"; an
// unanchored one may match anywhere, so it is prefixed with "^/?.*?",
// which consumes the slash without changing what the groups capture.
// Each top-level alternative is adapted separately.
func slashPattern(p string) string {
	alts := splitTopLevel(p)
	for i, a := range alts {
		switch {
		case strings.HasPrefix(a, "^/"):
		case strings.HasPrefix(a, "^"):
			alts[i] = "^/" + a[1:]
		case a == "" || a == ".*":
			// matches every path either way
		default:
			alts[i] = "^/?.*?(?:" + a + ")"
		}
	}
	return strings.Join(alts, "|")
}

// splitTopLevel splits a regular expression at "|" outside groups and
// character classes.
func splitTopLevel(p string) []string {
	var out []string
	depth, class, start := 0, false, 0
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == '\\':
			i++
		case class:
			if c == ']' {
				class = false
			}
		case c == '[':
			class = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == '|' && depth == 0:
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	return append(out, p[start:])
}

func wildcardToRegexp(p string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range p {
		switch r {
		case '*':
			b.WriteString("(.*)")
		case '?':
			b.WriteString("(.)")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return b.String()
}

// siteRelative makes a relative redirect target relative to the site
// root, which is how IIS and mod_rewrite resolve it.
func siteRelative(t string) string {
	if t == "" || strings.HasPrefix(t, "/") || strings.HasPrefix(t, "{") || strings.Contains(t, "://") {
		return t
	}
	return "/" + t
}

// ---- Apache .htaccess

var (
	apacheVarRe  = regexp.MustCompile(`%\{([A-Za-z_]+)(?::([^}]+))?\}`)
	apacheRefRe  = regexp.MustCompile(`([$%])(\d)`)
	apacheMapRe  = regexp.MustCompile(`\$\{([A-Za-z0-9_]+):`)
	apacheVarMap = map[string]string{
		"REQUEST_URI":   "URL", // Apache's REQUEST_URI has no query string
		"THE_REQUEST":   "",
		"DOCUMENT_ROOT": "",
	}
)

type htState struct {
	out   model.RewriteImport
	base  string
	conds []model.RewriteCondition
	ors   []bool
	n     int
	line  int
}

func (s *htState) warn(f string, a ...any) {
	s.out.Warnings = append(s.out.Warnings, fmt.Sprintf("Line %d: ", s.line)+fmt.Sprintf(f, a...))
}

func importHtaccess(text string) model.RewriteImport {
	s := &htState{base: "/"}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		s.line = i + 1
		line := strings.TrimSpace(lines[i])
		for strings.HasSuffix(line, "\\") && i+1 < len(lines) {
			i++
			line = strings.TrimSuffix(line, "\\") + " " + strings.TrimSpace(lines[i])
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		args := splitArgs(line)
		if len(args) == 0 {
			continue
		}
		dir := strings.ToLower(args[0])
		switch {
		case strings.HasPrefix(dir, "<"):
			continue // <IfModule>, </IfModule>: take the contents as they are
		case dir == "rewriteengine":
			if len(args) > 1 && strings.EqualFold(args[1], "off") {
				s.warn("RewriteEngine is off; the rules were imported anyway, disabled rules are not supported per engine.")
			}
		case dir == "rewritebase":
			if len(args) > 1 {
				s.base = "/" + strings.Trim(args[1], "/") + "/"
				if s.base == "//" {
					s.base = "/"
				}
			}
		case dir == "rewritecond":
			s.cond(args[1:])
		case dir == "rewriterule":
			s.rule(args[1:])
		case dir == "rewritemap":
			s.warn("RewriteMap reads external files or programs and is not supported; add a rewrite map in the console instead.")
		case dir == "redirect" || dir == "redirectpermanent" || dir == "redirecttemp" || dir == "redirectmatch":
			s.alias(dir, args[1:])
		case dir == "rewriteoptions":
		default:
			s.warn("%s is not a rewrite directive and was ignored.", args[0])
		}
	}
	if len(s.conds) > 0 {
		s.warn("RewriteCond lines without a following RewriteRule were ignored.")
	}
	return s.out
}

// splitArgs splits a directive into arguments, honouring double quotes.
func splitArgs(line string) []string {
	var args []string
	var cur strings.Builder
	inQ, has := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line) && line[i+1] == '"':
			cur.WriteByte('"')
			i++
			has = true
		case c == '"':
			inQ, has = !inQ, true
		case (c == ' ' || c == '\t') && !inQ:
			if has {
				args = append(args, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteByte(c)
			has = true
		}
	}
	if has {
		args = append(args, cur.String())
	}
	return args
}

func parseFlags(args []string) map[string]string {
	flags := map[string]string{}
	for _, a := range args {
		if !strings.HasPrefix(a, "[") || !strings.HasSuffix(a, "]") {
			continue
		}
		for _, f := range strings.Split(a[1:len(a)-1], ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(f), "=")
			flags[strings.ToUpper(k)] = v
		}
	}
	return flags
}

// apacheExpr converts %{VAR}, $n and %n references to NodeHoster syntax.
func (s *htState) apacheExpr(t string) string {
	t = apacheVarRe.ReplaceAllStringFunc(t, func(m string) string {
		sm := apacheVarRe.FindStringSubmatch(m)
		name, arg := strings.ToUpper(sm[1]), sm[2]
		switch name {
		case "HTTP":
			return "{HTTP_" + strings.ToUpper(strings.ReplaceAll(arg, "-", "_")) + "}"
		case "ENV", "SSL", "LA-U", "LA-F":
			s.warn("%%{%s:%s} is not supported.", name, arg)
			return ""
		}
		if v, ok := apacheVarMap[name]; ok {
			if v == "" {
				s.warn("%%{%s} is not supported.", name)
				return ""
			}
			name = v
		}
		return "{" + name + "}"
	})
	if apacheMapRe.MatchString(t) {
		s.warn("RewriteMap lookups (${map:key}) need a rewrite map of the same name.")
		t = apacheMapRe.ReplaceAllString(t, "{$1:")
	}
	return apacheRefRe.ReplaceAllStringFunc(t, func(m string) string {
		if m[0] == '$' {
			return "{R:" + m[1:] + "}"
		}
		return "{C:" + m[1:] + "}"
	})
}

func (s *htState) cond(args []string) {
	if len(args) < 2 {
		s.warn("RewriteCond needs a test string and a pattern.")
		return
	}
	flags := parseFlags(args[2:])
	c := model.RewriteCondition{Input: s.apacheExpr(args[0]), MatchType: "pattern", IgnoreCase: has(flags, "NC", "NOCASE")}
	p := args[1]
	if strings.HasPrefix(p, "!") {
		c.Negate, p = true, p[1:]
	}
	switch p {
	case "-f", "-F", "-s":
		c.MatchType = "isFile"
	case "-d":
		c.MatchType = "isDirectory"
	case "-l", "-L", "-h", "-x", "-U":
		s.warn("RewriteCond test %s is not supported; treated as a file test.", p)
		c.MatchType = "isFile"
	default:
		switch {
		case strings.HasPrefix(p, "="):
			c.Pattern = "^" + regexp.QuoteMeta(p[1:]) + "$"
		case strings.HasPrefix(p, "<") || strings.HasPrefix(p, ">") || strings.HasPrefix(p, "-"):
			s.warn("comparison %q is not supported; the condition was skipped.", p)
			return
		default:
			c.Pattern = p
		}
	}
	s.conds = append(s.conds, c)
	s.ors = append(s.ors, has(flags, "OR", "ORNEXT"))
}

func has(flags map[string]string, names ...string) bool {
	for _, n := range names {
		if _, ok := flags[n]; ok {
			return true
		}
	}
	return false
}

func (s *htState) rule(args []string) {
	conds, ors := s.conds, s.ors
	s.conds, s.ors = nil, nil
	if len(args) < 2 {
		s.warn("RewriteRule needs a pattern and a substitution.")
		return
	}
	s.n++
	flags := parseFlags(args[2:])
	r := model.RewriteRule{
		Name:       fmt.Sprintf("Imported rule %d", s.n),
		Enabled:    true,
		IgnoreCase: has(flags, "NC", "NOCASE"),
		Conditions: conds,
		Stop:       has(flags, "L", "LAST", "END"),
	}
	p := args[0]
	if strings.HasPrefix(p, "!") {
		r.Negate, p = true, p[1:]
	}
	r.Match = slashPattern(p)
	if len(ors) > 1 {
		anyOr, allOr := false, true
		for _, o := range ors[:len(ors)-1] {
			anyOr = anyOr || o
			allOr = allOr && o
		}
		switch {
		case allOr:
			r.MatchAny = true
		case anyOr:
			s.warn("conditions mixing [OR] and AND cannot be expressed; all conditions must match.")
		}
	}
	for _, f := range []string{"C", "CHAIN", "S", "SKIP", "N", "NEXT", "E", "ENV", "CO", "COOKIE", "T", "TYPE", "H", "HANDLER"} {
		if _, ok := flags[f]; ok {
			s.warn("flag [%s] is not supported and was ignored.", f)
		}
	}
	switch {
	case has(flags, "QSA", "QSAPPEND"):
		r.QueryString = "append"
	case has(flags, "QSD", "QSDISCARD"):
		r.QueryString = "discard"
	}
	sub := args[1]
	target := s.apacheExpr(sub)
	if sub != "-" && !strings.HasPrefix(target, "/") && !isAbsoluteURL(target) && !strings.HasPrefix(target, "{") {
		target = s.base + target
	}
	switch {
	case has(flags, "F", "FORBIDDEN"):
		r.Action, r.StatusCode = "block", 403
	case has(flags, "G", "GONE"):
		r.Action, r.StatusCode = "block", 410
	case has(flags, "R", "REDIRECT"):
		code := 302
		if v := flags["R"] + flags["REDIRECT"]; v != "" {
			switch strings.ToLower(v) {
			case "permanent":
				code = 301
			case "temp":
				code = 302
			case "seeother":
				code = 303
			default:
				if n, err := strconv.Atoi(v); err == nil {
					code = n
				}
			}
		}
		if code >= 400 {
			r.Action, r.StatusCode = "block", code
		} else {
			r.Action, r.StatusCode, r.Target = "redirect", code, target
		}
	case sub == "-":
		r.Action = "none"
	case has(flags, "P", "PROXY"):
		r.Action, r.Target = "rewrite", target
		if !isAbsoluteURL(target) {
			s.warn("[P] needs an absolute URL; the rule rewrites locally instead.")
		}
	case isAbsoluteURL(target):
		// mod_rewrite turns a substitution with a scheme into a redirect.
		r.Action, r.StatusCode, r.Target = "redirect", 302, target
	default:
		r.Action, r.Target = "rewrite", target
	}
	s.out.Rules = append(s.out.Rules, r)
}

// alias converts mod_alias Redirect and RedirectMatch.
func (s *htState) alias(dir string, args []string) {
	code := 302
	switch dir {
	case "redirectpermanent":
		code = 301
	case "redirecttemp":
		code = 302
	}
	if dir == "redirect" || dir == "redirectmatch" {
		if len(args) > 0 {
			switch strings.ToLower(args[0]) {
			case "permanent":
				code, args = 301, args[1:]
			case "temp":
				code, args = 302, args[1:]
			case "seeother":
				code, args = 303, args[1:]
			case "gone":
				code, args = 410, args[1:]
			default:
				if n, err := strconv.Atoi(args[0]); err == nil {
					code, args = n, args[1:]
				}
			}
		}
	}
	if len(args) < 1 || (code != 410 && len(args) < 2) {
		s.warn("%s needs a path and a URL.", dir)
		return
	}
	s.n++
	r := model.RewriteRule{Name: fmt.Sprintf("Imported redirect %d", s.n), Enabled: true, Stop: true}
	if dir == "redirectmatch" {
		r.Match = args[0] // mod_alias matches the full path, slash included
	} else {
		r.Match = "^" + regexp.QuoteMeta(strings.TrimSuffix(args[0], "/")) + "(/.*)?$"
	}
	if code == 410 {
		r.Action, r.StatusCode = "block", 410
	} else {
		target := args[1]
		if dir != "redirectmatch" {
			target = strings.TrimSuffix(target, "/") + "{R:1}"
		}
		r.Action, r.StatusCode, r.Target = "redirect", code, target
	}
	s.out.Rules = append(s.out.Rules, r)
}
