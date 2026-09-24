// Package waf is NodeHoster's web application firewall: it inspects
// requests for SQL injection, cross-site scripting, path traversal,
// command injection and similar attacks, scores them the way the OWASP
// Core Rule Set does (anomaly scoring) and tells the proxy whether to block.
//
// It is a purpose-built engine rather than Coraza running the CRS: the
// rules are compiled Go regular expressions (RE2, linear time) gated by a
// single Aho-Corasick pass over each value, which keeps a typical request
// in the low microseconds and adds no dependency; the CRS's rule language,
// its transformation pipeline and its PCRE-flavoured patterns would bring
// several megabytes of rules and code to a single-binary Windows service
// that only needs the attack classes a Node.js host sees.
//
// What is inspected: the path, query string arguments (names and values,
// decoded leniently, as Node.js does), cookies, the User-Agent and Referer
// (and, for a few rules such as Log4Shell and Shellshock, every other
// header but Authorization), and bodies that are form-encoded, JSON,
// multipart (fields and file names) or text, gzip- or deflate-compressed
// or not, up to a limit; the rest of a body streams to the application
// uninspected.
//
// Inspection fails closed: a request the engine cannot inspect completely
// (too many values, too costly, a body in an encoding it cannot read) is
// refused in block mode whatever its score, and logged in detect mode.
package waf

import (
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

const (
	// maxArgs bounds the argument names and values inspected per request
	// (query string, form fields, JSON keys and strings, multipart fields),
	// maxHeaderValues the headers and cookies, counted apart so that one
	// cannot use up the other. Beyond either, rule 920210 fires and the
	// request fails closed, unless the rule is excluded: then every value
	// is inspected (the work budget still applies).
	maxArgs         = 2048
	maxHeaderValues = 1024
	// maxValueLen bounds what is inspected of one value outside the body
	// (a header or cookie can be up to the server's header limit).
	maxValueLen = 64 << 10
	// minWorkBudget and workPerBodyByte bound the text run through regular
	// expressions per request (see inspection.work): matching is linear,
	// but a body stuffed with every rule's literals would otherwise send
	// all of it through every rule. Ordinary requests use a small fraction.
	minWorkBudget   = 4 << 20
	workPerBodyByte = 32
)

// ruleSet is a set of rule indexes.
type ruleSet [4]uint64

func (s *ruleSet) add(i int)      { s[i>>6] |= 1 << (i & 63) }
func (s *ruleSet) has(i int) bool { return s[i>>6]&(1<<(i&63)) != 0 }
func (s *ruleSet) or(o *ruleSet) {
	for i := range s {
		s[i] |= o[i]
	}
}
func (s *ruleSet) covers(o *ruleSet) bool {
	for i := range s {
		if o[i]&^s[i] != 0 {
			return false
		}
	}
	return true
}

func init() {
	if len(defs) > len(ruleSet{})*64 {
		panic("waf: too many rules for ruleSet")
	}
}

// Engine is a site's compiled firewall. It is immutable and safe for
// concurrent use.
type Engine struct {
	mode      string
	threshold int
	paranoia  int
	bodyLimit int
	budget    int // see minWorkBudget

	byTarget [nTargets][]*rule // active rules that apply to each target
	active   ruleSet           // every active rule, request-level ones included
	all      ruleSet           // every rule: what "all rules" excludes
	excl     []exclusion
}

type exclusion struct {
	path                   string  // cleaned and lowercase (cleanPrefix); "" = everywhere
	off                    bool    // nothing listed: the firewall is off under path
	rules                  ruleSet // the rules turned off (for names: not applied to them)
	args, cookies, headers []namePattern
}

func (x *exclusion) named() bool { return len(x.args)+len(x.cookies)+len(x.headers) > 0 }

type namePattern struct {
	s      string // lowercase
	prefix bool
}

func (p namePattern) match(name string) bool {
	if p.prefix {
		return len(name) >= len(p.s) && strings.EqualFold(name[:len(p.s)], p.s)
	}
	return strings.EqualFold(name, p.s)
}

func patterns(names []string) []namePattern {
	var out []namePattern
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		p := namePattern{s: n}
		if strings.HasSuffix(n, "*") {
			p = namePattern{s: strings.TrimSuffix(n, "*"), prefix: true}
		}
		out = append(out, p)
	}
	return out
}

