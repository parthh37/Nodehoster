// Web application firewall helpers: the site's Firewall tab, the settings
// card and the events page. Pure functions, mirrored from model/waf.go.

import type { WAFCategory, WAFConfig, WAFEvent, WAFExclusion, WAFMatch, WAFMode, WAFSettings } from '@/api/wafTypes';

export const WAF_MODES: { value: WAFMode; label: string; description: string }[] = [
  { value: 'off', label: 'Off', description: 'Requests are not inspected.' },
  { value: 'detect', label: 'Detect only', description: 'Requests that would be blocked are logged and let through.' },
  { value: 'block', label: 'Block', description: 'Requests that reach the anomaly threshold are refused with 403.' },
];

export const WAF_CATEGORIES: { value: WAFCategory; label: string }[] = [
  { value: 'sqli', label: 'SQL / NoSQL injection' },
  { value: 'xss', label: 'Cross-site scripting' },
  { value: 'lfi', label: 'Path traversal / file access' },
  { value: 'rfi', label: 'Remote file inclusion' },
  { value: 'rce', label: 'Command injection' },
  { value: 'nodejs', label: 'Node.js / JavaScript' },
  { value: 'php', label: 'PHP' },
  { value: 'java', label: 'Java (Log4Shell, OGNL)' },
  { value: 'scanner', label: 'Scanners' },
  { value: 'protocol', label: 'Protocol violations' },
];

export const PARANOIA_LEVELS = [
  { value: 1, label: '1 — Standard', description: 'Clear attacks only; no exclusions needed for ordinary sites.' },
  { value: 2, label: '2 — Strict', description: 'Catches more obfuscated attacks; expect a few exclusions.' },
  { value: 3, label: '3 — Paranoid', description: 'Flags anything suspicious (markup, SQL keywords); for high-value sites with tuning.' },
];

export function categoryLabel(c: string): string {
  return WAF_CATEGORIES.find((x) => x.value === c)?.label ?? c;
}

export function modeLabel(m: string | undefined): string {
  return WAF_MODES.find((x) => x.value === (m || 'off'))?.label ?? m ?? 'Off';
}

/** The values the server uses when fields are absent (WAFConfig.Paranoia and friends). */
export function effectiveWAF(cfg: WAFConfig | undefined): { mode: WAFMode; paranoia: number; threshold: number; bodyKB: number } {
  const c = cfg ?? {};
  const mode: WAFMode = c.mode === 'detect' || c.mode === 'block' ? c.mode : 'off';
  return { mode, paranoia: c.paranoiaLevel || 1, threshold: c.anomalyThreshold || 5, bodyKB: c.inspectBodyKB || 128 };
}

/** Mirror of model.DefaultWAF. */
export function defaultWAFSettings(): WAFSettings {
  return { defaultMode: 'detect', defaultParanoiaLevel: 1, defaultAnomalyThreshold: 5, eventRetentionDays: 30 };
}

/** Mirror of model.WAFSettings.ApplyDefaults. */
export function normalizeWAFSettings(s: Partial<WAFSettings> | null | undefined): WAFSettings {
  const d = defaultWAFSettings();
  if (!s || Object.values(s).every((v) => !v)) return d;
  return {
    defaultMode: s.defaultMode || 'off',
    defaultParanoiaLevel: s.defaultParanoiaLevel && s.defaultParanoiaLevel > 0 ? s.defaultParanoiaLevel : d.defaultParanoiaLevel,
    defaultAnomalyThreshold: s.defaultAnomalyThreshold && s.defaultAnomalyThreshold > 0 ? s.defaultAnomalyThreshold : d.defaultAnomalyThreshold,
    eventRetentionDays: s.eventRetentionDays && s.eventRetentionDays > 0 ? s.eventRetentionDays : d.eventRetentionDays,
  };
}

/** "argument q", "cookie session", "the path" — where a rule matched. */
export function matchWhere(m: Pick<WAFMatch, 'in' | 'name'>): string {
  switch (m.in) {
    case 'path':
      return 'the path';
    case 'arg':
      return `argument ${m.name ?? ''}`.trim();
    case 'argName':
      return `argument name ${m.name ?? ''}`.trim();
    case 'cookie':
      return `cookie ${m.name ?? ''}`.trim();
    case 'header':
      return `header ${m.name ?? ''}`.trim();
    case 'file':
      return 'an uploaded file name';
    case 'body':
      return 'the body';
    case 'query':
      return 'the query string';
    default:
      return 'the request';
  }
}

