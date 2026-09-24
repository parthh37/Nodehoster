// Typed functions for every endpoint in docs/API.md.

import { http, qs } from './client';
import { routePath } from './target';
import type { SecretRef, SecretRefCheck, SecretStore, SecretStoreStatus, SecretTestResult } from './types';
import type {
  TLSView,
  ACMEOptions,
  AdminSettings,
  Ban,
  BanRequest,
  APIToken,
  AuditEntry,
  AuthMethods,
  AvailableNode,
  AvailableRuntime,
  BackupDestination,
  BackupObject,
  BackupStatus,
  UpdateStatus,
  BackupTestResult,
  CertificateView,
  CreatedToken,
  DNSCatalogEntry,
  Deployment,
  ImportApplyRequest,
  ImportApplyResult,
  ImportPreview,
  ImportSource,
  LogLine,
  LogSearchParams,
  LogSearchResult,
  LogTarget,
  LogTargetStatus,
  LogType,
  MailHealth,
  MailMessage,
  MailStatus,
  MailTest,
  Me,
  MetricPoint,
  MimeMap,
  NHEvent,
  NodeVersions,
  PreviewDecision,
  PreviewView,
  RewriteImport,
  RuntimeReport,
  RestoreResult,
  RewriteImportRequest,
  Role,
  ServerInfo,
  Settings,
  Site,
  SiteGrant,
  SiteStatus,
  SharedSize,
  SiteView,
  SlotsView,
  SSOSettings,
  SSOTestResult,
  ServerConnection,
  ServerTest,
  ServerTestResult,
  ServerView,
  SwapPreview,
  SwapProgress,
  TaskRun,
  TaskRunStart,
  TaskView,
  TOTPSetup,
  User,
  WebhookTarget,
} from './types';

const enc = encodeURIComponent;

export const authApi = {
  login: (body: { username: string; password: string; totp?: string }) =>
    http.post<{ user: User }>('/api/auth/login', body, { noAuthRedirect: true }),
  logout: () => http.post('/api/auth/logout', undefined, { noAuthRedirect: true }),
  me: () => http.get<Me>('/api/auth/me'),
  changePassword: (current: string, next: string) => http.post('/api/auth/password', { current, new: next }),
  totpSetup: () => http.post<TOTPSetup>('/api/auth/totp/setup'),
  totpEnable: (code: string) => http.post('/api/auth/totp/enable', { code }),
  totpDisable: (code: string) => http.post('/api/auth/totp/disable', { code }),
  methods: () => http.get<AuthMethods>('/api/auth/methods', { noAuthRedirect: true }),
  /** A browser navigation (not fetch): the server redirects to the identity provider. */
  ssoStartUrl: (next?: string) => `/api/auth/oidc/start${qs({ next: next && next !== '/' ? next : undefined })}`,
};

export const serverApi = {
  info: () => http.get<ServerInfo>('/api/server/info'),
  metrics: (minutes = 60) => http.get<MetricPoint[]>(`/api/server/metrics${qs({ minutes })}`),
  events: (limit = 100, siteId?: string) => http.get<NHEvent[]>(`/api/events${qs({ limit, siteId })}`),
  audit: (limit = 100, offset = 0) => http.get<AuditEntry[]>(`/api/audit${qs({ limit, offset })}`),
  get backupUrl() {
    return routePath('/api/backup');
  },
  /** An archive with the configured contents and passphrase. */
  get backupArchiveUrl() {
    return routePath('/api/backup?format=zip');
  },
  restore: (file: File, passphrase?: string) => {
    const fd = new FormData();
    fd.append('file', file);
    if (passphrase) fd.append('passphrase', passphrase);
    return http.post<RestoreResult>('/api/restore', fd);
  },
};

export const logShippingApi = {
  status: () => http.get<LogTargetStatus[]>('/api/logshipping/status'),
  test: (t: LogTarget) => http.post('/api/logshipping/test', t),
  searchServerLog: (p: LogSearchParams, signal?: AbortSignal) =>
    http.get<LogSearchResult>(`/api/server/logs/search${qs({ ...p, regex: p.regex ? 1 : undefined })}`, { signal }),
};

