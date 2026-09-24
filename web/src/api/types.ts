// Hand-written mirrors of the Go structs in internal/model. Field names are
// the `json` tags. Times are RFC 3339 strings. Fields tagged `omitempty` are
// optional here.

import type { AlertSettings, SiteAlerts } from './alerts';
import type { WAFConfig, WAFSettings } from './wafTypes';

export const SECRET = '__SECRET__';

// ---------------------------------------------------------------- sites

/** worker: a Node.js process that serves no HTTP (queue consumer, bot). */
export type SiteType = 'node' | 'worker' | 'proxy' | 'static' | 'redirect';

export interface Binding {
  id: string;
  protocol: 'http' | 'https' | string;
  ip: string;
  port: number;
  host: string;
  certMode?: 'auto' | 'certificate' | '';
  certificateId?: string;
  /** HTTPS only: ask clients for a certificate (mutual TLS). Absent = ignore. */
  clientCert?: ClientCertPolicy | null;
  /** Deployment slot the binding routes to; "" or absent = production. */
  slot?: string;
}

export type ClientCertMode = 'ignore' | 'accept' | 'require';

/** A binding's client certificate policy (IIS "SSL Settings" › Client certificates). */
export interface ClientCertPolicy {
  mode?: ClientCertMode | string;
  /** Trusted issuing CAs, PEM. */
  caPem?: string;
  /** CN, subject DN or a DNS / email / URI SAN, ignoring case. */
  allowedSubjects?: string[] | null;
  /** SHA-256, hex. */
  allowedFingerprints?: string[] | null;
  /** accept mode: path prefixes answered 403 without a valid certificate. */
  requirePaths?: string[] | null;
}

export interface EnvVar {
  name: string;
  value: string;
  secret?: boolean;
  /** Read from a secret store when a process starts (value is then empty). */
  from?: SecretRef;
  /** Production variable that stays in production and is not given to deployment slots. */
  slotSetting?: boolean;
}

export interface HealthCheck {
  enabled: boolean;
  path: string;
  intervalSec: number;
  timeoutSec: number;
  unhealthyThreshold: number;
}

export interface RecycleConfig {
  memoryLimitMB?: number;
  periodicMinutes?: number;
  scheduleTimes?: string[];
  maxRequests?: number;
}

export interface ProcessLimits {
  cpuPercent?: number;
  memoryLimitMB?: number;
}

export interface RunAsConfig {
  enabled: boolean;
  username?: string;
  password?: string;
}

export type PortMode = 'auto' | 'fixed';
export type RestartPolicy = 'always' | 'on-failure' | 'never';
export type RapidFailAction = 'recover' | 'stop';

/** What runs a node or worker site's processes; absent on sites saved before runtimes existed (= node). */
export type RuntimeName = 'node' | 'bun' | 'deno' | 'python' | 'dotnet' | 'custom';

export type PythonServer = 'uvicorn' | 'hypercorn' | 'waitress';

/** How a python site starts: exactly one of node.script, module or server (with app). */
export interface PythonConfig {
  module?: string;
  server?: PythonServer | '' | string;
  /** "main:app" for server. */
  app?: string;
  /** Virtual environment relative to the application folder; "" = .venv. */
  venv?: string;
}

/**
 * The process configuration of node and worker sites, whatever the runtime
 * (the name is historical). For runtimes other than Node.js, script is the
 * entry (.py, app .dll/.exe, a custom program), npmScript a package.json
 * script (bun) or task (deno) and nodeArgs the runtime's own arguments.
 */
export interface NodeConfig {
  appRoot: string;
  script?: string;
  npmScript?: string;
  args?: string[];
  nodeArgs?: string[];
  nodeVersion?: string;
  env?: EnvVar[];
  runtime?: RuntimeName | string;
  /** bun, deno: installed version; python: "3.12" or python.exe; dotnet: dotnet.exe; "" = server default. */
  runtimeVersion?: string;
  python?: PythonConfig | null;
  instances: number;
  portMode: PortMode | string;
  fixedPort?: number;
  restartPolicy: RestartPolicy | string;
  maxRestarts: number;
  restartWindowSec: number;
  rapidFailAction: RapidFailAction | string;
  recoverAfterSec: number;
  startupTimeoutSec: number;
  shutdownTimeoutSec: number;
  agentEnabled: boolean;
  watchFiles: boolean;
  watchIgnore?: string[];
  healthCheck: HealthCheck;
  recycle: RecycleConfig;
  limits: ProcessLimits;
  runAs: RunAsConfig;
  loadBalancer: LoadBalancerConfig;
}

/** Share a node site's traffic between this server and others running the same app. */
export interface LoadBalancerConfig {
  enabled: boolean;
  localWeight: number;
  servers: Upstream[];
  strategy: LoadBalancing | string;
  healthCheck: HealthCheck;
  insecureSkipVerify: boolean;
}

export interface Upstream {
  url: string;
  weight?: number;
}

export type LoadBalancing = 'round_robin' | 'least_conn' | 'ip_hash' | 'random';

export interface ProxyConfig {
  upstreams: Upstream[];
  loadBalancing: LoadBalancing | string;
  healthCheck: HealthCheck;
  preserveHost: boolean;
  insecureSkipVerify: boolean;
}

export interface StaticConfig {
  root: string;
  indexFiles: string[];
  spaFallback: boolean;
  directoryBrowsing: boolean;
  cacheControl?: string;
}

export interface RedirectConfig {
  targetUrl: string;
  statusCode: number;
  preservePath: boolean;
}

export interface HeaderRule {
  action: 'set' | 'add' | 'remove' | string;
  name: string;
  value?: string;
}

export type RewriteAction = 'rewrite' | 'redirect' | 'block' | 'respond' | 'none';

export type ConditionMatchType = 'pattern' | 'isFile' | 'isDirectory';

