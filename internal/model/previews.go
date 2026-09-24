package model

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// PreviewConfig makes a git-deployed site the parent of preview
// deployments, like Azure Static Web Apps' pull request environments or a
// deployment slot per branch: each pull request (GitLab: merge request)
// or matching branch gets a temporary site of its own, cloned from this
// one, at an address such as pr-42.preview.example.com. The push webhook
// creates, redeploys and deletes them.
type PreviewConfig struct {
	Enabled bool `json:"enabled"`
	// HostPattern is the preview's host name. Its first label holds
	// {number} (the pull request number) and/or {branch} (the branch,
	// made DNS-safe): "pr-{number}.preview.example.com",
	// "{branch}.preview.example.com". A branch preview without {branch}
	// in the pattern takes the branch as its whole first label.
	HostPattern string `json:"hostPattern"`
	// PullRequests: pull requests opened, pushed to or reopened get a
	// preview; closing or merging one deletes it.
	PullRequests bool `json:"pullRequests"`
	// Branches are globs ("feature/*", "release-**") of branches whose
	// pushes get a preview; deleting the branch deletes it. The
	// production branch (Deploy.Git.Branch) never gets one.
	Branches []string `json:"branches,omitempty"`
	// AllowForks builds pull requests from forks. Off by default: anyone
	// can open one, and its code would run on this server.
	AllowForks bool `json:"allowForks"`
	// MaxPreviews caps this site's previews. A new one beyond it evicts
	// the preview pushed to least recently.
	MaxPreviews int `json:"maxPreviews"`
	// ExpireDays deletes a preview after this many days without a push
	// (0 = never).
	ExpireDays int `json:"expireDays"`

	// The binding every preview gets, with the host from HostPattern.
	Protocol string `json:"protocol"` // http | https
	IP       string `json:"ip,omitempty"`
	Port     int    `json:"port"`
	// CertMode (https): "auto" gets a Let's Encrypt certificate per
	// preview host (HTTP-01), deleted with the preview; "certificate"
	// uses CertificateID, a certificate for *.<suffix>; "wildcard" finds
	// or obtains one for *.<suffix> through DNS-01 with DNSProviderID.
	CertMode      string `json:"certMode,omitempty"`
	CertificateID string `json:"certificateId,omitempty"`
	DNSProviderID string `json:"dnsProviderId,omitempty"`

	// Env overrides the parent's variables in previews (a different
	// DATABASE_URL, say); secret values are encrypted like the site's.
	// PREVIEW, PREVIEW_BRANCH, PREVIEW_PR and PREVIEW_URL are always set.
	Env []EnvVar `json:"env,omitempty"`
	// BasicAuth, when enabled, replaces the parent's basic authentication
	// in previews; AllowIPs, when set, replaces its IP allow list. Either
	// keeps previews from being public.
	BasicAuth BasicAuthConfig `json:"basicAuth"`
	AllowIPs  []string        `json:"allowIps,omitempty"`

	// ReportStatus sets a commit status (GitHub, GitLab, Gitea) on the
	// deployed commit with the preview's address, so the pull request
	// links to it. StatusToken is used for it; empty = the git token.
	ReportStatus bool   `json:"reportStatus"`
	StatusToken  string `json:"statusToken,omitempty"` // secret
}

// PreviewInfo marks a site as a preview (Site.PreviewOf is its parent)
// and says what it shows. The server maintains it; it cannot be edited.
type PreviewInfo struct {
	Key    string `json:"key"`  // "pr:42" or "branch:feature/login", unique under the parent
	Kind   string `json:"kind"` // pr | branch
	Number int    `json:"number,omitempty"`
	Branch string `json:"branch"`
	// Ref is what is fetched from the parent's repository:
	// refs/heads/<branch>, or refs/pull/42/head for a fork's pull request.
	Ref      string    `json:"ref"`
	Commit   string    `json:"commit,omitempty"` // the head commit the last push named
	Title    string    `json:"title,omitempty"`
	Author   string    `json:"author,omitempty"`
	PRURL    string    `json:"prUrl,omitempty"`
	Fork     bool      `json:"fork,omitempty"`
	Provider string    `json:"provider,omitempty"` // github | gitlab | gitea, for status reports
	Host     string    `json:"host"`
	URL      string    `json:"url"`
	LastPush time.Time `json:"lastPush"`
	// Ready is set once a deployment has succeeded: the preview then
	// starts automatically.
	Ready bool `json:"ready,omitempty"`
}

