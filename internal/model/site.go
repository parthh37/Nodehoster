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
	// A managed Node.js process without HTTP: a queue consumer, a bot, a
	// long-running script. Supervised like a node site but never routed.
	SiteWorker SiteType = "worker"
)

// RunsNode reports whether the site is processes supervised by the process
// manager (node and worker sites, both configured by Node), whichever
// runtime (Node.js, Bun, Deno, Python, .NET, a custom command) runs them.
func (s *Site) RunsNode() bool { return s.Type == SiteNode || s.Type == SiteWorker }

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

	// Scheduled tasks (node and worker sites): scripts run on a schedule
	// in the site's release, environment and identity.
	Tasks []ScheduledTask `json:"tasks,omitempty"`

	// Alerts: overrides of the server-wide alert rules, and this site's own.
	Alerts SiteAlerts `json:"alerts"`
	// Deployment slots (node and worker sites): staging copies with their
	// own release, instances and bindings, swapped into production.
	Slots []DeploymentSlot `json:"slots,omitempty"`

	// ActiveRelease is the deployment whose files the site currently runs
	// from. Empty means Node.AppRoot / Static.Root are used as configured.
	ActiveRelease string `json:"activeRelease,omitempty"`

	// PreviewOf is set on a preview deployment: the ID of the site it
	// previews (see PreviewConfig). Preview says what it shows.
	PreviewOf string       `json:"previewOf,omitempty"`
	Preview   *PreviewInfo `json:"preview,omitempty"`
	// Slot is set only on the configuration derived for a deployment slot
	// (SlotSite): deployments made with it go to that slot. Never stored.
	Slot string `json:"-"`

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
	// ClientCert (HTTPS only) asks clients for a certificate: mutual TLS.
	// nil = ignore, as before.
	ClientCert *ClientCertPolicy `json:"clientCert,omitempty"`

	// Slot is the deployment slot the binding routes to ("" = production).
	// Bindings always stay with their slot on a swap, like Azure's custom
	// domains: only the releases change places.
	Slot string `json:"slot,omitempty"`
}

const (
	CertModeAuto   = "auto"
	CertModeManual = "certificate"
)

type EnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"` // encrypted at rest, masked in the API
	// From takes the value from a secret store when a process starts
	// (Value is then empty and Secret false).
	From *SecretRef `json:"from,omitempty"`
	// SlotSetting (production's variables): the variable stays with
	// production and is not given to deployment slots, like an Azure
	// deployment slot setting.
	SlotSetting bool `json:"slotSetting,omitempty"`
}

type NodeConfig struct {
	AppRoot   string   `json:"appRoot"`             // physical path of the application
	Script    string   `json:"script,omitempty"`    // entry file, e.g. "server.js"
	NpmScript string   `json:"npmScript,omitempty"` // alternatively run `npm run <script>`
	Args      []string `json:"args,omitempty"`
	NodeArgs  []string `json:"nodeArgs,omitempty"` // e.g. "--max-old-space-size=512"

	NodeVersion string   `json:"nodeVersion,omitempty"` // "" = server default
	Env         []EnvVar `json:"env,omitempty"`

	// Runtime runs the processes: node (also when empty), bun, deno,
	// python, dotnet or custom; see runtimes.go. For the others, Script is
	// the entry (.py file, app .dll or .exe, the program of a custom
	// command), NpmScript a package.json script (bun) or task (deno), and
	// NodeArgs the runtime's own arguments.
	Runtime        string        `json:"runtime,omitempty"`
	RuntimeVersion string        `json:"runtimeVersion,omitempty"` // bun, deno: version; python: version or python.exe; dotnet: dotnet.exe; "" = server default
	Python         *PythonConfig `json:"python,omitempty"`

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

	LoadBalancer LoadBalancerConfig `json:"loadBalancer"`
}

