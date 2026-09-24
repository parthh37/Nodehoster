import { afterEach, describe, expect, it } from 'vitest';
import { ApiError } from './client';
import { currentTarget, remoteError, routePath, setTarget, subscribeTarget } from './target';

describe('target', () => {
  afterEach(() => setTarget(null));

  it('routes requests to the server the console operates', () => {
    expect(routePath('/api/sites')).toBe('/api/sites');
    let calls = 0;
    const off = subscribeTarget(() => calls++);
    setTarget({ id: 'c1', name: 'web02', url: 'https://web02:8484', version: '1.0.0' });
    off();
    expect(calls).toBe(1);
    expect(currentTarget()?.name).toBe('web02');
    expect(routePath('/api/sites')).toBe('/api/servers/c1/proxy/sites');
    expect(routePath('/api/auth/me')).toBe('/api/auth/me');
  });

  it('explains an endpoint an older server does not have', () => {
    setTarget({ id: 'c1', name: 'web02', url: 'https://web02:8484', version: '1.0.0' });
    const missing = () => new ApiError(404, 'no such endpoint', undefined, { error: 'no such endpoint' });
    expect(remoteError(missing(), '/api/servers/c1/proxy/waf').message).toBe(
      'web02 runs NodeHoster 1.0.0, which does not have this feature. Update it to use this page there.',
    );
    // This server's own answers, and other errors, are left alone.
    expect(remoteError(missing(), '/api/servers').message).toBe('no such endpoint');
    expect(remoteError(new ApiError(404, 'not found', undefined, { error: 'not found' }), '/api/servers/c1/proxy/sites/x').message).toBe('not found');
    setTarget(null);
    expect(remoteError(missing(), '/api/servers/c1/proxy/waf').message).toBe('no such endpoint');
  });
});
