// Client-side mirror of model.Site.ApplyDefaults so new-site forms start with
// the same values the server would fill in.

import type {
  Binding,
  HealthCheck,
  NodeConfig,
  ProxyConfig,
  RedirectConfig,
  RoutingConfig,
  Site,
  SiteType,
  StaticConfig,
} from '@/api/types';

export const SITE_TYPES: { value: SiteType; label: string; description: string }[] = [
  { value: 'node', label: 'Node.js app', description: 'Run and supervise Node.js processes behind the reverse proxy.' },
  { value: 'proxy', label: 'Reverse proxy', description: 'Forward requests to one or more upstream URLs with load balancing.' },
  { value: 'static', label: 'Static site', description: 'Serve files from a folder, with optional SPA fallback.' },
  { value: 'redirect', label: 'Redirect', description: 'Send every request to another URL with a 30x status.' },
];

export function siteTypeLabel(t: SiteType | string): string {
  return SITE_TYPES.find((x) => x.value === t)?.label ?? t;
}

export const LB_STRATEGIES = [
  { value: 'round_robin', label: 'Round robin' },
  { value: 'least_conn', label: 'Least connections' },
  { value: 'ip_hash', label: 'Client IP hash (sticky)' },
  { value: 'random', label: 'Random' },
];

export const REDIRECT_CODES = [
  { value: 301, label: '301 Moved Permanently' },
  { value: 302, label: '302 Found' },
  { value: 303, label: '303 See Other' },
  { value: 307, label: '307 Temporary Redirect' },
  { value: 308, label: '308 Permanent Redirect' },
];

export const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$/;
export const HOST_RE = /^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
export const HHMM_RE = /^([01]\d|2[0-3]):[0-5]\d$/;
export const ENV_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

export function defaultHealthCheck(intervalSec: number): HealthCheck {
  return { enabled: false, path: '/', intervalSec, timeoutSec: 5, unhealthyThreshold: 3 };
}

export function defaultNode(): NodeConfig {
  return {
    appRoot: '',
    script: 'server.js',
    npmScript: '',
    args: [],
    nodeArgs: [],
    nodeVersion: '',
    env: [],
    instances: 1,
    portMode: 'auto',
    fixedPort: 0,
    restartPolicy: 'always',
    maxRestarts: 10,
    restartWindowSec: 300,
    startupTimeoutSec: 60,
    shutdownTimeoutSec: 15,
    agentEnabled: true,
    watchFiles: false,
    watchIgnore: ['node_modules', '.git', 'logs'],
    healthCheck: defaultHealthCheck(30),
    recycle: {},
    limits: {},
    runAs: { enabled: false },
  };
}

export function defaultProxy(): ProxyConfig {
  return {
    upstreams: [{ url: '', weight: 1 }],
    loadBalancing: 'round_robin',
    healthCheck: defaultHealthCheck(15),
    preserveHost: false,
    insecureSkipVerify: false,
  };
}

export function defaultStatic(): StaticConfig {
  return {
    root: '',
    indexFiles: ['index.html', 'index.htm', 'default.htm'],
    spaFallback: false,
    directoryBrowsing: false,
    cacheControl: '',
  };
}

export function defaultRedirect(): RedirectConfig {
  return { targetUrl: '', statusCode: 301, preservePath: true };
}

export function defaultRouting(): RoutingConfig {
  return {
    httpsRedirect: false,
    hsts: { enabled: false, maxAgeSec: 31536000, includeSubdomains: false, preload: false },
    compression: true,
    maxBodyMB: 0,
    timeoutSec: 0,
    webSockets: true,
    requestHeaders: [],
    responseHeaders: [],
    rewrites: [],
    locations: [],
    ip: { allow: [], deny: [] },
    basicAuth: { enabled: false, realm: 'Restricted', users: [], excludePaths: [] },
    rateLimit: { enabled: false, requestsPerSecond: 10, burst: 21 },
    maintenance: { enabled: false, html: '', allowIps: [], retryAfterSec: 0 },
    errorPages: {},
    accessLog: true,
  };
}

export function defaultBinding(protocol: 'http' | 'https' = 'http', host = ''): Binding {
  return {
    id: '',
    protocol,
    ip: '',
    port: protocol === 'https' ? 443 : 80,
    host,
    certMode: protocol === 'https' ? 'auto' : '',
    certificateId: '',
  };
}

export function newSite(type: SiteType): Site {
  const s: Site = {
    id: '',
    name: '',
    description: '',
    type,
    autoStart: true,
    bindings: [defaultBinding('http')],
    routing: defaultRouting(),
    deploy: {
      git: { repo: '', branch: 'main', token: '' },
      installCommand: type === 'node' ? 'npm ci --omit=dev' : '',
      buildCommand: '',
      keepReleases: 5,
      sharedPaths: [],
      webhookSecret: '',
    },
    createdAt: '',
    updatedAt: '',
  };
  if (type === 'node') s.node = defaultNode();
  if (type === 'proxy') s.proxy = defaultProxy();
  if (type === 'static') s.static = defaultStatic();
  if (type === 'redirect') s.redirect = defaultRedirect();
  return s;
}

/** Fill in nested objects that may be missing (null slices, old documents) so forms can bind safely. */
export function normalizeSite(input: Site): Site {
  const s: Site = { ...input };
  const r = defaultRouting();
  s.bindings = (s.bindings ?? []).map((b) => ({ ...b, ip: b.ip ?? '', host: b.host ?? '' }));
  s.routing = {
    ...r,
    ...s.routing,
    hsts: { ...r.hsts, ...s.routing?.hsts },
    ip: { ...r.ip, ...s.routing?.ip },
    basicAuth: { ...r.basicAuth, ...s.routing?.basicAuth, users: s.routing?.basicAuth?.users ?? [] },
    rateLimit: { ...r.rateLimit, ...s.routing?.rateLimit },
    maintenance: { ...r.maintenance, ...s.routing?.maintenance },
  };
  s.deploy = { ...s.deploy, git: { ...s.deploy?.git }, keepReleases: s.deploy?.keepReleases ?? 5 };
  if (s.type === 'node') {
    const d = defaultNode();
    const n = s.node ?? d;
    s.node = {
      ...d,
      ...n,
      healthCheck: { ...d.healthCheck, ...n.healthCheck },
      recycle: { ...n.recycle },
      limits: { ...n.limits },
      runAs: { ...d.runAs, ...n.runAs },
      env: n.env ?? [],
    };
  }
  if (s.type === 'proxy') {
    const d = defaultProxy();
    s.proxy = { ...d, ...s.proxy, upstreams: s.proxy?.upstreams ?? [], healthCheck: { ...d.healthCheck, ...s.proxy?.healthCheck } };
  }
  if (s.type === 'static') s.static = { ...defaultStatic(), ...s.static, indexFiles: s.static?.indexFiles ?? [] };
  if (s.type === 'redirect') s.redirect = { ...defaultRedirect(), ...s.redirect };
  return s;
}

/** Strip the live status (and anything else read-only) from a SiteView before PUT. */
export function toSite<T extends Site>(v: T): Site {
  const { status: _status, ...rest } = v as T & { status?: unknown };
  void _status;
  return rest as Site;
}
