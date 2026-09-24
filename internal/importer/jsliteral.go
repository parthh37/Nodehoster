package importer

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A PM2 ecosystem file is JavaScript, and the service runs as LocalSystem:
// it is never executed. The common case, a file that only exports an
// object literal, is read by this small parser of JavaScript literals
// (strings, template strings without ${…}, numbers, booleans, null,
// arrays, objects with quoted or bare keys, comments and trailing commas).
// Anything computed — a variable, a function call, process.env.X — is
// refused with a message that says how to export the configuration as
// JSON instead.

type jsError struct {
	line int
	msg  string
}

func (e *jsError) Error() string {
	if e.line > 0 {
		return fmt.Sprintf("line %d: %s", e.line, e.msg)
	}
	return e.msg
}

// errNotLiteral is appended to refusals of code.
const errNotLiteral = "NodeHoster never runs an uploaded ecosystem file, so it can only read one that exports plain values. " +
	"Run `pm2 jlist > apps.json` on the old server (with the apps started) and import apps.json instead."

type jsParser struct {
	src   string
	pos   int
	line  int
	depth int // arrays and objects being read
}

// maxJSDepth bounds the nesting of arrays and objects. The parser is
// recursive and a Go stack overflow cannot be recovered: an upload of a few
// megabytes of "[" would otherwise end the service and every site with it.
// Real ecosystem files nest a handful of levels.
const maxJSDepth = 256

// parseEcosystem reads `module.exports = <literal>` (or `export default`,
// or a bare literal such as `pm2 prettylist` output).
func parseEcosystem(src string) (any, error) {
	p := &jsParser{src: strings.TrimPrefix(src, "\uFEFF"), line: 1}
	p.skipSpace()
	// Directives such as 'use strict' may come first.
	for p.peekDirective() {
	}
	start := p.pos
	switch {
	case p.eatWord("module"):
		p.skipSpace()
		if !p.eat('.') {
			return nil, p.errf("expected module.exports = …; %s", errNotLiteral)
		}
		p.skipSpace()
		if !p.eatWord("exports") {
			return nil, p.errf("expected module.exports = …; %s", errNotLiteral)
		}
		p.skipSpace()
		if !p.eat('=') {
			return nil, p.errf("expected = after module.exports; %s", errNotLiteral)
		}
	case p.eatWord("export"):
		p.skipSpace()
		if !p.eatWord("default") {
			return nil, p.errf("only `export default {…}` is supported; %s", errNotLiteral)
		}
	default:
		c, _ := p.peek()
		if c != '{' && c != '[' {
			p.pos = start
			return nil, p.errf("the file does not start with module.exports = {…} (found %q); %s", p.snippet(), errNotLiteral)
		}
	}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	p.eat(';')
	p.skipSpace()
	if p.pos < len(p.src) {
		return nil, p.errf("unexpected %q after the exported value; %s", p.snippet(), errNotLiteral)
	}
	return v, nil
}

func (p *jsParser) errf(format string, a ...any) error {
	return &jsError{line: p.line, msg: fmt.Sprintf(format, a...)}
}

// snippet is the text at the current position, for messages.
func (p *jsParser) snippet() string {
	rest := p.src[p.pos:]
	if i := strings.IndexAny(rest, "\r\n"); i >= 0 {
		rest = rest[:i]
	}
	if len(rest) > 40 {
		rest = rest[:40] + "…"
	}
	return rest
}

func (p *jsParser) peek() (rune, int) {
	if p.pos >= len(p.src) {
		return 0, 0
	}
	return utf8.DecodeRuneInString(p.src[p.pos:])
}

func (p *jsParser) advance(n int) {
	p.line += strings.Count(p.src[p.pos:p.pos+n], "\n")
	p.pos += n
}

func (p *jsParser) eat(c rune) bool {
	if r, n := p.peek(); n > 0 && r == c {
		p.advance(n)
		return true
	}
	return false
}

func (p *jsParser) eatWord(w string) bool {
	if !strings.HasPrefix(p.src[p.pos:], w) {
		return false
	}
	if next := p.pos + len(w); next < len(p.src) {
		if r, _ := utf8.DecodeRuneInString(p.src[next:]); isIdentPart(r) {
			return false
		}
	}
	p.advance(len(w))
	return true
}

