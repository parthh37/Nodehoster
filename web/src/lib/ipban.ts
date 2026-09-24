// Helpers for automatic IP banning: the Security settings tab and the
// banned addresses list.

import type { Ban } from '@/api/types';

const IPV4 = /^(\d{1,3})(\.\d{1,3}){3}$/;

/** Validation for a manual ban: an IP address or a CIDR range; null when valid. */
export function banAddressError(v: string): string | null {
  const s = v.trim();
  if (!s) return 'Enter an IP address or a range';
  const [ip, bits, extra] = s.split('/');
  if (extra !== undefined) return 'Not an address or range';
  const v4 = IPV4.test(ip) && ip.split('.').every((o) => Number(o) <= 255);
  const v6 = !v4 && ip.includes(':') && /^[0-9a-fA-F:.]+$/.test(ip);
  if (!v4 && !v6) return 'Not an IP address';
  if (bits === undefined) return null;
  if (!/^\d{1,3}$/.test(bits)) return 'Not a prefix length';
  const n = Number(bits);
  if (v4 && (n < 8 || n > 32)) return 'Use a prefix between /8 and /32';
  if (v6 && (n < 32 || n > 128)) return 'Use a prefix between /32 and /128';
  return null;
}

/** "in 12 min", "in 3 h", "until removed", "expired". */
export function banRemaining(b: Pick<Ban, 'expiresAt'>, now: number): string {
  if (!b.expiresAt) return 'until removed';
  const ms = Date.parse(b.expiresAt) - now;
  if (!(ms > 0)) return 'expired';
  const min = Math.ceil(ms / 60_000);
  if (min < 60) return `in ${min} min`;
  const h = Math.round(min / 60);
  if (h < 48) return `in ${h} h`;
  return `in ${Math.round(h / 24)} days`;
}

/** The length of the n-th automatic ban, as the server escalates it. */
export function banLengthMinutes(strike: number, first: number, max: number): number {
  return Math.min(first * 2 ** Math.max(0, strike - 1), max);
}