export const updatesApi = {
  status: () => http.get<UpdateStatus>('/api/updates'),
  check: () => http.post<UpdateStatus>('/api/updates/check'),
  install: () => http.post<UpdateStatus>('/api/updates/install'),
};

export const backupsApi = {
  status: () => http.get<BackupStatus>('/api/backups'),
  run: () => http.post<BackupStatus>('/api/backups/run'),
  test: (d: BackupDestination) => http.post<BackupTestResult>('/api/backups/test', d),
  sharedSizes: () => http.get<SharedSize[]>('/api/backups/shared-sizes'),
  files: (destId: string) => http.get<BackupObject[]>(`/api/backups/destinations/${enc(destId)}/files`),
  restoreFrom: (destId: string, file: string, passphrase?: string) =>
    http.post<RestoreResult>(`/api/backups/destinations/${enc(destId)}/restore`, { file, passphrase: passphrase || undefined }),
};

export type SiteAction = 'start' | 'stop' | 'restart' | 'recycle';

export const bansApi = {
  list: () => http.get<Ban[]>('/api/bans'),
  ban: (body: BanRequest) => http.post<Ban>('/api/bans', body),
  /** Also takes any address inside a banned range. */
  unban: (address: string) => http.del(`/api/bans/${enc(address)}`),
};

export const sitesApi = {
  list: () => http.get<SiteView[]>('/api/sites'),
  get: (id: string) => http.get<SiteView>(`/api/sites/${enc(id)}`),
  create: (site: Site) => http.post<SiteView>('/api/sites', site),
  update: (id: string, site: Site) => http.put<SiteView>(`/api/sites/${enc(id)}`, site),
  remove: (id: string, deleteFiles: boolean) => http.del(`/api/sites/${enc(id)}${qs({ deleteFiles: deleteFiles || undefined })}`),
  action: (id: string, action: SiteAction) => http.post<SiteStatus>(`/api/sites/${enc(id)}/${action}`),
  status: (id: string) => http.get<SiteStatus>(`/api/sites/${enc(id)}/status`),
  /** `slot`: a deployment slot's metrics. */
  metrics: (id: string, minutes = 60, slot?: string) => http.get<MetricPoint[]>(`/api/sites/${enc(id)}/metrics${qs({ minutes, slot })}`),
  /** `slot`: application lines of that slot only, or that slot's access log. */
  logs: (id: string, type: LogType, lines = 500, slot?: string) => http.get<LogLine[]>(`/api/sites/${enc(id)}/logs${qs({ type, lines, slot })}`),
  logsDownloadUrl: (id: string, type: LogType, slot?: string) => routePath(`/api/sites/${enc(id)}/logs/download${qs({ type, slot })}`),
  clearLogs: (id: string) => http.post(`/api/sites/${enc(id)}/logs/clear`),
  purgeCache: (id: string, path?: string) => http.post<{ purged: number }>(`/api/sites/${enc(id)}/cache/purge`, path ? { path } : {}),
  searchLogs: (id: string, p: LogSearchParams, signal?: AbortSignal) =>
    http.get<LogSearchResult>(`/api/sites/${enc(id)}/logs/search${qs({ ...p, regex: p.regex ? 1 : undefined })}`, { signal }),

  /** `slot` filters the history ("production" or a slot's name); without it: all. */
  deployments: (id: string, slot?: string) => http.get<Deployment[]>(`/api/sites/${enc(id)}/deployments${qs({ slot })}`),
  /** `slot`: deploy to a deployment slot instead of production. */
  deployZip: (id: string, file: File, slot?: string) => {
    const fd = new FormData();
    fd.append('file', file);
    return http.post<Deployment>(`/api/sites/${enc(id)}/deploy/zip${qs({ slot })}`, fd);
  },
  deployGit: (id: string, branch?: string, slot?: string) =>
    http.post<Deployment>(`/api/sites/${enc(id)}/deploy/git${qs({ slot })}`, branch ? { branch } : {}),
  /** `slot`: activate the release in a deployment slot instead of production. */
  activate: (id: string, depId: string, slot?: string) =>
    http.post<Deployment>(`/api/sites/${enc(id)}/deployments/${enc(depId)}/activate${qs({ slot })}`),
  deploymentLog: (id: string, depId: string) => http.text(`/api/sites/${enc(id)}/deployments/${enc(depId)}/log`),
};

