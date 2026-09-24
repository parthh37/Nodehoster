// Package preview is the logic of preview deployments that needs no
// server: reading pull request and push webhooks from GitHub, GitLab and
// Gitea, deciding what a delivery means for a site's previews, naming
// previews (DNS-safe host names), matching branch patterns and reporting
// commit statuses back to the git host. core runs the lifecycle.
package preview

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

// maxLabel is the longest DNS label.
const maxLabel = 63

// Key identifies a preview under its parent: one per pull request, one
// per previewed branch.
func Key(kind string, number int, branch string) string {
	if kind == model.PreviewPR {
		return "pr:" + strconv.Itoa(number)
	}
	return "branch:" + branch
}

// shortHash is 6 hex digits of a string's SHA-256: stable, so a name
// that needed it gets the same one every time.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:3])
}

var nonLabel = regexp.MustCompile(`[^a-z0-9]+`)

// Slug makes a branch name DNS-label safe: lower case, runs of anything
// but letters and digits become one '-', no '-' at either end.
// "Feature/Login_Page" is "feature-login-page". A branch with nothing
// usable in it becomes "branch-<hash>".
func Slug(branch string) string {
	s := strings.Trim(nonLabel.ReplaceAllString(strings.ToLower(branch), "-"), "-")
	if s == "" {
		return "branch-" + shortHash(branch)
	}
	return s
}

// fit shortens a slug to n characters, replacing the end by a hash of the
// full branch name so that two long branches sharing a prefix still get
// different names.
func fit(slug, branch string, n int) string {
	if len(slug) <= n {
		return slug
	}
	h := shortHash(branch)
	keep := max(n-len(h)-1, 1)
	return strings.Trim(slug[:keep], "-") + "-" + h
}

// Label is the first label of a preview's host name, from the pattern's
// first label: {number} and {branch} replaced for a pull request; for a
// branch, {branch} replaced and {number} dropped, or the branch as the
// whole label when the pattern has no {branch}.
func Label(patternLabel, kind string, number int, branch string) string {
	var label string
	switch {
	case kind == model.PreviewPR:
		label = strings.ReplaceAll(patternLabel, "{number}", strconv.Itoa(number))
	case strings.Contains(patternLabel, "{branch}"):
		label = strings.ReplaceAll(patternLabel, "{number}", "")
	default:
		label = "{branch}"
	}
	if strings.Contains(label, "{branch}") {
		fixed := len(strings.ReplaceAll(label, "{branch}", ""))
		slug := fit(Slug(branch), branch, max(maxLabel-fixed, 8))
		label = strings.ReplaceAll(label, "{branch}", slug)
	}
	return cleanLabel(label)
}

var dashes = regexp.MustCompile(`-{2,}`)

// cleanLabel collapses the dashes a dropped placeholder leaves and trims
// the label to a valid one.
func cleanLabel(l string) string {
	l = strings.Trim(dashes.ReplaceAllString(l, "-"), "-")
	if len(l) > maxLabel {
		l = strings.TrimRight(l[:maxLabel], "-")
	}
	if l == "" {
		l = "preview"
	}
	return l
}

// Host is a preview's host name: the label from Label under the
// pattern's suffix. When taken says that name is used already (by another
// preview whose branch looks the same, or by any other site), a hash of
// the preview's key is appended, which makes it unique.
func Host(pattern, kind string, number int, branch string, taken func(host string) bool) string {
	pl, suffix := model.SplitHostPattern(pattern)
	label := Label(pl, kind, number, branch)
	host := label + "." + suffix
	if taken == nil || !taken(host) {
		return host
	}
	h := shortHash(Key(kind, number, branch))
	if len(label)+1+len(h) > maxLabel {
		label = strings.TrimRight(label[:maxLabel-1-len(h)], "-")
	}
	return label + "-" + h + "." + suffix
}

// SiteName is a preview's site name: the parent's name and the preview's
// label, within the 64 characters a site name may have.
func SiteName(parent, label string) string {
	const maxName = 64
	name := parent + " " + label
	if len(name) <= maxName {
		return name
	}
	keep := maxName - len(label) - 1
	if keep < 1 {
		return label[:min(len(label), maxName)]
	}
	return strings.TrimRight(parent[:keep], " ._-") + " " + label
}

// Unique returns name with a short hash of seed appended, for a second
// try when a site already has the name.
func Unique(name, seed string) string {
	h := shortHash(seed)
	if len(name)+1+len(h) > 64 {
		name = strings.TrimRight(name[:64-1-len(h)], " ._-")
	}
	return name + "-" + h
}

// URL is the address of a preview binding.
func URL(protocol, host string, port int) string {
	if (protocol == "http" && port == 80) || (protocol == "https" && port == 443) {
		return protocol + "://" + host
	}
	return protocol + "://" + host + ":" + strconv.Itoa(port)
}

// MatchBranch reports whether a branch matches one of the patterns: '*'
// matches within a path segment (not '/'), '**' across segments, '?' one
// character other than '/'. "feature/*" matches feature/login but not
// feature/a/b; "feature/**" matches both.
func MatchBranch(patterns []string, branch string) bool {
	for _, p := range patterns {
		if globMatch(strings.TrimSpace(p), branch) {
			return true
		}
	}
	return false
}

func globMatch(p, s string) bool {
	for len(p) > 0 {
		switch {
		case strings.HasPrefix(p, "**"):
			rest := strings.TrimLeft(p, "*")
			for i := 0; i <= len(s); i++ {
				if globMatch(rest, s[i:]) {
					return true
				}
			}
			return false
		case p[0] == '*':
			rest := p[1:]
			for i := 0; i <= len(s); i++ {
				if globMatch(rest, s[i:]) {
					return true
				}
				if i < len(s) && s[i] == '/' {
					return false
				}
			}
			return false
		case p[0] == '?':
			if s == "" || s[0] == '/' {
				return false
			}
		default:
			if s == "" || s[0] != p[0] {
				return false
			}
		}
		p, s = p[1:], s[1:]
	}
	return s == ""
}

// ValidRef reports whether a branch or ref name from a webhook is safe to
// hand to git: only the characters branch names normally use, not
// starting with '-' (which git would read as an option) and without the
// sequences git refuses (.., //, @{, a trailing / or .lock).
func ValidRef(ref string) bool {
	if ref == "" || len(ref) > 255 || ref[0] == '-' || ref[0] == '/' || ref[0] == '.' {
		return false
	}
	for _, r := range ref {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' || r == '/' || r == '+'
		if !ok {
			return false
		}
	}
	return !strings.Contains(ref, "..") && !strings.Contains(ref, "//") && !strings.Contains(ref, "/.") &&
		!strings.HasSuffix(ref, "/") && !strings.HasSuffix(ref, ".") && !strings.HasSuffix(ref, ".lock")
}
