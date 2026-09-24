import { describe, expect, it } from 'vitest';
import type { Binding, EnvVar, Site, SlotsView, SwapProgress } from '@/api/types';
import {
  ALL_SLOTS,
  effectiveSlotEnv,
  finishedSwap,
  hasSlots,
  instanceCounts,
  logSlotParam,
  newSlot,
  normalizeSlot,
  parseStatusRanges,
  releaseSlot,
  releaseSlots,
  slotBindings,
  slotEnvSummary,
  slotLabel,
  slotOptions,
  slotRelease,
  statusAccepted,
  statusRangesError,
  suggestSlotName,
  swapPhaseLabel,
  swapStepIndex,
  validateSlotName,
  warmupPathError,
  warmupTimeoutError,
  withoutSlot,
} from './slots';

const env = (name: string, value: string, extra: Partial<EnvVar> = {}): EnvVar => ({ name, value, ...extra });
const binding = (host: string, slot?: string): Binding => ({ id: host, protocol: 'http', ip: '', port: 80, host, ...(slot !== undefined ? { slot } : {}) });

type SlotSite = Pick<Site, 'type' | 'activeRelease' | 'slots'>;
const site = (over: Partial<SlotSite> = {}): SlotSite => ({
  type: 'node',
  activeRelease: 'r-prod',
  slots: [
    { ...newSlot('staging'), activeRelease: 'r-stage' },
    { ...newSlot('canary'), activeRelease: 'r-prod' },
  ],
  ...over,
});

describe('newSlot', () => {
  it('has the server defaults', () => {
    expect(newSlot()).toEqual({ name: 'staging', autoSwap: false, warmup: { paths: ['/'], statuses: '200-399', timeoutSec: 120 } });
    expect(newSlot('qa').name).toBe('qa');
  });
});

describe('hasSlots / slotLabel / normalizeSlot', () => {
  it('only counts node and worker sites', () => {
    expect(hasSlots(site())).toBe(true);
    expect(hasSlots(site({ slots: [] }))).toBe(false);
    expect(hasSlots({ type: 'static', slots: [newSlot()] })).toBe(false);
    expect(hasSlots(undefined)).toBe(false);
  });
  it('names production', () => {
    expect(slotLabel('')).toBe('production');
    expect(slotLabel(undefined)).toBe('production');
    expect(slotLabel('staging')).toBe('staging');
    expect(normalizeSlot(' Production ')).toBe('');
    expect(normalizeSlot('Staging')).toBe('staging');
  });
});

describe('validateSlotName', () => {
  it.each(['staging', 'a', 'qa-2', 'x'.repeat(32)])('accepts %j', (n) => expect(validateSlotName(n, [])).toBeNull());
  it.each(['', 'production', 'Staging', '-a', 'a-', 'a_b', 'a b', 'x'.repeat(33)])('rejects %j', (n) => expect(validateSlotName(n, [])).not.toBeNull());
  it('rejects duplicates', () => expect(validateSlotName('staging', ['staging'])).toMatch(/Another slot/));
});

describe('suggestSlotName', () => {
  it('picks a free name', () => {
    expect(suggestSlotName([])).toBe('staging');
    expect(suggestSlotName(['staging'])).toBe('testing');
    expect(suggestSlotName(['staging', 'testing', 'preview', 'canary'])).toBe('staging-2');
  });
});

describe('parseStatusRanges', () => {
  it('reads ranges and single statuses', () => {
    expect(parseStatusRanges('200-399')).toEqual({ ok: true, ranges: [{ from: 200, to: 399 }] });
    expect(parseStatusRanges(' 200-299 , 401,')).toEqual({
      ok: true,
      ranges: [
        { from: 200, to: 299 },
        { from: 401, to: 401 },
      ],
    });
  });
  it.each(['', ' , ', '99', '600', '300-200', 'abc', '200-', '2x0', '200-299-300', '100-600'])('rejects %j', (s) => {
    const r = parseStatusRanges(s);
    expect(r.ok).toBe(false);
    expect(statusRangesError(s)).not.toBeNull();
  });
  it('matches codes', () => {
    const r = parseStatusRanges('200-299,401');
    if (!r.ok) throw new Error(r.error);
    expect(statusAccepted(204, r.ranges)).toBe(true);
    expect(statusAccepted(401, r.ranges)).toBe(true);
    expect(statusAccepted(302, r.ranges)).toBe(false);
  });
});

