import { describe, expect, it } from 'vitest';
import { hitRatioLabel, purgePathError, validateCachePath, validateHeaderName } from './cache';

describe('purgePathError', () => {
  it('accepts no path (everything) and absolute prefixes', () => {
    expect(purgePathError('')).toBeNull();
    expect(purgePathError('/blog')).toBeNull();
  });
  it('rejects relative prefixes', () => {
    expect(purgePathError('blog/')).not.toBeNull();
  });
});

describe('validators', () => {
  it.each(['/api', '/'])('bypass path %j is valid', (v) => expect(validateCachePath(v)).toBeNull());
  it('bypass path must be absolute', () => expect(validateCachePath('api')).not.toBeNull());
  it.each(['X-Device', 'Accept-Language', 'cookie'])('header %j is valid', (v) => expect(validateHeaderName(v)).toBeNull());
  it.each(['X Device', 'a:b', ''])('header %j is invalid', (v) => expect(validateHeaderName(v)).not.toBeNull());
});

describe('hitRatioLabel', () => {
  it('describes an idle cache', () => {
    expect(hitRatioLabel(undefined)).toBe('no requests yet');
    expect(hitRatioLabel({ entries: 0, bytes: 0, hits: 0, misses: 0, hitRatio: 0 })).toBe('no requests yet');
  });
  it('rounds the ratio and shows the request count', () => {
    expect(hitRatioLabel({ entries: 3, bytes: 10, hits: 1003, misses: 201, hitRatio: 1003 / 1204 })).toBe('83% of 1,204');
  });
});
