import { describe, expect, it } from 'vitest';
import type { ServerHealth } from '@/api/types';
import {
  capAccess,
  compareVersions,
  formatFingerprint,
  healthLabel,
  healthState,
  isLocalApiPath,
  isProxiedUrl,
  mayUseServer,
  memoryPercent,
  missingEndpointMessage,
  parseFingerprint,
  routeApiPath,
  serverUrlError,
  versionNote,
} from './servers';

describe('routeApiPath', () => {
  it('leaves everything on this server when none is selected', () => {
    expect(routeApiPath('/api/sites', null)).toBe('/api/sites');
  });
  it('sends server APIs through the connection proxy', () => {
    expect(routeApiPath('/api/sites', 'abc')).toBe('/api/servers/abc/proxy/sites');
    expect(routeApiPath('/api/sites/x/logs/stream?type=out', 'abc')).toBe('/api/servers/abc/proxy/sites/x/logs/stream?type=out');
    expect(routeApiPath('/api/bans/2001%3Adb8%3A%3A%2F48', 'a b')).toBe('/api/servers/a%20b/proxy/bans/2001%3Adb8%3A%3A%2F48');
    expect(routeApiPath('/api/stream', 'abc')).toBe('/api/servers/abc/proxy/stream');
  });
  it('keeps the account and the connections local', () => {
    for (const p of ['/api/auth/me', '/api/auth/logout', '/api/tokens', '/api/tokens/1', '/api/servers', '/api/servers/abc/check', '/api/servers?x=1', '/login']) {
      expect(isLocalApiPath(p)).toBe(true);
      expect(routeApiPath(p, 'abc')).toBe(p);
    }
    // Only whole segments: an endpoint that merely starts alike is remote.
    expect(isLocalApiPath('/api/serversettings')).toBe(false);
    expect(isLocalApiPath('/api/tokensx')).toBe(false);
  });
  it('recognizes proxied URLs', () => {
    expect(isProxiedUrl('/api/servers/abc/proxy/sites')).toBe(true);
    expect(isProxiedUrl('/api/servers/abc/check')).toBe(false);
    expect(isProxiedUrl('/api/sites')).toBe(false);
  });
});

describe('capAccess', () => {
  it('never exceeds the local role', () => {
    expect(capAccess({ role: 'admin' }, 'operator')).toEqual({ role: 'operator' });
    expect(capAccess({ role: 'admin' }, 'viewer')).toEqual({ role: 'viewer' });
    expect(capAccess({ role: 'operator' }, 'admin')).toEqual({ role: 'operator' });
    expect(capAccess({ role: 'viewer' }, 'operator')).toEqual({ role: 'viewer' });
  });
  it('caps site grants', () => {
    const a = capAccess({ role: 'sites', sites: [{ siteId: 's', role: 'operator' }] }, 'viewer');
    expect(a).toEqual({ role: 'sites', sites: [{ siteId: 's', role: 'viewer' }] });
    expect(capAccess({ role: 'sites', sites: [{ siteId: 's', role: 'operator' }] }, 'operator')?.sites?.[0].role).toBe('operator');
  });
  it('gives nothing without both sides', () => {
    expect(capAccess(undefined, 'admin')).toBeUndefined();
    expect(capAccess({ role: 'admin' }, undefined)).toBeUndefined();
    expect(capAccess({ role: 'admin' }, 'sites')).toEqual({ role: 'sites', sites: [] });
  });
});

describe('mayUseServer', () => {
  it('compares the local role with the minimum', () => {
    expect(mayUseServer('admin', { minRole: 'admin' })).toBe(true);
    expect(mayUseServer('operator', { minRole: 'admin' })).toBe(false);
    expect(mayUseServer('operator', { minRole: 'operator' })).toBe(true);
    expect(mayUseServer('viewer', { minRole: 'viewer' })).toBe(true);
    expect(mayUseServer('sites', { minRole: 'viewer' })).toBe(false);
    expect(mayUseServer(undefined, { minRole: 'viewer' })).toBe(false);
  });
});

const health = (h: Partial<ServerHealth>): ServerHealth => ({
  reachable: true,
  checkedAt: '2026-09-24T10:00:00Z',
  latencyMs: 12,
  cpuPercent: 0,
  cpuCount: 0,
  memTotal: 0,
  memUsed: 0,
  sites: 0,
  running: 0,
  degraded: 0,
  failed: 0,
  stopped: 0,
  ...h,
});

describe('health', () => {
  it('reads the state', () => {
    expect(healthState(undefined)).toBe('unknown');
    expect(healthState(health({ checkedAt: undefined, reachable: false }))).toBe('unknown');
    expect(healthState(health({ reachable: false, error: 'x' }))).toBe('offline');
    expect(healthState(health({ failed: 1 }))).toBe('warning');
    expect(healthState(health({ degraded: 2 }))).toBe('warning');
    expect(healthState(health({ running: 3 }))).toBe('online');
  });
  it('labels it', () => {
    expect(healthLabel(health({ failed: 2 }))).toBe('2 failed');
    expect(healthLabel(health({ degraded: 1 }))).toBe('1 degraded');
    expect(healthLabel(health({ reachable: false }))).toBe('Offline');
    expect(healthLabel(undefined)).toBe('Not checked yet');
  });
  it('computes memory use', () => {
    expect(memoryPercent({ memTotal: 8, memUsed: 2 })).toBe(25);
    expect(memoryPercent({ memTotal: 0, memUsed: 2 })).toBeUndefined();
  });
});

describe('versions', () => {
  it('compares', () => {
    expect(compareVersions('1.2.0', '1.10.0')).toBeLessThan(0);
    expect(compareVersions('v2.0.0', '1.99.9')).toBeGreaterThan(0);
    expect(compareVersions('1.2', '1.2.0')).toBe(0);
    expect(compareVersions('dev', '1.0.0')).toBe(0);
  });
  it('notes skew', () => {
    expect(versionNote('1.2.0', '1.2.0')).toBeNull();
    expect(versionNote('1.1.0', '1.2.0')).toMatch(/older/);
    expect(versionNote('1.3.0', '1.2.0')).toMatch(/newer/);
    expect(versionNote(undefined, '1.2.0')).toBeNull();
    expect(missingEndpointMessage('web02', '1.1.0')).toBe('web02 runs NodeHoster 1.1.0, which does not have this feature. Update it to use this page there.');
    expect(missingEndpointMessage('web02', undefined)).toMatch(/^web02 does not have this feature/);
  });
});

describe('fingerprints and URLs', () => {
  const fp = '0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF';
  it('formats and parses', () => {
    expect(formatFingerprint(fp).slice(0, 8)).toBe('01:23:45');
    expect(parseFingerprint(formatFingerprint(fp))).toBe(fp);
    expect(parseFingerprint(`SHA256:${fp.toLowerCase()}`)).toBe(fp);
    expect(parseFingerprint('')).toBe('');
    expect(parseFingerprint('abc')).toBeNull();
  });
  it('checks URLs', () => {
    expect(serverUrlError('https://web02:8484')).toBeNull();
    expect(serverUrlError('web02:8484')).toBeNull();
    expect(serverUrlError('http://127.0.0.1:8484')).toBeNull();
    expect(serverUrlError('http://web02:8484')).toMatch(/https/);
    expect(serverUrlError('')).toMatch(/Enter/);
    expect(serverUrlError('https://web02/?a=1')).toMatch(/query/);
  });
});
