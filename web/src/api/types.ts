// Hand-written mirrors of the Go structs in internal/model. Field names are
// the `json` tags. Times are RFC 3339 strings. Fields tagged `omitempty` are
// optional here.

export const SECRET = '__SECRET__';

// ---------------------------------------------------------------- sites

export type SiteType = 'node' | 'proxy' | 'static' | 'redirect';

export interface Binding {
  id: string;
  protocol: 'http' | 'https' | string;
  ip: string;
  port: number;
  host: string;
  certMode?: 'auto' | 'certificate' | '';
  certificateId?: string;
}

export interface EnvVar {
  name: string;
  value: string;
  secret?: boolean;
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

export interface NodeConfig {
  appRoot: string;
  script?: string;
  npmScript?: string;
  args?: string[];
  nodeArgs?: string[];
  nodeVersion?: string;
  env?: EnvVar[];
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
}

export interface GitSource {
  repo?: string;
  branch?: string;
  token?: string;
}

export interface DeployConfig {
  git: GitSource;
  installCommand?: string;
  buildCommand?: string;
  keepReleases: number;
  sharedPaths?: string[];
  webhookSecret?: string;
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
  activeRelease?: string;
  createdAt: string;
  updatedAt: string;
}

export interface SiteView extends Site {
  status: SiteStatus;
}

export type DeploymentSource = 'zip' | 'git' | 'webhook' | 'rollback';
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
