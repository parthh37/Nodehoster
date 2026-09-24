// Helpers for client certificates (mutual TLS) on HTTPS bindings, OCSP
// stapling status of certificates and HTTP/3.

import type { ClientCertPolicy, OCSPStatus } from '@/api/types';

export type ClientCertMode = 'ignore' | 'accept' | 'require';

export function clientCertMode(p: ClientCertPolicy | null | undefined): ClientCertMode {
  const m = (p?.mode ?? '').trim().toLowerCase();
  return m === 'accept' || m === 'require' ? m : 'ignore';
}

/** The policy with another mode; switching to ignore keeps what was entered. */
export function withClientCertMode(p: ClientCertPolicy | null | undefined, mode: ClientCertMode): ClientCertPolicy | null {
  if (mode === 'ignore' && !p) return null;
  return { ...(p ?? {}), mode };
}

export interface PemSummary {
  /** Certificates in the bundle. */
  count: number;
  /** Why the server would refuse it; null when it looks right. */
  error: string | null;
}

const BLOCK = /-----BEGIN ([A-Z0-9 ]+)-----([\s\S]*?)-----END \1-----/g;

/** Reads a PEM bundle the way the server checks it (without parsing X.509). */
export function pemSummary(pem: string | undefined | null): PemSummary {
  const text = (pem ?? '').trim();
  if (!text) return { count: 0, error: 'Add the certificate authorities that issue the client certificates (PEM).' };
  if (text.length > 256 * 1024) return { count: 0, error: 'The bundle is larger than 256 KB.' };
  let count = 0;
  let rest = text;
  for (const m of text.matchAll(BLOCK)) {
    if (m[1] !== 'CERTIFICATE') {
      return { count, error: `The bundle contains a ${m[1].toLowerCase()}; only certificates belong here.` };
    }
    if (!/^[A-Za-z0-9+/=\s]+$/.test(m[2]) || !m[2].trim()) {
      return { count, error: `Certificate ${count + 1} is not valid PEM.` };
    }
    count++;
    rest = rest.replace(m[0], '');
  }
  if (count === 0) return { count, error: 'No PEM certificate found (-----BEGIN CERTIFICATE-----).' };
  if (rest.trim()) return { count, error: 'Text after the last certificate is not PEM.' };
  if (count > 100) return { count, error: 'At most 100 certificates.' };
  return { count, error: null };
}

/** A SHA-256 fingerprint as the server stores it: upper-case hex, no separators. */
export function normalizeFingerprint(f: string): string {
  return f.trim().replace(/[:\s-]/g, '').toUpperCase();
}

export function fingerprintError(f: string): string | null {
  return /^[0-9A-F]{64}$/.test(normalizeFingerprint(f)) ? null : 'A SHA-256 fingerprint is 64 hex digits';
}

/** Mirrors the server's check of requirePaths entries (model.validRequirePath). */
export function pathError(p: string): string | null {
  const path = p.trim();
  if (!path.startsWith('/')) return 'Must start with /';
  if (/[\\%?#;:\u0000-\u001f\u007f]/.test(path)) return 'Must be a plain path, without \\ % ? # ; : or control characters';
  if (path.split('/').some((seg) => seg !== '' && seg.replace(/[. ]+$/, '') === '')) return 'Must not contain . or .. segments';
  return null;
}

/** "Required · 2 CAs · 3 allowed" for a binding's summary; "" when ignored. */
export function clientCertSummary(p: ClientCertPolicy | null | undefined): string {
  const mode = clientCertMode(p);
  if (mode === 'ignore') return '';
  const parts = [mode === 'require' ? 'Client certificate required' : 'Client certificate accepted'];
  const { count } = pemSummary(p?.caPem);
  if (count) parts.push(`${count} CA${count === 1 ? '' : 's'}`);
  const allowed = (p?.allowedSubjects?.length ?? 0) + (p?.allowedFingerprints?.length ?? 0);
  if (allowed) parts.push(`${allowed} allowed`);
  const paths = p?.requirePaths?.length ?? 0;
  if (mode === 'accept' && paths) parts.push(`required on ${paths} path${paths === 1 ? '' : 's'}`);
  return parts.join(' · ');
}

export type OcspTone = 'gray' | 'green' | 'amber' | 'red' | 'blue';

export interface OcspBadgeInfo {
  label: string;
  tone: OcspTone;
  /** A sentence for a tooltip. */
  detail: string;
}

function day(iso: string | undefined): string {
  return iso ? iso.slice(0, 16).replace('T', ' ') : '';
}

/** How a certificate's OCSP stapling is shown in lists. */
export function ocspBadge(s: OCSPStatus | undefined | null): OcspBadgeInfo | null {
  if (!s) return null;
  const mustStaple = s.mustStaple && !s.stapled ? ' The certificate is Must-Staple: browsers may refuse it without a staple.' : '';
  switch (s.state) {
    case 'none':
      return { label: 'none', tone: 'gray', detail: 'The certificate names no OCSP responder (as Let’s Encrypt’s since 2025): nothing to staple.' };
    case 'pending':
      return { label: 'pending', tone: 'blue', detail: 'The OCSP responder has not been asked yet.' };
    case 'good':
      return s.stapled
        ? { label: 'stapled', tone: 'green', detail: `A good OCSP response is stapled to handshakes${s.nextUpdate ? `, valid until ${day(s.nextUpdate)}` : ''}.` }
        : { label: 'good', tone: 'amber', detail: 'The responder says good, but no response is stapled.' + mustStaple };
    case 'revoked':
      return {
        label: 'revoked',
        tone: 'red',
        detail: `The CA revoked this certificate${s.revokedAt ? ` on ${day(s.revokedAt)}` : ''}${s.revocationReason ? ` (${s.revocationReason})` : ''}. Replace it.`,
      };
    case 'unknown':
      return { label: 'unknown', tone: 'amber', detail: 'The OCSP responder does not know this certificate.' + mustStaple };
    default:
      return { label: 'error', tone: s.mustStaple ? 'red' : 'amber', detail: (s.lastError || 'No valid OCSP response could be obtained.') + mustStaple };
  }
}
