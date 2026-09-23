import { describe, expect, it } from 'vitest';
import { highlight, patternProblem, timeRange } from './logSearch';

const marked = (segs: { text: string; match: boolean }[]) => segs.map((s) => (s.match ? `[${s.text}]` : s.text)).join('');

describe('highlight', () => {
  it('marks plain matches case-insensitively, special characters literally', () => {
    expect(marked(highlight('GET /a.js 200 get', 'get'))).toBe('[GET] /a.js 200 [get]');
    expect(marked(highlight('a.b axb', 'a.b'))).toBe('[a.b] axb');
    expect(marked(highlight('(x) y', '(x)'))).toBe('[(x)] y');
  });
  it('uses regular expressions as given', () => {
    expect(marked(highlight('status 500 and 503', '50\\d', true))).toBe('status [500] and [503]');
    expect(marked(highlight('Error error', 'error', true))).toBe('Error [error]');
    expect(marked(highlight('Error error', '(?i)error', true))).toBe('[Error] [error]');
  });
  it('survives empty, zero-width and invalid patterns', () => {
    expect(highlight('abc', '')).toEqual([{ text: 'abc', match: false }]);
    expect(marked(highlight('abc', 'x*', true))).toBe('abc');
    expect(marked(highlight('abc', '^', true))).toBe('abc');
    expect(highlight('abc', '(', true)).toEqual([{ text: 'abc', match: false }]);
  });
});

describe('patternProblem', () => {
  it('checks the length of regular expressions only', () => {
    expect(patternProblem('a'.repeat(600), true)).toMatch(/512/);
    expect(patternProblem('a'.repeat(600), false)).toBeNull();
    expect(patternProblem('(?P<x>a)', true)).toBeNull();
  });
});

describe('timeRange', () => {
  const now = Date.parse('2026-03-01T12:00:00Z');
  it('turns presets into since', () => {
    expect(timeRange('15m', '', '', now)).toEqual({ since: '2026-03-01T11:45:00.000Z' });
    expect(timeRange('7d', '', '', now)).toEqual({ since: '2026-02-22T12:00:00.000Z' });
    expect(timeRange('all', '', '', now)).toEqual({});
  });
  it('reads custom local times, leaving empty ends open', () => {
    const r = timeRange('custom', '2026-03-01T02:30', '', now);
    expect(r.until).toBeUndefined();
    expect(new Date(r.since!).getTime()).toBe(new Date('2026-03-01T02:30').getTime());
    expect(timeRange('custom', 'nonsense', '2026-03-02T00:00', now).since).toBeUndefined();
  });
});
