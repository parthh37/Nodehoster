// Package model holds the persistent configuration types shared by every
// subsystem and exposed verbatim over the REST API. Site configuration is
// stored as a JSON document, so adding a field here is a schema change that
// needs no migration: old rows simply decode with the zero value.
package model

import (
	"path/filepath"
	"time"
)

type SiteType string

const (
	SiteNode     SiteType = "node"     // managed Node.js process(es) behind the proxy
	SiteProxy    SiteType = "proxy"    // reverse proxy to arbitrary upstream URLs
	SiteStatic   SiteType = "static"   // static files from a directory
	SiteRedirect SiteType = "redirect" // redirect every request elsewhere
)

// Site is the unit of hosting, the equivalent of an IIS site: a set of
// bindings plus what answers the requests arriving on them.
type Site struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Type        SiteType  `json:"type"`
	AutoStart   bool      `json:"autoStart"`
	Bindings    []Binding `json:"bindings"`

	Node     *NodeConfig     `json:"node,omitempty"`
	Proxy    *ProxyConfig    `json:"proxy,omitempty"`
	Static   *StaticConfig   `json:"static,omitempty"`
	Redirect *RedirectConfig `json:"redirect,omitempty"`

	Routing RoutingConfig `json:"routing"`
	Deploy  DeployConfig  `json:"deploy"`

	// ActiveRelease is the deployment whose files the site currently runs
	// from. Empty means Node.AppRoot / Static.Root are used as configured.
	ActiveRelease string `json:"activeRelease,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Binding mirrors an IIS binding: protocol, IP, port and host name.
type Binding struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"` // "http" | "https"
	IP       string `json:"ip"`       // "" or "*" = all addresses
	Port     int    `json:"port"`
	Host     string `json:"host"` // "" = any host; "*.example.com" wildcard allowed

	// HTTPS only. CertMode "auto" obtains and renews a Let's Encrypt
	// certificate for Host; "certificate" uses CertificateID.
	CertMode      string `json:"certMode,omitempty"`
	CertificateID string `json:"certificateId,omitempty"`
}

const (
	CertModeAuto   = "auto"
	CertModeManual = "certificate"
)

type EnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"` // encrypted at rest, masked in the API
}

type NodeConfig struct {
	AppRoot   string   `json:"appRoot"`             // physical path of the application
	Script    string   `json:"script,omitempty"`    // entry file, e.g. "server.js"
	NpmScript string   `json:"npmScript,omitempty"` // alternatively run `npm run <script>`
	Args      []string `json:"args,omitempty"`
	NodeArgs  []string `json:"nodeArgs,omitempty"` // e.g. "--max-old-space-size=512"

	NodeVersion string   `json:"nodeVersion,omitempty"` // "" = server default
	Env         []EnvVar `json:"env,omitempty"`

	Instances int    `json:"instances"`           // processes load-balanced behind the site
	PortMode  string `json:"portMode"`            // "auto" (PORT env assigned) | "fixed"
	FixedPort int    `json:"fixedPort,omitempty"` // when PortMode == "fixed" (1 instance only)

	RestartPolicy      string   `json:"restartPolicy"`      // "always" | "on-failure" | "never"
	MaxRestarts        int      `json:"maxRestarts"`        // within RestartWindowSec, 0 = unlimited
	RestartWindowSec   int      `json:"restartWindowSec"`   // rapid-fail window (IIS rapid-fail protection)
	RapidFailAction    string   `json:"rapidFailAction"`    // "recover" (retry after a pause) | "stop" (wait for an operator)
	RecoverAfterSec    int      `json:"recoverAfterSec"`    // first pause before "recover" retries; doubles per trip, capped at an hour
	StartupTimeoutSec  int      `json:"startupTimeoutSec"`  // time to start listening
	ShutdownTimeoutSec int      `json:"shutdownTimeoutSec"` // graceful stop before kill
	AgentEnabled       bool     `json:"agentEnabled"`       // inject the NodeHoster agent (graceful shutdown + metrics)
	WatchFiles         bool     `json:"watchFiles"`         // restart when app files change
	WatchIgnore        []string `json:"watchIgnore,omitempty"`

	HealthCheck HealthCheck   `json:"healthCheck"`
	Recycle     RecycleConfig `json:"recycle"`
	Limits      ProcessLimits `json:"limits"`
	RunAs       RunAsConfig   `json:"runAs"`
}

