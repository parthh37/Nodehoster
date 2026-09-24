import { describe, expect, it } from 'vitest';
import type { AlertRule } from '@/api/alerts';
import type { Site } from '@/api/types';
import {
  ALERT_METRICS,
  alertCounts,
  alertSubject,
  defaultAlerts,
  describeRule,
  effectiveRules,
  formatAlertValue,
  memoryLimitMB,
  newRule,
  normalizeAlerts,
  normalizeRule,
  ruleApplies,
  ruleError,
  silenceText,
  withSiteRule,
  withoutSiteRule,
} from './alerts';
import { defaultNode, newSite } from './siteDefaults';

function nodeSite(p: { agent?: boolean; limit?: number } = {}): Site {
  const s = newSite('node');
  s.node = { ...defaultNode(), agentEnabled: !!p.agent };
  s.node.recycle = { ...s.node.recycle, memoryLimitMB: p.limit };
  return s;
}

describe('normalizeAlerts', () => {
  it('fills settings saved before alerts existed', () => {
    expect(normalizeAlerts(undefined)).toEqual(defaultAlerts());
    expect(normalizeAlerts({} as never)).toEqual(defaultAlerts());
  });
  it('keeps a saved configuration', () => {
    const a = normalizeAlerts({ enabled: true, siteRules: [], serverRules: null, recoveryMinutes: 0, emailTo: null });
    expect(a).toEqual({ enabled: true, siteRules: [], serverRules: [], recoveryMinutes: 2, emailTo: [] });
  });
  it('fills rule defaults like the server', () => {
    expect(normalizeRule({ id: 'e', metric: 'errorRate', threshold: 5, forMinutes: 5, severity: '' })).toEqual({
      id: 'e', metric: 'errorRate', threshold: 5, forMinutes: 5, severity: 'warning', windowMinutes: 5, minRequests: 20,
    });
    expect(normalizeRule({ id: 'c', metric: 'cpu', threshold: 5, forMinutes: 5, severity: 'critical', windowMinutes: 9 })).not.toHaveProperty('windowMinutes');
  });
});

describe('newRule', () => {
  it('picks a free ID', () => {
    expect(newRule('cpu', []).id).toBe('cpu');
    expect(newRule('cpu', ['CPU', 'cpu-2']).id).toBe('cpu-3');
    expect(newRule('latencyP95', ['latency-p95'], 'site-')).toMatchObject({ id: 'site-latencyp95', threshold: 2000, windowMinutes: 5 });
  });
});

describe('ruleApplies', () => {
  it('matches the site type and its configuration', () => {
    const node = nodeSite();
    expect(ruleApplies({ metric: 'instanceCpu' }, node)).toBe(true);
    expect(ruleApplies({ metric: 'memoryPercent' }, node)).toBe(false);
    expect(ruleApplies({ metric: 'memoryPercent' }, nodeSite({ limit: 512 }))).toBe(true);
    expect(ruleApplies({ metric: 'eventLoopLag' }, node)).toBe(false);
    expect(ruleApplies({ metric: 'eventLoopLag' }, nodeSite({ agent: true }))).toBe(true);
    expect(ruleApplies({ metric: 'diskFree' }, node)).toBe(false);
    const stat = newSite('static');
    expect(ruleApplies({ metric: 'errorRate' }, stat)).toBe(true);
    expect(ruleApplies({ metric: 'cpu' }, stat)).toBe(false);
    const worker = newSite('worker');
    expect(ruleApplies({ metric: 'errorRate' }, worker)).toBe(false);
    expect(ruleApplies({ metric: 'instancesDown' }, worker)).toBe(true);
  });
  it('uses the lower memory limit', () => {
    const s = nodeSite({ limit: 1024 });
    s.node!.limits = { ...s.node!.limits, memoryLimitMB: 800 };
    expect(memoryLimitMB(s)).toBe(800);
    expect(memoryLimitMB(newSite('static'))).toBe(0);
  });
});

describe('effectiveRules', () => {
  const defaults = defaultAlerts().siteRules!;
  it('lists server rules, overrides in place and site rules after', () => {
    const s = nodeSite({ agent: true });
    s.alerts = {
      rules: [
        { id: 'site-memory', metric: 'memory', threshold: 800, forMinutes: 5, severity: 'warning' },
        { id: 'ERRORS', metric: 'errorRate', threshold: 1, forMinutes: 5, severity: 'critical', windowMinutes: 5, minRequests: 20 },
        { id: 'latency-p95', metric: 'latencyP95', threshold: 2000, forMinutes: 10, severity: 'warning', disabled: true },
      ],
    };
    const eff = effectiveRules(defaults, s);
    expect(eff.map((e) => `${e.rule.id}:${e.source}${e.off ? ':off' : ''}${e.skipped ? ':skip' : ''}`)).toEqual([
      'cpu:server',
      'memory-limit:server:skip',
      'event-loop-lag:server',
      'ERRORS:override',
      'latency-p95:override:off',
      'instances-down:server',
      'site-memory:site',
    ]);
    expect(eff[3].base?.threshold).toBe(5);
    expect(eff[1].skipped).toBe('no memory limit set');
  });
  it('says why rules do not apply', () => {
    const eff = effectiveRules(defaults, newSite('static'));
    expect(eff.filter((e) => !e.skipped).map((e) => e.rule.id)).toEqual(['errors', 'latency-p95']);
    expect(eff[0].skipped).toBe('Node.js sites only');
  });
});

