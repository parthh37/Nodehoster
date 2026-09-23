// Session affinity helpers for the routing editor.

import type { Site } from '@/api/types';

/** An RFC 6265 cookie name, as the server validates it. */
export const COOKIE_NAME_RE = /^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$/;

/** 400 days, the longest cookie lifetime browsers accept. */
export const MAX_COOKIE_LIFETIME_SEC = 400 * 24 * 3600;

/** Site types whose requests are spread over several backends. */
export function affinitySupported(site: Pick<Site, 'type'>): boolean {
  return site.type === 'node' || site.type === 'proxy';
}

/**
 * What the affinity cookie pins for this site's current configuration, or
 * null when there is only one backend (no cookie is set then).
 */
export function affinityTargets(site: Pick<Site, 'type' | 'node' | 'proxy'>): string | null {
  if (site.type === 'proxy') {
    return (site.proxy?.upstreams?.length ?? 0) > 1 ? 'upstream' : null;
  }
  if (site.type === 'node' && site.node) {
    const many = (site.node.instances ?? 1) > 1;
    if (site.node.loadBalancer?.enabled) return many ? 'server and instance' : 'server';
    return many ? 'instance' : null;
  }
  return null;
}