describe('warm-up validators', () => {
  it('checks paths', () => {
    expect(warmupPathError('/')).toBeNull();
    expect(warmupPathError('/health?deep=1')).toBeNull();
    expect(warmupPathError('health')).not.toBeNull();
    expect(warmupPathError('/a b')).not.toBeNull();
  });
  it('checks the timeout', () => {
    expect(warmupTimeoutError(5)).toBeNull();
    expect(warmupTimeoutError(1800)).toBeNull();
    expect(warmupTimeoutError(4)).not.toBeNull();
    expect(warmupTimeoutError(1801)).not.toBeNull();
  });
});

describe('effectiveSlotEnv', () => {
  const prod = [env('NODE_ENV', 'production'), env('DB', 'prod-db', { slotSetting: true }), env('API', 'x'), env('KEY', 's', { secret: true })];
  it('inherits non-sticky production variables', () => {
    expect(effectiveSlotEnv(prod, []).map((e) => e.name)).toEqual(['NODE_ENV', 'API', 'KEY']);
    expect(effectiveSlotEnv(undefined, undefined)).toEqual([]);
  });
  it('lets the slot override by name in place and append the rest', () => {
    const out = effectiveSlotEnv(prod, [env('API', 'y'), env('DB', 'stage-db'), env('EXTRA', '1')]);
    expect(out.map((e) => `${e.name}=${e.value}`)).toEqual(['NODE_ENV=production', 'API=y', 'KEY=s', 'DB=stage-db', 'EXTRA=1']);
  });
  it('never marks slot variables as slot settings and keeps inputs intact', () => {
    const slot = [env('X', '1', { slotSetting: true })];
    const out = effectiveSlotEnv([], slot);
    expect(out[0].slotSetting).toBeUndefined();
    expect(slot[0].slotSetting).toBe(true);
  });
  it('summarizes origins and sticky variables', () => {
    const s = slotEnvSummary(prod, [env('API', 'y'), env('EXTRA', '1', { secret: true })]);
    expect(s.sticky).toEqual(['DB']);
    expect(s.vars).toEqual([
      { name: 'NODE_ENV', secret: false, origin: 'production' },
      { name: 'API', secret: false, origin: 'overridden' },
      { name: 'KEY', secret: true, origin: 'production' },
      { name: 'EXTRA', secret: true, origin: 'slot' },
    ]);
  });
});

describe('releases', () => {
  it('finds the slots that run a release', () => {
    expect(releaseSlots(site(), { id: 'r-prod' })).toEqual(['production', 'canary']);
    expect(releaseSlots(site(), { id: 'r-stage' })).toEqual(['staging']);
    expect(releaseSlots(site(), { id: 'other' })).toEqual([]);
    expect(releaseSlots(site(), { id: 'dep-1', releaseDir: 'C:\\sites\\x\\releases\\r-stage' })).toEqual(['staging']);
  });
  it('names one slot or null', () => {
    expect(releaseSlot(site(), 'r-stage')).toBe('staging');
    expect(releaseSlot(site(), 'r-prod')).toBe('production');
    expect(releaseSlot(site(), 'nope')).toBeNull();
    expect(releaseSlot(site({ activeRelease: undefined }), '')).toBeNull();
  });
  it('reads the release of a slot', () => {
    expect(slotRelease(site(), '')).toBe('r-prod');
    expect(slotRelease(site(), 'production')).toBe('r-prod');
    expect(slotRelease(site(), 'staging')).toBe('r-stage');
    expect(slotRelease(site(), 'missing')).toBeUndefined();
  });
});

