package waf

import (
	"regexp"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

// Redaction of what events keep. A matched value whose name looks like a
// password, a token or a session ID is not recorded (sensitive); these
// cover what has no name: a body inspected whole, and the path.

const redacted = model.WAFRedacted

// fieldKey finds "name=" and "name":/'name': in text; with its value in
// fieldPair.
var (
	fieldKey  = regexp.MustCompile(`(?i)([a-z0-9_.\-\[\]]+)["']?\s*[:=]`)
	fieldPair = regexp.MustCompile(`(?i)([a-z0-9_.\-\[\]]+)(["']?\s*[:=]\s*["']?)([^"'&,;\s}\]]+)`)
)

// sensitiveBefore reports whether the text a body match follows ends in a
// field that looks sensitive: "user=bob&password=x" and
// {"user":"bob","password":"x are, "password=x&q=" is not. Everything
// after the last field separator counts, so an attack whose own quotes
// and equals signs follow the name is still attributed to it.
func sensitiveBefore(before string) bool {
	if len(before) > 1024 {
		before = before[len(before)-1024:]
	}
	cut := strings.LastIndexAny(before, "&\n\r")
	for _, sep := range []string{`,"`, `, "`, `,'`} {
		if i := strings.LastIndex(before, sep); i > cut {
			cut = i
		}
	}
	seg := before[cut+1:]
	for _, m := range fieldKey.FindAllStringSubmatch(seg, -1) {
		if sensitiveName(m[1]) {
			return true
		}
	}
	return false
}

// redactPairs replaces the values of sensitive-looking fields in a piece
// of a body: "password=hunter2&q=" becomes "password=[redacted]&q=".
func redactPairs(s string) string {
	locs := fieldPair.FindAllStringSubmatchIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, l := range locs {
		if !sensitiveName(s[l[2]:l[3]]) {
			continue
		}
		b.WriteString(s[last:l[6]])
		b.WriteString(redacted)
		last = l[7]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// redactPath replaces path segments that look like tokens (a password
// reset or magic-link token, an API key, a JWT) with [redacted]: at least
// 24 characters of the base64, base64url or hex alphabets (and the dots
// of a JWT), letters and digits mixed, with a run of 16 or more letters
// and digits. Slugs ("how-to-deploy-node-in-2024") and UUIDs stay.
func redactPath(p string) string {
	if len(p) < 24 {
		return p
	}
	segs := strings.Split(p, "/")
	changed := false
	for i, s := range segs {
		if tokenLike(s) {
			segs[i], changed = redacted, true
		}
	}
	if !changed {
		return p
	}
	return strings.Join(segs, "/")
}

func tokenLike(s string) bool {
	if len(s) < 24 {
		return false
	}
	letter, digit := false, false
	run, longest := 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digit = true
			run++
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			letter = true
			run++
		case c == '-' || c == '_' || c == '.' || c == '~' || c == '+' || c == '=':
			run = 0
		default:
			return false
		}
		longest = max(longest, run)
	}
	return letter && digit && longest >= 16
}
