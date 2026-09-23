// Pure helpers for log search: highlighting matches and turning the time
// range picker's choice into the API's since/until.

export interface Segment {
  text: string;
  match: boolean;
}

/** Longest pattern the server accepts. */
export const MAX_PATTERN = 512;

/**
 * Splits text into matching and non-matching segments. A plain query
 * matches case-insensitively; a regex is used as given (RE2 syntax is
 * close enough to JavaScript's for highlighting; a pattern JavaScript
 * cannot compile is simply not highlighted).
 */
export function highlight(text: string, query: string, regex = false): Segment[] {
  if (!query) return [{ text, match: false }];
  let re: RegExp;
  try {
    const ci = /^\(\?[msU]*i[msU]*\)/.test(query);
    re = regex ? new RegExp(stripInlineFlags(query), ci ? 'gi' : 'g') : new RegExp(escapeRegExp(query), 'gi');
  } catch {
    return [{ text, match: false }];
  }
  const out: Segment[] = [];
  let last = 0;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    if (m[0] === '') {
      re.lastIndex++; // zero-width match: move on
      if (re.lastIndex > text.length) break;
      continue;
    }
    if (m.index > last) out.push({ text: text.slice(last, m.index), match: false });
    out.push({ text: m[0], match: true });
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push({ text: text.slice(last), match: false });
  return out.length ? out : [{ text, match: false }];
}

/** Go's (?i) and friends are not JavaScript syntax. */
function stripInlineFlags(p: string): string {
  return p.replace(/^\(\?[imsU]+\)/, '');
}

export function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/**
 * A problem the browser can see in a pattern before asking the server, or
 * null. Only the length: the server's RE2 syntax differs from
 * JavaScript's (it accepts (?P<name>…), for one), so it has the last word.
 */
export function patternProblem(q: string, regex: boolean): string | null {
  if (regex && q.length > MAX_PATTERN) return `At most ${MAX_PATTERN} characters`;
  return null;
}

export type RangePreset = '15m' | '1h' | '24h' | '7d' | 'all' | 'custom';

export const RANGE_PRESETS: { value: RangePreset; label: string }[] = [
  { value: '15m', label: 'Last 15 minutes' },
  { value: '1h', label: 'Last hour' },
  { value: '24h', label: 'Last 24 hours' },
  { value: '7d', label: 'Last 7 days' },
  { value: 'all', label: 'All time' },
  { value: 'custom', label: 'Custom range…' },
];

const PRESET_MS: Record<string, number> = { '15m': 15 * 60_000, '1h': 3_600_000, '24h': 86_400_000, '7d': 7 * 86_400_000 };

/**
 * since/until (RFC 3339) for a preset, or for a custom range given as
 * datetime-local values ("2026-03-01T02:30", local time). Empty or
 * invalid ends are left open.
 */
export function timeRange(preset: RangePreset, from = '', to = '', now = Date.now()): { since?: string; until?: string } {
  if (preset === 'all') return {};
  if (preset !== 'custom') return { since: new Date(now - PRESET_MS[preset]).toISOString() };
  const out: { since?: string; until?: string } = {};
  const f = parseLocal(from);
  const t = parseLocal(to);
  if (f) out.since = f.toISOString();
  if (t) out.until = t.toISOString();
  return out;
}

function parseLocal(v: string): Date | null {
  if (!v) return null;
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? null : d;
}