// LoadBalancerConfig makes this server the front door for an application
// that also runs on other servers: requests are shared between the local
// instances and those servers. Each server hosts and deploys the site
// itself; this only decides where a request is answered.
type LoadBalancerConfig struct {
	Enabled            bool        `json:"enabled"`
	LocalWeight        int         `json:"localWeight"` // this server's share, like an upstream weight
	Servers            []Upstream  `json:"servers"`     // "http://10.0.0.12" — the site's binding on that server
	Strategy           string      `json:"strategy"`    // round_robin | least_conn | ip_hash | random
	HealthCheck        HealthCheck `json:"healthCheck"`
	InsecureSkipVerify bool        `json:"insecureSkipVerify"`
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

// RewriteRule is an inbound rule of the IIS URL Rewrite module. Match is a
// regular expression on the path including its leading "/". Targets may
// use {R:n} (rule captures, or $n), {C:n} (captures of the last matched
// condition), server variables such as {HTTP_HOST} or {QUERY_STRING},
// rewrite maps as {MapName:key} and {ToLower:…}, {ToUpper:…},
// {UrlEncode:…}, {UrlDecode:…}.
type RewriteRule struct {
	Name       string             `json:"name"`
	Enabled    bool               `json:"enabled"`
	Match      string             `json:"match"`
	Negate     bool               `json:"negate,omitempty"` // the rule applies when Match does not match
	IgnoreCase bool               `json:"ignoreCase,omitempty"`
	Host       string             `json:"host,omitempty"` // shorthand for a {HTTP_HOST} condition
	Conditions []RewriteCondition `json:"conditions,omitempty"`
	MatchAny   bool               `json:"matchAny,omitempty"` // conditions: any instead of all
	Action     string             `json:"action"`             // rewrite | redirect | block | respond | none
	// Target of rewrite and redirect. A rewrite to an absolute http(s) URL
	// proxies the request there, like URL Rewrite with ARR.
	Target       string `json:"target,omitempty"`
	QueryString  string `json:"queryString,omitempty"`  // "" keep unless the target has one | append | discard
	PreserveHost bool   `json:"preserveHost,omitempty"` // rewrite to a URL: forward the client's Host
	StatusCode   int    `json:"statusCode,omitempty"`
	Body         string `json:"body,omitempty"`        // respond
	ContentType  string `json:"contentType,omitempty"` // respond; "" = text/plain
	Stop         bool   `json:"stop"`                  // stop processing further rules
}

// RewriteCondition is an IIS URL Rewrite condition: Input (text with
// server variables, e.g. "{HTTP_HOST}") matched against Pattern, or tested
// as a file or directory under the site's physical path.
type RewriteCondition struct {
	Input      string `json:"input"`
	MatchType  string `json:"matchType"` // pattern | isFile | isDirectory
	Pattern    string `json:"pattern,omitempty"`
	Negate     bool   `json:"negate,omitempty"`
	IgnoreCase bool   `json:"ignoreCase,omitempty"`
}

// RewriteMap is a named lookup table used in targets as {Name:key}. Keys
// are matched without regard to case, like IIS.
type RewriteMap struct {
	Name         string            `json:"name"`
	DefaultValue string            `json:"defaultValue,omitempty"`
	Entries      map[string]string `json:"entries"`
}

// OutboundRule rewrites responses: a header (e.g. Location), URLs in HTML
// tag attributes (IIS filterByTags) or text anywhere in a text body.
type OutboundRule struct {
	Name       string             `json:"name"`
	Enabled    bool               `json:"enabled"`
	Scope      string             `json:"scope"`            // header | tags | body
	Header     string             `json:"header,omitempty"` // scope header
	Tags       []string           `json:"tags,omitempty"`   // scope tags: a, area, base, form, frame, head, iframe, img, input, link, script
	Match      string             `json:"match"`
	Negate     bool               `json:"negate,omitempty"`
	IgnoreCase bool               `json:"ignoreCase,omitempty"`
	Conditions []RewriteCondition `json:"conditions,omitempty"`
	MatchAny   bool               `json:"matchAny,omitempty"`
	Action     string             `json:"action"` // rewrite | none
	Value      string             `json:"value,omitempty"`
	Stop       bool               `json:"stop"`
}

// MimeMap maps a file extension to a Content-Type, like an IIS mimeMap.
type MimeMap struct {
	Extension string `json:"extension"` // ".webmanifest"
	Type      string `json:"type"`      // "application/manifest+json"
}

const (
	UnknownMimeServe = "serve" // as application/octet-stream
	UnknownMimeDeny  = "deny"  // 404, like IIS without a MIME map
)

// RewriteImportRequest is the body of POST /rewrite/import: an IIS
// web.config (or just its <rewrite> section) or Apache .htaccess rules.
type RewriteImportRequest struct {
	Format string `json:"format"` // webconfig | htaccess
	Text   string `json:"text"`
}

// RewriteImport is what an import produced. Nothing is saved: the caller
// adds the rules to a site and saves it.
type RewriteImport struct {
	Rules         []RewriteRule  `json:"rules"`
	OutboundRules []OutboundRule `json:"outboundRules"`
	RewriteMaps   []RewriteMap   `json:"rewriteMaps"`
	Warnings      []string       `json:"warnings"` // what could not be converted
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
	HTTPSRedirect   bool           `json:"httpsRedirect"`
	HSTS            HSTSConfig     `json:"hsts"`
	Compression     bool           `json:"compression"`
	MaxBodyMB       int            `json:"maxBodyMB,omitempty"`  // 0 = unlimited
	TimeoutSec      int            `json:"timeoutSec,omitempty"` // upstream response header timeout
	WebSockets      bool           `json:"webSockets"`
	RequestHeaders  []HeaderRule   `json:"requestHeaders,omitempty"`
	ResponseHeaders []HeaderRule   `json:"responseHeaders,omitempty"`
	Rewrites        []RewriteRule  `json:"rewrites,omitempty"`
	OutboundRules   []OutboundRule `json:"outboundRules,omitempty"`
	RewriteMaps     []RewriteMap   `json:"rewriteMaps,omitempty"`
	Locations       []Location     `json:"locations,omitempty"`
	// Files served by this site (static site, static-folder locations):
	// extra or overriding MIME types, and what to do with extensions no
	// map knows ("" = the server setting).
	MimeTypes        []MimeMap         `json:"mimeTypes,omitempty"`
	UnknownMimeTypes string            `json:"unknownMimeTypes,omitempty"`
	IP               IPRestrictions    `json:"ip"`
	BasicAuth        BasicAuthConfig   `json:"basicAuth"`
	RateLimit        RateLimitConfig   `json:"rateLimit"`
	Maintenance      MaintenanceConfig `json:"maintenance"`
	ErrorPages       map[string]string `json:"errorPages,omitempty"` // "502" -> HTML
	AccessLog        bool              `json:"accessLog"`
	Affinity         AffinityConfig    `json:"affinity"`
	Cache            CacheConfig       `json:"cache"`
	Banning          SiteBanning       `json:"banning"`
	WAF              WAFConfig         `json:"waf"`
}

// CacheConfig is an in-memory response cache in front of a node or proxy
// site, like IIS output caching or ARR's cache: GET and HEAD responses that are
// cacheable by HTTP's rules (Cache-Control, Expires, Vary) are answered
// from memory. The budget is per site, so one busy site cannot evict
// another's entries; the server's worst case is the sum of the budgets.
type CacheConfig struct {
	Enabled     bool `json:"enabled"`
	MaxMemoryMB int  `json:"maxMemoryMB"` // this site's budget; least recently used entries are evicted
	MaxObjectKB int  `json:"maxObjectKB"` // larger responses are passed through, not stored
	// DefaultTTLSec applies to responses without Cache-Control max-age,
	// s-maxage or Expires. 0 = cache only responses that declare freshness.
	DefaultTTLSec int      `json:"defaultTtlSec,omitempty"`
	VaryByQuery   string   `json:"varyByQuery"`           // all | none | listed
	QueryParams   []string `json:"queryParams,omitempty"` // the parameters that matter when varyByQuery is listed
	VaryHeaders   []string `json:"varyHeaders,omitempty"` // request headers that select a variant, on top of the response's Vary
	BypassPaths   []string `json:"bypassPaths,omitempty"` // path prefixes never cached, e.g. /api
}

// CacheStats are shown with a site's status.
type CacheStats struct {
	Entries  int     `json:"entries"`
	Bytes    int64   `json:"bytes"`
	Hits     int64   `json:"hits"`
	Misses   int64   `json:"misses"`
	HitRatio float64 `json:"hitRatio"` // hits / (hits + misses), 0-1
}

// AffinityConfig is ARR's "client affinity": a cookie pins a client to the
// backend that answered it first, whichever way the site spreads requests
// (a node site's instances, its load-balanced servers, a proxy site's
// upstreams). Unlike ip_hash it survives CDNs, NAT and changing client
// addresses, which Socket.IO long-polling and in-memory sessions need.
type AffinityConfig struct {
	Enabled     bool   `json:"enabled"`
	CookieName  string `json:"cookieName"`            // "" = NHAffinity
	LifetimeSec int    `json:"lifetimeSec,omitempty"` // 0 = until the browser closes
}

// DefaultAffinityCookie is the affinity cookie's name unless one is set.
const DefaultAffinityCookie = "NHAffinity"

type GitSource struct {
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	Token  string `json:"token,omitempty"` // secret, used for HTTPS auth
	// TokenFrom reads the token from a secret store at each deployment
	// instead (Token is then empty).
	TokenFrom *SecretRef `json:"tokenFrom,omitempty"`
}

type DeployConfig struct {
	Git            GitSource `json:"git"`
	InstallCommand string    `json:"installCommand,omitempty"` // "npm ci --omit=dev"
	BuildCommand   string    `json:"buildCommand,omitempty"`
	KeepReleases   int       `json:"keepReleases"`
	SharedPaths    []string  `json:"sharedPaths,omitempty"`   // persisted across releases (".env", "uploads")
	WebhookSecret  string    `json:"webhookSecret,omitempty"` // secret
	// Previews: a temporary site per pull request or branch (previews.go).
	Previews PreviewConfig `json:"previews"`
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

	// Slot is the deployment slot the deployment was made to ("" =
	// production). A swap moves the release, not this record.
	Slot string `json:"slot,omitempty"`
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
