import { describe, expect, it } from 'vitest';
import type { Site } from '@/api/types';
import { COOKIE_NAME_RE, affinitySupported, affinityTargets } from './affinity';
import { newSite } from './siteDefaults';

describe('COOKIE_NAME_RE', () => {
  it.each(['NHAffinity', 'ARRAffinity', 'my.app-sticky', 'a', 'x'.repeat(64)])('accepts %j', (v) => {
    expect(COOKIE_NAME_RE.test(v)).toBe(true);
  });
  it.each(['', 'has space', 'semi;colon', 'eq=', 'x'.repeat(65), 'quote"', 'comma,'])('rejects %j', (v) => {
    expect(COOKIE_NAME_RE.test(v)).toBe(false);
  });
});

describe('affinityTargets', () => {
  const node = (instances: number, lb = false): Site => {
    const s = newSite('node');
    s.node!.instances = instances;
    s.node!.loadBalancer.enabled = lb;
    return s;
  };
  it('pins instances of a node site with several', () => {
    expect(affinityTargets(node(1))).toBeNull();
    expect(affinityTargets(node(3))).toBe('instance');
  });
  it('pins the server, and the instance, of a load-balanced node site', () => {
    expect(affinityTargets(node(1, true))).toBe('server');
    expect(affinityTargets(node(2, true))).toBe('server and instance');
  });
  it('pins upstreams of a proxy site with several', () => {
    const s = newSite('proxy');
    expect(affinityTargets(s)).toBeNull();
    s.proxy!.upstreams = [{ url: 'http://a' }, { url: 'http://b' }];
    expect(affinityTargets(s)).toBe('upstream');
  });
  it('does not apply to static and redirect sites', () => {
    expect(affinitySupported(newSite('static'))).toBe(false);
    expect(affinitySupported(newSite('redirect'))).toBe(false);
    expect(affinityTargets(newSite('static'))).toBeNull();
    expect(affinitySupported(newSite('proxy'))).toBe(true);
  });
});
