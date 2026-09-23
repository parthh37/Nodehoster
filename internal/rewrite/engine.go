// Package rewrite is NodeHoster's URL Rewrite module, modelled on the IIS
// module of the same name: ordered inbound rules with conditions, server
// variables, back-references and rewrite maps; rewrites to an absolute URL
// that proxy the request (ARR); and outbound rules that rewrite response
// headers and URLs in HTML. The proxy compiles an Engine per site
// configuration and consults it for every request.
package rewrite

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

type condition struct {
	model.RewriteCondition
	input expr
	re    *regexp.Regexp // matchType pattern
}

type inRule struct {
	model.RewriteRule
	re     *regexp.Regexp
	named  map[string]int
	host   *regexp.Regexp
	conds  []condition
	target expr
}

type outRule struct {
	model.OutboundRule
	re    *regexp.Regexp
	named map[string]int
	conds []condition
	value expr
	tags  map[string]bool
}

// Engine is a site's compiled rewrite configuration. It is immutable and
// safe for concurrent use.
type Engine struct {
	in   []*inRule
	out  []*outRule
	maps map[string]*rewriteMap
	root string // physical path of the site for {REQUEST_FILENAME}
}

// Compile builds the engine for a site's routing configuration. root is
// the site's physical directory ("" when it has none). Disabled rules are
// skipped; an invalid rule is an error, which Validate reports with the
// field that caused it before a configuration is ever saved.
func Compile(r model.RoutingConfig, root string) (*Engine, error) {
	e := &Engine{maps: map[string]*rewriteMap{}, root: root}
	for _, m := range r.RewriteMaps {
		rm := &rewriteMap{def: m.DefaultValue, entries: make(map[string]string, len(m.Entries))}
		for k, v := range m.Entries {
			rm.entries[strings.ToLower(k)] = v
		}
		e.maps[strings.ToLower(m.Name)] = rm
	}
	for i, rw := range r.Rewrites {
		if !rw.Enabled {
			continue
		}
		f := fmt.Sprintf("routing.rewrites[%d]", i)
		ir := &inRule{RewriteRule: rw}
		var err error
		if ir.re, err = model.CompilePattern(rw.Match, rw.IgnoreCase); err != nil {
			return nil, &model.ValidationError{Field: f + ".match", Message: err.Error()}
		}
		ir.named = namedGroups(ir.re)
		if rw.Host != "" {
			if ir.host, err = regexp.Compile("(?i)" + rw.Host); err != nil {
				return nil, &model.ValidationError{Field: f + ".host", Message: err.Error()}
			}
		}
		if ir.conds, err = e.compileConds(f, rw.Conditions); err != nil {
			return nil, err
		}
		if rw.Action == "rewrite" || rw.Action == "redirect" {
			if ir.target, err = parseExpr(rw.Target, e.maps); err != nil {
				return nil, &model.ValidationError{Field: f + ".target", Message: err.Error()}
			}
		}
		e.in = append(e.in, ir)
	}
	for i, o := range r.OutboundRules {
		if !o.Enabled {
			continue
		}
		f := fmt.Sprintf("routing.outboundRules[%d]", i)
		or := &outRule{OutboundRule: o, tags: map[string]bool{}}
		var err error
		if or.re, err = model.CompilePattern(o.Match, o.IgnoreCase); err != nil {
			return nil, &model.ValidationError{Field: f + ".match", Message: err.Error()}
		}
		or.named = namedGroups(or.re)
		if or.conds, err = e.compileConds(f, o.Conditions); err != nil {
			return nil, err
		}
		if o.Action == "rewrite" {
			if or.value, err = parseExpr(o.Value, e.maps); err != nil {
				return nil, &model.ValidationError{Field: f + ".value", Message: err.Error()}
			}
		}
		for _, t := range o.Tags {
			or.tags[strings.ToLower(t)] = true
		}
		e.out = append(e.out, or)
	}
	return e, nil
}