/**
 * The narrowest exclusion that would have let an event's request through:
 * its rules, under its path, and — when every match was in a named
 * argument, cookie or header — only for those.
 */
export function suggestExclusion(ev: Pick<WAFEvent, 'id' | 'path' | 'matches'>): WAFExclusion {
  const ruleIds = [...new Set(ev.matches.map((m) => m.ruleId))].sort((a, b) => a - b);
  const x: WAFExclusion = { path: ev.path || undefined, ruleIds, comment: `From request ${ev.id}` };
  const named = ev.matches.length > 0 && ev.matches.every((m) => !!m.name && ['arg', 'argName', 'file', 'cookie', 'header'].includes(m.in));
  if (named) {
    const pick = (kinds: string[]) => [...new Set(ev.matches.filter((m) => kinds.includes(m.in)).map((m) => m.name as string))];
    const args = pick(['arg', 'argName', 'file']);
    const cookies = pick(['cookie']);
    const headers = pick(['header']);
    if (args.length) x.args = args;
    if (cookies.length) x.cookies = cookies;
    if (headers.length) x.headers = headers;
  }
  return x;
}

/** A path prefix one level up ("/admin/posts/12" -> "/admin/posts/"), for widening an exclusion. */
export function parentPath(path: string): string {
  const p = path.replace(/\/+$/, '');
  const i = p.lastIndexOf('/');
  return i <= 0 ? '/' : p.slice(0, i + 1);
}

/** An exclusion in a few words, like the audit log's. */
export function describeExclusion(x: WAFExclusion): string {
  const parts = [...(x.ruleIds ?? []).map((id) => `rule ${id}`), ...(x.categories ?? []).map(categoryLabel)];
  let what = parts.length ? parts.join(', ') : 'all rules';
  const targets = [
    ...(x.args ?? []).map((n) => `argument ${n}`),
    ...(x.cookies ?? []).map((n) => `cookie ${n}`),
    ...(x.headers ?? []).map((n) => `header ${n}`),
  ];
  if (targets.length) what += ` for ${targets.join(', ')}`;
  else if (!parts.length) what = 'Firewall off';
  return `${what}${x.path ? ` under ${x.path}` : ' on the whole site'}`;
}

/** Client-side mirror of WAFExclusion.validate; null when valid. */
export function exclusionError(x: WAFExclusion): string | null {
  if (x.path && !x.path.startsWith('/')) return 'The path must start with /';
  if ((x.ruleIds ?? []).some((id) => !Number.isInteger(id) || id <= 0)) return 'Rule IDs are positive numbers';
  const names = [...(x.args ?? []), ...(x.cookies ?? []), ...(x.headers ?? [])];
  if (names.some((n) => !n.trim() || n.trim() === '*')) return 'Enter names (a trailing * matches a prefix)';
  const listed = (x.ruleIds?.length ?? 0) + (x.categories?.length ?? 0) + names.length;
  if (!x.path && listed === 0) return 'List rules, categories or names — or give a path under which the firewall is off';
  return null;
}

/** "942100, 941110 941120" -> [942100, 941110, 941120]; null when a part is not a number. */
export function parseRuleIds(text: string): number[] | null {
  const parts = text.split(/[\s,;]+/).filter(Boolean);
  const ids = parts.map((p) => Number(p));
  if (ids.some((n) => !Number.isInteger(n) || n <= 0)) return null;
  return [...new Set(ids)];
}

/** Comma- or line-separated names, trimmed, without blanks or duplicates. */
export function parseNames(text: string): string[] {
  return [...new Set(text.split(/[\n,]+/).map((s) => s.trim()).filter(Boolean))];
}

/** Colour of a severity badge. */
export function severityTone(sev: string): 'danger' | 'warning' | 'neutral' {
  if (sev === 'critical' || sev === 'error') return 'danger';
  if (sev === 'warning') return 'warning';
  return 'neutral';
}
