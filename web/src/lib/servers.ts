// Multi-server management: which requests go to a connected server through
// this one's proxy, what the signed-in user may do there, and how a
// server's health reads. The server enforces all of it; this only decides
// where requests go and what the console shows.
import type { Access, Role, ServerConnection, ServerHealth } from '@/api/types';

/**
 * API paths that always stay on this server, whichever server the console
 * operates: the account (sign-in, password, two-factor, API tokens) and
 * the connections themselves (including their proxy).
 */
const LOCAL_PREFIXES = ['/api/auth/', '/api/tokens', '/api/servers'];

export function isLocalApiPath(path: string): boolean {
  if (!path.startsWith('/api/')) return true;
  return LOCAL_PREFIXES.some((p) => path === p || path.startsWith(p.endsWith('/') ? p : `${p}/`) || path.startsWith(`${p}?`));
}

/** The URL of an API path on a connected server (serverId), or on this one (null). */
export function routeApiPath(path: string, serverId: string | null): string {
  if (!serverId || isLocalApiPath(path)) return path;
  return `/api/servers/${encodeURIComponent(serverId)}/proxy/${path.slice('/api/'.length)}`;
}

/** Whether a URL goes through a connection's proxy. */
export function isProxiedUrl(url: string): boolean {
  return /^\/api\/servers\/[^/]+\/proxy\//.test(url);
}

const RANK: Record<string, number> = { viewer: 1, operator: 2, admin: 3 };

/**
 * What the console may offer on a connected server: the token's access
 * there, never above the local user's role. A remote server of this
 * version applies the same cap itself; for an older one, the proxy still
 * keeps local viewers read-only.
 */
export function capAccess(remote: Access | undefined, localRole: Role | undefined): Access | undefined {
  if (!remote || !localRole) return undefined;
  const limit = RANK[localRole] ?? 0;
  if (limit === 0) return { role: 'sites', sites: [] };
  const cap = <R extends string>(r: R): R => ((RANK[r] ?? 0) > limit ? (localRole as unknown as R) : r);
  if (remote.role === 'sites') {
    return { role: 'sites', sites: (remote.sites ?? []).map((g) => ({ ...g, role: (RANK[g.role] ?? 0) > limit ? 'viewer' : g.role })) };
  }
  return { role: cap(remote.role) };
}

/** Whether a local role may use a connection (its minimum role). */
export function mayUseServer(localRole: Role | undefined, s: Pick<ServerConnection, 'minRole'>): boolean {
  if (!localRole || localRole === 'sites') return false;
  return (RANK[localRole] ?? 0) >= (RANK[s.minRole] ?? 3);
}

export type HealthState = 'online' | 'warning' | 'offline' | 'unknown';

/** A server's state for the overview: offline, online, or online with failed sites. */
export function healthState(h: ServerHealth | undefined): HealthState {
  if (!h || !h.checkedAt) return 'unknown';
  if (!h.reachable) return 'offline';
  if (h.failed > 0 || h.degraded > 0) return 'warning';
  return 'online';
}

export function healthLabel(h: ServerHealth | undefined): string {
  switch (healthState(h)) {
    case 'unknown':
      return 'Not checked yet';
    case 'offline':
      return 'Offline';
    case 'warning':
      return h!.failed > 0 ? `${h!.failed} failed` : `${h!.degraded} degraded`;
    default:
      return 'Online';
  }
}

/** Memory in use, as a percentage (undefined when unknown). */
export function memoryPercent(h: Pick<ServerHealth, 'memTotal' | 'memUsed'>): number | undefined {
  return h.memTotal > 0 ? (h.memUsed / h.memTotal) * 100 : undefined;
}

/**
 * Compares two NodeHoster versions ("1.4.2", "v1.10.0-rc1"): negative when
 * a is older. Unknown versions compare equal.
 */
export function compareVersions(a: string | undefined, b: string | undefined): number {
  const parse = (v: string | undefined) => {
    const m = /^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?/.exec((v ?? '').trim());
    return m ? [Number(m[1]), Number(m[2] ?? 0), Number(m[3] ?? 0)] : null;
  };
  const x = parse(a);
  const y = parse(b);
  if (!x || !y) return 0;
  for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return x[i] - y[i];
  return 0;
}

/** A note when a connected server runs another version than this one. */
export function versionNote(remote: string | undefined, local: string | undefined): string | null {
  if (!remote || !local || remote === local) return null;
  const c = compareVersions(remote, local);
  if (c < 0) return `older than this server (${local}): newer features are not available there`;
  if (c > 0) return `newer than this server (${local})`;
  return null;
}

/**
 * The message for an endpoint a connected server does not have (a 404
 * "no such endpoint" from an older version).
 */
export function missingEndpointMessage(name: string, version: string | undefined): string {
  return `${name}${version ? ` runs NodeHoster ${version}, which` : ''} does not have this feature. Update it to use this page there.`;
}

/** Groups a SHA-256 fingerprint by bytes (AB:CD:…) for comparing it with the server's. */
export function formatFingerprint(fp: string | undefined): string {
  if (!fp) return '';
  const hex = fp.replace(/[^0-9a-fA-F]/g, '').toUpperCase();
  return hex.match(/.{1,2}/g)?.join(':') ?? '';
}

/** Normalizes a fingerprint as entered (colons, spaces, "SHA256:"), or null when it is not one. */
export function parseFingerprint(input: string): string | null {
  let s = input.trim();
  if (!s) return '';
  s = s.replace(/^sha-?256\s*:/i, '');
  s = s.replace(/[\s:-]/g, '');
  return /^[0-9a-fA-F]{64}$/.test(s) ? s.toUpperCase() : null;
}

/** Client-side check of a web console URL, as the server normalizes it; null when fine. */
export function serverUrlError(input: string): string | null {
  const raw = input.trim();
  if (!raw) return 'Enter the web console URL, e.g. https://web02:8484';
  let u: URL;
  try {
    u = new URL(raw.includes('://') ? raw : `https://${raw}`);
  } catch {
    return 'Not a URL';
  }
  if (u.username || u.password || u.search || u.hash) return 'The URL cannot have a user name, a query or a fragment';
  if (u.protocol === 'http:') {
    const h = u.hostname.replace(/^\[|\]$/g, '');
    if (h !== 'localhost' && h !== '::1' && !/^127\./.test(h)) return 'Use https://: the API token must not cross the network in clear text';
  } else if (u.protocol !== 'https:') {
    return 'The URL must start with https://';
  }
  return null;
}
