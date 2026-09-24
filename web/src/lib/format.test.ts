import { describe, expect, it } from 'vitest';
import {
  daysUntil,
  durationBetween,
  formatBytes,
  formatCompact,
  formatDate,
  formatDateTime,
  formatDuration,
  formatHourMinute,
  formatMs,
  formatNumber,
  formatPercent,
  formatSpan,
  formatTime,
  formatUptime,
  parseDate,
  pluralize,
  relativeTime,
  secondsSince,
} from './format';

// Number/date output depends on the runtime's default locale, so expectations that involve
// separators are derived from Intl with the same options instead of being hard-coded.
const n0 = (n: number) => new Intl.NumberFormat().format(n);
const n1 = (n: number) => new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 }).format(n);
const n2 = (n: number) => new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(n);
const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });

const NOW = Date.parse('2026-06-15T12:00:00Z');
const iso = (offsetSec: number) => new Date(NOW + offsetSec * 1000).toISOString();

const MISSING = [undefined, null, NaN, Infinity, -Infinity];

describe('formatNumber', () => {
  it.each(MISSING)('renders %s as a dash', (v) => expect(formatNumber(v)).toBe('—'));

  it('rounds to an integer by default', () => {
    expect(formatNumber(1234.6)).toBe(n0(1235));
    expect(formatNumber(0.4)).toBe('0');
    expect(formatNumber(-2.5)).toBe(n0(Math.round(-2.5)));
  });

  it('supports 1 and 2 fraction digits', () => {
    expect(formatNumber(1.26, 1)).toBe(n1(1.26));
    expect(formatNumber(1.256, 2)).toBe(n2(1.256));
    expect(formatNumber(3, 2)).toBe('3');
  });
});

describe('formatCompact', () => {
  it.each(MISSING)('renders %s as a dash', (v) => expect(formatCompact(v)).toBe('—'));

  it('leaves numbers under 1000 unscaled', () => {
    expect(formatCompact(0)).toBe('0');
    expect(formatCompact(999)).toBe('999');
    expect(formatCompact(12.34)).toBe(n1(12.3));
  });

  it('uses k, M and B suffixes', () => {
    expect(formatCompact(1000)).toBe('1k');
    expect(formatCompact(1500)).toBe(`${n1(1.5)}k`);
    expect(formatCompact(1_234_567)).toBe(`${n1(1.2)}M`);
    expect(formatCompact(2_500_000_000)).toBe(`${n1(2.5)}B`);
    expect(formatCompact(7e12)).toBe(`${n0(7000)}B`);
  });

  it('scales negative numbers by magnitude', () => {
    expect(formatCompact(-1500)).toBe(`${n1(-1.5)}k`);
    expect(formatCompact(-2_000_000)).toBe(`${n1(-2)}M`);
  });
});

describe('formatBytes', () => {
  it.each(MISSING)('renders %s as a dash', (v) => expect(formatBytes(v)).toBe('—'));

  it('renders zero without a fraction', () => {
    expect(formatBytes(0)).toBe('0 B');
  });

  it('shows whole bytes below 1 KB', () => {
    expect(formatBytes(1)).toBe('1 B');
    expect(formatBytes(1023)).toBe('1023 B');
  });

  it('uses binary (1024) units', () => {
    expect(formatBytes(1024)).toBe('1.0 KB');
    expect(formatBytes(1536)).toBe('1.5 KB');
    expect(formatBytes(1024 ** 2)).toBe('1.0 MB');
    expect(formatBytes(1.25 * 1024 ** 3)).toBe('1.3 GB');
    expect(formatBytes(1024 ** 4)).toBe('1.0 TB');
    expect(formatBytes(1024 ** 5)).toBe('1.0 PB');
  });

  it('drops fractions once the value reaches 100 of a unit', () => {
    expect(formatBytes(99.5 * 1024)).toBe('99.5 KB');
    expect(formatBytes(100 * 1024)).toBe('100 KB');
    expect(formatBytes(512.4 * 1024 ** 2)).toBe('512 MB');
  });

  it('caps at PB for huge values', () => {
    expect(formatBytes(2048 * 1024 ** 5)).toBe('2048 PB');
  });

  it('honors the digits argument', () => {
    expect(formatBytes(1536, 0)).toBe('2 KB');
    expect(formatBytes(1536, 2)).toBe('1.50 KB');
  });

  it('formats negative values by magnitude', () => {
    expect(formatBytes(-2048)).toBe('-2.0 KB');
  });

  // BUG: for 0 < |bytes| < 1 the unit index is negative (format.ts:27), producing e.g.
  // "512 undefined". Fractional byte values reach this via chart axis ticks (OverviewTab AreaChart).
  it.skip('handles fractional byte values below 1', () => {
    expect(formatBytes(0.5)).toBe('1 B');
  });
});

