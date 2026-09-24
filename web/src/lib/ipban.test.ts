import { describe, expect, it } from 'vitest';
import { banAddressError, banLengthMinutes, banRemaining } from './ipban';
import { defaultIPBan, normalizeIPBan } from './settingsDefaults';

describe('banAddressError', () => {
  it.each(['203.0.113.7', '203.0.113.0/24', '10.0.0.0/8', '2001:db8::1', '2001:db8::/64', '::ffff:1.2.3.4'])('accepts %j', (v) => {
    expect(banAddressError(v)).toBeNull();
  });
  it.each(['', 'example.com', '1.2.3', '256.1.1.1', '10.0.0.0/4', '2001:db8::/16', '1.2.3.4/33', '1.2.3.4/x', '1.2.3.4/8/9'])('rejects %j', (v) => {
    expect(banAddressError(v)).not.toBeNull();
  });
});

describe('banRemaining', () => {
  const now = Date.parse('2026-09-01T12:00:00Z');
  it('describes the time left', () => {
    expect(banRemaining({ expiresAt: null }, now)).toBe('until removed');
    expect(banRemaining({ expiresAt: '2026-09-01T12:14:10Z' }, now)).toBe('in 15 min');
    expect(banRemaining({ expiresAt: '2026-09-01T15:00:00Z' }, now)).toBe('in 3 h');
    expect(banRemaining({ expiresAt: '2026-09-05T12:00:00Z' }, now)).toBe('in 4 days');
    expect(banRemaining({ expiresAt: '2026-09-01T11:00:00Z' }, now)).toBe('expired');
  });
});

describe('banLengthMinutes', () => {
  it('doubles up to the maximum', () => {
    expect([1, 2, 3, 4, 5, 8].map((n) => banLengthMinutes(n, 15, 120))).toEqual([15, 30, 60, 120, 120, 120]);
  });
});

describe('normalizeIPBan', () => {
  it('uses the defaults for settings saved before banning existed', () => {
    expect(normalizeIPBan(undefined)).toEqual(defaultIPBan());
  });
  it('keeps saved values and fills null lists', () => {
    const n = normalizeIPBan({ enabled: true, trapPaths: null, allowList: null, notFound: { threshold: 0, windowSec: 60 } });
    expect(n.enabled).toBe(true);
    expect(n.trapPaths).toEqual([]);
    expect(n.allowList).toEqual([]);
    expect(n.notFound.threshold).toBe(0);
    expect(n.authFailures).toEqual(defaultIPBan().authFailures);
  });
});
