package rewrite

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// An expression is a rewrite target, condition input or outbound value
// compiled into segments: literal text, back-references ({R:n}, {C:n},
// $n), server variables ({HTTP_HOST}) and calls ({ToLower:…}, {Map:…})
// whose argument is itself an expression.
type expr []segment

type segKind int

const (
	segText segKind = iota
	segRule         // {R:n}, $n, ${name}
	segCond         // {C:n}
	segVar          // {NAME}
	segFunc         // {ToLower:…} and friends
	segMap          // {MapName:…}
)

type segment struct {
	kind  segKind
	text  string // literal, variable name (upper case), function or map name (lower case), named group
	index int    // back-reference index; -1 = named group in text
	arg   expr
}

var functions = map[string]func(string) string{
	"tolower":          strings.ToLower,
	"toupper":          strings.ToUpper,
	"urlencode":        url.QueryEscape,
	"escapedatastring": url.PathEscape,
	"urldecode": func(s string) string {
		if d, err := url.QueryUnescape(s); err == nil {
			return d
		}
		return s
	},
}

var varNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseExpr compiles s. maps holds the lower-cased names of the rewrite
// maps that exist, so a typo is reported instead of silently producing
// an empty string.
func parseExpr(s string, maps map[string]*rewriteMap) (expr, error) {
	e, rest, err := parseUntil(s, maps, false)
	if err != nil {
		return nil, err
	}
	if rest != "" {
		return nil, fmt.Errorf("unexpected %q", rest)
	}
	return e, nil
}

// parseUntil parses until the end of s or, when nested, until the "}"
// that closes the enclosing call, returning what follows it.
func parseUntil(s string, maps map[string]*rewriteMap, nested bool) (expr, string, error) {
	var out expr
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			out = append(out, segment{kind: segText, text: lit.String()})
			lit.Reset()
		}
	}
	for len(s) > 0 {
		switch c := s[0]; {
		case c == '}' && nested:
			flush()
			return out, s[1:], nil
		case c == '$':
			seg, n, ok := parseDollar(s)
			if !ok {
				lit.WriteByte('$')
				s = s[1:]
				continue
			}
			if seg == nil { // "$$"
				lit.WriteByte('$')
			} else {
				flush()
				out = append(out, *seg)
			}
			s = s[n:]
		case c == '{':
			seg, rest, ok, err := parseBrace(s[1:], maps)
			if err != nil {
				return nil, "", err
			}
			if !ok { // not an expression: a literal brace, e.g. in JSON
				lit.WriteByte('{')
				s = s[1:]
				continue
			}
			flush()
			out = append(out, seg)
			s = rest
		default:
			lit.WriteByte(c)
			s = s[1:]
		}
	}
	if nested {
		return nil, "", fmt.Errorf("missing }")
	}
	flush()
	return out, "", nil
}

// parseDollar handles the Go-style references the first rewrite rules
// used: $1, ${1}, ${name}, $name and $$.
func parseDollar(s string) (*segment, int, bool) {
	if len(s) < 2 {
		return nil, 0, false
	}
	if s[1] == '$' {
		return nil, 2, true
	}
	name, n := "", 0
	if s[1] == '{' {
		end := strings.IndexByte(s, '}')
		if end < 0 {
			return nil, 0, false
		}
		name, n = s[2:end], end+1
	} else {
		i := 1
		for i < len(s) && (isAlnum(s[i]) || s[i] == '_') {
			i++
		}
		name, n = s[1:i], i
	}
	if name == "" {
		return nil, 0, false
	}
	if idx, err := strconv.Atoi(name); err == nil {
		return &segment{kind: segRule, index: idx}, n, true
	}
	if !varNameRe.MatchString(name) {
		return nil, 0, false
	}
	return &segment{kind: segRule, index: -1, text: name}, n, true
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// parseBrace parses what follows a "{". ok is false when the text is not
// an expression at all ("{" followed by a space or quote), which keeps
// literal braces usable in custom response bodies.
func parseBrace(s string, maps map[string]*rewriteMap) (seg segment, rest string, ok bool, err error) {
	i := 0
	for i < len(s) && (isAlnum(s[i]) || s[i] == '_') {
		i++
	}
	name := s[:i]
	if name == "" || i == len(s) {
		return seg, "", false, nil
	}
	switch s[i] {
	case '}':
		if !varNameRe.MatchString(name) {
			return seg, "", false, nil
		}
		return segment{kind: segVar, text: strings.ToUpper(name)}, s[i+1:], true, nil
	case ':':
	default:
		return seg, "", false, nil
	}
	after := s[i+1:]
	upper := strings.ToUpper(name)
	if upper == "R" || upper == "C" {
		j := 0
		for j < len(after) && after[j] >= '0' && after[j] <= '9' {
			j++
		}
		if j == 0 || j == len(after) || after[j] != '}' {
			return seg, "", false, fmt.Errorf("{%s:n} needs a capture number", upper)
		}
		idx, _ := strconv.Atoi(after[:j])
		k := segRule
		if upper == "C" {
			k = segCond
		}
		return segment{kind: k, index: idx}, after[j+1:], true, nil
	}
	arg, rest, err := parseUntil(after, maps, true)
	if err != nil {
		return seg, "", false, fmt.Errorf("{%s:…}: %w", name, err)
	}
	lower := strings.ToLower(name)
	if _, ok := functions[lower]; ok {
		return segment{kind: segFunc, text: lower, arg: arg}, rest, true, nil
	}
	if _, ok := maps[lower]; ok {
		return segment{kind: segMap, text: lower, arg: arg}, rest, true, nil
	}
	return seg, "", false, fmt.Errorf("{%s:…} is neither a function nor a rewrite map", name)
}

// scope is what an expression is evaluated against.
type scope struct {
	vars  func(name string) string
	rule  []string       // rule captures
	named map[string]int // named groups of the rule pattern
	cond  []string       // captures of the last matched condition
	maps  map[string]*rewriteMap
}

func (e expr) expand(sc *scope) string {
	if len(e) == 1 && e[0].kind == segText {
		return e[0].text
	}
	var b strings.Builder
	for _, s := range e {
		switch s.kind {
		case segText:
			b.WriteString(s.text)
		case segRule:
			idx := s.index
			if idx < 0 {
				var ok bool
				if idx, ok = sc.named[s.text]; !ok {
					continue
				}
			}
			if idx < len(sc.rule) {
				b.WriteString(sc.rule[idx])
			}
		case segCond:
			if s.index < len(sc.cond) {
				b.WriteString(sc.cond[s.index])
			}
		case segVar:
			if sc.vars != nil {
				b.WriteString(sc.vars(s.text))
			}
		case segFunc:
			b.WriteString(functions[s.text](s.arg.expand(sc)))
		case segMap:
			b.WriteString(sc.maps[s.text].lookup(s.arg.expand(sc)))
		}
	}
	return b.String()
}

// uses reports whether the expression reads a server variable.
func (e expr) uses(name string) bool {
	for _, s := range e {
		if (s.kind == segVar && s.text == name) || s.arg.uses(name) {
			return true
		}
	}
	return false
}

type rewriteMap struct {
	def     string
	entries map[string]string // lower-cased keys
}

func (m *rewriteMap) lookup(k string) string {
	if v, ok := m.entries[strings.ToLower(k)]; ok {
		return v
	}
	return m.def
}
