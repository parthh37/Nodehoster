package model

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	mapNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
	mimeExtRe = regexp.MustCompile(`^\.([A-Za-z0-9][A-Za-z0-9._+-]{0,31})?$`) // "." = files without an extension
	mimeRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*(\s*;\s*[A-Za-z0-9_-]+=[^;\s]+)*$`)
	headerRe  = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

// OutboundTags are the HTML tags an outbound rule can filter on, with the
// attributes that hold URLs (the IIS filterByTags set).
var OutboundTags = map[string][]string{
	"a":      {"href"},
	"area":   {"href"},
	"base":   {"href"},
	"form":   {"action"},
	"frame":  {"src", "longdesc"},
	"head":   {"profile"},
	"iframe": {"src", "longdesc"},
	"img":    {"src", "longdesc", "usemap"},
	"input":  {"src", "usemap"},
	"link":   {"href"},
	"script": {"src"},
}

// CompilePattern compiles a rewrite pattern the way the engine does.
func CompilePattern(p string, ignoreCase bool) (*regexp.Regexp, error) {
	if ignoreCase {
		p = "(?i)" + p
	}
	return regexp.Compile(p)
}

// ValidateRewrites checks the rewrite rules, outbound rules and maps.
func (r RoutingConfig) ValidateRewrites() error {
	maps := map[string]bool{}
	for i, m := range r.RewriteMaps {
		f := fmt.Sprintf("routing.rewriteMaps[%d]", i)
		if !mapNameRe.MatchString(m.Name) {
			return verr(f+".name", "use letters, digits, '_', '.' or '-', starting with a letter")
		}
		key := strings.ToLower(m.Name)
		if maps[key] {
			return verr(f+".name", "another map is called %q", m.Name)
		}
		maps[key] = true
	}
	for i, rw := range r.Rewrites {
		f := fmt.Sprintf("routing.rewrites[%d]", i)
		if _, err := CompilePattern(rw.Match, rw.IgnoreCase); err != nil {
			return verr(f+".match", "invalid regular expression: %v", err)
		}
		if rw.Host != "" {
			if _, err := regexp.Compile(rw.Host); err != nil {
				return verr(f+".host", "invalid regular expression: %v", err)
			}
		}
		if err := validateConditions(f, rw.Conditions); err != nil {
			return err
		}
		switch rw.Action {
		case "rewrite", "redirect":
			if strings.TrimSpace(rw.Target) == "" {
				return verr(f+".target", "a target is required")
			}
		case "block", "respond", "none":
		default:
			return verr(f+".action", "must be rewrite, redirect, block, respond or none")
		}
		switch rw.QueryString {
		case "", "append", "discard":
		default:
			return verr(f+".queryString", "must be append or discard")
		}
		c := rw.StatusCode
		switch rw.Action {
		case "redirect":
			if c != 0 && c != 301 && c != 302 && c != 303 && c != 307 && c != 308 {
				return verr(f+".statusCode", "must be 301, 302, 303, 307 or 308")
			}
		case "block":
			if c != 0 && (c < 400 || c > 599) {
				return verr(f+".statusCode", "must be between 400 and 599")
			}
		case "respond":
			if c != 0 && (c < 200 || c > 599) {
				return verr(f+".statusCode", "must be between 200 and 599")
			}
		}
	}
	for i, o := range r.OutboundRules {
		f := fmt.Sprintf("routing.outboundRules[%d]", i)
		switch o.Scope {
		case "header":
			if !headerRe.MatchString(o.Header) {
				return verr(f+".header", "enter a header name, e.g. Location")
			}
		case "tags":
			if len(o.Tags) == 0 {
				return verr(f+".tags", "choose at least one tag")
			}
			for _, t := range o.Tags {
				if _, ok := OutboundTags[strings.ToLower(t)]; !ok {
					return verr(f+".tags", "%q is not a supported tag", t)
				}
			}
		case "body":
		default:
			return verr(f+".scope", "must be header, tags or body")
		}
		if _, err := CompilePattern(o.Match, o.IgnoreCase); err != nil {
			return verr(f+".match", "invalid regular expression: %v", err)
		}
		if err := validateConditions(f, o.Conditions); err != nil {
			return err
		}
		if o.Action != "rewrite" && o.Action != "none" {
			return verr(f+".action", "must be rewrite or none")
		}
	}
	return nil
}

func validateConditions(f string, cs []RewriteCondition) error {
	for j, c := range cs {
		cf := fmt.Sprintf("%s.conditions[%d]", f, j)
		if strings.TrimSpace(c.Input) == "" {
			return verr(cf+".input", "an input is required, e.g. {HTTP_HOST}")
		}
		switch c.MatchType {
		case "", "pattern":
			if _, err := CompilePattern(c.Pattern, c.IgnoreCase); err != nil {
				return verr(cf+".pattern", "invalid regular expression: %v", err)
			}
		case "isFile", "isDirectory":
		default:
			return verr(cf+".matchType", "must be pattern, isFile or isDirectory")
		}
	}
	return nil
}

func validateMimeMaps(f string, maps []MimeMap) error {
	seen := map[string]bool{}
	for i, m := range maps {
		ext := strings.ToLower(m.Extension)
		if !mimeExtRe.MatchString(ext) {
			return verr(fmt.Sprintf("%s[%d].extension", f, i), "enter an extension starting with a dot, e.g. .webmanifest (\".\" for files without one)")
		}
		if seen[ext] {
			return verr(fmt.Sprintf("%s[%d].extension", f, i), "%s is listed twice", ext)
		}
		seen[ext] = true
		if !mimeRe.MatchString(strings.TrimSpace(m.Type)) {
			return verr(fmt.Sprintf("%s[%d].type", f, i), "enter a MIME type, e.g. application/json")
		}
	}
	return nil
}

// Validate checks the server-wide MIME settings.
func (m *MimeSettings) Validate() error {
	if m.UnknownTypes == "" {
		m.UnknownTypes = UnknownMimeServe
	}
	if m.UnknownTypes != UnknownMimeServe && m.UnknownTypes != UnknownMimeDeny {
		return verr("mime.unknownTypes", "must be serve or deny")
	}
	return validateMimeMaps("mime.types", m.Types)
}