/** IIS URL Rewrite condition: Input (text with server variables) matched against Pattern, or tested as a file/directory. */
export interface RewriteCondition {
  input: string;
  matchType: ConditionMatchType | string;
  pattern?: string;
  negate?: boolean;
  ignoreCase?: boolean;
}

/** '' keeps the original query unless the target has one. */
export type RewriteQueryString = '' | 'append' | 'discard';

/** Inbound rule. Match is a regular expression on the path including its leading "/". */
export interface RewriteRule {
  name: string;
  enabled: boolean;
  match: string;
  negate?: boolean;
  ignoreCase?: boolean;
  /** Shorthand for a {HTTP_HOST} condition. */
  host?: string;
  conditions?: RewriteCondition[];
  matchAny?: boolean;
  action: RewriteAction | string;
  /** A rewrite to an absolute http(s) URL proxies the request there. */
  target?: string;
  queryString?: RewriteQueryString | string;
  preserveHost?: boolean;
  statusCode?: number;
  body?: string;
  contentType?: string;
  stop: boolean;
}

/** Lookup table used in targets as {Name:key}. */
export interface RewriteMap {
  name: string;
  defaultValue?: string;
  entries: Record<string, string> | null;
}

export type OutboundScope = 'header' | 'tags' | 'body';

export interface OutboundRule {
  name: string;
  enabled: boolean;
  scope: OutboundScope | string;
  header?: string;
  tags?: string[];
  match: string;
  negate?: boolean;
  ignoreCase?: boolean;
  conditions?: RewriteCondition[];
  matchAny?: boolean;
  action: 'rewrite' | 'none' | string;
  value?: string;
  stop: boolean;
}

export type RewriteImportFormat = 'webconfig' | 'htaccess';

export interface RewriteImportRequest {
  format: RewriteImportFormat;
  text: string;
}

/** Result of POST /rewrite/import. Nothing is saved server-side. */
export interface RewriteImport {
  rules: RewriteRule[] | null;
  outboundRules: OutboundRule[] | null;
  rewriteMaps: RewriteMap[] | null;
  warnings: string[] | null;
}

/** Maps a file extension (".webmanifest") to a Content-Type. */
export interface MimeMap {
  extension: string;
  type: string;
}

/** serve = as application/octet-stream; deny = 404 (IIS without a MIME map). */
export type UnknownMimeTypes = 'serve' | 'deny';

export type LocationKind = 'site' | 'url' | 'static';

export interface Location {
  path: string;
  kind: LocationKind | string;
  siteId?: string;
  url?: string;
  root?: string;
  stripPrefix: boolean;
}

export interface HSTSConfig {
  enabled: boolean;
  maxAgeSec: number;
  includeSubdomains: boolean;
  preload: boolean;
}

export interface BasicAuthUser {
  username: string;
  passwordHash?: string;
  password?: string;
}

export interface BasicAuthConfig {
  enabled: boolean;
  realm: string;
  users: BasicAuthUser[] | null;
  excludePaths?: string[];
}

export interface RateLimitConfig {
  enabled: boolean;
  requestsPerSecond: number;
  burst: number;
}

export interface IPRestrictions {
  allow?: string[];
  deny?: string[];
}

export interface MaintenanceConfig {
  enabled: boolean;
  html?: string;
  allowIps?: string[];
  retryAfterSec?: number;
}

export interface RoutingConfig {
  httpsRedirect: boolean;
  hsts: HSTSConfig;
  compression: boolean;
  maxBodyMB?: number;
  timeoutSec?: number;
  webSockets: boolean;
  requestHeaders?: HeaderRule[];
  responseHeaders?: HeaderRule[];
  rewrites?: RewriteRule[];
  outboundRules?: OutboundRule[];
  rewriteMaps?: RewriteMap[];
  locations?: Location[];
  mimeTypes?: MimeMap[];
  /** '' = the server setting. */
  unknownMimeTypes?: '' | UnknownMimeTypes | string;
  ip: IPRestrictions;
  basicAuth: BasicAuthConfig;
  rateLimit: RateLimitConfig;
  maintenance: MaintenanceConfig;
  errorPages?: Record<string, string>;
  accessLog: boolean;
  affinity: AffinityConfig;
  cache: CacheConfig;
  banning: SiteBanning;
  /** Web application firewall; absent on sites saved before it existed (off). */
  waf?: WAFConfig;
}

/** The site's part in automatic IP banning. */
export interface SiteBanning {
  exempt?: boolean;
  allowTrapPaths?: boolean;
}

/** In-memory response cache (IIS output caching). */
export interface CacheConfig {
  enabled: boolean;
  /** This site's budget. */
  maxMemoryMB: number;
  maxObjectKB: number;
  /** For responses without max-age/Expires; 0 = cache only those that declare freshness. */
  defaultTtlSec?: number;
  varyByQuery: 'all' | 'none' | 'listed' | string;
  queryParams?: string[];
  varyHeaders?: string[];
  bypassPaths?: string[];
}

export interface CacheStats {
  entries: number;
  bytes: number;
  hits: number;
  misses: number;
  /** 0-1 */
  hitRatio: number;
}

/** Cookie-based session affinity (ARR client affinity). */
export interface AffinityConfig {
  enabled: boolean;
  /** '' = NHAffinity */
  cookieName: string;
  /** 0 = until the browser closes */
  lifetimeSec?: number;
}

export interface GitSource {
  repo?: string;
  branch?: string;
  token?: string;
  /** Read from a secret store at each deployment instead of token. */
  tokenFrom?: SecretRef;
}

export interface DeployConfig {
  git: GitSource;
  installCommand?: string;
  buildCommand?: string;
  keepReleases: number;
  sharedPaths?: string[];
  webhookSecret?: string;
  /** A temporary site per pull request or branch (see PreviewConfig). */
  previews?: PreviewConfig;
}

