// Typed functions for every endpoint in docs/API.md.

import { http, qs } from './client';
import type {
  ACMEOptions,
  AdminSettings,
  APIToken,
  AuditEntry,
  AvailableNode,
  CertificateView,
  CreatedToken,
  DNSCatalogEntry,
  Deployment,
  LogLine,
  LogType,
  Me,
  MetricPoint,
  NHEvent,
  NodeVersions,
  Role,
  ServerInfo,
  Settings,
  Site,
  SiteStatus,
  SiteView,
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
};

export const serverApi = {
  info: () => http.get<ServerInfo>('/api/server/info'),
  metrics: (minutes = 60) => http.get<MetricPoint[]>(`/api/server/metrics${qs({ minutes })}`),
  events: (limit = 100, siteId?: string) => http.get<NHEvent[]>(`/api/events${qs({ limit, siteId })}`),
  audit: (limit = 100, offset = 0) => http.get<AuditEntry[]>(`/api/audit${qs({ limit, offset })}`),
  backupUrl: '/api/backup',
  restore: (file: File) => {
    const fd = new FormData();
    fd.append('file', file);
    return http.post('/api/restore', fd);
  },
};

export type SiteAction = 'start' | 'stop' | 'restart' | 'recycle';

export const sitesApi = {
  list: () => http.get<SiteView[]>('/api/sites'),
  get: (id: string) => http.get<SiteView>(`/api/sites/${enc(id)}`),
  create: (site: Site) => http.post<SiteView>('/api/sites', site),
  update: (id: string, site: Site) => http.put<SiteView>(`/api/sites/${enc(id)}`, site),
  remove: (id: string, deleteFiles: boolean) => http.del(`/api/sites/${enc(id)}${qs({ deleteFiles: deleteFiles || undefined })}`),
  action: (id: string, action: SiteAction) => http.post<SiteStatus>(`/api/sites/${enc(id)}/${action}`),
  status: (id: string) => http.get<SiteStatus>(`/api/sites/${enc(id)}/status`),
  metrics: (id: string, minutes = 60) => http.get<MetricPoint[]>(`/api/sites/${enc(id)}/metrics${qs({ minutes })}`),
  logs: (id: string, type: LogType, lines = 500) => http.get<LogLine[]>(`/api/sites/${enc(id)}/logs${qs({ type, lines })}`),
  logsDownloadUrl: (id: string, type: LogType) => `/api/sites/${enc(id)}/logs/download${qs({ type })}`,
  clearLogs: (id: string) => http.post(`/api/sites/${enc(id)}/logs/clear`),

  deployments: (id: string) => http.get<Deployment[]>(`/api/sites/${enc(id)}/deployments`),
  deployZip: (id: string, file: File) => {
    const fd = new FormData();
    fd.append('file', file);
    return http.post<Deployment>(`/api/sites/${enc(id)}/deploy/zip`, fd);
  },
  deployGit: (id: string, branch?: string) =>
    http.post<Deployment>(`/api/sites/${enc(id)}/deploy/git`, branch ? { branch } : {}),
  activate: (id: string, depId: string) =>
    http.post<Deployment>(`/api/sites/${enc(id)}/deployments/${enc(depId)}/activate`),
  deploymentLog: (id: string, depId: string) => http.text(`/api/sites/${enc(id)}/deployments/${enc(depId)}/log`),
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
  update: (id: string, body: { name: string; autoRenew: boolean }) =>
    http.put<CertificateView>(`/api/certificates/${enc(id)}`, body),
  remove: (id: string) => http.del(`/api/certificates/${enc(id)}`),
  export: (id: string, format: 'pfx' | 'pem', password: string | undefined, fallbackName: string) =>
    http.download('POST', `/api/certificates/${enc(id)}/export`, { format, password: password || undefined }, fallbackName),
};

export const nodeApi = {
  versions: () => http.get<NodeVersions>('/api/node/versions'),
  available: () => http.get<AvailableNode[]>('/api/node/available'),
  install: (version: string) => http.post('/api/node/versions', { version }),
  remove: (version: string) => http.del(`/api/node/versions/${enc(version)}`),
};

export const settingsApi = {
  get: () => http.get<Settings>('/api/settings'),
  put: (s: Settings) => http.put<Settings>('/api/settings', s),
  dnsCatalog: () => http.get<DNSCatalogEntry[]>('/api/settings/dns-catalog'),
  testWebhook: (w: WebhookTarget) => http.post('/api/settings/webhooks/test', w),
  getAdmin: () => http.get<AdminSettings>('/api/settings/admin'),
  putAdmin: (a: AdminSettings) => http.put<AdminSettings>('/api/settings/admin', a),
};

export const usersApi = {
  list: () => http.get<User[]>('/api/users'),
  create: (body: { username: string; password: string; role: Role }) => http.post<User>('/api/users', body),
  update: (id: string, body: { role?: Role; disabled?: boolean; password?: string }) =>
    http.put<User>(`/api/users/${enc(id)}`, body),
  remove: (id: string) => http.del(`/api/users/${enc(id)}`),
};

export const tokensApi = {
  list: () => http.get<APIToken[]>('/api/tokens'),
  create: (name: string, expiresDays?: number) =>
    http.post<CreatedToken>('/api/tokens', expiresDays ? { name, expiresDays } : { name }),
  revoke: (id: string) => http.del(`/api/tokens/${enc(id)}`),
};
