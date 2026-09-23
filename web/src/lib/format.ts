// Formatting helpers for bytes, durations, dates and numbers.

const nf = new Intl.NumberFormat();
const nf1 = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 });
const nf2 = new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 });

export function formatNumber(n: number | undefined | null, digits = 0): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return '—';
  if (digits === 0) return nf.format(Math.round(n));
  return digits === 1 ? nf1.format(n) : nf2.format(n);
}

/** 1234567 -> "1.2M" */
export function formatCompact(n: number | undefined | null): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return '—';
  const abs = Math.abs(n);
  if (abs < 1000) return nf1.format(n);
  if (abs < 1e6) return `${nf1.format(n / 1e3)}k`;
  if (abs < 1e9) return `${nf1.format(n / 1e6)}M`;
  return `${nf1.format(n / 1e9)}B`;
}

export function formatBytes(bytes: number | undefined | null, digits = 1): string {
  if (bytes === undefined || bytes === null || !Number.isFinite(bytes)) return '—';
  if (bytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  const i = Math.min(units.length - 1, Math.floor(Math.log(Math.abs(bytes)) / Math.log(1024)));
  const v = bytes / Math.pow(1024, i);
  return `${v.toFixed(i === 0 ? 0 : v >= 100 ? 0 : digits)} ${units[i]}`;
}

export function formatPercent(p: number | undefined | null, digits = 1): string {
  if (p === undefined || p === null || !Number.isFinite(p)) return '—';
  return `${p.toFixed(digits)}%`;
}

export function formatMs(ms: number | undefined | null): string {
  if (ms === undefined || ms === null || !Number.isFinite(ms)) return '—';
  if (ms === 0) return '0 ms';
  if (ms < 1) return `${ms.toFixed(2)} ms`;
  if (ms < 1000) return `${ms < 10 ? ms.toFixed(1) : Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(2)} s`;
}

/** Compact duration: 3d 4h, 5h 12m, 4m 10s, 12s */
export function formatDuration(totalSeconds: number | undefined | null): string {
  if (totalSeconds === undefined || totalSeconds === null || !Number.isFinite(totalSeconds)) return '—';
  let s = Math.max(0, Math.floor(totalSeconds));
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

export function parseDate(v: string | null | undefined): Date | null {
  if (!v) return null;
  const d = new Date(v);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return null;
  return d;
}

export function secondsSince(v: string | null | undefined, now = Date.now()): number | null {
  const d = parseDate(v);
  return d ? (now - d.getTime()) / 1000 : null;
}

export function formatUptime(since: string | null | undefined, now = Date.now()): string {
  const s = secondsSince(since, now);
  return s === null ? '—' : formatDuration(s);
}

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });

export function relativeTime(v: string | null | undefined, now = Date.now()): string {
  const d = parseDate(v);
  if (!d) return '—';
  const diff = (d.getTime() - now) / 1000;
  const abs = Math.abs(diff);
  if (abs < 45) return diff <= 0 ? 'just now' : 'in a few seconds';
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute');
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour');
  if (abs < 86400 * 30) return rtf.format(Math.round(diff / 86400), 'day');
  if (abs < 86400 * 365) return rtf.format(Math.round(diff / (86400 * 30)), 'month');
  return rtf.format(Math.round(diff / (86400 * 365)), 'year');
}

const dtf = new Intl.DateTimeFormat(undefined, {
  year: 'numeric',
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
});
const df = new Intl.DateTimeFormat(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
const tf = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
const hm = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', hour12: false });

export function formatDateTime(v: string | null | undefined): string {
  const d = parseDate(v);
  return d ? dtf.format(d) : '—';
}

export function formatDate(v: string | null | undefined): string {
  const d = parseDate(v);
  return d ? df.format(d) : '—';
}

export function formatTime(v: string | Date | null | undefined): string {
  const d = v instanceof Date ? v : parseDate(v);
  return d ? tf.format(d) : '—';
}

export function formatHourMinute(v: string | Date | null | undefined): string {
  const d = v instanceof Date ? v : parseDate(v);
  return d ? hm.format(d) : '';
}

/** Whole days until a date (negative when in the past). */
export function daysUntil(v: string | null | undefined, now = Date.now()): number | null {
  const d = parseDate(v);
  if (!d) return null;
  return Math.floor((d.getTime() - now) / 86400000);
}

export function pluralize(n: number, one: string, many = `${one}s`): string {
  return `${formatNumber(n)} ${n === 1 ? one : many}`;
}

export function durationBetween(start: string | null | undefined, end: string | null | undefined, now = Date.now()): string {
  const s = parseDate(start);
  if (!s) return '—';
  const e = parseDate(end);
  return formatDuration(((e ? e.getTime() : now) - s.getTime()) / 1000);
}
