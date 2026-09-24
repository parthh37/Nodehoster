// Resource alerts: the metric catalog, the rule defaults (mirrors of
// internal/model/alerts.go), how a site's rules combine with the
// server-wide ones, and formatting for the Alerts pages.

import type { Alert, AlertList, AlertRule, AlertSettings, SiteAlerts } from '@/api/alerts';
import type { Site } from '@/api/types';
import { runsNode } from './siteDefaults';

export type MetricUnit = '%' | 'MB' | 'ms' | 'instances';

export interface MetricInfo {
  key: string;
  label: string;
  /** What the threshold is compared with, for forms. */
  hint: string;
  unit: MetricUnit;
  server?: boolean;
  /** Fires below the threshold (free disk space). */
  below?: boolean;
  /** Needs Node.js processes (node and worker sites). */
  node?: boolean;
  /** Needs HTTP (not workers). */
  http?: boolean;
  /** A rate over windowMinutes with minRequests. */
  windowed?: boolean;
  max: number;
  /** A sensible first threshold. */
  threshold: number;
}

export const ALERT_METRICS: MetricInfo[] = [
  { key: 'instanceCpu', label: 'CPU of the busiest instance', hint: '% of one CPU core, as the Overview shows it: a Node.js process using a whole core is at 100%.', unit: '%', node: true, max: 10000, threshold: 90 },
  { key: 'cpu', label: 'CPU of all instances', hint: '% of one CPU core, summed over the instances (4 busy instances can reach 400%).', unit: '%', node: true, max: 100000, threshold: 200 },
  { key: 'memory', label: 'Memory of all instances', hint: 'Working set of every instance’s process tree, together.', unit: 'MB', node: true, max: 10 << 20, threshold: 2048 },
  { key: 'memoryPercent', label: 'Memory, % of the limit', hint: 'The largest instance, against its memory limit (the hard limit or the recycle limit, whichever is lower). Sites without a limit are skipped.', unit: '%', node: true, max: 1000, threshold: 90 },
  { key: 'eventLoopLag', label: 'Event-loop lag', hint: 'The worst instance, as the NodeHoster agent reports it (sites with the agent on).', unit: 'ms', node: true, max: 600000, threshold: 250 },
  { key: 'errorRate', label: '5xx error rate', hint: '5xx answers, % of the requests in the window.', unit: '%', http: true, windowed: true, max: 100, threshold: 5 },
  { key: 'latency', label: 'Average response time', hint: 'Over the window.', unit: 'ms', http: true, windowed: true, max: 600000, threshold: 1000 },
  { key: 'latencyP95', label: '95th percentile response time', hint: 'Over the window, estimated from a histogram (to within its bucket).', unit: 'ms', http: true, windowed: true, max: 600000, threshold: 2000 },
  { key: 'instancesDown', label: 'Instances down', hint: 'Configured instances not ready and healthy. 0 fires when any instance is down.', unit: 'instances', node: true, max: 1000, threshold: 0 },
  { key: 'serverCpu', label: 'Server CPU', hint: '% of the whole machine.', unit: '%', server: true, max: 100, threshold: 90 },
  { key: 'serverMemory', label: 'Server memory', hint: '% of the machine’s memory in use.', unit: '%', server: true, max: 100, threshold: 90 },
  { key: 'diskFree', label: 'Free disk space', hint: '% free on the emptiest drive holding the data directory or a site. Fires below the limit.', unit: '%', server: true, below: true, max: 100, threshold: 10 },
];

export function metricInfo(key: string): MetricInfo | undefined {
  return ALERT_METRICS.find((m) => m.key === key);
}

export function siteMetrics(): MetricInfo[] {
  return ALERT_METRICS.filter((m) => !m.server);
}

export function serverMetrics(): MetricInfo[] {
  return ALERT_METRICS.filter((m) => m.server);
}

/** Mirror of model.DefaultAlerts. */
export function defaultAlerts(): AlertSettings {
  return {
    enabled: false,
    siteRules: [
      { id: 'cpu', metric: 'instanceCpu', threshold: 90, forMinutes: 10, severity: 'warning' },
      { id: 'memory-limit', metric: 'memoryPercent', threshold: 90, forMinutes: 5, severity: 'warning' },
      { id: 'event-loop-lag', metric: 'eventLoopLag', threshold: 250, forMinutes: 5, severity: 'warning' },
      { id: 'errors', metric: 'errorRate', threshold: 5, forMinutes: 5, severity: 'critical', windowMinutes: 5, minRequests: 20 },
      { id: 'latency-p95', metric: 'latencyP95', threshold: 2000, forMinutes: 10, severity: 'warning', windowMinutes: 5, minRequests: 20 },
      { id: 'instances-down', metric: 'instancesDown', threshold: 0, forMinutes: 5, severity: 'critical' },
    ],
    serverRules: [
      { id: 'server-cpu', metric: 'serverCpu', threshold: 90, forMinutes: 15, severity: 'warning' },
      { id: 'server-memory', metric: 'serverMemory', threshold: 90, forMinutes: 10, severity: 'warning' },
      { id: 'disk-free', metric: 'diskFree', threshold: 10, forMinutes: 5, severity: 'critical' },
    ],
    recoveryMinutes: 2,
    emailTo: [],
  };
}