type HealthCheck struct {
	Enabled            bool   `json:"enabled"`
	Path               string `json:"path"`
	IntervalSec        int    `json:"intervalSec"`
	TimeoutSec         int    `json:"timeoutSec"`
	UnhealthyThreshold int    `json:"unhealthyThreshold"`
}

// RecycleConfig mirrors IIS application pool recycling.
type RecycleConfig struct {
	MemoryLimitMB   int      `json:"memoryLimitMB,omitempty"`   // private bytes of the process tree
	PeriodicMinutes int      `json:"periodicMinutes,omitempty"` // regular time interval
	ScheduleTimes   []string `json:"scheduleTimes,omitempty"`   // "HH:MM" local time
	MaxRequests     int64    `json:"maxRequests,omitempty"`
}

// ProcessLimits are enforced by a Windows Job Object.
type ProcessLimits struct {
	CPUPercent    int `json:"cpuPercent,omitempty"`    // hard cap, 1-100
	MemoryLimitMB int `json:"memoryLimitMB,omitempty"` // hard cap; the process is killed above it
}

// RunAsConfig is the equivalent of an application pool identity.
type RunAsConfig struct {
	Enabled  bool   `json:"enabled"`
	Username string `json:"username,omitempty"` // "DOMAIN\\user" or "user"
	Password string `json:"password,omitempty"` // secret
}

type Upstream struct {
	URL    string `json:"url"`
	Weight int    `json:"weight,omitempty"`
}

type ProxyConfig struct {
	Upstreams          []Upstream  `json:"upstreams"`
	LoadBalancing      string      `json:"loadBalancing"` // round_robin | least_conn | ip_hash | random
	HealthCheck        HealthCheck `json:"healthCheck"`
	PreserveHost       bool        `json:"preserveHost"`
	InsecureSkipVerify bool        `json:"insecureSkipVerify"`
}

type StaticConfig struct {
	Root              string   `json:"root"`
	IndexFiles        []string `json:"indexFiles"`
	SPAFallback       bool     `json:"spaFallback"`
	DirectoryBrowsing bool     `json:"directoryBrowsing"`
	CacheControl      string   `json:"cacheControl,omitempty"`
}

type RedirectConfig struct {
	TargetURL    string `json:"targetUrl"`
	StatusCode   int    `json:"statusCode"`
	PreservePath bool   `json:"preservePath"`
}

type HeaderRule struct {
	Action string `json:"action"` // set | add | remove
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
}

// RewriteRule is a small subset of the IIS URL Rewrite module.
type RewriteRule struct {
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Match      string `json:"match"`            // regular expression on the path (+query if IncludeQuery)
	Host       string `json:"host,omitempty"`   // optional regular expression condition on Host
	Action     string `json:"action"`           // rewrite | redirect | block | respond
	Target     string `json:"target,omitempty"` // $1-style substitutions allowed
	StatusCode int    `json:"statusCode,omitempty"`
	Body       string `json:"body,omitempty"` // for respond
	Stop       bool   `json:"stop"`           // stop processing further rules
}

// Location mounts another backend under a path prefix, like an IIS
// application or virtual directory beneath the site.
type Location struct {
	Path        string `json:"path"`             // "/api"
	Kind        string `json:"kind"`             // site | url | static
	SiteID      string `json:"siteId,omitempty"` // Kind == site
	URL         string `json:"url,omitempty"`    // Kind == url
	Root        string `json:"root,omitempty"`   // Kind == static
	StripPrefix bool   `json:"stripPrefix"`
}

type HSTSConfig struct {
	Enabled           bool `json:"enabled"`
	MaxAgeSec         int  `json:"maxAgeSec"`
	IncludeSubdomains bool `json:"includeSubdomains"`
	Preload           bool `json:"preload"`
}

type BasicAuthUser struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash,omitempty"` // bcrypt; set via Password on write
	Password     string `json:"password,omitempty"`     // write-only
}

