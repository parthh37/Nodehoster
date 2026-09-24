import { describe, expect, it } from 'vitest';
import {
  clientCertMode,
  clientCertSummary,
  fingerprintError,
  normalizeFingerprint,
  ocspBadge,
  pathError,
  pemSummary,
  withClientCertMode,
} from './tls';

const CERT = '-----BEGIN CERTIFICATE-----\nMIIBdzCCAR2gAwIBAgIBATAKBggqhkjOPQQDAjAS\nMRAwDgYDVQQDEwdUZXN0IENB\n-----END CERTIFICATE-----';

describe('clientCertMode', () => {
  it('reads absent and odd values as ignore', () => {
    expect(clientCertMode(undefined)).toBe('ignore');
    expect(clientCertMode(null)).toBe('ignore');
    expect(clientCertMode({ mode: '' })).toBe('ignore');
    expect(clientCertMode({ mode: ' Require ' })).toBe('require');
    expect(clientCertMode({ mode: 'accept' })).toBe('accept');
  });
  it('switching off keeps what was entered, and nothing stays nothing', () => {
    expect(withClientCertMode(undefined, 'ignore')).toBeNull();
    expect(withClientCertMode({ mode: 'require', caPem: CERT }, 'ignore')).toEqual({ mode: 'ignore', caPem: CERT });
    expect(withClientCertMode(null, 'accept')).toEqual({ mode: 'accept' });
  });
});

describe('pemSummary', () => {
  it('counts certificates', () => {
    expect(pemSummary(CERT)).toEqual({ count: 1, error: null });
    expect(pemSummary(`${CERT}\n\n${CERT}\n`)).toEqual({ count: 2, error: null });
  });
  it.each([
    ['', 'Add the certificate'],
    ['hello', 'No PEM certificate'],
    [`${CERT}\n-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----`, 'private key'],
    [`${CERT}\ntrailing`, 'Text after'],
    ['-----BEGIN CERTIFICATE-----\n!!!\n-----END CERTIFICATE-----', 'not valid PEM'],
  ])('refuses %j', (pem, msg) => {
    expect(pemSummary(pem).error).toContain(msg);
  });
});

describe('fingerprints and paths', () => {
  const fp = 'ab:'.repeat(31) + 'ab';
  it('normalizes like the server', () => {
    expect(normalizeFingerprint(` ${fp} `)).toBe('AB'.repeat(32));
    expect(fingerprintError(fp)).toBeNull();
    expect(fingerprintError('abcd')).not.toBeNull();
    expect(fingerprintError('zz'.repeat(32))).not.toBeNull();
  });
  it('wants absolute paths', () => {
    expect(pathError('/admin')).toBeNull();
    expect(pathError('admin')).not.toBeNull();
  });
});

describe('clientCertSummary', () => {
  it('describes the policy', () => {
    expect(clientCertSummary(null)).toBe('');
    expect(clientCertSummary({ mode: 'ignore', caPem: CERT })).toBe('');
    expect(clientCertSummary({ mode: 'require', caPem: CERT, allowedSubjects: ['a'], allowedFingerprints: ['b'] })).toBe(
      'Client certificate required · 1 CA · 2 allowed',
    );
    expect(clientCertSummary({ mode: 'accept', caPem: `${CERT}\n${CERT}`, requirePaths: ['/admin'] })).toBe(
      'Client certificate accepted · 2 CAs · required on 1 path',
    );
  });
});

describe('ocspBadge', () => {
  it('shows each state', () => {
    expect(ocspBadge(undefined)).toBeNull();
    expect(ocspBadge({ state: 'none', stapled: false })?.label).toBe('none');
    expect(ocspBadge({ state: 'good', stapled: true, nextUpdate: '2026-10-01T12:00:00Z' })).toMatchObject({ label: 'stapled', tone: 'green' });
    expect(ocspBadge({ state: 'good', stapled: true, nextUpdate: '2026-10-01T12:00:00Z' })?.detail).toContain('2026-10-01 12:00');
    expect(ocspBadge({ state: 'revoked', stapled: false, revocationReason: 'key compromise' })).toMatchObject({ label: 'revoked', tone: 'red' });
    expect(ocspBadge({ state: 'error', stapled: false, lastError: 'HTTP 500' })).toMatchObject({ tone: 'amber', detail: 'HTTP 500' });
  });
  it('warns louder for Must-Staple certificates without a staple', () => {
    const b = ocspBadge({ state: 'error', stapled: false, mustStaple: true, lastError: 'timeout' });
    expect(b?.tone).toBe('red');
    expect(b?.detail).toContain('Must-Staple');
  });
});