describe('formatPercent', () => {
  it.each(MISSING)('renders %s as a dash', (v) => expect(formatPercent(v)).toBe('—'));

  it('uses one fraction digit by default', () => {
    expect(formatPercent(12.345)).toBe('12.3%');
    expect(formatPercent(0)).toBe('0.0%');
    expect(formatPercent(100, 0)).toBe('100%');
    expect(formatPercent(33.3333, 2)).toBe('33.33%');
  });
});

describe('formatMs', () => {
  it.each(MISSING)('renders %s as a dash', (v) => expect(formatMs(v)).toBe('—'));

  it('picks precision by magnitude', () => {
    expect(formatMs(0)).toBe('0 ms');
    expect(formatMs(0.123)).toBe('0.12 ms');
    expect(formatMs(5.24)).toBe('5.2 ms');
    expect(formatMs(9.94)).toBe('9.9 ms');
    expect(formatMs(10)).toBe('10 ms');
    expect(formatMs(250.4)).toBe('250 ms');
    expect(formatMs(999)).toBe('999 ms');
  });

  it('switches to seconds from 1000 ms', () => {
    expect(formatMs(1000)).toBe('1.00 s');
    expect(formatMs(1500)).toBe('1.50 s');
    expect(formatMs(65_432)).toBe('65.43 s');
  });
});

describe('formatDuration', () => {
  it.each(MISSING)('renders %s as a dash', (v) => expect(formatDuration(v)).toBe('—'));

  it.each([
    [0, '0s'],
    [0.9, '0s'],
    [12, '12s'],
    [59, '59s'],
    [60, '1m 0s'],
    [250, '4m 10s'],
    [3599, '59m 59s'],
    [3600, '1h 0m'],
    [5 * 3600 + 12 * 60 + 59, '5h 12m'],
    [86399, '23h 59m'],
    [86400, '1d 0h'],
    [3 * 86400 + 4 * 3600 + 3599, '3d 4h'],
    [400 * 86400, '400d 0h'],
  ])('formats %s seconds as %s', (sec, out) => expect(formatDuration(sec)).toBe(out));

  it('clamps negative durations to zero', () => {
    expect(formatDuration(-30)).toBe('0s');
  });
});

describe('parseDate', () => {
  it('returns null for empty and invalid input', () => {
    expect(parseDate(undefined)).toBeNull();
    expect(parseDate(null)).toBeNull();
    expect(parseDate('')).toBeNull();
    expect(parseDate('not a date')).toBeNull();
  });

  it("treats Go's zero time and the Unix epoch as unset", () => {
    expect(parseDate('0001-01-01T00:00:00Z')).toBeNull();
    expect(parseDate('1970-01-01T00:00:00Z')).toBeNull();
  });

  it('parses RFC 3339 timestamps, including fractional seconds and offsets', () => {
    expect(parseDate('2026-06-15T12:00:00Z')?.getTime()).toBe(NOW);
    expect(parseDate('2026-06-15T14:00:00.123456789+02:00')?.getTime()).toBe(NOW + 123);
  });
});

describe('secondsSince / formatUptime / daysUntil', () => {
  it('measures seconds relative to the given clock', () => {
    expect(secondsSince(iso(-90), NOW)).toBe(90);
    expect(secondsSince(iso(30), NOW)).toBe(-30);
    expect(secondsSince('', NOW)).toBeNull();
    expect(secondsSince('0001-01-01T00:00:00Z', NOW)).toBeNull();
  });

  it('formats uptime as a compact duration', () => {
    expect(formatUptime(iso(-(2 * 3600 + 5 * 60)), NOW)).toBe('2h 5m');
    expect(formatUptime(null, NOW)).toBe('—');
  });

  it('counts whole days, flooring toward the past', () => {
    expect(daysUntil(iso(10 * 86400 + 3600), NOW)).toBe(10);
    expect(daysUntil(iso(86400 - 1), NOW)).toBe(0);
    expect(daysUntil(iso(-1), NOW)).toBe(-1);
    expect(daysUntil(iso(-3 * 86400), NOW)).toBe(-3);
    expect(daysUntil(undefined, NOW)).toBeNull();
  });
});

