// Pure helpers for the Log shipping settings.

import type { LogShippingSettings, LogSource, LogTarget, LogTargetType } from '@/api/types';

export const LOG_SOURCES: { value: LogSource; label: string; description: string }[] = [
  { value: 'app', label: 'Application output', description: "Sites' stdout, stderr and NodeHoster's lines about them" },
  { value: 'access', label: 'Access logs', description: 'One record per request, for sites with access logging on' },
  { value: 'server', label: 'Server log', description: "NodeHoster's own log, from the minimum level" },
  { value: 'event', label: 'Events', description: 'Crashes, deployments, certificates, backups…' },
  { value: 'audit', label: 'Audit log', description: 'Who changed what, sign-ins' },
];

export const TARGET_TYPES: { value: LogTargetType; label: string; description: string }[] = [
  { value: 'syslog', label: 'Syslog', description: 'RFC 5424 over UDP, TCP or TLS: rsyslog, syslog-ng, Graylog, Papertrail, SIEMs' },
  { value: 'seq', label: 'Seq', description: 'Structured events (CLEF) with an API key' },
  { value: 'http', label: 'HTTP (JSON)', description: 'Batches of JSON records: Logstash, Vector, Fluent Bit, a custom collector' },
];

export const FACILITIES = ['user', 'daemon', 'local0', 'local1', 'local2', 'local3', 'local4', 'local5', 'local6', 'local7'];

export function newTarget(type: LogTargetType): LogTarget {
  const base: LogTarget = { id: '', name: '', type, enabled: true, sources: ['app', 'event'], siteIds: [], minLevel: 'info' };
  switch (type) {
    case 'syslog':
      return { ...base, syslog: { address: '', transport: 'udp', facility: 'local0', appName: '', hostname: '', caCert: '', insecureSkipVerify: false } };
    case 'seq':
      return { ...base, seq: { url: '', apiKey: '' } };
    case 'http':
      return { ...base, http: { url: '', format: 'json', headers: [] } };
  }
}

export function normalizeLogShipping(s: Partial<LogShippingSettings> | null | undefined): LogShippingSettings {
  return {
    targets: (s?.targets ?? []).map((t) => ({
      ...t,
      sources: t.sources ?? [],
      siteIds: t.siteIds ?? [],
      minLevel: t.minLevel || 'info',
      http: t.http ? { ...t.http, headers: t.http.headers ?? [] } : t.http,
    })),
  };
}

/** Where a target sends, for the list. */
export function targetSummary(t: LogTarget): string {
  switch (t.type) {
    case 'syslog':
      return t.syslog ? `${t.syslog.transport}://${t.syslog.address}` : '';
    case 'seq':
      return t.seq?.url ?? '';
    case 'http':
      return t.http ? `${t.http.url} (${t.http.format === 'ndjson' ? 'NDJSON' : 'JSON array'})` : '';
  }
  return '';
}

/** The first problem with a target as edited, or null. */
export function targetProblem(t: LogTarget): { field: string; message: string } | null {
  if (!t.name.trim()) return { field: 'name', message: 'Give the target a name' };
  if (t.enabled && t.sources.length === 0) return { field: 'sources', message: 'Choose at least one source' };
  switch (t.type) {
    case 'syslog': {
      const a = t.syslog?.address.trim() ?? '';
      if (!/^(\[[^\]]+\]|[^\s:]+):\d{1,5}$/.test(a)) return { field: 'syslog.address', message: 'Use host:port, e.g. logs.example.com:514' };
      return null;
    }
    case 'seq':
      if (!/^https?:\/\/./.test(t.seq?.url.trim() ?? '')) return { field: 'seq.url', message: 'Enter the Seq server URL' };
      return null;
    case 'http':
      if (!/^https?:\/\/./.test(t.http?.url.trim() ?? '')) return { field: 'http.url', message: 'Enter an http(s) URL' };
      for (const h of t.http?.headers ?? []) {
        if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(h.name)) return { field: 'http.headers', message: `"${h.name}" is not a header name` };
      }
      return null;
  }
  return null;
}

/** "sent 1,204 · dropped 3" style health of a target. */
export function statusTone(s: { enabled: boolean; lastError?: string; lastErrorAt?: string; lastSuccess?: string; dropped: number; failed: number }): 'green' | 'amber' | 'red' | 'gray' {
  if (!s.enabled) return 'gray';
  const err = s.lastErrorAt ? Date.parse(s.lastErrorAt) : 0;
  const ok = s.lastSuccess ? Date.parse(s.lastSuccess) : 0;
  if (err > ok) return 'red';
  if (s.dropped > 0 || s.failed > 0) return 'amber';
  return 'green';
}
