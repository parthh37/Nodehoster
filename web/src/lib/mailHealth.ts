// Pure helpers for the mail deliverability report (Mail → Deliverability).

import type { MailCheck, MailCheckStatus, MailHealth } from '@/api/types';

// ---------------------------------------------------------------- domains and addresses

/** Same rule as the server (model.mailDomainRe). */
const DOMAIN_RE = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/;

/** Lower-cases a typed domain and drops surrounding space, a trailing dot and an "user@" prefix. */
export function normalizeDomain(v: string): string {
  let d = v.trim().toLowerCase();
  const at = d.lastIndexOf('@');
  if (at >= 0) d = d.slice(at + 1);
  return d.replace(/\.$/, '');
}

export function validateDomain(v: string): string | null {
  const d = normalizeDomain(v);
  return d.length <= 253 && DOMAIN_RE.test(d) ? null : 'Enter a domain name, e.g. example.com';
}

/**
 * Adds the domains typed in `input` (separated by spaces or commas) to
 * `current`, skipping duplicates. Nothing is added when any of them is invalid.
 */
export function addDomains(current: string[], input: string): { domains: string[]; error: string | null } {
  const parts = input
    .split(/[\s,;]+/)
    .map((s) => s.trim())
    .filter(Boolean);
  for (const p of parts) {
    const err = validateDomain(p);
    if (err) return { domains: current, error: parts.length > 1 ? `${p}: ${err}` : err };
  }
  const out = [...current];
  for (const p of parts) {
    const d = normalizeDomain(p);
    if (!out.includes(d)) out.push(d);
  }
  return { domains: out, error: null };
}

const IPV4_RE = /^((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)$/;

/** A single IPv4 or IPv6 address (no CIDR, no zone), as Go's net.ParseIP accepts. */
export function isIPAddress(v: string): boolean {
  const s = v.trim();
  if (IPV4_RE.test(s)) return true;
  if (!s.includes(':') || !/^[0-9a-fA-F:.]+$/.test(s)) return false;
  // Embedded IPv4 (::ffff:1.2.3.4) must itself be valid.
  const tail = s.slice(s.lastIndexOf(':') + 1);
  if (tail.includes('.') && !IPV4_RE.test(tail)) return false;
  try {
    new URL(`http://[${s}]/`);
    return true;
  } catch {
    return false;
  }
}

export function validatePublicIP(v: string): string | null {
  return !v.trim() || isIPAddress(v) ? null : 'Enter an IP address, e.g. 203.0.113.10';
}

// ---------------------------------------------------------------- results

export interface CheckCounts {
  fail: number;
  warn: number;
  pass: number;
  info: number;
  total: number;
}

const ORDER: MailCheckStatus[] = ['fail', 'warn', 'pass', 'info'];

export function countChecks(checks: readonly MailCheck[] | null | undefined): CheckCounts {
  const c: CheckCounts = { fail: 0, warn: 0, pass: 0, info: 0, total: 0 };
  for (const x of checks ?? []) {
    const s = (ORDER as string[]).includes(x.status) ? (x.status as MailCheckStatus) : 'info';
    c[s]++;
    c.total++;
  }
  return c;
}

/** Counts over the server checks and every domain's checks. */
export function summarizeHealth(h: MailHealth | null | undefined): CheckCounts {
  if (!h) return countChecks([]);
  return countChecks([...(h.server ?? []), ...(h.domains ?? []).flatMap((d) => d.checks ?? [])]);
}

/** The most serious status counted: fail > warn > pass > info. */
export function worstOfCounts(c: CheckCounts): MailCheckStatus {
  if (c.fail) return 'fail';
  if (c.warn) return 'warn';
  if (c.pass) return 'pass';
  return 'info';
}

/** The most serious status among the checks: fail > warn > pass > info. */
export function worstStatus(checks: readonly MailCheck[] | null | undefined): MailCheckStatus {
  return worstOfCounts(countChecks(checks));
}

/** "2 failed, 1 warning, 9 passed"; empty categories are left out. */
export function summaryText(c: CheckCounts): string {
  const parts: string[] = [];
  if (c.fail) parts.push(`${c.fail} failed`);
  if (c.warn) parts.push(`${c.warn} ${c.warn === 1 ? 'warning' : 'warnings'}`);
  if (c.pass) parts.push(`${c.pass} passed`);
  if (!parts.length) return c.total ? 'Nothing to judge' : 'No checks';
  return parts.join(', ');
}

// ---------------------------------------------------------------- fixes

export type FixView =
  | { kind: 'none' }
  /** A DNS record to publish: `value` goes at `name`. */
  | { kind: 'dns'; type: 'TXT' | 'A'; name: string; value: string }
  /** An instruction sentence. */
  | { kind: 'hint'; text: string };

/**
 * How to present a check's fix. With fixDns the fix is a record value to
 * publish there: an A record when it reads "A <ip>" (the value is then the
 * address alone), otherwise a TXT record. Without fixDns it is a sentence.
 */
export function fixView(c: Pick<MailCheck, 'fix' | 'fixDns'>): FixView {
  const fix = (c.fix ?? '').trim();
  if (!fix) return { kind: 'none' };
  const name = (c.fixDns ?? '').trim();
  if (!name) return { kind: 'hint', text: fix };
  if (fix.startsWith('A ')) return { kind: 'dns', type: 'A', name, value: fix.slice(2).trim() };
  return { kind: 'dns', type: 'TXT', name, value: fix };
}