// skipSpace skips white space and comments.
func (p *jsParser) skipSpace() {
	for p.pos < len(p.src) {
		rest := p.src[p.pos:]
		switch {
		case strings.HasPrefix(rest, "//"):
			i := strings.IndexByte(rest, '\n')
			if i < 0 {
				i = len(rest)
			}
			p.advance(i)
		case strings.HasPrefix(rest, "/*"):
			i := strings.Index(rest[2:], "*/")
			if i < 0 {
				p.advance(len(rest))
				return
			}
			p.advance(i + 4)
		default:
			r, n := p.peek()
			if !unicode.IsSpace(r) {
				return
			}
			p.advance(n)
		}
	}
}

func (p *jsParser) peekDirective() bool {
	save, line := p.pos, p.line
	if c, _ := p.peek(); c == '\'' || c == '"' {
		if s, err := p.str(c); err == nil && (s == "use strict") {
			p.skipSpace()
			p.eat(';')
			p.skipSpace()
			return true
		}
	}
	p.pos, p.line = save, line
	return false
}

func (p *jsParser) value() (any, error) {
	p.skipSpace()
	c, n := p.peek()
	switch {
	case n == 0:
		return nil, p.errf("unexpected end of file")
	case c == '{' || c == '[':
		if p.depth >= maxJSDepth {
			return nil, p.errf("arrays and objects are nested more than %d levels deep; this is not a PM2 configuration", maxJSDepth)
		}
		p.depth++
		defer func() { p.depth-- }()
		if c == '{' {
			return p.object()
		}
		return p.array()
	case c == '\'' || c == '"' || c == '`':
		return p.concat()
	case c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9'):
		return p.number()
	case isIdentStart(c):
		word := p.ident()
		switch word {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null", "undefined":
			return nil, nil
		}
		// Say what the expression is: process.env.PORT, require('x')…
		expr := word
		for {
			p.skipSpace()
			if p.eat('.') {
				p.skipSpace()
				expr += "." + p.ident()
				continue
			}
			if c, _ := p.peek(); c == '(' {
				expr += "(…)"
			}
			break
		}
		return nil, p.errf("%s is not a plain value; %s", expr, errNotLiteral)
	}
	return nil, p.errf("unexpected %q; %s", p.snippet(), errNotLiteral)
}

func (p *jsParser) ident() string {
	start := p.pos
	for {
		r, n := p.peek()
		if n == 0 || !(isIdentPart(r) || (p.pos == start && isIdentStart(r))) {
			break
		}
		p.advance(n)
	}
	return p.src[start:p.pos]
}

func isIdentStart(r rune) bool { return r == '_' || r == '$' || unicode.IsLetter(r) }
func isIdentPart(r rune) bool  { return isIdentStart(r) || unicode.IsDigit(r) }

