import { describe, expect, it } from 'vitest';
import type { LogShippingSettings } from '@/api/types';
import { newTarget, normalizeLogShipping, statusTone, targetProblem, targetSummary } from './logShipping';

describe('normalizeLogShipping', () => {
  it('fills missing lists', () => {
    expect(normalizeLogShipping(undefined)).toEqual({ targets: [] });
    const n = normalizeLogShipping({ targets: [{ ...newTarget('http'), sources: null, siteIds: null, minLevel: '', http: { url: 'x', format: 'json', headers: null } }] } as unknown as LogShippingSettings);
    expect(n.targets[0]).toMatchObject({ sources: [], siteIds: [], minLevel: 'info', http: { headers: [] } });
  });
});

describe('targets', () => {
  it('summarizes', () => {
    const s = newTarget('syslog');
    s.syslog!.address = 'logs:514';
    expect(targetSummary(s)).toBe('udp://logs:514');
    const h = newTarget('http');
    h.http!.url = 'https://c/in';
    h.http!.format = 'ndjson';
    expect(targetSummary(h)).toBe('https://c/in (NDJSON)');
  });
  it('finds the first problem', () => {
    const s = newTarget('syslog');
    expect(targetProblem(s)?.field).toBe('name');
    s.name = 'x';
    expect(targetProblem(s)?.field).toBe('syslog.address');
    s.syslog!.address = 'logs.example.com:6514';
    expect(targetProblem(s)).toBeNull();
    s.syslog!.address = '[::1]:514';
    expect(targetProblem(s)).toBeNull();
    s.sources = [];
    expect(targetProblem(s)?.field).toBe('sources');
    const h = newTarget('http');
    h.name = 'h';
    h.http!.url = 'https://c';
    h.http!.headers = [{ name: 'Bad Name', value: '', secret: false }];
    expect(targetProblem(h)?.field).toBe('http.headers');
  });
  it('rates health', () => {
    const base = { enabled: true, dropped: 0, failed: 0 };
    expect(statusTone({ ...base, lastSuccess: '2026-03-01T10:00:00Z' })).toBe('green');
    expect(statusTone({ ...base, lastSuccess: '2026-03-01T10:00:00Z', lastErrorAt: '2026-03-01T11:00:00Z' })).toBe('red');
    expect(statusTone({ ...base, dropped: 5 })).toBe('amber');
    expect(statusTone({ ...base, enabled: false })).toBe('gray');
  });
});
