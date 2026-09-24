// Permission logic of the console, mirroring internal/auth/access.go. The
// server enforces all of it; the console only uses it to hide what the
// user cannot do.
import type { Access, Me, Role, SiteGrant, SiteRole, User } from '@/api/types';

const RANK: Record<string, number> = { viewer: 1, operator: 2, admin: 3 };
const rank = (r: string | undefined) => (r ? RANK[r] ?? 0 : 0);

/** The effective access of the signed-in caller. */
export function accessOf(me: Me | undefined): Access | undefined {
  if (!me) return undefined;
  // Servers from before per-site permissions do not send access.
  return me.access ?? { role: me.user.role, sites: me.user.sites };
}

/** A user or token limited to some sites, with no server-wide rights. */
export function isSiteScoped(a: Access | undefined): boolean {
  return a?.role === 'sites';
}

/** The role on one site, or undefined when the site is not accessible. */
export function siteRole(a: Access | undefined, siteId: string): Role | undefined {
  if (!a) return undefined;
  if (!isSiteScoped(a)) return rank(a.role) > 0 ? a.role : undefined;
  let best: SiteRole | undefined;
  for (const g of a.sites ?? []) {
    if (g.siteId === siteId && rank(g.role) > rank(best)) best = g.role;
  }
  return best;
}

export function canViewSite(a: Access | undefined, siteId: string): boolean {
  return rank(siteRole(a, siteId)) >= RANK.viewer;
}

/** Start, stop, restart, recycle, deploy and clear logs of a site. */
export function canOperate(a: Access | undefined, siteId: string): boolean {
  return rank(siteRole(a, siteId)) >= RANK.operator;
}

/** A role on the whole server (undefined for site-scoped access). */
export function serverRole(a: Access | undefined): Role | undefined {
  return a && !isSiteScoped(a) && rank(a.role) > 0 ? a.role : undefined;
}

/** Server-wide operator or administrator (certificates, mail queue). */
export function canOperateServer(a: Access | undefined): boolean {
  return rank(serverRole(a)) >= RANK.operator;
}

/** Server administrator: settings, users, site configuration. */
export function isServerAdmin(a: Access | undefined): boolean {
  return serverRole(a) === 'admin';
}

/** The strongest role anywhere: what a new API token may be limited to at most. */
export function highestRole(a: Access | undefined): Role | undefined {
  if (!a) return undefined;
  if (!isSiteScoped(a)) return serverRole(a);
  let best: SiteRole | undefined;
  for (const g of a.sites ?? []) if (rank(g.role) > rank(best)) best = g.role;
  return best;
}

/** Maximum roles a new token can be limited to; admin only without a site restriction. */
export function tokenRoleChoices(a: Access | undefined, restrictedToSites: boolean): Role[] {
  const top = rank(highestRole(a));
  return (['viewer', 'operator', 'admin'] as Role[]).filter((r) => rank(r) <= top && !(restrictedToSites && r === 'admin'));
}

/** Sets (or, with role null, removes) the grant of one site. */
export function setGrant(grants: SiteGrant[], siteId: string, role: SiteRole | null): SiteGrant[] {
  const rest = grants.filter((g) => g.siteId !== siteId);
  if (!role) return rest;
  const i = grants.findIndex((g) => g.siteId === siteId);
  const next = { siteId, role };
  if (i < 0) return [...rest, next];
  return [...grants.slice(0, i), next, ...grants.slice(i + 1)];
}

/** The users list's "Site access" column. */
export function describeSiteAccess(u: Pick<User, 'role' | 'sites'>, siteName: (id: string) => string | undefined): string {
  if (u.role !== 'sites') return 'All sites';
  const grants = u.sites ?? [];
  if (grants.length === 0) return 'No sites';
  if (grants.length === 1) return `${siteName(grants[0].siteId) ?? grants[0].siteId} (${grants[0].role})`;
  return `${grants.length} sites`;
}

/** A token's restriction, for the tokens list. */
export function describeTokenRestriction(t: { role?: Role | ''; siteIds?: string[] | null }, siteName: (id: string) => string | undefined): string {
  const parts: string[] = [];
  if (t.role) parts.push(`${t.role} at most`);
  if (t.siteIds) {
    parts.push(t.siteIds.length === 0 ? 'no sites' : t.siteIds.map((id) => siteName(id) ?? 'deleted site').join(', '));
  }
  return parts.length ? parts.join('; ') : 'Your access';
}