const (
	PreviewPR     = "pr"
	PreviewBranch = "branch"

	PreviewCertAuto     = CertModeAuto
	PreviewCertManual   = CertModeManual
	PreviewCertWildcard = "wildcard"

	DefaultMaxPreviews = 10
	MaxPreviewsLimit   = 100
	MaxPreviewEnvVars  = 100
	// PreviewKeepReleases: a preview keeps its active release and the
	// one before it at most (prune never removes those two).
	PreviewKeepReleases = 1
)

// Preview states, as the API shows them.
const (
	PreviewPending   = "pending"   // created, nothing deployed yet
	PreviewDeploying = "deploying" // a deployment is running or queued
	PreviewReady     = "ready"
	PreviewFailed    = "failed" // the last deployment failed
	PreviewDeleting  = "deleting"
)

// PreviewView is a preview as GET /sites/{id}/previews lists it.
type PreviewView struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Preview        PreviewInfo `json:"preview"`
	State          string      `json:"state"`
	SiteState      SiteState   `json:"siteState"`
	LastDeployment *Deployment `json:"lastDeployment,omitempty"`
	CreatedAt      time.Time   `json:"createdAt"`
}

// IsPreview reports whether the site is another site's preview.
func (s *Site) IsPreview() bool { return s.PreviewOf != "" }

var (
	patternLabelRe = regexp.MustCompile(`^[a-z0-9-]*$`)
	branchGlobRe   = regexp.MustCompile(`^[A-Za-z0-9._/*?-]+$`)
)

// SplitHostPattern returns the first label of a host pattern (the one
// with the placeholders) and the rest, the suffix a wildcard certificate
// for the previews covers.
func SplitHostPattern(p string) (label, suffix string) {
	label, suffix, _ = strings.Cut(p, ".")
	return label, suffix
}

func (p *PreviewConfig) applyDefaults() {
	p.HostPattern = strings.ToLower(strings.TrimSpace(p.HostPattern))
	p.Protocol = strings.ToLower(strings.TrimSpace(p.Protocol))
	if p.IP == "*" {
		p.IP = ""
	}
	if !p.Enabled {
		return
	}
	if p.Protocol == "" {
		p.Protocol = "http"
	}
	if p.Port == 0 {
		p.Port = 80
		if p.Protocol == "https" {
			p.Port = 443
		}
	}
	if p.Protocol == "https" && p.CertMode == "" {
		p.CertMode = PreviewCertAuto
		if p.CertificateID != "" {
			p.CertMode = PreviewCertManual
		}
	}
	if p.Protocol == "http" {
		p.CertMode, p.CertificateID, p.DNSProviderID = "", "", ""
	}
	if p.MaxPreviews <= 0 {
		p.MaxPreviews = DefaultMaxPreviews
	}
	if p.BasicAuth.Realm == "" {
		p.BasicAuth.Realm = "Preview"
	}
}