// Validate checks a site's firewall configuration, rule IDs included.
func Validate(cfg model.WAFConfig, t model.SiteType) error {
	if err := cfg.Validate(t); err != nil {
		return err
	}
	for i, x := range cfg.Exclusions {
		for _, id := range x.RuleIDs {
			if _, ok := byID[id]; !ok {
				return &model.ValidationError{Field: "routing.waf.exclusions[" + strconv.Itoa(i) + "].ruleIds", Message: "there is no rule " + strconv.Itoa(id)}
			}
		}
	}
	return nil
}

// Compile builds a site's engine; nil when the firewall is off, so that
// a site without one pays nothing.
func Compile(cfg model.WAFConfig) *Engine {
	if !cfg.Enabled() {
		return nil
	}
	e := &Engine{mode: cfg.Mode, threshold: cfg.Threshold(), paranoia: cfg.Paranoia(), bodyLimit: cfg.BodyLimit()}
	e.budget = max(minWorkBudget, workPerBodyByte*e.bodyLimit)
	for _, r := range rules {
		e.all.add(r.idx)
		if r.Paranoia > e.paranoia {
			continue
		}
		e.active.add(r.idx)
		in := r.in
		if e.paranoia >= 2 {
			in |= r.in2
		}
		for b := 0; b < nTargets; b++ {
			if in&(1<<b) != 0 {
				e.byTarget[b] = append(e.byTarget[b], r)
			}
		}
	}
	for _, x := range cfg.Exclusions {
		c := exclusion{path: cleanPrefix(x.Path), args: patterns(x.Args), cookies: patterns(x.Cookies), headers: patterns(x.Headers)}
		for _, id := range x.RuleIDs {
			if r, ok := byID[id]; ok {
				c.rules.add(r.idx)
			}
		}
		for _, cat := range x.Categories {
			for _, r := range rules {
				if r.Category == cat {
					c.rules.add(r.idx)
				}
			}
		}
		listed := len(x.RuleIDs)+len(x.Categories) > 0
		switch {
		case c.named() && !listed:
			c.rules = e.all
		case !c.named() && !listed:
			c.off = true
		}
		e.excl = append(e.excl, c)
	}
	return e
}

// Mode is off, detect or block.
func (e *Engine) Mode() string { return e.mode }

// Threshold is the anomaly score that blocks.
func (e *Engine) Threshold() int { return e.threshold }

// Result is what inspection found.
type Result struct {
	Score     int
	Threshold int
	Paranoia  int
	Matches   []model.WAFMatch // one per rule that matched, in the order found
	// Incomplete: inspection stopped before it saw the whole request (too
	// many values, the work budget spent, a body in an encoding it cannot
	// read) and the rule saying so was not excluded. The request counts as
	// exceeding the threshold: refused in block mode, logged in detect
	// mode.
	Incomplete bool
	// Busy: block mode, and the body was not inspected because the
	// server-wide budget for buffering bodies was spent (see
	// SetMaxBufferedBytes). The proxy answers 503; nothing was matched.
	Busy bool
}

// Exceeded reports whether the request reached the anomaly threshold, or
// could not be inspected completely.
func (r Result) Exceeded() bool { return r.Score >= r.Threshold || r.Incomplete }

// inspection is the state of one request's inspection.
type inspection struct {
	e     *Engine
	skip  ruleSet // rules excluded for the whole request
	fired ruleSet // rules that matched already: each counts once
	named []*exclusion
	// nArgs and nHeaders count the values inspected (see maxArgs).
	nArgs, nHeaders int
	// work is the text run through regular expressions so far; past the
	// engine's budget inspection stops, and rule 920220 fires.
	work int
	// done: the threshold is reached (the verdict cannot change), the
	// request is known to be incomplete or, in block mode, its body could
	// not be buffered. No further value is inspected.
	done bool
	res  Result
}