type BasicAuthConfig struct {
	Enabled      bool            `json:"enabled"`
	Realm        string          `json:"realm"`
	Users        []BasicAuthUser `json:"users"`
	ExcludePaths []string        `json:"excludePaths,omitempty"`
}

type RateLimitConfig struct {
	Enabled           bool    `json:"enabled"`
	RequestsPerSecond float64 `json:"requestsPerSecond"`
	Burst             int     `json:"burst"`
}

type IPRestrictions struct {
	Allow []string `json:"allow,omitempty"` // CIDR or single IP; non-empty = default deny
	Deny  []string `json:"deny,omitempty"`
}

type MaintenanceConfig struct {
	Enabled       bool     `json:"enabled"`
	HTML          string   `json:"html,omitempty"`
	AllowIPs      []string `json:"allowIps,omitempty"` // bypass for these clients
	RetryAfterSec int      `json:"retryAfterSec,omitempty"`
}

type RoutingConfig struct {
	HTTPSRedirect   bool              `json:"httpsRedirect"`
	HSTS            HSTSConfig        `json:"hsts"`
	Compression     bool              `json:"compression"`
	MaxBodyMB       int               `json:"maxBodyMB,omitempty"`  // 0 = unlimited
	TimeoutSec      int               `json:"timeoutSec,omitempty"` // upstream response header timeout
	WebSockets      bool              `json:"webSockets"`
	RequestHeaders  []HeaderRule      `json:"requestHeaders,omitempty"`
	ResponseHeaders []HeaderRule      `json:"responseHeaders,omitempty"`
	Rewrites        []RewriteRule     `json:"rewrites,omitempty"`
	Locations       []Location        `json:"locations,omitempty"`
	IP              IPRestrictions    `json:"ip"`
	BasicAuth       BasicAuthConfig   `json:"basicAuth"`
	RateLimit       RateLimitConfig   `json:"rateLimit"`
	Maintenance     MaintenanceConfig `json:"maintenance"`
	ErrorPages      map[string]string `json:"errorPages,omitempty"` // "502" -> HTML
	AccessLog       bool              `json:"accessLog"`
}

type GitSource struct {
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	Token  string `json:"token,omitempty"` // secret, used for HTTPS auth
}

type DeployConfig struct {
	Git            GitSource `json:"git"`
	InstallCommand string    `json:"installCommand,omitempty"` // "npm ci --omit=dev"
	BuildCommand   string    `json:"buildCommand,omitempty"`
	KeepReleases   int       `json:"keepReleases"`
	SharedPaths    []string  `json:"sharedPaths,omitempty"`   // persisted across releases (".env", "uploads")
	WebhookSecret  string    `json:"webhookSecret,omitempty"` // secret
}

// Deployment is a record of one deploy attempt.
type Deployment struct {
	ID         string     `json:"id"`
	SiteID     string     `json:"siteId"`
	Source     string     `json:"source"` // zip | git | webhook | rollback
	Status     string     `json:"status"` // running | succeeded | failed
	Commit     string     `json:"commit,omitempty"`
	Message    string     `json:"message,omitempty"`
	ReleaseDir string     `json:"releaseDir,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	User       string     `json:"user,omitempty"`
}

// ReleaseDir is where a deployment's files live.
func ReleaseDir(sitesDir, siteID, release string) string {
	return filepath.Join(sitesDir, siteID, "releases", release)
}

// SharedDir holds files that persist across deployments.
func SharedDir(sitesDir, siteID string) string {
	return filepath.Join(sitesDir, siteID, "shared")
}

// ResolveRoot returns the directory a site actually serves from. When a
// deployment is active, a relative configured path is resolved inside the
// release (e.g. "dist" for a static build) and an absolute one is ignored.
func (s *Site) ResolveRoot(sitesDir, configured string) string {
	if s.ActiveRelease == "" {
		return configured
	}
	base := ReleaseDir(sitesDir, s.ID, s.ActiveRelease)
	if configured == "" || filepath.IsAbs(configured) || isWindowsAbs(configured) {
		return base
	}
	return filepath.Join(base, configured)
}

func isWindowsAbs(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}