// Validate reports the first problem in a routing configuration's rewrite
// rules, such as a reference to a rewrite map that does not exist.
func Validate(r model.RoutingConfig) error {
	// Check disabled rules too, on copies: the slices are the caller's.
	r.Rewrites = slices.Clone(r.Rewrites)
	for i := range r.Rewrites {
		r.Rewrites[i].Enabled = true
	}
	r.OutboundRules = slices.Clone(r.OutboundRules)
	for i := range r.OutboundRules {
		r.OutboundRules[i].Enabled = true
	}
	_, err := Compile(r, "")
	return err
}

func (e *Engine) compileConds(f string, cs []model.RewriteCondition) ([]condition, error) {
	out := make([]condition, 0, len(cs))
	for j, c := range cs {
		cf := fmt.Sprintf("%s.conditions[%d]", f, j)
		cc := condition{RewriteCondition: c}
		var err error
		if cc.input, err = parseExpr(c.Input, e.maps); err != nil {
			return nil, &model.ValidationError{Field: cf + ".input", Message: err.Error()}
		}
		if c.MatchType == "" || c.MatchType == "pattern" {
			if cc.re, err = model.CompilePattern(c.Pattern, c.IgnoreCase); err != nil {
				return nil, &model.ValidationError{Field: cf + ".pattern", Message: err.Error()}
			}
		}
		out = append(out, cc)
	}
	return out, nil
}

func namedGroups(re *regexp.Regexp) map[string]int {
	var m map[string]int
	for i, n := range re.SubexpNames() {
		if n != "" {
			if m == nil {
				m = map[string]int{}
			}
			m[n] = i
		}
	}
	return m
}

// Empty reports whether the engine has nothing to do.
func (e *Engine) Empty() bool { return e == nil || (len(e.in) == 0 && len(e.out) == 0) }

// HasOutbound reports whether responses need to pass through the engine.
func (e *Engine) HasOutbound() bool { return e != nil && len(e.out) > 0 }

// ---- inbound

// Env is what the proxy knows about a request beyond the request itself.
type Env struct {
	ClientIP string
	TLS      bool
	Port     string // local port the request arrived on
}

// Result tells the proxy what to do after the inbound rules ran. A zero
// Action means continue with the (possibly rewritten) request.
type Result struct {
	Action       string // "" | redirect | block | respond | proxy
	Location     string // redirect
	Status       int
	Body         string // respond
	ContentType  string // respond
	ProxyURL     *url.URL
	PreserveHost bool
}

// Inbound runs the inbound rules against r, rewriting r.URL in place.
func (e *Engine) Inbound(r *http.Request, env Env) Result {
	if e == nil || len(e.in) == 0 {
		return Result{}
	}
	original := r.URL.RequestURI()
	sc := &scope{maps: e.maps}
	sc.vars = func(name string) string { return e.requestVar(name, r, env, original) }
	for _, rule := range e.in {
		if rule.host != nil && !rule.host.MatchString(hostOnly(r.Host)) {
			continue
		}
		m := rule.re.FindStringSubmatch(r.URL.Path)
		if (m != nil) == rule.Negate {
			continue
		}
		sc.rule, sc.named, sc.cond = m, rule.named, nil
		if !e.conditionsMatch(rule.conds, rule.MatchAny, sc) {
			continue
		}
		switch rule.Action {
		case "redirect":
			code := rule.StatusCode
			if code == 0 {
				code = http.StatusMovedPermanently
			}
			return Result{Action: "redirect", Status: code, Location: redirectTarget(rule.target.expand(sc), r.URL.RawQuery, rule.QueryString)}
		case "block":
			code := rule.StatusCode
			if code == 0 {
				code = http.StatusForbidden
			}
			return Result{Action: "block", Status: code}
		case "respond":
			code := rule.StatusCode
			if code == 0 {
				code = http.StatusOK
			}
			ct := rule.ContentType
			if ct == "" {
				ct = "text/plain; charset=utf-8"
			}
			return Result{Action: "respond", Status: code, Body: rule.Body, ContentType: ct}
		case "rewrite":
			target := rule.target.expand(sc)
			if isAbsoluteURL(target) {
				u, err := url.Parse(target)
				if err != nil {
					return Result{Action: "block", Status: http.StatusBadGateway}
				}
				u.RawQuery = joinQuery(u.RawQuery, r.URL.RawQuery, u.RawQuery != "" || strings.HasSuffix(target, "?"), rule.QueryString)
				return Result{Action: "proxy", ProxyURL: u, PreserveHost: rule.PreserveHost}
			}
			p, q, hasQ := strings.Cut(target, "?")
			if !strings.HasPrefix(p, "/") {
				p = "/" + p // relative to the site root, as in IIS
			}
			r.URL.Path, r.URL.RawPath = p, ""
			r.URL.RawQuery = joinQuery(q, r.URL.RawQuery, hasQ, rule.QueryString)
		}
		if rule.Stop {
			break
		}
	}
	return Result{}
}