// Inspect inspects a request. A body that is inspected is read up to the
// engine's limit and replaced by one that replays what was read before
// the rest, so the application receives it whole, streamed as before.
func (e *Engine) Inspect(r *http.Request) Result {
	in := inspection{e: e, res: Result{Threshold: e.threshold, Paranoia: e.paranoia}}
	var clean, sent string
	if len(e.excl) > 0 {
		clean, sent = requestPaths(r.URL.Path)
	}
	for i := range e.excl {
		x := &e.excl[i]
		// Both the path as sent and the path with its dot segments
		// resolved must be under the exclusion's: /hooks/../login is not
		// under /hooks, and /login/../hooks/x is not either, since an
		// application that does not resolve dot segments (Express) routes
		// it under /login.
		if x.path != "" && (!underPrefix(clean, x.path) || !underPrefix(sent, x.path)) {
			continue
		}
		switch {
		case x.off:
			return in.res
		case x.named():
			in.named = append(in.named, x)
		default:
			in.skip.or(&x.rules)
		}
	}
	in.value(tPath, model.WAFInPath, "", r.URL.EscapedPath(), false)
	in.query(r.URL.RawQuery)
	in.headers(r)
	in.protocol(r)
	if !in.done {
		in.body(r)
	}
	return in.res
}

// cleanPrefix is an exclusion's path as it is matched: lowercase (paths
// are matched without regard to case, as IIS and Express do), with dot
// segments resolved and repeated and trailing slashes removed. "" stays
// "" (the whole site).
func cleanPrefix(p string) string {
	if p == "" {
		return ""
	}
	return strings.ToLower(path.Clean("/" + p))
}

// requestPaths returns a request's path cleaned as cleanPrefix does, and
// as sent: lowercase with repeated slashes collapsed, dot segments kept.
func requestPaths(p string) (clean, sent string) {
	var b strings.Builder
	b.Grow(len(p) + 1)
	prev := byte(0)
	if !strings.HasPrefix(p, "/") {
		b.WriteByte('/')
		prev = '/'
	}
	for i := 0; i < len(p); i++ {
		if c := p[i]; c != '/' || prev != '/' {
			b.WriteByte(c)
			prev = c
		}
	}
	return strings.ToLower(path.Clean("/" + p)), strings.ToLower(b.String())
}

// underPrefix reports whether p is prefix or below it, on a segment
// boundary: /api covers /api and /api/x, not /api-admin.
func underPrefix(p, prefix string) bool {
	if prefix == "/" {
		return true
	}
	return strings.HasPrefix(p, prefix) && (len(p) == len(prefix) || p[len(prefix)] == '/')
}

