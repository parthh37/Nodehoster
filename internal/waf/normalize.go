package waf

import (
	"html"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Normalisation. Attacks hide behind encodings the application undoes and
// a naive filter does not: %27, %2527 (twice), %u0027 (IIS), &#39;,
// &apos;, ', overlong UTF-8 (%c0%ae for "."), full-width "＜", mixed
// case, SQL comments inside keywords, NUL bytes. Rules match the value
// with all of that undone.

// maxDecodeRounds bounds repeated decoding: each round undoes one layer
// of URL, HTML-entity or JavaScript escaping.
const maxDecodeRounds = 4

// decode undoes URL encoding (with %uXXXX; plus is '+' as space, for
// query strings and forms, on the first round only), HTML entities and
// JavaScript escapes, repeatedly, until nothing changes.
func decode(s string, plus bool) string {
	for round := 0; round < maxDecodeRounds; round++ {
		prev := s
		if strings.IndexByte(s, '%') >= 0 || (plus && strings.IndexByte(s, '+') >= 0) {
			s = urlDecode(s, plus)
		}
		plus = false
		if strings.IndexByte(s, '&') >= 0 {
			s = html.UnescapeString(s)
		}
		if strings.IndexByte(s, '\\') >= 0 {
			s = jsDecode(s)
		}
		if s == prev {
			break
		}
	}
	return s
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func hex4(s string) (rune, bool) {
	var r rune
	for i := 0; i < 4; i++ {
		v, ok := unhex(s[i])
		if !ok {
			return 0, false
		}
		r = r<<4 | rune(v)
	}
	return r, true
}

// urlDecode decodes %XX and %uXXXX leniently, as applications do: an
// invalid escape stays as it is instead of failing the whole value.
func urlDecode(s string, plus bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '+' && plus:
			b.WriteByte(' ')
			continue
		case c != '%':
			b.WriteByte(c)
			continue
		}
		if i+6 <= len(s) && (s[i+1] == 'u' || s[i+1] == 'U') {
			if r, ok := hex4(s[i+2 : i+6]); ok {
				b.WriteRune(r)
				i += 5
				continue
			}
		}
		if i+2 < len(s) {
			h, ok1 := unhex(s[i+1])
			l, ok2 := unhex(s[i+2])
			if ok1 && ok2 {
				b.WriteByte(h<<4 | l)
				i += 2
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// jsDecode undoes \xHH, \uHHHH and \u{H...} escapes (the others, \n and
// friends, cannot hide anything the rules look for).
func jsDecode(s string) string {
	if !strings.Contains(s, `\x`) && !strings.Contains(s, `\u`) && !strings.Contains(s, `\X`) && !strings.Contains(s, `\U`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		switch s[i+1] {
		case 'x', 'X':
			if i+3 < len(s) {
				h, ok1 := unhex(s[i+2])
				l, ok2 := unhex(s[i+3])
				if ok1 && ok2 {
					b.WriteByte(h<<4 | l)
					i += 3
					continue
				}
			}
		case 'u', 'U':
			if i+2 < len(s) && s[i+2] == '{' {
				if end := strings.IndexByte(s[i+3:], '}'); end > 0 && end <= 6 {
					var r rune
					ok := true
					for _, d := range []byte(s[i+3 : i+3+end]) {
						v, good := unhex(d)
						if !good {
							ok = false
							break
						}
						r = r<<4 | rune(v)
					}
					if ok && utf8.ValidRune(r) {
						b.WriteRune(r)
						i += 3 + end
						continue
					}
				}
			} else if i+6 <= len(s) {
				if r, ok := hex4(s[i+2 : i+6]); ok {
					b.WriteRune(r)
					i += 5
					continue
				}
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// fold lowercases s, maps full-width ASCII (U+FF01-U+FF5E) and overlong
// UTF-8 encodings of ASCII to ASCII, and removes NUL bytes, which it
// reports. Bytes that are not valid UTF-8 are kept as they are (Go's
// strings.ToLower would turn them into U+FFFD). An ASCII string that needs
// none of that is returned as it is, without allocating.
func fold(s string) (string, bool) {
	clean := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= utf8.RuneSelf || c == 0 || ('A' <= c && c <= 'Z') {
			clean = false
			break
		}
	}
	if clean {
		return s, false
	}
	nul := false
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0:
			nul = true
			i++
			continue
		case c < utf8.RuneSelf:
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			b = append(b, c)
			i++
			continue
		}
		// Overlong encodings of ASCII: C0/C1 + continuation, and E0 80-9F +
		// continuation. IIS once decoded them; some decoders still do.
		if (c == 0xc0 || c == 0xc1) && i+1 < len(s) && s[i+1]&0xc0 == 0x80 {
			if a := (c&0x1f)<<6 | s[i+1]&0x3f; a != 0 {
				b = append(b, lowerASCII(a))
			} else {
				nul = true
			}
			i += 2
			continue
		}
		if c == 0xe0 && i+2 < len(s) && s[i+1]&0xe0 == 0x80 && s[i+2]&0xc0 == 0x80 {
			if a := (s[i+1]&0x3f)<<6 | s[i+2]&0x3f; a != 0 {
				b = append(b, lowerASCII(a))
			} else {
				nul = true
			}
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			b = append(b, c)
			i++
			continue
		}
		switch {
		case r >= 0xff01 && r <= 0xff5e:
			b = append(b, lowerASCII(byte(r-0xff01+0x21)))
		case r == 0x2215 || r == 0x2044: // division slash, fraction slash
			b = append(b, '/')
		case r == 0x2216 || r == 0xfe68: // set minus, small reverse solidus
			b = append(b, '\\')
		default:
			b = utf8.AppendRune(b, unicode.ToLower(r))
		}
		i += size
	}
	return string(b), nul
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// sqlForm replaces SQL comments with a space, so that UN/**/ION and
// UNION/*x*/SELECT read as keywords: /* ... */ (unterminated: to the
// end), and -- or # comments that end at a line break.
func sqlForm(s string) string {
	if !strings.Contains(s, "/*") && !strings.Contains(s, "--") && strings.IndexByte(s, '#') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "/*"):
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				i = len(s)
			} else {
				i += 2 + end + 1
			}
			b.WriteByte(' ')
		case s[i] == '#' || strings.HasPrefix(s[i:], "--"):
			nl := strings.IndexByte(s[i:], '\n')
			if nl < 0 {
				b.WriteString(s[i:])
				i = len(s)
				continue
			}
			i += nl
			b.WriteByte(' ')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// snippet makes matched text safe to store and show: at most maxSnippet
// bytes (on a rune boundary), valid UTF-8, control characters escaped.
const maxSnippet = 120

func snippet(s string) string { return safeText(s, maxSnippet) }

// safeText truncates s to at most max bytes (on a rune boundary, marked
// with …), makes it valid UTF-8 and escapes control characters.
func safeText(s string, max int) string {
	trunc := false
	if len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s, trunc = s[:cut], true
	}
	s = strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			// Controls, and the bidirectional overrides that could make
			// the text read differently from what it is.
			b.WriteString(`\u`)
			const hexd = "0123456789abcdef"
			b.WriteByte(hexd[r>>12&0xf])
			b.WriteByte(hexd[r>>8&0xf])
			b.WriteByte(hexd[r>>4&0xf])
			b.WriteByte(hexd[r&0xf])
		default:
			b.WriteRune(r)
		}
	}
	if trunc {
		b.WriteString("…")
	}
	return b.String()
}