/** Preview deployments of a site. Their settings are part of the site (deploy.previews). */
export const previewsApi = {
  list: (id: string) => http.get<PreviewView[]>(`/api/sites/${enc(id)}/previews`),
  /** Deploys a branch as a preview (created if needed). */
  deployBranch: (id: string, branch: string) => http.post<PreviewDecision>(`/api/sites/${enc(id)}/previews`, { branch }),
  redeploy: (id: string, previewId: string) => http.post<PreviewView>(`/api/sites/${enc(id)}/previews/${enc(previewId)}/redeploy`),
  remove: (id: string, previewId: string) => http.del(`/api/sites/${enc(id)}/previews/${enc(previewId)}`),
};

/** Scheduled tasks. Definitions are edited as part of the site (sitesApi.update). */
export const tasksApi = {
  list: (id: string) => http.get<TaskView[]>(`/api/sites/${enc(id)}/tasks`),
  /** `task` is the task's id or name. */
  run: (id: string, task: string) => http.post<TaskRunStart>(`/api/sites/${enc(id)}/tasks/${enc(task)}/run`),
  runs: (id: string, task?: string, limit = 50) => http.get<TaskRun[]>(`/api/sites/${enc(id)}/runs${qs({ task, limit })}`),
  cancel: (id: string, runId: string) => http.post<TaskRun>(`/api/sites/${enc(id)}/runs/${enc(runId)}/cancel`),
  log: (id: string, runId: string) => http.text(`/api/sites/${enc(id)}/runs/${enc(runId)}/log`),
  logDownloadUrl: (id: string, runId: string) => routePath(`/api/sites/${enc(id)}/runs/${enc(runId)}/log${qs({ download: true })}`),
};

export interface AcmeRequest {
  name: string;
  domains: string[];
  acme: ACMEOptions;
  autoRenew: boolean;
}

export const certsApi = {
  list: () => http.get<CertificateView[]>('/api/certificates'),
  get: (id: string) => http.get<CertificateView>(`/api/certificates/${enc(id)}`),
  requestAcme: (body: AcmeRequest) => http.post<CertificateView>('/api/certificates/acme', body),
  import: (p: { file: File; keyFile?: File | null; password?: string; name?: string }) => {
    const fd = new FormData();
    fd.append('file', p.file);
    if (p.keyFile) fd.append('keyFile', p.keyFile);
    if (p.password) fd.append('password', p.password);
    if (p.name) fd.append('name', p.name);
    return http.post<CertificateView>('/api/certificates/import', fd);
  },
  selfSigned: (body: { name: string; domains: string[]; validDays: number }) =>
    http.post<CertificateView>('/api/certificates/selfsigned', body),
  renew: (id: string) => http.post<CertificateView>(`/api/certificates/${enc(id)}/renew`),
  /** Asks the certificate's OCSP responder now. */
  checkOcsp: (id: string) => http.post<CertificateView>(`/api/certificates/${enc(id)}/ocsp`),
  update: (id: string, body: { name: string; autoRenew: boolean }) =>
    http.put<CertificateView>(`/api/certificates/${enc(id)}`, body),
  remove: (id: string) => http.del(`/api/certificates/${enc(id)}`),
  export: (id: string, format: 'pfx' | 'pem', password: string | undefined, fallbackName: string) =>
    http.download('POST', `/api/certificates/${enc(id)}/export`, { format, password: password || undefined }, fallbackName),
};