// validatePreviews checks the preview settings of a site. Settings that
// need the server (certificates, DNS providers) are checked by core.
func (s *Site) validatePreviews() error {
	p := s.Deploy.Previews
	if s.IsPreview() {
		if p.Enabled {
			return verr("deploy.previews.enabled", "a preview cannot have previews of its own")
		}
		return nil
	}
	if !p.Enabled {
		return nil
	}
	const f = "deploy.previews"
	switch {
	case s.Type != SiteNode && s.Type != SiteStatic:
		return verr(f+".enabled", "previews need a Node.js application or static site")
	case strings.TrimSpace(s.Deploy.Git.Repo) == "":
		return verr(f+".enabled", "previews are built from git: set the repository first")
	case strings.TrimSpace(s.Deploy.Git.Branch) == "":
		return verr("deploy.git.branch", "previews need the production branch set, so that it never gets one")
	case s.Deploy.WebhookSecret == "":
		return verr("deploy.webhookSecret", "previews are created by the push webhook: set its secret")
	case !p.PullRequests && len(p.Branches) == 0:
		return verr(f+".pullRequests", "choose pull requests and/or branches to preview")
	}
	if err := ValidateHostPattern(p.HostPattern); err != nil {
		return verr(f+".hostPattern", "%v", err)
	}
	for i, g := range p.Branches {
		if !branchGlobRe.MatchString(g) || strings.HasPrefix(g, "-") || strings.HasPrefix(g, "/") || strings.Contains(g, "..") {
			return verr(fmt.Sprintf("%s.branches[%d]", f, i), "%q is not a branch pattern (letters, digits, . _ - / and the wildcards * ** ?)", g)
		}
	}
	if p.MaxPreviews > MaxPreviewsLimit {
		return verr(f+".maxPreviews", "at most %d previews", MaxPreviewsLimit)
	}
	if p.ExpireDays < 0 || p.ExpireDays > 365 {
		return verr(f+".expireDays", "must be between 0 (never) and 365 days")
	}
	if p.Protocol != "http" && p.Protocol != "https" {
		return verr(f+".protocol", "must be http or https")
	}
	if p.Port < 1 || p.Port > 65535 {
		return verr(f+".port", "must be between 1 and 65535")
	}
	if p.IP != "" && net.ParseIP(p.IP) == nil {
		return verr(f+".ip", "%q is not an IP address", p.IP)
	}
	if p.Protocol == "https" {
		switch p.CertMode {
		case PreviewCertAuto:
		case PreviewCertManual:
			if p.CertificateID == "" {
				return verr(f+".certificateId", "select a certificate for *.%s", hostSuffix(p.HostPattern))
			}
		case PreviewCertWildcard:
			if p.DNSProviderID == "" {
				return verr(f+".dnsProviderId", "a wildcard certificate is obtained through DNS-01: select a DNS provider")
			}
		default:
			return verr(f+".certMode", "must be auto, certificate or wildcard")
		}
	}
	if len(p.Env) > MaxPreviewEnvVars {
		return verr(f+".env", "at most %d variables", MaxPreviewEnvVars)
	}
	for i, e := range p.Env {
		if !envNameRe.MatchString(e.Name) {
			return verr(fmt.Sprintf("%s.env[%d].name", f, i), "%q is not a valid variable name", e.Name)
		}
		if isInjectedPreviewVar(e.Name) {
			return verr(fmt.Sprintf("%s.env[%d].name", f, i), "%s is set by NodeHoster in every preview", e.Name)
		}
	}
	if p.BasicAuth.Enabled && len(p.BasicAuth.Users) == 0 {
		return verr(f+".basicAuth.users", "add at least one user")
	}
	for i, c := range p.AllowIPs {
		if _, err := ParseCIDROrIP(c); err != nil {
			return verr(fmt.Sprintf("%s.allowIps[%d]", f, i), "%v", err)
		}
	}
	return nil
}

// PreviewVars are the variables NodeHoster sets in every preview.
var PreviewVars = []string{"PREVIEW", "PREVIEW_BRANCH", "PREVIEW_PR", "PREVIEW_URL"}

func isInjectedPreviewVar(name string) bool {
	for _, v := range PreviewVars {
		if strings.EqualFold(v, name) {
			return true
		}
	}
	return false
}

func hostSuffix(pattern string) string {
	_, s := SplitHostPattern(pattern)
	return s
}

// ValidateHostPattern checks a preview host pattern: placeholders in the
// first label only, at least one of them, room left in that label for a
// name, and a valid host name after it.
func ValidateHostPattern(p string) error {
	if p == "" {
		return fmt.Errorf("enter a host pattern such as pr-{number}.preview.example.com")
	}
	label, suffix := SplitHostPattern(p)
	if suffix == "" {
		return fmt.Errorf("add the domain the previews live under, e.g. {branch}.preview.example.com")
	}
	if !strings.Contains(label, "{number}") && !strings.Contains(label, "{branch}") {
		return fmt.Errorf("the first label must contain {number} or {branch}")
	}
	if strings.ContainsAny(suffix, "{}*") {
		return fmt.Errorf("placeholders go in the first label only")
	}
	fixed := strings.NewReplacer("{number}", "", "{branch}", "").Replace(label)
	if !patternLabelRe.MatchString(fixed) {
		return fmt.Errorf("the first label may only have letters, digits and '-' around the placeholders")
	}
	if len(fixed) > 40 {
		return fmt.Errorf("the first label leaves too little room for the preview's name")
	}
	if !hostRe.MatchString(suffix) {
		return fmt.Errorf("%q is not a valid host name", suffix)
	}
	if len(p) > 200 {
		return fmt.Errorf("too long")
	}
	return nil
}