describe('withSiteRule / withoutSiteRule', () => {
  const r: AlertRule = { id: 'cpu', metric: 'instanceCpu', threshold: 95, forMinutes: 10, severity: 'warning' };
  it('adds, replaces and removes', () => {
    let a = withSiteRule(undefined, r);
    expect(a.rules).toEqual([r]);
    a = withSiteRule({ disabled: false, ...a }, { ...r, id: 'CPU', threshold: 99 });
    expect(a.rules).toHaveLength(1);
    expect(a.rules![0].threshold).toBe(99);
    a = withoutSiteRule(a, 'cpu');
    expect(a).toEqual({ disabled: false });
  });
});

describe('formatting', () => {
  it('formats values like the server', () => {
    expect(formatAlertValue('cpu', 93.4)).toBe('93%');
    expect(formatAlertValue('errorRate', 2.46)).toBe('2.5%');
    expect(formatAlertValue('memory', 1234.4)).toBe('1,234 MB');
    expect(formatAlertValue('latencyP95', 850.2)).toBe('850 ms');
    expect(formatAlertValue('latencyP95', 2430)).toBe('2.4 s');
    expect(formatAlertValue('instancesDown', 2)).toBe('2');
  });
  it('describes rules', () => {
    const d = defaultAlerts();
    expect(describeRule(d.siteRules![0])).toBe('CPU of the busiest instance above 90% for 10 min');
    expect(describeRule(d.siteRules![3])).toBe('5xx error rate above 5% (over 5 min, at least 20 requests) for 5 min');
    expect(describeRule(d.siteRules![5])).toBe('Any instance down for 5 min');
    expect(describeRule({ ...d.siteRules![5], threshold: 2, forMinutes: 0 })).toBe('More than 2 instances down');
    expect(describeRule(d.serverRules![2])).toBe('Free disk space below 10% for 5 min');
  });
  it('describes silences and subjects', () => {
    const t = (iso: string) => iso.slice(11, 16);
    expect(silenceText({}, t)).toBe('');
    expect(silenceText({ silence: { by: 'bob', at: '' } }, t)).toBe('acknowledged by bob');
    expect(silenceText({ silence: { by: 'al', at: '', until: '2026-09-24T14:30:00Z' } }, t)).toBe('until 14:30 by al');
    expect(alertSubject({ siteId: 's', siteName: 'api' })).toBe('api');
    expect(alertSubject({})).toBe('Server');
  });
});

describe('ruleError', () => {
  const ok: AlertRule = { id: 'x', metric: 'errorRate', threshold: 5, forMinutes: 5, severity: 'warning', windowMinutes: 5, minRequests: 20 };
  it('accepts the defaults', () => {
    for (const r of [...defaultAlerts().siteRules!, ...defaultAlerts().serverRules!]) expect(ruleError(r)).toBeNull();
  });
  it.each<[Partial<AlertRule>, string]>([
    [{ metric: 'load' }, 'metric'],
    [{ id: 'has space' }, 'ID'],
    [{ threshold: 101 }, 'limit'],
    [{ forMinutes: 2000 }, 'period'],
    [{ windowMinutes: 45 }, 'window'],
    [{ repeatHours: 200 }, 'Reminders'],
  ])('rejects %j', (patch, word) => {
    expect(ruleError({ ...ok, ...patch })).toContain(word);
  });
});

describe('alertCounts', () => {
  it('counts firing alerts by severity, silenced apart', () => {
    const a = (severity: string, silenced = false) =>
      ({ id: '', ruleId: '', metric: 'cpu', severity, threshold: 0, forMinutes: 0, state: 'firing', value: 0, peak: 0, message: '', since: '', notified: true, silence: silenced ? { by: 'x', at: '' } : undefined });
    expect(alertCounts({ enabled: true, firing: [a('critical'), a('warning'), a('critical', true)], pending: [a('critical')] })).toEqual({ critical: 1, warning: 1, silenced: 1 });
    expect(alertCounts(undefined)).toEqual({ critical: 0, warning: 0, silenced: 0 });
  });
});

it('has a unique key per metric', () => {
  expect(new Set(ALERT_METRICS.map((m) => m.key)).size).toBe(ALERT_METRICS.length);
});