export interface Site {
  id: string;
  name: string;
  description?: string;
  type: SiteType;
  autoStart: boolean;
  bindings: Binding[];
  node?: NodeConfig;
  proxy?: ProxyConfig;
  static?: StaticConfig;
  redirect?: RedirectConfig;
  routing: RoutingConfig;
  deploy: DeployConfig;
  /** Scheduled tasks (node and worker sites). */
  tasks?: ScheduledTask[];
  /** Overrides of the server-wide alert rules, and the site's own (api/alerts.ts). */
  alerts?: SiteAlerts;
  activeRelease?: string;
  /** Set on a preview deployment: its parent site. Server-maintained. */
  previewOf?: string;
  preview?: PreviewInfo;
  /** Deployment slots besides production (node and worker sites). */
  slots?: DeploymentSlot[];
  createdAt: string;
  updatedAt: string;
}

export interface SiteView extends Site {
  status: SiteStatus;
}

export type DeploymentSource = 'zip' | 'git' | 'webhook' | 'rollback' | 'preview';
export type DeploymentStatus = 'running' | 'succeeded' | 'failed';

export interface Deployment {
  id: string;
  siteId: string;
  source: DeploymentSource | string;
  status: DeploymentStatus | string;
  commit?: string;
  message?: string;
  releaseDir?: string;
  startedAt: string;
  finishedAt?: string | null;
  user?: string;
  /** Deployment slot the deployment was made to; "" = production. */
  slot?: string;
}

// ---------------------------------------------------------------- scheduled tasks

/** What happens when a run is due while the previous one is still going. */
export type TaskOverlap = 'skip' | 'queue' | 'allow';

/** A script a node or worker site runs on a schedule, inside the site's sandbox. */
export interface ScheduledTask {
  /** Generated by the server when empty; stable across renames. */
  id: string;
  name: string;
  /** 5-field cron, a shorthand (@daily) or "@every 15m", in server local time. Empty = on demand only. */
  schedule: string;
  /** Relative to the application folder. */
  script?: string;
  npmScript?: string;
  args?: string[];
  enabled: boolean;
  timeoutSec: number;
  overlap: TaskOverlap | string;
  /** Extra variables on top of the site's; secrets come back as "__SECRET__". */
  env?: EnvVar[];
}

export type RunStatus = 'running' | 'succeeded' | 'failed' | 'timeout' | 'cancelled' | 'skipped';

export interface TaskRun {
  id: string;
  siteId: string;
  taskId: string;
  /** The task's name when it ran. */
  taskName: string;
  trigger: 'schedule' | 'manual' | string;
  user?: string;
  status: RunStatus | string;
  startedAt: string;
  finishedAt?: string | null;
  exitCode?: number | null;
  error?: string;
}

/** GET /sites/{id}/tasks: a definition with its live schedule state. */
export interface TaskView extends ScheduledTask {
  /** Absent when disabled, on demand only, or never. */
  nextRunAt?: string | null;
  /** The run in progress, else the last run that was not skipped. */
  lastRun?: TaskRun | null;
  /** Ids of the runs in progress. */
  running: string[] | null;
  /** A run waits for the current one (overlap "queue"). */
  queued: boolean;
}

/** POST /sites/{id}/tasks/{task}/run. */
export interface TaskRunStart {
  /** null when the run was queued behind the current one. */
  run: TaskRun | null;
  queued: boolean;
}

// ---------------------------------------------------------------- runtime

export type SiteState = 'stopped' | 'starting' | 'running' | 'degraded' | 'stopping' | 'failed';

export type InstanceState = 'starting' | 'ready' | 'unhealthy' | 'stopping' | 'exited' | 'crashed';

export interface InstanceStatus {
  index: number;
  pid: number;
  port: number;
  state: InstanceState | string;
  healthy: boolean;
  startedAt?: string | null;
  restarts: number;
  lastExitCode?: number | null;
  lastExitAt?: string | null;
  cpuPercent: number;
  memoryBytes: number;
  requests: number;
  activeConns: number;
  heapUsedBytes?: number;
  heapTotalBytes?: number;
  eventLoopLagMs?: number;
  nodeVersion?: string;
  /** The runtime and version that started the process (every runtime). */
  runtime?: RuntimeName | string;
  runtimeVersion?: string;
  /** An agent reports heap and event-loop lag (Node.js, Bun). */
  agent?: boolean;
}

export interface TrafficStats {
  requests: number;
  status2xx: number;
  status3xx: number;
  status4xx: number;
  status5xx: number;
  bytesIn: number;
  bytesOut: number;
  avgLatencyMs: number;
  rps: number;
}

export interface UpstreamStatus {
  url: string;
  local?: boolean;
  healthy: boolean;
  activeConns: number;
  lastError?: string;
}

export interface SiteStatus {
  siteId: string;
  state: SiteState;
  message?: string;
  instances: InstanceStatus[] | null;
  traffic: TrafficStats;
  upstreams?: UpstreamStatus[] | null;
  cache?: CacheStats | null;
}

export interface MetricPoint {
  t: string;
  req: number;
  err: number;
  lat: number;
  cpu: number;
  mem: number;
}

export interface ServerInfo {
  version: string;
  commit: string;
  hostname: string;
  os: string;
  startedAt: string;
  cpuPercent: number;
  cpuCount: number;
  memTotal: number;
  memUsed: number;
  diskTotal: number;
  diskFree: number;
  dataDir: string;
  listeners: string[] | null;
  goVersion: string;
  isService: boolean;
}

export type LogStream = 'stdout' | 'stderr' | 'system';

export interface LogLine {
  t: string;
  s: LogStream | string;
  i: number;
  m: string;
  /** Deployment slot whose instance wrote the line; "" = production. */
  slot?: string;
}

export type LogType = 'app' | 'access';

// ---------------------------------------------------------------- certificates

export type CertSource = 'acme' | 'imported' | 'selfsigned';
export type CertStatus = 'pending' | 'valid' | 'error' | 'expired';
export type Challenge = 'http-01' | 'dns-01';
export type KeyType = 'ec256' | 'ec384' | 'rsa2048' | 'rsa4096';