export const tlsApi = {
  /** TLS settings and the HTTP/3 (UDP) listeners they opened. */
  get: () => http.get<TLSView>('/api/tls'),
};

export const nodeApi = {
  versions: () => http.get<NodeVersions>('/api/node/versions'),
  available: () => http.get<AvailableNode[]>('/api/node/available'),
  install: (version: string) => http.post('/api/node/versions', { version }),
  remove: (version: string) => http.del(`/api/node/versions/${enc(version)}`),
};

/** Bun and Deno are installed by NodeHoster; Python and .NET are found. */
export const runtimesApi = {
  report: () => http.get<RuntimeReport>('/api/runtimes'),
  refresh: () => http.post<RuntimeReport>('/api/runtimes/refresh'),
  available: (rt: 'bun' | 'deno') => http.get<AvailableRuntime[]>(`/api/runtimes/${rt}/available`),
  install: (rt: 'bun' | 'deno', version: string) => http.post(`/api/runtimes/${rt}/versions`, { version }),
  remove: (rt: 'bun' | 'deno', version: string) => http.del(`/api/runtimes/${rt}/versions/${enc(version)}`),
};

export const settingsApi = {
  get: () => http.get<Settings>('/api/settings'),
  put: (s: Settings) => http.put<Settings>('/api/settings', s),
  dnsCatalog: () => http.get<DNSCatalogEntry[]>('/api/settings/dns-catalog'),
  testWebhook: (w: WebhookTarget) => http.post('/api/settings/webhooks/test', w),
  getAdmin: () => http.get<AdminSettings>('/api/settings/admin'),
  putAdmin: (a: AdminSettings) => http.put<AdminSettings>('/api/settings/admin', a),
  ssoCallbackUrl: () => http.get<{ redirectUrl: string }>('/api/settings/sso/callback-url'),
  ssoTest: (s: SSOSettings) => http.post<SSOTestResult>('/api/settings/sso/test', s),
};

export const usersApi = {
  list: () => http.get<User[]>('/api/users'),
  /** sso: signs in with single sign-on only (no password). */
  create: (body: { username: string; password?: string; sso?: boolean; role: Role; sites?: SiteGrant[] }) => http.post<User>('/api/users', body),
  update: (id: string, body: { role?: Role; sites?: SiteGrant[]; disabled?: boolean; password?: string }) =>
    http.put<User>(`/api/users/${enc(id)}`, body),
  remove: (id: string) => http.del(`/api/users/${enc(id)}`),
};

export const tokensApi = {
  list: () => http.get<APIToken[]>('/api/tokens'),
  create: (name: string, expiresDays?: number, restrict?: { role?: Role | ''; siteIds?: string[] }) =>
    http.post<CreatedToken>('/api/tokens', {
      name,
      ...(expiresDays ? { expiresDays } : {}),
      ...(restrict?.role ? { role: restrict.role } : {}),
      ...(restrict?.siteIds?.length ? { siteIds: restrict.siteIds } : {}),
    }),
  revoke: (id: string) => http.del(`/api/tokens/${enc(id)}`),
};

export type MailQueueFilter = 'queued' | 'failed' | '';

export const mailApi = {
  status: () => http.get<MailStatus>('/api/mail/status'),
  queue: (state: MailQueueFilter = '') => http.get<MailMessage[]>(`/api/mail/queue${qs({ state })}`),
  retry: (id: string) => http.post(`/api/mail/queue/${enc(id)}/retry`),
  retryAll: () => http.post('/api/mail/queue/retry'),
  remove: (id: string) => http.del(`/api/mail/queue/${enc(id)}`),
  emlUrl: (id: string) => routePath(`/api/mail/queue/${enc(id)}/eml`),
  test: (body: MailTest) => http.post<MailMessage>('/api/mail/test', body),
  /** Deliverability checks for the configured sending domains plus `domains`; takes up to ~25 s. */
  health: (domains: string[] = [], signal?: AbortSignal) => {
    const sp = new URLSearchParams();
    for (const d of domains) sp.append('domain', d);
    const q = sp.toString();
    return http.get<MailHealth>(`/api/mail/health${q ? `?${q}` : ''}`, { signal });
  },
};

