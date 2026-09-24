import { describe, expect, it } from 'vitest';
import type { Access, Me } from '@/api/types';
import {
  accessOf,
  canOperate,
  canOperateServer,
  canViewSite,
  describeSiteAccess,
  describeTokenRestriction,
  highestRole,
  isServerAdmin,
  isSiteScoped,
  serverRole,
  setGrant,
  siteRole,
  tokenRoleChoices,
} from './access';

const admin: Access = { role: 'admin' };
const operator: Access = { role: 'operator' };
const viewer: Access = { role: 'viewer' };
const agency: Access = {
  role: 'sites',
  sites: [
    { siteId: 'a', role: 'operator' },
    { siteId: 'b', role: 'viewer' },
  ],
};

describe('site permissions', () => {
  it('gives server-wide roles every site', () => {
    expect(canViewSite(viewer, 'x')).toBe(true);
    expect(canOperate(viewer, 'x')).toBe(false);
    expect(canOperate(operator, 'x')).toBe(true);
    expect(canOperate(admin, 'x')).toBe(true);
    expect(siteRole(admin, 'x')).toBe('admin');
  });

  it('limits site-scoped access to the grants', () => {
    expect(canOperate(agency, 'a')).toBe(true);
    expect(canViewSite(agency, 'b')).toBe(true);
    expect(canOperate(agency, 'b')).toBe(false);
    expect(canViewSite(agency, 'c')).toBe(false);
    expect(siteRole(agency, 'c')).toBeUndefined();
  });

  it('allows nothing without access or with an unknown role', () => {
    expect(canViewSite(undefined, 'a')).toBe(false);
    expect(canViewSite({ role: 'root' as never }, 'a')).toBe(false);
    expect(canViewSite({ role: 'sites' }, 'a')).toBe(false);
  });
});

describe('server permissions', () => {
  it('never reach site-scoped access', () => {
    expect(isSiteScoped(agency)).toBe(true);
    expect(serverRole(agency)).toBeUndefined();
    expect(canOperateServer(agency)).toBe(false);
    expect(isServerAdmin(agency)).toBe(false);
  });

  it('follow the server role', () => {
    expect(isServerAdmin(admin)).toBe(true);
    expect(isServerAdmin(operator)).toBe(false);
    expect(canOperateServer(operator)).toBe(true);
    expect(canOperateServer(viewer)).toBe(false);
    expect(isSiteScoped(viewer)).toBe(false);
  });
});

describe('accessOf', () => {
  const user = { id: 'u', username: 'x', totpEnabled: false, disabled: false, createdAt: '' };
  it('prefers the effective access the server sends', () => {
    const me: Me = { user: { ...user, role: 'admin' }, mustChangePassword: false, access: { role: 'sites', sites: [] } };
    expect(accessOf(me)).toEqual({ role: 'sites', sites: [] });
  });
  it('falls back to the user for older servers', () => {
    const me: Me = { user: { ...user, role: 'operator' }, mustChangePassword: false };
    expect(accessOf(me)).toEqual({ role: 'operator', sites: undefined });
    expect(accessOf(undefined)).toBeUndefined();
  });
});

describe('tokens', () => {
  it('offers roles up to the strongest one', () => {
    expect(highestRole(agency)).toBe('operator');
    expect(highestRole({ role: 'sites', sites: [{ siteId: 'a', role: 'viewer' }] })).toBe('viewer');
    expect(tokenRoleChoices(admin, false)).toEqual(['viewer', 'operator', 'admin']);
    expect(tokenRoleChoices(admin, true)).toEqual(['viewer', 'operator']);
    expect(tokenRoleChoices(viewer, false)).toEqual(['viewer']);
    expect(tokenRoleChoices(agency, false)).toEqual(['viewer', 'operator']);
  });

  it('describes restrictions', () => {
    const name = (id: string) => ({ a: 'site-a' })[id];
    expect(describeTokenRestriction({}, name)).toBe('Your access');
    expect(describeTokenRestriction({ role: '', siteIds: null }, name)).toBe('Your access');
    expect(describeTokenRestriction({ role: 'viewer' }, name)).toBe('viewer at most');
    expect(describeTokenRestriction({ role: 'operator', siteIds: ['a', 'gone'] }, name)).toBe('operator at most; site-a, deleted site');
    expect(describeTokenRestriction({ siteIds: [] }, name)).toBe('no sites');
  });
});

describe('grants editing', () => {
  it('adds, changes in place and removes', () => {
    let g = setGrant([], 'a', 'viewer');
    g = setGrant(g, 'b', 'operator');
    expect(g).toEqual([
      { siteId: 'a', role: 'viewer' },
      { siteId: 'b', role: 'operator' },
    ]);
    g = setGrant(g, 'a', 'operator');
    expect(g[0]).toEqual({ siteId: 'a', role: 'operator' });
    expect(g).toHaveLength(2);
    expect(setGrant(g, 'a', null)).toEqual([{ siteId: 'b', role: 'operator' }]);
    expect(setGrant(g, 'zzz', null)).toEqual(g);
  });

  it('summarizes site access', () => {
    const name = (id: string) => ({ a: 'site-a' })[id];
    expect(describeSiteAccess({ role: 'viewer' }, name)).toBe('All sites');
    expect(describeSiteAccess({ role: 'sites', sites: [] }, name)).toBe('No sites');
    expect(describeSiteAccess({ role: 'sites', sites: [{ siteId: 'a', role: 'operator' }] }, name)).toBe('site-a (operator)');
    expect(describeSiteAccess({ role: 'sites', sites: [{ siteId: 'x', role: 'viewer' }] }, name)).toBe('x (viewer)');
    expect(describeSiteAccess(agency, name)).toBe('2 sites');
  });
});