export interface ACMEOptions {
  challenge: Challenge | string;
  dnsProviderId?: string;
  keyType?: KeyType | string;
}

export interface Certificate {
  id: string;
  name: string;
  source: CertSource | string;
  domains: string[] | null;
  acme?: ACMEOptions;
  autoRenew: boolean;
  managed: boolean;
  status: CertStatus | string;
  lastError?: string;
  issuer?: string;
  subject?: string;
  serial?: string;
  fingerprint?: string;
  notBefore?: string | null;
  notAfter?: string | null;
  renewAfter?: string | null;
  lastAttempt?: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface CertUsage {
  siteId: string;
  siteName: string;
  binding: string;
}

export interface CertificateView extends Certificate {
  usedBy: CertUsage[] | null;
  /** OCSP stapling state; absent while the certificate is not issued. */
  ocsp?: OCSPStatus;
}

export type OCSPState = 'none' | 'pending' | 'good' | 'revoked' | 'unknown' | 'error';

export interface OCSPStatus {
  state: OCSPState | string;
  responder?: string;
  mustStaple?: boolean;
  stapled: boolean;
  thisUpdate?: string;
  nextUpdate?: string;
  revokedAt?: string;
  revocationReason?: string;
  lastCheck?: string;
  nextCheck?: string;
  lastError?: string;
}

// ---------------------------------------------------------------- settings

export interface DNSProvider {
  id: string;
  name: string;
  provider: string;
  credentials: Record<string, string>;
}

export type WebhookFormat = 'generic' | 'slack' | 'teams' | 'discord';

export interface WebhookTarget {
  id: string;
  name: string;
  url: string;
  format: WebhookFormat | string;
  events: string[] | null;
  enabled: boolean;
}

export interface ACMESettings {
  email: string;
  directory: string;
  eabKeyId?: string;
  eabHmac?: string;
  keyType: KeyType | string;
  agreeTos: boolean;
  renewBeforeDays: number;
}

export interface TLSSettings {
  minVersion: '1.2' | '1.3' | string;
  http2: boolean;
  /** A UDP (QUIC) listener next to every HTTPS listener, advertised with Alt-Svc. */
  http3?: boolean;
}

/** GET/PUT /api/tls. */
export interface TLSView extends TLSSettings {
  http3Listeners: string[];
}

export interface ProxySettings {
  serverHeader: string;
  trustedProxies?: string[];
  defaultPageHtml?: string;
  readHeaderTimeoutSec: number;
  idleTimeoutSec: number;
}

export interface Settings {
  acme: ACMESettings;
  tls: TLSSettings;
  proxy: ProxySettings;
  portRangeStart: number;
  portRangeEnd: number;
  defaultNodeVersion: string;
  dnsProviders: DNSProvider[] | null;
  webhooks: WebhookTarget[] | null;
  logMaxSizeMB: number;
  logMaxFiles: number;
  logRetentionDays: number;
  certExpiryWarnDays: number;
  mime: MimeSettings;
  mail: MailSettings;
  sso: SSOSettings;
  ipBan: IPBanSettings;
  backup: BackupSettings;
  logShipping: LogShippingSettings;
  updates: UpdateSettings;
  alerts: AlertSettings;
  secretStores: SecretStore[];
  /** Defaults for sites of other runtimes that pin no version. */
  runtimes?: RuntimeDefaults;
  waf: WAFSettings;
}

/** model.RuntimeDefaults. */
export interface RuntimeDefaults {
  bun?: string;
  deno?: string;
  /** "3.12" or a path to python.exe; "" = the newest found. */
  python?: string;
  /** Path to dotnet.exe; "" = found automatically. */
  dotnet?: string;
}

/** Automatic updates from the signed release feed (model.UpdateSettings). */
export interface UpdateSettings {
  auto: boolean;
  /** "HH:MM", server local time: when automatic installs happen. */
  time: string;
  /** 0 = Sunday … 6 = Saturday; empty = every day. */
  weekdays: number[];
}

export type UpdateState = 'idle' | 'checking' | 'downloading' | 'installing';

export interface UpdateRelease {
  version: string;
  published: string;
  size: number;
  /** Release notes page. */
  notes: string;
  /** Not installed automatically (the update policy); "Install now" does. */
  manual: boolean;
  /** Installing it failed before; not retried automatically. */
  failed: boolean;
}

export interface UpdateResult {
  from: string;
  to: string;
  trigger: 'schedule' | 'manual';
  startedAt: string;
  finishedAt?: string;
  ok: boolean;
  exitCode: number;
  error?: string;
  log?: string;
}

export interface UpdateStatus extends UpdateSettings {
  current: string;
  state: UpdateState;
  supported: boolean;
  reason?: string;
  available?: UpdateRelease;
  lastCheck?: string;
  lastError?: string;
  nextCheck?: string;
  nextInstall?: string;
  lastResult?: UpdateResult;
}

/** Single sign-on to the console with OpenID Connect (Entra ID and others). */
export interface SSOSettings {
  enabled: boolean;
  /** Sign-in button text; empty = "Sign in with Microsoft" for Entra ID, else "Sign in with SSO". */
  label?: string;
  issuer: string;
  clientId: string;
  /** Secret: "__SECRET__" when set. */
  clientSecret: string;
  /** Requested besides openid, profile and email. */
  scopes: string[] | null;
  usernameClaim: string;
  disablePassword: boolean;
  autoCreate: boolean;
  defaultRole?: Role | '';
  roleClaim?: string;
  roleMap: SSORoleRule[] | null;
}

export interface SSORoleRule {
  value: string;
  role: Role;
}

/** How users sign in (public, for the login page). */
export interface AuthMethods {
  password: boolean;
  sso: boolean;
  ssoLabel?: string;
}

export interface SSOTestResult {
  ok: boolean;
  issuer?: string;
  authorizationEndpoint?: string;
  tokenEndpoint?: string;
  jwksUri?: string;
  keys: number;
  redirectUrl: string;
  problems: string[];
}

/** Automatic IP banning (fail2ban-style). A threshold of 0 turns a rule off. */
export interface BanRule {
  threshold: number;
  windowSec: number;
}

export interface IPBanSettings {
  enabled: boolean;
  authFailures: BanRule;
  notFound: BanRule;
  rateLimited: BanRule;
  /** Requests the web application firewall blocked. */
  wafBlocks: BanRule;
  trapPaths: string[] | null;
  banMinutes: number;
  maxBanMinutes: number;
  allowList: string[] | null;
  ipv6Prefix: number;
}

export interface Ban {
  address: string;
  reason: string;
  manual?: boolean;
  strikes: number;
  createdAt: string;
  /** Absent: until removed. */
  expiresAt?: string | null;
  createdBy?: string;
}

export interface BanRequest {
  address: string;
  /** 0 = until removed */
  minutes: number;
  reason?: string;
}

/** Server-wide MIME types, added to or overriding the built-in table. */
export interface MimeSettings {
  types: MimeMap[] | null;
  unknownTypes: UnknownMimeTypes | string;
}

export interface DNSCatalogField {
  key: string;
  label: string;
  secret: boolean;
  optional: boolean;
}

export interface DNSCatalogEntry {
  code: string;
  name: string;
  fields: DNSCatalogField[];
}

export type AdminTLSMode = 'selfsigned' | 'certificate' | 'none';

export interface AdminSettings {
  listen: string;
  tls: AdminTLSMode | string;
  certificateId: string;
  restartRequired: boolean;
}

// ---------------------------------------------------------------- users & audit

/** A server-wide role, or `sites` for a user allowed on selected sites only. */
export type Role = 'admin' | 'operator' | 'viewer' | 'sites';

/** The role a site-scoped user has on one site (never admin). */
export type SiteRole = 'viewer' | 'operator';

export interface SiteGrant {
  siteId: string;
  role: SiteRole;
}

/**
 * What the signed-in caller may do: a server-wide role (which covers every
 * site), or role `sites` with per-site grants.
 */
export interface Access {
  role: Role;
  sites?: SiteGrant[];
}

export interface User {
  id: string;
  username: string;
  role: Role;
  /** Grants of a site-scoped user (role `sites`). */
  sites?: SiteGrant[];
  totpEnabled: boolean;
  disabled: boolean;
  lastLogin?: string | null;
  createdAt: string;
  /** Created by a single sign-on: no password until an administrator sets one. */
  sso?: boolean;
}

export interface Me {
  user: User;
  mustChangePassword: boolean;
  /** Effective access (the user's, narrowed by the API token if any). */
  access?: Access;
}

export interface APIToken {
  id: string;
  userId: string;
  name: string;
  prefix: string;
  /** Maximum role; empty = the owner's. */
  role?: Role | '';
  /** Sites the token is restricted to; null = every site the owner can access. */
  siteIds?: string[] | null;
  expiresAt?: string | null;
  lastUsed?: string | null;
  createdAt: string;
}

export interface CreatedToken {
  token: string;
  info: APIToken;
}

export interface AuditEntry {
  id: number;
  time: string;
  user: string;
  ip: string;
  action: string;
  target: string;
  detail?: string;
}

export type EventLevel = 'info' | 'warning' | 'error';

export interface NHEvent {
  id: number;
  time: string;
  level: EventLevel | string;
  type: string;
  siteId?: string;
  message: string;
}

export interface TOTPSetup {
  secret: string;
  url: string;
}

// ---------------------------------------------------------------- node runtimes

export interface SystemNode {
  version: string;
  path: string;
}

export type NodeInstallStatus = 'installing' | 'installed' | 'error';

export interface InstalledNode {
  version: string;
  path: string;
  status: NodeInstallStatus | string;
  progress: number;
  error?: string;
  isDefault: boolean;
}

export interface NodeVersions {
  system: SystemNode | null;
  installed: InstalledNode[] | null;
}

export interface AvailableNode {
  version: string;
  lts: string | false;
  date: string;
  security: boolean;
}

// ---------------------------------------------------------------- other runtimes (GET /api/runtimes)

/** Bun or Deno: versions NodeHoster installed, and the one on PATH. */
export interface ManagedRuntime {
  system: SystemNode | null;
  installed: InstalledNode[] | null;
}

export interface PythonInterpreter {
  version: string;
  path: string;
  /** py (the py launcher) | registry | path | folder */
  source: string;
  isDefault: boolean;
}

export interface DotnetRuntime {
  name: string;
  version: string;
  path: string;
}

export interface DotnetInfo {
  host: string;
  runtimes: DotnetRuntime[] | null;
}

export interface RuntimeReport {
  bun: ManagedRuntime;
  deno: ManagedRuntime;
  python: PythonInterpreter[] | null;
  dotnet: DotnetInfo | null;
  defaults: RuntimeDefaults;
}

export interface AvailableRuntime {
  version: string;
  date: string;
}

// ---------------------------------------------------------------- mail (built-in SMTP server)

export type MailDelivery = 'direct' | 'smarthost';
export type SmartHostSecurity = 'starttls' | 'tls' | 'none';

export interface MailUser {
  username: string;
  /** "__SECRET__" when set; never the hash itself. */
  passwordHash?: string;
  /** Write-only: non-empty sets a new password, empty keeps the existing one. */
  password?: string;
}

export interface MailSmartHost {
  host: string;
  port: number;
  security: SmartHostSecurity | string;
  username?: string;
  password?: string;
  insecureSkipVerify: boolean;
}

export interface DKIMKey {
  domain: string;
  selector: string;
  enabled: boolean;
  /** "__SECRET__" for a stored key; "" on a new key = generate RSA-2048 on save; otherwise an imported PEM. */
  privateKey?: string;
  /** Read-only, filled by the server. */
  dnsName?: string;
  /** Read-only TXT value to publish at dnsName. */
  dnsRecord?: string;
}

export interface MailSettings {
  enabled: boolean;
  listenIp: string;
  port: number;
  hostname?: string;
  /** The address receivers see mail coming from; "" = detect (set it behind NAT). */
  publicIp?: string;
  allowIps: string[] | null;
  requireAuth: boolean;
  users?: MailUser[] | null;
  certificateId?: string;
  allowedSenderDomains?: string[] | null;
  maxMessageMB: number;
  maxRecipients: number;
  delivery: MailDelivery | string;
  smartHost: MailSmartHost;
  expireHours: number;
  keepFailedDays: number;
  dkim?: DKIMKey[] | null;
  pickupDirectory: boolean;
}

export type MailMessageState = 'queued' | 'sending' | 'failed';
export type MailRecipientState = 'pending' | 'delivered' | 'failed';
export type MailSource = 'smtp' | 'pickup' | 'test';

export interface MailRecipient {
  address: string;
  state: MailRecipientState | string;
  error?: string;
  deliveredAt?: string | null;
}

export interface MailMessage {
  id: string;
  from: string;
  recipients: MailRecipient[] | null;
  subject?: string;
  size: number;
  source: MailSource | string;
  clientIp?: string;
  user?: string;
  state: MailMessageState | string;
  attempts: number;
  receivedAt: string;
  nextAttempt?: string | null;
  lastAttempt?: string | null;
  lastError?: string;
}

export interface MailStatus {
  enabled: boolean;
  listening: boolean;
  addr?: string;
  error?: string;
  queued: number;
  failed: number;
  accepted: number;
  delivered: number;
  bounced: number;
  since: string;
}

export interface MailTest {
  from?: string;
  to: string;
}

// ---------------------------------------------------------------- mail deliverability

export type MailCheckStatus = 'pass' | 'warn' | 'fail' | 'info';

/** One test of the deliverability report. */
export interface MailCheck {
  /** "SPF", "Reverse DNS (PTR)", "DKIM (selector)", ... */
  name: string;
  status: MailCheckStatus | string;
  /** What was found, in a sentence. */
  detail: string;
  /** The DNS record found. */
  record?: string;
  /** A DNS record value to publish at fixDns, or (without fixDns) an instruction. */
  fix?: string;
  /** DNS name the fix record belongs at. */
  fixDns?: string;
}

export interface MailDomainHealth {
  domain: string;
  checks: MailCheck[] | null;
}

/** GET /api/mail/health: this server's reputation and each sending domain's records. */
export interface MailHealth {
  publicIp?: string;
  hostname: string;
  delivery: MailDelivery | string;
  server: MailCheck[] | null;
  domains: MailDomainHealth[] | null;
  checkedAt: string;
}

// ---------------------------------------------------------------- import (IIS, iisnode web.config, PM2)

/** iis = an uploaded applicationHost.config; local-iis = this server's (Windows). */
export type ImportSource = 'iis' | 'local-iis' | 'webconfig' | 'pm2';

export type ImportNoteLevel = 'converted' | 'approximated' | 'skipped';

export interface ImportNote {
  level: ImportNoteLevel | string;
  text: string;
}

/** One way of importing an item: a complete site draft, or a task added to a site. */
export interface ImportOption {
  /** "Node.js application", "Background worker", "Static site", "Reverse proxy", "Redirect", "Scheduled task". */
  label: string;
  kind: 'site' | 'task' | string;
  site?: Site;
  task?: ScheduledTask;
  /** "import:<item key>", an existing node/worker site's id, or empty (the user picks). */
  taskSite?: string;
}

/** Something found in the source: an IIS site or application, a PM2 app. */
export interface ImportItem {
  key: string;
  /** For display: `IIS site "Shop"`, `PM2 app "api"`. */
  source: string;
  options: ImportOption[];
  /** The proposed option. */
  choice: number;
  /** Proposed for import (false when there are conflicts). */
  selected: boolean;
  notes: ImportNote[];
  /** Validation problems of the proposed option as proposed. */
  conflicts: string[];
}

/** POST /api/import/preview. Nothing is created. */
export interface ImportPreview {
  source: ImportSource | string;
  items: ImportItem[];
  warnings: string[];
}

export interface ImportApplyItem {
  key: string;
  kind: 'site' | 'task' | string;
  site?: Site;
  task?: ScheduledTask;
  taskSite?: string;
}

export interface ImportApplyRequest {
  /** For the audit log. */
  source?: string;
  items: ImportApplyItem[];
  /** Start the created sites; otherwise they are left stopped. */
  start: boolean;
}

export interface ImportCreated {
  key: string;
  kind: 'site' | 'task' | string;
  /** The created site, or the site that got the task. */
  siteId: string;
  name: string;
  warning?: string;
}

export interface ImportFailed {
  key: string;
  name: string;
  error: string;
  field?: string;
}

export interface ImportApplyResult {
  created: ImportCreated[];
  failed: ImportFailed[];
}

// ---------------------------------------------------------------- backups

export type BackupDestinationType = 'folder' | 's3' | 'azure' | 'sftp';

/** Scheduled backups (Settings → Backups). Secrets come back as SECRET. */
export interface BackupSettings {
  enabled: boolean;
  /** "HH:MM", server local time. */
  time: string;
  /** 0 = Sunday … 6 = Saturday; empty = every day. */
  weekdays: number[];
  keepLast: number;
  keepDays: number;
  includeCertificates: boolean;
  includeShared: boolean;
  /** Empty = every site. */
  sharedSiteIds: string[];
  passphrase?: string;
  destinations: BackupDestination[];
}

export interface BackupDestination {
  id: string;
  name: string;
  type: BackupDestinationType;
  enabled: boolean;
  folder?: { path: string };
  s3?: {
    endpoint?: string;
    region: string;
    bucket: string;
    prefix?: string;
    accessKeyId: string;
    secretAccessKey: string;
    pathStyle: boolean;
  };
  azure?: {
    account: string;
    container: string;
    prefix?: string;
    sasToken?: string;
    accountKey?: string;
    endpoint?: string;
  };
  sftp?: {
    host: string;
    port: number;
    username: string;
    password?: string;
    privateKey?: string;
    passphrase?: string;
    directory: string;
    /** SHA256:… fingerprint of the server's host key. */
    hostKey: string;
  };
}

export type BackupRunStatus = 'success' | 'partial' | 'failed';

export interface BackupDestResult {
  id: string;
  name: string;
  ok: boolean;
  error?: string;
  pruned: number;
}

export interface BackupRun {
  id: string;
  trigger: 'schedule' | 'manual' | string;
  startedAt: string;
  finishedAt: string;
  status: BackupRunStatus;
  error?: string;
  file?: string;
  size: number;
  encrypted: boolean;
  contents: string[];
  destinations: BackupDestResult[];
}

export interface BackupStatus {
  enabled: boolean;
  running: boolean;
  runningSince?: string;
  runningWhat?: string;
  nextRun?: string;
  encrypted: boolean;
  hostname: string;
  history: BackupRun[];
}

export interface BackupTestResult {
  ok: boolean;
  error?: string;
  /** SFTP: the server's host key fingerprint, to confirm. */
  hostKey?: string;
}

export interface BackupObject {
  name: string;
  size: number;
  modified: string;
  host: string;
  created: string;
}

export interface SharedSize {
  siteId: string;
  siteName: string;
  bytes: number;
  files: number;
  partial: boolean;
}

export interface RestoreResult {
  format: 'json' | 'archive';
  hostname?: string;
  created?: string;
  encrypted: boolean;
  sites: number;
  certificates: number;
  sharedSites: string[];
  warnings: string[];
}

// ---------------------------------------------------------------- log shipping & search

export type LogSource = 'server' | 'app' | 'access' | 'event' | 'audit';
export type LogLevel = 'debug' | 'info' | 'warning' | 'error';
export type LogTargetType = 'syslog' | 'seq' | 'http';

export interface LogShippingSettings {
  targets: LogTarget[];
}

export interface LogTarget {
  id: string;
  name: string;
  type: LogTargetType;
  enabled: boolean;
  sources: LogSource[];
  /** Records of other sites are not sent; empty = every site. */
  siteIds: string[];
  /** Lowest server-log level sent. */
  minLevel: LogLevel;
  syslog?: {
    address: string;
    transport: 'udp' | 'tcp' | 'tls';
    facility: string;
    appName: string;
    hostname: string;
    caCert?: string;
    insecureSkipVerify: boolean;
  };
  seq?: { url: string; apiKey?: string };
  http?: { url: string; format: 'json' | 'ndjson'; headers: { name: string; value: string; secret: boolean }[] };
}

export interface LogTargetStatus {
  id: string;
  name: string;
  type: LogTargetType;
  enabled: boolean;
  queued: number;
  sent: number;
  dropped: number;
  failed: number;
  lastError?: string;
  lastErrorAt?: string;
  lastSuccess?: string;
}

export interface LogSearchResult {
  lines: LogLine[];
  /** Stopped at the time/byte budget; older lines may still match. */
  truncated: boolean;
  cursor?: string;
  scannedBytes: number;
}

export interface LogSearchParams {
  q?: string;
  regex?: boolean;
  stream?: 'all' | 'stdout' | 'stderr' | 'system';
  source?: LogType;
  level?: LogLevel;
  since?: string;
  until?: string;
  limit?: number;
  cursor?: string;
  /** Deployment slot: filters application lines, or picks the slot's access log. */
  slot?: string;
}

// ---------------------------------------------------------------- deployment slots

/** How a slot's instances are warmed up before a swap. */
export interface WarmupConfig {
  /** Requested on every instance; default ["/"]. */
  paths: string[];
  /** Accepted statuses, e.g. "200-399" (the default) or "200-299,401". */
  statuses: string;
  /** For all instances and paths together, 5..1800; default 120. */
  timeoutSec: number;
}

/** A second copy of a node or worker site with its own release, instances and bindings. */
export interface DeploymentSlot {
  name: string;
  /** The slot's own variables: override production's of the same name. */
  env?: EnvVar[];
  /** 0 or absent = as many as production. */
  instances?: number;
  /** Swap into production after a successful deployment to this slot. */
  autoSwap: boolean;
  warmup: WarmupConfig;
  /** Managed by the server; ignored on save. */
  activeRelease?: string;
}

export interface SlotStatus {
  /** "production" or the slot's name. */
  name: string;
  release?: string;
  status: SiteStatus;
  bindings: Binding[] | null;
  autoSwap?: boolean;
}

export type SwapPhase = 'preparing' | 'warming' | 'swapping';

export interface SwapProgress {
  slot: string;
  phase: SwapPhase | string;
  message?: string;
  user?: string;
  /** Started by auto-swap after a deployment. */
  auto?: boolean;
  startedAt: string;
}

export interface SwapResult {
  slot: string;
  succeeded: boolean;
  message: string;
  user?: string;
  auto?: boolean;
  startedAt: string;
  finishedAt: string;
  /** Releases after the swap (before it, when it failed). */
  productionRelease?: string;
  slotRelease?: string;
}

/** GET /sites/{id}/slots: production first. */
export interface SlotsView {
  slots: SlotStatus[] | null;
  /** Present while a swap runs. */
  swap?: SwapProgress | null;
  lastSwap?: SwapResult | null;
}

/** What swapping a slot into production would do. */
export interface SwapPreview {
  slot: string;
  productionRelease: string;
  slotRelease: string;
  warmup: WarmupConfig;
  /** Plain-language steps, in order. */
  changes: string[] | null;
  warnings: string[] | null;
  /** Non-empty: the swap would be refused. */
  blockers: string[] | null;
}

// ---------------------------------------------------------------- preview deployments

export type PreviewCertMode = 'auto' | 'certificate' | 'wildcard';

/** deploy.previews of a git-deployed node or static site. */
export interface PreviewConfig {
  enabled: boolean;
  /** "pr-{number}.preview.example.com" or "{branch}.preview.example.com". */
  hostPattern: string;
  pullRequests: boolean;
  /** Globs of branches whose pushes get a preview ("feature/*", "release/**"). */
  branches?: string[];
  /** Build pull requests from forks (untrusted code). */
  allowForks: boolean;
  /** Beyond it, the least recently pushed preview is evicted. */
  maxPreviews: number;
  /** Days without a push before a preview is deleted; 0 = never. */
  expireDays: number;
  protocol: 'http' | 'https' | string;
  ip?: string;
  port: number;
  certMode?: PreviewCertMode | '';
  certificateId?: string;
  dnsProviderId?: string;
  /** Overrides of the site's variables; PREVIEW* are always set. */
  env?: EnvVar[];
  /** Replaces the site's basic authentication in previews when enabled. */
  basicAuth: BasicAuthConfig;
  /** Replaces the site's IP allow list in previews when set. */
  allowIps?: string[];
  reportStatus: boolean;
  /** Secret; empty = the git access token. */
  statusToken?: string;
}

export type PreviewKind = 'pr' | 'branch';

export interface PreviewInfo {
  key: string;
  kind: PreviewKind;
  number?: number;
  branch: string;
  ref: string;
  commit?: string;
  title?: string;
  author?: string;
  prUrl?: string;
  fork?: boolean;
  provider?: 'github' | 'gitlab' | 'gitea' | string;
  host: string;
  url: string;
  lastPush: string;
  ready?: boolean;
}

export type PreviewState = 'pending' | 'deploying' | 'ready' | 'failed' | 'deleting';

/** GET /sites/{id}/previews */
export interface PreviewView {
  id: string;
  name: string;
  preview: PreviewInfo;
  state: PreviewState | string;
  siteState: SiteState;
  lastDeployment?: Deployment;
  createdAt: string;
}

/** What a preview request (webhook delivery, POST /previews) was taken as. */
export interface PreviewDecision {
  action: 'deploy' | 'delete' | 'ignore';
  key?: string;
  reason: string;
}

// ---------------------------------------------------------------- server connections

/** Another NodeHoster server managed from this one (Servers page, server switcher). */
export interface ServerConnection {
  id: string;
  name: string;
  /** The remote web console's base URL, e.g. https://web02:8484. */
  url: string;
  /** An API token created on that server; SECRET when stored. */
  token: string;
  /** Pinned SHA-256 of the server's certificate (64 hex digits); '' = verified against trusted roots. */
  fingerprint?: string;
  /** The least local role that may use the connection. */
  minRole: Exclude<Role, 'sites'>;
  createdAt?: string;
  updatedAt?: string;
}

export interface ServerHealth {
  /** Answered with the token accepted. False without checkedAt: not checked yet. */
  reachable: boolean;
  error?: string;
  checkedAt?: string;
  latencyMs: number;
  /** Since when the server is in its current state, as seen from here. */
  since?: string;
  version?: string;
  commit?: string;
  hostname?: string;
  os?: string;
  cpuPercent: number;
  cpuCount: number;
  memTotal: number;
  memUsed: number;
  sites: number;
  running: number;
  degraded: number;
  failed: number;
  stopped: number;
  /** The token's user there, and its role ('sites' when limited to some sites). */
  user?: string;
  role?: Role;
}

export interface ServerView extends ServerConnection {
  health: ServerHealth;
}

export interface PeerCertificate {
  fingerprint: string;
  subject: string;
  issuer: string;
  dnsNames: string[];
  notBefore: string;
  notAfter: string;
  /** The chain verifies for the host name against the trusted roots of this server. */
  verified: boolean;
  verifyError?: string;
}

export interface ServerTest {
  id?: string;
  url: string;
  token: string;
  fingerprint?: string;
}

export interface ServerTestResult {
  /** Absent over plain HTTP (to this computer only). */
  certificate?: PeerCertificate;
  /** The TLS connection is trusted as configured; the token is only tried then. */
  trusted: boolean;
  health: ServerHealth;
}

// ---------------------------------------------------------------- secret stores

export type SecretStoreType = 'vault' | 'infisical' | 'bitwarden';

/** A value taken from a secret store: store name and the secret in it (model.SecretRef). */
export interface SecretRef {
  store: string;
  ref: string;
}

export interface VaultStore {
  auth: 'token' | 'approle';
  token?: string;
  roleId?: string;
  secretId?: string;
  authMount?: string;
  namespace?: string;
  mount: string;
  kvVersion: number;
}

export interface InfisicalStore {
  clientId: string;
  clientSecret: string;
  projectId: string;
  environment: string;
}

export interface BitwardenStore {
  accessToken: string;
  region?: 'us' | 'eu' | '';
  apiUrl?: string;
  identityUrl?: string;
}

/** An external secret manager (model.SecretStore). Credentials come back as SECRET. */
export interface SecretStore {
  id: string;
  name: string;
  type: SecretStoreType;
  url: string;
  caCert?: string;
  cacheTtlSec: number;
  watchIntervalSec: number;
  vault?: VaultStore;
  infisical?: InfisicalStore;
  bitwarden?: BitwardenStore;
}

export interface SecretStoreStatus {
  name: string;
  type: SecretStoreType;
  cached: number;
  references: number;
  lastSuccess?: string;
  lastError?: string;
  lastErrorAt?: string;
  tokenExpires?: string;
}

/** Outcome of a connection test or a resolution; never carries the value. */
export interface SecretTestResult {
  ok: boolean;
  error?: string;
  detail?: string;
}

export interface SecretRefCheck {
  field: string;
  variable?: string;
  task?: string;
  ref: SecretRef;
  ok: boolean;
  error?: string;
}