export const rewriteApi = {
  import: (body: RewriteImportRequest) => http.post<RewriteImport>('/api/rewrite/import', body),
};

export const mimeApi = {
  defaults: () => http.get<MimeMap[]>('/api/mime/defaults'),
};

/** Importing sites from IIS, an iisnode web.config or PM2 (administrators). */
export const importApi = {
  /** Reads an uploaded file or pasted text; `name`/`appRoot` are for a single web.config. */
  preview: (source: Exclude<ImportSource, 'local-iis'>, input: File | string, extra: { name?: string; appRoot?: string } = {}) => {
    if (typeof input === 'string') return http.post<ImportPreview>('/api/import/preview', { source, text: input, ...extra });
    const fd = new FormData();
    fd.append('source', source);
    fd.append('file', input);
    if (extra.name) fd.append('name', extra.name);
    if (extra.appRoot) fd.append('appRoot', extra.appRoot);
    return http.post<ImportPreview>('/api/import/preview', fd);
  },
  /** This server's applicationHost.config (Windows with IIS only). */
  previewLocalIIS: () => http.post<ImportPreview>('/api/import/preview', { source: 'local-iis' }),
  apply: (req: ImportApplyRequest) => http.post<ImportApplyResult>('/api/import/apply', req),
};

/**
 * Connections to other servers. Always this server's (never proxied):
 * administrators manage them; the list holds those the caller may use.
 */
export const serversApi = {
  list: () => http.get<ServerView[]>('/api/servers'),
  get: (id: string) => http.get<ServerView>(`/api/servers/${enc(id)}`),
  check: (id: string) => http.post<ServerView>(`/api/servers/${enc(id)}/check`),
  create: (s: Omit<ServerConnection, 'id'>) => http.post<ServerView>('/api/servers', s),
  update: (id: string, s: ServerConnection) => http.put<ServerView>(`/api/servers/${enc(id)}`, s),
  remove: (id: string) => http.del(`/api/servers/${enc(id)}`),
  test: (t: ServerTest) => http.post<ServerTestResult>('/api/servers/test', t),
  /** Who the connection's token is on that server, capped at the caller's role. */
  remoteMe: (id: string) => http.get<Me>(`/api/servers/${enc(id)}/proxy/auth/me`),
};

/** Secret stores (Settings → Secret stores). No endpoint returns a value. */
export const secretStoresApi = {
  status: () => http.get<SecretStoreStatus[]>('/api/secret-stores'),
  test: (store: SecretStore, ref?: string) => http.post<SecretTestResult>('/api/secret-stores/test', { store, ref: ref || undefined }),
  resolve: (r: SecretRef) => http.post<SecretTestResult>('/api/secret-stores/resolve', r),
  checkSite: (siteId: string) => http.post<SecretRefCheck[]>(`/api/sites/${enc(siteId)}/secrets/check`),
};

export type SlotAction = 'start' | 'stop' | 'recycle';

/**
 * Deployment slots. The slots and their settings are edited as part of the
 * site (sitesApi.update); `slot` may be "production" for the site itself.
 */
export const slotsApi = {
  list: (id: string) => http.get<SlotsView>(`/api/sites/${enc(id)}/slots`),
  swapPreview: (id: string, slot: string) => http.get<SwapPreview>(`/api/sites/${enc(id)}/slots/${enc(slot)}/swap`),
  /** Starts a swap into production; it runs in the background (poll list). */
  swap: (id: string, slot: string) => http.post<SwapProgress>(`/api/sites/${enc(id)}/slots/${enc(slot)}/swap`),
  action: (id: string, slot: string, action: SlotAction) => http.post<SlotsView>(`/api/sites/${enc(id)}/slots/${enc(slot)}/${action}`),
};