/** A rule's defaults, as the server fills them. Does not modify the input. */
export function normalizeRule(r: AlertRule): AlertRule {
  const out: AlertRule = { ...r, severity: r.severity || 'warning' };
  if (metricInfo(r.metric)?.windowed) {
    out.windowMinutes = r.windowMinutes && r.windowMinutes > 0 ? r.windowMinutes : 5;
    out.minRequests = r.minRequests && r.minRequests > 0 ? r.minRequests : 20;
  } else {
    delete out.windowMinutes;
    delete out.minRequests;
  }
  return out;
}

/** Mirror of model.AlertSettings.ApplyDefaults. */
export function normalizeAlerts(a: Partial<AlertSettings> | null | undefined): AlertSettings {
  if (!a || (!a.enabled && !a.siteRules && !a.serverRules && !a.recoveryMinutes && !a.emailTo)) return defaultAlerts();
  return {
    enabled: !!a.enabled,
    siteRules: (a.siteRules ?? []).map(normalizeRule),
    serverRules: (a.serverRules ?? []).map(normalizeRule),
    recoveryMinutes: a.recoveryMinutes && a.recoveryMinutes > 0 ? a.recoveryMinutes : 2,
    emailTo: a.emailTo ?? [],
  };
}

/** A new rule for a metric, with an ID no other rule has ("site-" for a site's own). */
export function newRule(metric: string, taken: string[], prefix = ''): AlertRule {
  const info = metricInfo(metric);
  const base = `${prefix}${metric.toLowerCase()}`;
  const lower = new Set(taken.map((t) => t.toLowerCase()));
  let id = base;
  for (let n = 2; lower.has(id); n++) id = `${base}-${n}`;
  return normalizeRule({ id, metric, threshold: info?.threshold ?? 0, forMinutes: 5, severity: 'warning' });
}

/** Mirror of model.AlertRuleApplies: can the metric be measured for this site? */
export function ruleApplies(r: Pick<AlertRule, 'metric'>, site: Pick<Site, 'type' | 'node'>): boolean {
  const info = metricInfo(r.metric);
  if (!info || info.server) return false;
  if (info.node && (!runsNode(site.type) || !site.node)) return false;
  if (info.http && site.type === 'worker') return false;
  if (r.metric === 'memoryPercent') return memoryLimitMB(site) > 0;
  if (r.metric === 'eventLoopLag') return !!site.node?.agentEnabled;
  return true;
}

/** Why a server-wide rule is not evaluated for a site, or null. */
export function notAppliedReason(r: Pick<AlertRule, 'metric'>, site: Pick<Site, 'type' | 'node'>): string | null {
  if (ruleApplies(r, site)) return null;
  const info = metricInfo(r.metric);
  if (info?.node && !runsNode(site.type)) return 'Node.js sites only';
  if (info?.http && site.type === 'worker') return 'not for workers';
  if (r.metric === 'memoryPercent') return 'no memory limit set';
  if (r.metric === 'eventLoopLag') return 'the NodeHoster agent is off';
  return 'not measurable here';
}

/** Mirror of NodeConfig.MemoryLimitMB. */
export function memoryLimitMB(site: Pick<Site, 'node'>): number {
  const vals = [site.node?.limits?.memoryLimitMB ?? 0, site.node?.recycle?.memoryLimitMB ?? 0].filter((v) => v > 0);
  return vals.length ? Math.min(...vals) : 0;
}

export type RuleSource = 'server' | 'override' | 'site';

export interface EffectiveRule {
  rule: AlertRule;
  source: RuleSource;
  /** The server-wide rule an override replaces. */
  base?: AlertRule;
  /** Turned off for this site (an override with disabled). */
  off: boolean;
  /** Why it is not evaluated for this site, or null when it is. */
  skipped: string | null;
}

/**
 * The rules of a site as the site's Alerts tab lists them: every
 * server-wide rule (replaced by the site's override when there is one),
 * then the site's own. Mirror of model.EffectiveAlertRules, which keeps
 * only the rules in force.
 */
export function effectiveRules(defaults: AlertRule[], site: Pick<Site, 'type' | 'node' | 'alerts'>): EffectiveRule[] {
  const own = site.alerts?.rules ?? [];
  const byId = new Map(own.map((r) => [r.id.toLowerCase(), r]));
  const seen = new Set<string>();
  const out: EffectiveRule[] = [];
  for (const d of defaults) {
    const key = d.id.toLowerCase();
    seen.add(key);
    const o = byId.get(key);
    const rule = o ?? d;
    out.push({ rule, source: o ? 'override' : 'server', base: o ? d : undefined, off: !!rule.disabled, skipped: notAppliedReason(rule, site) });
  }
  for (const r of own) {
    if (!seen.has(r.id.toLowerCase())) out.push({ rule: r, source: 'site', off: !!r.disabled, skipped: notAppliedReason(r, site) });
  }
  return out;
}