// value inspects one value. raw is as received; plus: '+' is a space.
func (in *inspection) value(t target, where, name, raw string, plus bool) {
	if raw == "" || in.done || !in.count(t) {
		return
	}
	cands := in.e.byTarget[bitIndex(t)]
	if len(cands) == 0 {
		return
	}
	skip := in.skip
	skip.or(&in.fired)
	if name != "" && len(in.named) > 0 {
		for _, x := range in.named {
			if x.matches(where, name) {
				skip.or(&x.rules)
			}
		}
	}
	if skip.covers(&in.e.active) {
		return
	}
	if len(raw) > maxValueLen && t != tBody && t != tArg {
		raw = raw[:maxValueLen]
	}
	dec := decode(raw, plus)
	norm, _ := fold(dec)
	var bits, rawBits litSet
	matcher.scan(norm, &bits)
	var sql, rawLower string
	sqlDone, rawDone := false, false
	for _, r := range cands {
		if skip.has(r.idx) {
			continue
		}
		s, lb := norm, &bits
		switch r.form {
		case fSQL:
			if !sqlDone {
				sql, sqlDone = sqlForm(norm), true
			}
			s = sql
		case fRaw:
			// Every raw-form rule looks for an escape.
			if !rawDone {
				rawDone = true
				if strings.IndexByte(raw, '%') >= 0 {
					rawLower = strings.ToLower(raw)
					matcher.scan(rawLower, &rawBits)
				}
			}
			s, lb = rawLower, &rawBits
		case fDecoded:
			s = dec
		}
		ok := true
		for i := range r.groups {
			if !r.groups[i].intersects(lb) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if in.work += len(s); in.work > in.e.budget {
			// Not excludable: the budget is what keeps inspection cheap.
			in.hit(byID[920220], model.WAFInRequest, "", "stopped at "+where+" "+name)
			in.res.Incomplete, in.done = true, true
			return
		}
		var snip string
		at := -1
		if r.re != nil {
			loc := r.re.FindStringIndex(s)
			if loc == nil {
				continue
			}
			snip, at = s[loc[0]:loc[1]], loc[0]
		} else if snip, ok = r.fn(s); !ok {
			continue
		}
		if t == tBody {
			// A body inspected whole has no field names to judge by: what
			// precedes the match says whether it is in a password.
			if at < 0 {
				at = strings.Index(s, snip)
			}
			if at >= 0 && sensitiveBefore(s[:at]) {
				snip = redacted
			}
		}
		// Once the threshold is reached nothing more is inspected, but the
		// rest of a short value's rules still run: the event then names
		// what the value is (${jndi:ldap://...} is Log4Shell before it is
		// a URL). A long one stops here, the verdict being known.
		in.hit(r, where, name, snip)
		if in.done && len(s) > 4<<10 {
			return
		}
		skip.add(r.idx)
	}
}

// count counts a value towards its limit (maxArgs, maxHeaderValues) and
// reports whether to inspect it. Past the limit rule 920210 fires and
// inspection stops, failing closed; with the rule excluded, every value
// is inspected.
func (in *inspection) count(t target) bool {
	n, limit, what := &in.nArgs, maxArgs, "arguments"
	if t&(tHeader|tUA|tReferer|tCookie) != 0 {
		n, limit, what = &in.nHeaders, maxHeaderValues, "headers and cookies"
	}
	if *n++; *n != limit+1 {
		return true
	}
	return !in.incomplete(920210, "more than "+strconv.Itoa(limit)+" "+what)
}

// incomplete records a request-level rule saying that the request cannot
// be inspected completely and, if it fired (it is active and not
// excluded), stops inspection: the request fails closed. It reports
// whether the rule fired.
func (in *inspection) incomplete(id int, text string) bool {
	in.request(id, model.WAFInRequest, "", text)
	if !in.fired.has(byID[id].idx) {
		return false
	}
	in.res.Incomplete, in.done = true, true
	return true
}

func (x *exclusion) matches(where, name string) bool {
	var ps []namePattern
	switch where {
	case model.WAFInArg, model.WAFInArgName, model.WAFInFile:
		ps = x.args
	case model.WAFInCookie:
		ps = x.cookies
	case model.WAFInHeader:
		ps = x.headers
	}
	for _, p := range ps {
		if p.match(name) {
			return true
		}
	}
	return false
}

func bitIndex(t target) int {
	for i := 0; i < nTargets; i++ {
		if t == 1<<i {
			return i
		}
	}
	panic("waf: not a single target")
}

func (in *inspection) hit(r *rule, where, name, text string) {
	in.fired.add(r.idx)
	in.res.Score += r.Score
	if in.res.Score >= in.e.threshold {
		in.done = true
	}
	m := model.WAFMatch{RuleID: r.ID, Category: r.Category, Severity: r.Severity, Score: r.Score, Message: r.Message, In: where, Name: snippetName(name)}
	switch {
	case sensitive(where, name) || text == redacted:
		m.Snippet = redacted
	case where == model.WAFInBody:
		m.Snippet = snippet(redactPairs(text))
	case where == model.WAFInPath:
		m.Snippet = snippet(redactPath(text))
	default:
		m.Snippet = snippet(text)
	}
	in.res.Matches = append(in.res.Matches, m)
}

// request records a request-level rule, if it is active and not excluded.
func (in *inspection) request(id int, where, name, text string) {
	r := byID[id]
	if in.done || !in.e.active.has(r.idx) || in.skip.has(r.idx) || in.fired.has(r.idx) {
		return
	}
	if name != "" {
		for _, x := range in.named {
			if x.matches(where, name) && x.rules.has(r.idx) {
				return
			}
		}
	}
	in.hit(r, where, name, text)
}

func snippetName(name string) string {
	if len(name) > 200 {
		return snippet(name[:200])
	}
	return snippet(name)
}

// sensitive reports whether a value's name suggests a secret, whose
// matched text is not recorded.
func sensitive(where, name string) bool {
	if name == "" || where == model.WAFInArgName || where == model.WAFInFile {
		return false
	}
	return sensitiveName(name)
}

// sensitiveName reports whether a field, cookie or header name suggests a
// password, a token or a session ID: session cookies (connect.sid,
// PHPSESSID, JSESSIONID, ASP.NET_SessionId...) included.
func sensitiveName(name string) bool {
	n := strings.ToLower(name)
	for _, s := range []string{"pass", "pwd", "secret", "token", "apikey", "api_key", "api-key", "auth", "sess", "jwt", "csrf", "xsrf", "cvv", "card", "credential", "private"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	for _, s := range []string{".sid", "_sid", "-sid"} {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return slices.Contains([]string{"sid", "pin", "otp", "key", "ssn", "code"}, n)
}

// query inspects a query string (or a form body), leniently: a pair that
// is not valid URL encoding is inspected as it is rather than dropped, as
// Node.js's parsers keep it.
func (in *inspection) query(q string) {
	for q != "" {
		var kv string
		kv, q, _ = strings.Cut(q, "&")
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		name := decode(k, true)
		in.value(tArgName, model.WAFInArgName, name, k, true)
		in.value(tArg, model.WAFInArg, name, v, true)
	}
}

// scannerHeaders are sent by vulnerability scanners.
var scannerHeaders = []string{"acunetix-", "x-wipp", "x-scan-memo", "x-request-memo", "x-arachni-", "x-wvs-", "bugbounty"}

func (in *inspection) headers(r *http.Request) {
	for name, vals := range r.Header {
		t := tHeader
		switch name {
		case "Cookie":
			for _, v := range vals {
				in.cookies(v)
			}
			continue
		case "Authorization", "Proxy-Authorization":
			continue
		case "User-Agent":
			t = tUA
		case "Referer":
			t = tReferer
		}
		lower := strings.ToLower(name)
		for _, p := range scannerHeaders {
			if strings.HasPrefix(lower, p) {
				in.request(913110, model.WAFInHeader, name, name)
			}
		}
		for _, v := range vals {
			in.value(t, model.WAFInHeader, name, v, false)
		}
	}
}

func (in *inspection) cookies(h string) {
	for h != "" {
		var c string
		c, h, _ = strings.Cut(h, ";")
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		name, v, _ := strings.Cut(c, "=")
		in.value(tCookie, model.WAFInCookie, strings.TrimSpace(name), strings.Trim(v, `"`), false)
	}
}

// protocol checks the request's framing and method.
func (in *inspection) protocol(r *http.Request) {
	switch r.Method {
	case "TRACE", "TRACK", "DEBUG":
		in.request(920170, model.WAFInRequest, "", r.Method)
	case http.MethodGet, http.MethodHead:
		if r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
			in.request(920160, model.WAFInRequest, "", r.Method+" with a body")
		}
	}
	// Conflicting framing (Content-Length with Transfer-Encoding, differing
	// Content-Lengths) never gets this far: Go's server refuses it or, as
	// RFC 9112 asks, drops Content-Length from a chunked request.
	if r.Header.Get("User-Agent") == "" {
		in.request(920180, model.WAFInHeader, "User-Agent", "")
	}
	if bad := invalidEscape(r.URL.RawQuery); bad != "" {
		in.request(920120, model.WAFInQuery, "", bad)
	}
}

// invalidEscape returns the first % in s that starts no escape (%XX or
// %uXXXX), with what follows it, or "".
func invalidEscape(s string) string {
	for i := strings.IndexByte(s, '%'); i >= 0; {
		rest := s[i+1:]
		ok := false
		if len(rest) >= 2 {
			_, a := unhex(rest[0])
			_, b := unhex(rest[1])
			ok = a && b
		}
		if !ok && len(rest) >= 5 && (rest[0] == 'u' || rest[0] == 'U') {
			_, ok = hex4(rest[1:5])
		}
		if !ok {
			return s[i:min(len(s), i+8)]
		}
		j := strings.IndexByte(rest, '%')
		if j < 0 {
			break
		}
		i += 1 + j
	}
	return ""
}
