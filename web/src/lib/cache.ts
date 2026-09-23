// Response cache helpers for the routing editor and the site overview.

import type { CacheStats } from '@/api/types';

export const VARY_BY_QUERY = [
  { value: 'all', label: 'Whole query string', description: 'Every distinct query string is cached separately (parameter order does not matter).' },
  { value: 'none', label: 'Ignore the query string', description: 'One entry per path, whatever the query string.' },
  { value: 'listed', label: 'Only these parameters', description: 'Other parameters (utm_source, fbclid…) share an entry.' },
];

/** Validation for the optional path prefix of a purge; null when valid. */
export function purgePathError(path: string): string | null {
  if (path === '') return null;
  return path.startsWith('/') ? null : 'Start with /, e.g. /blog';
}

/** Validation for a bypass path prefix. */
export const validateCachePath = (v: string) => (v.startsWith('/') ? null : 'Start with /, e.g. /api');

/** Validation for a header name (an RFC 7230 token). */
export const validateHeaderName = (v: string) => (/^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$/.test(v) ? null : 'Not a header name');

/** "83% of 1,204" style summary of the hit ratio. */
export function hitRatioLabel(s: CacheStats | null | undefined): string {
  const total = (s?.hits ?? 0) + (s?.misses ?? 0);
  if (!s || total === 0) return 'no requests yet';
  return `${Math.round(s.hitRatio * 100)}% of ${total.toLocaleString('en-US')}`;
}