func (p *jsParser) object() (any, error) {
	p.eat('{')
	out := map[string]any{}
	for {
		p.skipSpace()
		if p.eat('}') {
			return out, nil
		}
		var key string
		c, _ := p.peek()
		switch {
		case c == '\'' || c == '"':
			s, err := p.str(c)
			if err != nil {
				return nil, err
			}
			key = s
		case c >= '0' && c <= '9':
			v, err := p.number()
			if err != nil {
				return nil, err
			}
			key = fmt.Sprint(v)
		case isIdentStart(c):
			key = p.ident()
		case c == '.':
			return nil, p.errf("a spread (...) is not a plain value; %s", errNotLiteral)
		case c == '[':
			return nil, p.errf("a computed key is not a plain value; %s", errNotLiteral)
		default:
			return nil, p.errf("expected a property name, found %q", p.snippet())
		}
		p.skipSpace()
		if !p.eat(':') {
			if c, _ := p.peek(); c == '(' {
				return nil, p.errf("%s is a function; %s", key, errNotLiteral)
			}
			return nil, p.errf("%s is not a plain value (expected `%s: value`); %s", key, key, errNotLiteral)
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out[key] = v
		p.skipSpace()
		if p.eat(',') {
			continue
		}
		if p.eat('}') {
			return out, nil
		}
		return nil, p.errf("expected , or } after %s, found %q", key, p.snippet())
	}
}

func (p *jsParser) array() (any, error) {
	p.eat('[')
	out := []any{}
	for {
		p.skipSpace()
		if p.eat(']') {
			return out, nil
		}
		if c, _ := p.peek(); c == ',' {
			return nil, p.errf("empty array element")
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skipSpace()
		if p.eat(',') {
			continue
		}
		if p.eat(']') {
			return out, nil
		}
		return nil, p.errf("expected , or ] in an array, found %q", p.snippet())
	}
}

// concat reads a string, or strings joined with + (as `pm2 prettylist`
// prints long values).
func (p *jsParser) concat() (any, error) {
	var b strings.Builder
	for {
		c, _ := p.peek()
		s, err := p.str(c)
		if err != nil {
			return nil, err
		}
		b.WriteString(s)
		save, line := p.pos, p.line
		p.skipSpace()
		if p.eat('+') {
			p.skipSpace()
			if c, _ := p.peek(); c == '\'' || c == '"' || c == '`' {
				continue
			}
			return nil, p.errf("only strings can be joined with +; %s", errNotLiteral)
		}
		p.pos, p.line = save, line
		return b.String(), nil
	}
}

func (p *jsParser) str(quote rune) (string, error) {
	startLine := p.line
	p.advance(1)
	var b strings.Builder
	for {
		r, n := p.peek()
		if n == 0 {
			p.line = startLine
			return "", p.errf("unterminated string")
		}
		p.advance(n)
		switch {
		case r == quote:
			return b.String(), nil
		case r == '$' && quote == '`':
			if c, _ := p.peek(); c == '{' {
				return "", p.errf("a template string with ${…} is not a plain value; %s", errNotLiteral)
			}
			b.WriteRune(r)
		case r == '\n' && quote != '`':
			return "", p.errf("unterminated string")
		case r == '\\':
			e, n := p.peek()
			if n == 0 {
				return "", p.errf("unterminated string")
			}
			p.advance(n)
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'v':
				b.WriteByte('\v')
			case '0':
				b.WriteByte(0)
			case '\r':
				p.eat('\n') // line continuation
			case '\n':
			case 'x':
				v, err := p.hex(2)
				if err != nil {
					return "", err
				}
				b.WriteRune(rune(v))
			case 'u':
				if p.eat('{') {
					end := strings.IndexByte(p.src[p.pos:], '}')
					if end < 0 {
						return "", p.errf("bad \\u{…} escape")
					}
					v, err := strconv.ParseUint(p.src[p.pos:p.pos+end], 16, 32)
					if err != nil {
						return "", p.errf("bad \\u{…} escape")
					}
					p.advance(end + 1)
					b.WriteRune(rune(v))
					continue
				}
				v, err := p.hex(4)
				if err != nil {
					return "", err
				}
				b.WriteRune(rune(v))
			default:
				b.WriteRune(e) // \\ \' \" \` and identity escapes
			}
		default:
			b.WriteRune(r)
		}
	}
}

func (p *jsParser) hex(digits int) (uint64, error) {
	if p.pos+digits > len(p.src) {
		return 0, p.errf("bad escape")
	}
	v, err := strconv.ParseUint(p.src[p.pos:p.pos+digits], 16, 32)
	if err != nil {
		return 0, p.errf("bad escape")
	}
	p.advance(digits)
	return v, nil
}

func (p *jsParser) number() (any, error) {
	start := p.pos
	neg := false
	if p.eat('-') {
		neg = true
	} else {
		p.eat('+')
	}
	p.skipSpace()
	numStart := p.pos
	rest := p.src[p.pos:]
	if len(rest) > 2 && rest[0] == '0' && (rest[1] == 'x' || rest[1] == 'X') {
		p.advance(2)
		for {
			r, n := p.peek()
			if n == 0 || !strings.ContainsRune("0123456789abcdefABCDEF_", r) {
				break
			}
			p.advance(n)
		}
		v, err := strconv.ParseInt(strings.ReplaceAll(p.src[numStart+2:p.pos], "_", ""), 16, 64)
		if err != nil {
			return nil, p.errf("bad number %q", p.src[start:p.pos])
		}
		if neg {
			v = -v
		}
		return float64(v), nil
	}
	for {
		r, n := p.peek()
		if n == 0 || !strings.ContainsRune("0123456789._eE", r) {
			if (r == '-' || r == '+') && p.pos > numStart && strings.ContainsRune("eE", rune(p.src[p.pos-1])) {
				p.advance(1)
				continue
			}
			break
		}
		p.advance(n)
	}
	text := strings.ReplaceAll(p.src[numStart:p.pos], "_", "")
	if text == "" {
		if w := p.ident(); w == "Infinity" {
			return nil, p.errf("Infinity is not a usable value")
		}
		return nil, p.errf("unexpected %q", p.snippet())
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil, p.errf("bad number %q", p.src[start:p.pos])
	}
	if neg {
		v = -v
	}
	return v, nil
}