// joinQuery combines a target's query with the request's according to a
// rule's query string mode. The default ("") keeps the original query
// unless the target has one, which is how rules behaved before the mode
// existed.
func joinQuery(target, original string, targetHasQuery bool, mode string) string {
	switch mode {
	case "discard":
		return target
	case "append":
		if target == "" {
			return original
		}
		if original == "" {
			return target
		}
		return target + "&" + original
	default:
		if targetHasQuery {
			return target
		}
		return original
	}
}

func redirectTarget(target, query, mode string) string {
	switch mode {
	case "discard":
		return target
	case "append":
		if query == "" {
			return target
		}
		if strings.Contains(target, "?") {
			return target + "&" + query
		}
		return target + "?" + query
	default:
		if !strings.Contains(target, "?") && query != "" && !strings.Contains(target, "://") {
			return target + "?" + query
		}
		return target
	}
}

func isAbsoluteURL(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

func (e *Engine) conditionsMatch(cs []condition, any bool, sc *scope) bool {
	if len(cs) == 0 {
		return true
	}
	for _, c := range cs {
		ok := e.conditionMatches(c, sc)
		if any && ok {
			return true
		}
		if !any && !ok {
			return false
		}
	}
	return !any
}

func (e *Engine) conditionMatches(c condition, sc *scope) bool {
	in := c.input.expand(sc)
	switch c.MatchType {
	case "isFile", "isDirectory":
		ok := false
		if in != "" {
			if fi, err := os.Stat(in); err == nil {
				ok = fi.IsDir() == (c.MatchType == "isDirectory")
			}
		}
		return ok != c.Negate
	}
	m := c.re.FindStringSubmatch(in)
	if c.Negate {
		return m == nil
	}
	if m == nil {
		return false
	}
	sc.cond = m
	return true
}

// requestVar resolves an IIS server variable for a request.
func (e *Engine) requestVar(name string, r *http.Request, env Env, original string) string {
	switch name {
	case "HTTP_HOST":
		return r.Host
	case "QUERY_STRING":
		return r.URL.RawQuery
	case "URL", "PATH_INFO":
		return r.URL.Path
	case "REQUEST_URI", "UNENCODED_URL":
		return original
	case "REQUEST_METHOD":
		return r.Method
	case "REMOTE_ADDR", "REMOTE_HOST":
		return env.ClientIP
	case "HTTPS":
		if env.TLS {
			return "on"
		}
		return "off"
	case "SERVER_PORT":
		return env.Port
	case "SERVER_PORT_SECURE":
		if env.TLS {
			return "1"
		}
		return "0"
	case "SERVER_NAME":
		return hostOnly(r.Host)
	case "SERVER_PROTOCOL":
		return r.Proto
	case "REQUEST_FILENAME":
		return e.physicalPath(r.URL.Path)
	case "CACHE_URL":
		scheme := "http"
		if env.TLS {
			scheme = "https"
		}
		return scheme + "://" + r.Host + r.URL.RequestURI()
	}
	if h, ok := strings.CutPrefix(name, "HTTP_"); ok {
		return r.Header.Get(strings.ReplaceAll(h, "_", "-"))
	}
	return ""
}

// physicalPath maps a URL path into the site's directory, never outside
// it, the way IIS computes {REQUEST_FILENAME}.
func (e *Engine) physicalPath(p string) string {
	if e.root == "" || strings.ContainsAny(p, "\\:\x00") {
		return ""
	}
	root, err := filepath.Abs(e.root)
	if err != nil {
		return ""
	}
	return filepath.Join(root, filepath.FromSlash(path.Clean("/"+p)))
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}