describe('slotOptions / slotBindings', () => {
  it('lists production first', () => {
    expect(slotOptions(site())).toEqual([
      { value: '', label: 'Production' },
      { value: 'staging', label: 'staging' },
      { value: 'canary', label: 'canary' },
    ]);
    expect(slotOptions({ type: 'proxy', slots: [newSlot()] })).toEqual([{ value: '', label: 'Production' }]);
  });
  it('groups bindings by slot', () => {
    const list = [binding('a'), binding('b', ''), binding('s', 'staging')];
    expect(slotBindings(list, '').map((b) => b.host)).toEqual(['a', 'b']);
    expect(slotBindings(list, 'production').map((b) => b.host)).toEqual(['a', 'b']);
    expect(slotBindings(list, 'staging').map((b) => b.host)).toEqual(['s']);
  });
});

describe('withoutSlot', () => {
  const s = { slots: [newSlot('staging'), newSlot('qa')], bindings: [binding('a'), binding('s', 'staging'), binding('q', 'qa')] };
  it('removes a slot and its bindings', () => {
    const r = withoutSlot(s, 'staging');
    expect(r.slots!.map((x) => x.name)).toEqual(['qa']);
    expect(r.bindings.map((b) => b.host)).toEqual(['a', 'q']);
    expect(s.slots).toHaveLength(2);
  });
});

describe('swap phases', () => {
  it('labels and orders phases', () => {
    expect(swapPhaseLabel('warming')).toBe('Warming up');
    expect(swapPhaseLabel('odd')).toBe('odd');
    expect(swapStepIndex('preparing')).toBe(0);
    expect(swapStepIndex('swapping')).toBe(2);
    expect(swapStepIndex('odd')).toBe(-1);
  });
});

describe('finishedSwap', () => {
  const prev: SwapProgress = { slot: 'staging', phase: 'warming', startedAt: '2026-09-24T10:00:00Z' };
  const result = { slot: 'staging', succeeded: true, message: 'ok', startedAt: '2026-09-24T10:00:00Z', finishedAt: '2026-09-24T10:00:30Z' };
  const view = (v: Partial<SlotsView>): SlotsView => ({ slots: [], ...v });
  it('is null while the swap runs or without one', () => {
    expect(finishedSwap(prev, view({ swap: prev }))).toBeNull();
    expect(finishedSwap(undefined, view({ lastSwap: result }))).toBeNull();
    expect(finishedSwap(prev, undefined)).toBeNull();
  });
  it('returns the result of that swap', () => {
    expect(finishedSwap(prev, view({ lastSwap: result }))).toBe(result);
  });
  it('ignores an older result or another slot', () => {
    expect(finishedSwap(prev, view({ lastSwap: { ...result, finishedAt: '2026-09-24T09:00:00Z' } }))).toBeNull();
    expect(finishedSwap(prev, view({ lastSwap: { ...result, slot: 'qa' } }))).toBeNull();
  });
});

describe('instanceCounts', () => {
  const inst = (state: string) => ({ index: 0, pid: 1, port: 1, state, healthy: true, restarts: 0, cpuPercent: 0, memoryBytes: 0, requests: 0, activeConns: 0 });
  it('counts ready of running instances', () => {
    expect(instanceCounts({ instances: [inst('ready'), inst('starting'), inst('exited'), inst('crashed'), inst('ready')] })).toEqual({ ready: 2, total: 3 });
    expect(instanceCounts({ instances: null })).toEqual({ ready: 0, total: 0 });
    expect(instanceCounts(undefined)).toEqual({ ready: 0, total: 0 });
  });
});

describe('logSlotParam', () => {
  it('filters application lines only when a slot is picked', () => {
    expect(logSlotParam(ALL_SLOTS, false)).toBeUndefined();
    expect(logSlotParam('', false)).toBe('production');
    expect(logSlotParam('staging', false)).toBe('staging');
  });
  it("always names the access log's slot", () => {
    expect(logSlotParam(ALL_SLOTS, true)).toBe('production');
    expect(logSlotParam('', true)).toBe('production');
    expect(logSlotParam('staging', true)).toBe('staging');
  });
});
