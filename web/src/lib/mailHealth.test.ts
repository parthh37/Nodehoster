import { describe, expect, it } from 'vitest';
import type { MailCheck, MailHealth } from '@/api/types';
import {
  addDomains,
  countChecks,
  fixView,
  isIPAddress,
  normalizeDomain,
  summarizeHealth,
  summaryText,
  validateDomain,
  validatePublicIP,
  worstOfCounts,
  worstStatus,
} from './mailHealth';

const check = (status: string, p: Partial<MailCheck> = {}): MailCheck => ({ name: 'x', status, detail: '', ...p });

describe('normalizeDomain / validateDomain', () => {
  it('lower-cases, trims and drops a trailing dot or a mailbox', () => {
    expect(normalizeDomain('  Example.COM. ')).toBe('example.com');
    expect(normalizeDomain('app@Mail.Example.com')).toBe('mail.example.com');
  });

  it('accepts domain names', () => {
    for (const d of ['example.com', 'mail.example.co.uk', 'xn--bcher-kva.example', 'a-b.io', 'EXAMPLE.com.']) {
      expect(validateDomain(d)).toBeNull();
    }
  });

  it('rejects everything else', () => {
    for (const d of ['', 'localhost', 'example', '-a.com', 'a-.com', 'exa mple.com', 'example.c', '1.2.3.4', 'http://example.com', 'a..com']) {
      expect(validateDomain(d)).not.toBeNull();
    }
  });
});

describe('addDomains', () => {
  it('adds normalized domains and skips duplicates', () => {
    expect(addDomains(['a.com'], ' B.com, a.com c.org ')).toEqual({ domains: ['a.com', 'b.com', 'c.org'], error: null });
  });

  it('adds nothing when one is invalid and names it', () => {
    const r = addDomains(['a.com'], 'b.com nope');
    expect(r.domains).toEqual(['a.com']);
    expect(r.error).toMatch(/^nope: /);
  });

  it('reports a single invalid domain without the prefix', () => {
    expect(addDomains([], 'nope').error).toBe('Enter a domain name, e.g. example.com');
  });

  it('ignores empty input', () => {
    expect(addDomains(['a.com'], '  ,  ')).toEqual({ domains: ['a.com'], error: null });
  });
});

describe('isIPAddress / validatePublicIP', () => {
  it('accepts IPv4 and IPv6 addresses', () => {
    for (const ip of ['203.0.113.10', '0.0.0.0', '255.255.255.255', '::1', '2001:db8::25', 'fe80::1:2:3:4', '::ffff:192.0.2.1']) {
      expect(isIPAddress(ip)).toBe(true);
    }
  });

  it('rejects networks, host names and malformed addresses', () => {
    for (const ip of ['', '10.0.0.0/8', '256.1.1.1', '1.2.3', '01.2.3.4', 'mail.example.com', '2001:db8:::1', '1:2:3:4:5:6:7:8:9', '::ffff:999.0.2.1', 'fe80::1%eth0']) {
      expect(isIPAddress(ip)).toBe(false);
    }
  });

  it('allows blank (detect)', () => {
    expect(validatePublicIP('')).toBeNull();
    expect(validatePublicIP('  ')).toBeNull();
    expect(validatePublicIP('1.2.3.4')).toBeNull();
    expect(validatePublicIP('10.0.0.0/8')).not.toBeNull();
  });
});

describe('counts', () => {
  it('counts by status, treating unknown statuses as info', () => {
    expect(countChecks([check('pass'), check('fail'), check('warn'), check('pass'), check('info'), check('odd')])).toEqual({
      fail: 1,
      warn: 1,
      pass: 2,
      info: 2,
      total: 6,
    });
    expect(countChecks(null).total).toBe(0);
  });

  it('summarizes server and domain checks together', () => {
    const h: MailHealth = {
      hostname: 'mail.example.com',
      delivery: 'direct',
      checkedAt: '2026-09-24T10:00:00Z',
      server: [check('pass'), check('warn')],
      domains: [
        { domain: 'a.com', checks: [check('fail'), check('pass')] },
        { domain: 'b.com', checks: null },
      ],
    };
    expect(summarizeHealth(h)).toEqual({ fail: 1, warn: 1, pass: 2, info: 0, total: 4 });
    expect(summarizeHealth({ ...h, server: null, domains: null }).total).toBe(0);
    expect(summarizeHealth(undefined).total).toBe(0);
  });

  it('picks the worst status', () => {
    expect(worstStatus([check('pass'), check('warn'), check('info')])).toBe('warn');
    expect(worstStatus([check('warn'), check('fail')])).toBe('fail');
    expect(worstStatus([check('info'), check('pass')])).toBe('pass');
    expect(worstStatus([])).toBe('info');
    expect(worstOfCounts({ fail: 0, warn: 0, pass: 0, info: 3, total: 3 })).toBe('info');
    expect(worstOfCounts({ fail: 1, warn: 0, pass: 5, info: 0, total: 6 })).toBe('fail');
  });

  it('writes a summary', () => {
    expect(summaryText({ fail: 2, warn: 1, pass: 9, info: 1, total: 13 })).toBe('2 failed, 1 warning, 9 passed');
    expect(summaryText({ fail: 0, warn: 3, pass: 0, info: 0, total: 3 })).toBe('3 warnings');
    expect(summaryText({ fail: 0, warn: 0, pass: 0, info: 2, total: 2 })).toBe('Nothing to judge');
    expect(summaryText({ fail: 0, warn: 0, pass: 0, info: 0, total: 0 })).toBe('No checks');
  });
});

describe('fixView', () => {
  it('has nothing to show without a fix', () => {
    expect(fixView({})).toEqual({ kind: 'none' });
    expect(fixView({ fix: '  ', fixDns: 'example.com' })).toEqual({ kind: 'none' });
  });

  it('shows a TXT record to publish at fixDns', () => {
    expect(fixView({ fix: 'v=spf1 ip4:203.0.113.10 ~all', fixDns: 'example.com' })).toEqual({
      kind: 'dns',
      type: 'TXT',
      name: 'example.com',
      value: 'v=spf1 ip4:203.0.113.10 ~all',
    });
    expect(fixView({ fix: 'v=DMARC1; p=none', fixDns: '_dmarc.example.com' })).toMatchObject({ type: 'TXT', name: '_dmarc.example.com' });
  });

  it('shows an A record with the address alone', () => {
    expect(fixView({ fix: 'A 203.0.113.10', fixDns: 'mail.example.com' })).toEqual({
      kind: 'dns',
      type: 'A',
      name: 'mail.example.com',
      value: '203.0.113.10',
    });
  });

  it('treats a fix without fixDns as an instruction, even if it starts with "A "', () => {
    expect(fixView({ fix: 'Ask the provider to unblock outbound port 25.' })).toEqual({ kind: 'hint', text: 'Ask the provider to unblock outbound port 25.' });
    expect(fixView({ fix: 'A record needed', fixDns: '' })).toEqual({ kind: 'hint', text: 'A record needed' });
  });
});