describe('relativeTime', () => {
  it('returns a dash for missing dates', () => {
    expect(relativeTime(null, NOW)).toBe('—');
    expect(relativeTime('0001-01-01T00:00:00Z', NOW)).toBe('—');
  });

  it('says "just now" for the recent past and "in a few seconds" for the near future', () => {
    expect(relativeTime(iso(0), NOW)).toBe('just now');
    expect(relativeTime(iso(-44), NOW)).toBe('just now');
    expect(relativeTime(iso(10), NOW)).toBe('in a few seconds');
  });

  it('picks minute, hour, day, month and year units', () => {
    expect(relativeTime(iso(-45), NOW)).toBe(rtf.format(-1, 'minute'));
    expect(relativeTime(iso(-5 * 60), NOW)).toBe(rtf.format(-5, 'minute'));
    expect(relativeTime(iso(2 * 3600), NOW)).toBe(rtf.format(2, 'hour'));
    expect(relativeTime(iso(-3 * 86400), NOW)).toBe(rtf.format(-3, 'day'));
    expect(relativeTime(iso(-60 * 86400), NOW)).toBe(rtf.format(-2, 'month'));
    expect(relativeTime(iso(-2 * 365 * 86400), NOW)).toBe(rtf.format(-2, 'year'));
  });
});

describe('date/time formatting', () => {
  const d = new Date(NOW);

  it('returns a dash (or empty for hour:minute) when the date is missing', () => {
    expect(formatDateTime(null)).toBe('—');
    expect(formatDate('bogus')).toBe('—');
    expect(formatTime(undefined)).toBe('—');
    expect(formatHourMinute(null)).toBe('');
  });

  it('formats valid timestamps with the locale formatter', () => {
    const dtf = new Intl.DateTimeFormat(undefined, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    const df = new Intl.DateTimeFormat(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
    expect(formatDateTime(d.toISOString())).toBe(dtf.format(d));
    expect(formatDate(d.toISOString())).toBe(df.format(d));
  });

  it('accepts Date objects as well as strings for time formatting', () => {
    expect(formatTime(d)).toBe(formatTime(d.toISOString()));
    expect(formatHourMinute(d)).toBe(formatHourMinute(d.toISOString()));
    expect(formatTime(d)).not.toBe('—');
  });
});

describe('pluralize', () => {
  it('uses the singular form only for exactly one', () => {
    expect(pluralize(1, 'site')).toBe('1 site');
    expect(pluralize(0, 'site')).toBe('0 sites');
    expect(pluralize(2, 'site')).toBe('2 sites');
  });

  it('accepts an irregular plural', () => {
    expect(pluralize(3, 'entry', 'entries')).toBe('3 entries');
    expect(pluralize(1, 'entry', 'entries')).toBe('1 entry');
  });

  it('formats the count', () => {
    expect(pluralize(12345, 'request')).toBe(`${n0(12345)} requests`);
  });
});

describe('durationBetween', () => {
  it('returns a dash without a valid start', () => {
    expect(durationBetween(null, iso(0), NOW)).toBe('—');
    expect(durationBetween('0001-01-01T00:00:00Z', iso(0), NOW)).toBe('—');
  });

  it('measures between start and end', () => {
    expect(durationBetween(iso(-600), iso(-300), NOW)).toBe('5m 0s');
  });

  it('measures up to now when the end is missing or zero', () => {
    expect(durationBetween(iso(-75), null, NOW)).toBe('1m 15s');
    expect(durationBetween(iso(-75), '0001-01-01T00:00:00Z', NOW)).toBe('1m 15s');
  });
});

describe('formatSpan', () => {
  it.each(MISSING)('renders %s as a dash', (v) => expect(formatSpan(v)).toBe('—'));

  it('spells out every non-zero unit', () => {
    expect(formatSpan(0)).toBe('0 seconds');
    expect(formatSpan(1)).toBe('1 second');
    expect(formatSpan(45)).toBe('45 seconds');
    expect(formatSpan(60)).toBe('1 minute');
    expect(formatSpan(5400)).toBe('1 hour 30 minutes');
    expect(formatSpan(3600)).toBe('1 hour');
    expect(formatSpan(604800)).toBe('7 days');
    expect(formatSpan(90061)).toBe('1 day 1 hour 1 minute 1 second');
  });

  it('floors fractions and clamps negatives', () => {
    expect(formatSpan(59.9)).toBe('59 seconds');
    expect(formatSpan(-5)).toBe('0 seconds');
  });
});