/** The site's alert settings with rule (an override or its own) replacing the rule of the same ID. */
export function withSiteRule(a: SiteAlerts | undefined, rule: AlertRule): SiteAlerts {
  const rules = [...(a?.rules ?? [])];
  const i = rules.findIndex((r) => r.id.toLowerCase() === rule.id.toLowerCase());
  if (i >= 0) rules[i] = rule;
  else rules.push(rule);
  return { ...a, rules };
}

/** The site's alert settings without its rule of that ID (an override goes back to the server's rule). */
export function withoutSiteRule(a: SiteAlerts | undefined, id: string): SiteAlerts {
  const rules = (a?.rules ?? []).filter((r) => r.id.toLowerCase() !== id.toLowerCase());
  const out: SiteAlerts = { ...a };
  if (rules.length) out.rules = rules;
  else delete out.rules;
  return out;
}

/** Mirror of alerts.FormatValue. */
export function formatAlertValue(metric: string, v: number): string {
  const info = metricInfo(metric);
  switch (info?.unit) {
    case 'MB':
      return `${Math.round(v).toLocaleString('en-US')} MB`;
    case 'ms':
      return v >= 1000 ? `${Math.round(v / 100) / 10} s` : `${Math.round(v)} ms`;
    case 'instances':
      return String(Math.round(v));
  }
  if (v < 10 && v !== Math.trunc(v)) return `${Math.round(v * 10) / 10}%`;
  return `${Math.round(v)}%`;
}

/** "CPU of the busiest instance above 90% for 10 min". */
export function describeRule(r: AlertRule): string {
  const info = metricInfo(r.metric);
  const label = info?.label ?? r.metric;
  const cmp = info?.below ? 'below' : 'above';
  const held = r.forMinutes > 0 ? ` for ${r.forMinutes} min` : '';
  const window = info?.windowed ? ` (over ${r.windowMinutes ?? 5} min, at least ${r.minRequests ?? 20} requests)` : '';
  if (r.metric === 'instancesDown') {
    return r.threshold === 0 ? `Any instance down${held}` : `More than ${r.threshold} instances down${held}`;
  }
  return `${label} ${cmp} ${formatAlertValue(r.metric, r.threshold)}${window}${held}`;
}

/** Client-side check of a rule, mirroring model's validation; null when valid. */
export function ruleError(r: AlertRule): string | null {
  const info = metricInfo(r.metric);
  if (!info) return 'Choose a metric';
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,47}$/.test(r.id)) return "The ID must be 1-48 letters, digits, '.', '_' or '-'";
  if (!(r.threshold >= 0) || r.threshold > info.max) return `The limit must be between 0 and ${info.max}`;
  if (!(r.forMinutes >= 0) || r.forMinutes > 1440) return 'The period must be between 0 and 1440 minutes';
  if (info.windowed && (!r.windowMinutes || r.windowMinutes < 1 || r.windowMinutes > 30)) return 'The window must be between 1 and 30 minutes';
  if ((r.repeatHours ?? 0) < 0 || (r.repeatHours ?? 0) > 168) return 'Reminders must be between 0 and 168 hours apart';
  return null;
}

/** Firing alerts by severity, not counting silenced ones. */
export function alertCounts(l: AlertList | undefined): { critical: number; warning: number; silenced: number } {
  const c = { critical: 0, warning: 0, silenced: 0 };
  for (const a of l?.firing ?? []) {
    if (a.silence) c.silenced++;
    else if (a.severity === 'critical') c.critical++;
    else c.warning++;
  }
  return c;
}

/** "until 14:30 by alice", "acknowledged by bob", "" when not silenced. */
export function silenceText(a: Pick<Alert, 'silence'>, formatTime: (iso: string) => string): string {
  const s = a.silence;
  if (!s) return '';
  return s.until ? `until ${formatTime(s.until)} by ${s.by}` : `acknowledged by ${s.by}`;
}

/** Who an alert is about: its site's name, or "Server". */
export function alertSubject(a: Pick<Alert, 'siteId' | 'siteName'>): string {
  return a.siteId ? (a.siteName ?? a.siteId) : 'Server';
}

export const SILENCE_CHOICES = [
  { minutes: 30, label: '30 minutes' },
  { minutes: 60, label: '1 hour' },
  { minutes: 240, label: '4 hours' },
  { minutes: 1440, label: '1 day' },
  { minutes: 10080, label: '1 week' },
  { minutes: 0, label: 'Until it resolves (acknowledge)' },
];
