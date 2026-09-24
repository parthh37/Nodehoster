import { describe, expect, it } from 'vitest';
import type { WAFEvent } from '@/api/wafTypes';
import {
  describeExclusion,
  effectiveWAF,
  exclusionError,
  matchWhere,
  normalizeWAFSettings,
  parentPath,
  parseNames,
  parseRuleIds,
  suggestExclusion,
} from './waf';
import { defaultIPBan, normalizeIPBan, normalizeSettings } from './settingsDefaults';
import type { Settings } from '@/api/types';

const ev = (matches: WAFEvent['matches'], path = '/admin/posts/12'): Pick<WAFEvent, 'id' | 'path' | 'matches'> => ({ id: 'abc123', path, matches });
const m = (ruleId: number, where: string, name?: string) => ({ ruleId, category: 'xss', severity: 'critical', score: 5, message: 'x', in: where, name });

describe('effectiveWAF', () => {
  it('treats a missing configuration as off with the defaults', () => {
    expect(effectiveWAF(undefined)).toEqual({ mode: 'off', paranoia: 1, threshold: 5, bodyKB: 128 });
    expect(effectiveWAF({ mode: '' })).toMatchObject({ mode: 'off' });
    expect(effectiveWAF({ mode: 'block', paranoiaLevel: 2, anomalyThreshold: 10, inspectBodyKB: 64 })).toEqual({ mode: 'block', paranoia: 2, threshold: 10, bodyKB: 64 });
  });
});

describe('normalizeWAFSettings', () => {
  it('uses the defaults for settings saved before the firewall existed', () => {
    expect(normalizeWAFSettings(undefined)).toEqual({ defaultMode: 'detect', defaultParanoiaLevel: 1, defaultAnomalyThreshold: 5, eventRetentionDays: 30 });
    expect(normalizeWAFSettings({})).toMatchObject({ defaultMode: 'detect' });
  });
  it('keeps saved values and fills zeros like the server', () => {
    expect(normalizeWAFSettings({ defaultMode: 'block', eventRetentionDays: 7 })).toEqual({ defaultMode: 'block', defaultParanoiaLevel: 1, defaultAnomalyThreshold: 5, eventRetentionDays: 7 });
    expect(normalizeWAFSettings({ eventRetentionDays: 7 }).defaultMode).toBe('off');
  });
  it('is part of the settings normalization, with the ban rule', () => {
    const s = normalizeSettings({ ipBan: { enabled: true } } as unknown as Settings);
    expect(s.waf.defaultMode).toBe('detect');
    expect(s.ipBan.wafBlocks).toEqual({ threshold: 5, windowSec: 60 });
    expect(normalizeIPBan({ wafBlocks: { threshold: 0, windowSec: 60 } }).wafBlocks.threshold).toBe(0);
    expect(defaultIPBan().wafBlocks).toEqual({ threshold: 5, windowSec: 60 });
  });
});

describe('suggestExclusion', () => {
  it('scopes to the named targets when every match has one', () => {
    const x = suggestExclusion(ev([m(941100, 'arg', 'content'), m(941110, 'arg', 'content'), m(942100, 'cookie', 'prefs')]));
    expect(x).toEqual({ path: '/admin/posts/12', ruleIds: [941100, 941110, 942100], args: ['content'], cookies: ['prefs'], comment: 'From request abc123' });
  });
  it('excludes the rules under the path otherwise', () => {
    const x = suggestExclusion(ev([m(930100, 'path'), m(941100, 'arg', 'q')]));
    expect(x.args).toBeUndefined();
    expect(x.ruleIds).toEqual([930100, 941100]);
    expect(exclusionError(x)).toBeNull();
  });
});

describe('exclusions', () => {
  it('validates like the server', () => {
    expect(exclusionError({ ruleIds: [942100] })).toBeNull();
    expect(exclusionError({ path: '/hooks' })).toBeNull();
    expect(exclusionError({})).not.toBeNull();
    expect(exclusionError({ path: 'admin', ruleIds: [1] })).not.toBeNull();
    expect(exclusionError({ args: [' '] })).not.toBeNull();
    expect(exclusionError({ cookies: ['*'] })).not.toBeNull();
  });
  it('describes them', () => {
    expect(describeExclusion({ ruleIds: [942100], args: ['content'], path: '/admin' })).toBe('rule 942100 for argument content under /admin');
    expect(describeExclusion({ categories: ['sqli'] })).toBe('SQL / NoSQL injection on the whole site');
    expect(describeExclusion({ headers: ['X-Template'] })).toBe('all rules for header X-Template on the whole site');
    expect(describeExclusion({ path: '/webhooks/' })).toBe('Firewall off under /webhooks/');
  });
  it('parses rule IDs and names', () => {
    expect(parseRuleIds('942100, 941110 941110;930100')).toEqual([942100, 941110, 930100]);
    expect(parseRuleIds('942100, abc')).toBeNull();
    expect(parseRuleIds('')).toEqual([]);
    expect(parseNames('content,\n body , ,content')).toEqual(['content', 'body']);
  });
  it('widens paths', () => {
    expect(parentPath('/admin/posts/12')).toBe('/admin/posts/');
    expect(parentPath('/admin/')).toBe('/');
    expect(parentPath('/x')).toBe('/');
  });
});

describe('matchWhere', () => {
  it('names where a rule matched', () => {
    expect(matchWhere({ in: 'arg', name: 'q' })).toBe('argument q');
    expect(matchWhere({ in: 'header', name: 'User-Agent' })).toBe('header User-Agent');
    expect(matchWhere({ in: 'path' })).toBe('the path');
    expect(matchWhere({ in: 'request' })).toBe('the request');
  });
});
