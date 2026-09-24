// Deployment slots: pure helpers mirroring internal/model/slots.go.

import type { Binding, Deployment, DeploymentSlot, EnvVar, InstanceStatus, Site, SiteStatus, SwapPhase, SwapProgress, SwapResult, SlotsView } from '@/api/types';
import { runsNode } from './siteDefaults';

/** Names the site itself where a slot name is expected. */
export const PRODUCTION = 'production';
/** How many slots a site may have besides production. */
export const MAX_SLOTS = 4;
export const SLOT_NAME_RE = /^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/;
export const DEFAULT_WARMUP_STATUSES = '200-399';
export const DEFAULT_WARMUP_TIMEOUT_SEC = 120;
export const MIN_WARMUP_TIMEOUT_SEC = 5;
export const MAX_WARMUP_TIMEOUT_SEC = 1800;
export const MAX_WARMUP_PATHS = 10;

/** A new slot with the server's defaults. */
export function newSlot(name = 'staging'): DeploymentSlot {
  return { name, autoSwap: false, warmup: { paths: ['/'], statuses: DEFAULT_WARMUP_STATUSES, timeoutSec: DEFAULT_WARMUP_TIMEOUT_SEC } };
}

/** The slots of a node or worker site (none for other types). */
export function siteSlots(site: Pick<Site, 'type' | 'slots'> | null | undefined): DeploymentSlot[] {
  return site && runsNode(site.type) ? (site.slots ?? []) : [];
}

export function hasSlots(site: Pick<Site, 'type' | 'slots'> | null | undefined): boolean {
  return siteSlots(site).length > 0;
}

/** Display name of a slot: "" and undefined are production. */
export function slotLabel(name: string | undefined | null): string {
  return name ? name : PRODUCTION;
}

/** The server's form of a slot name: "production" is the site itself (""). */
export function normalizeSlot(name: string | undefined | null): string {
  const n = (name ?? '').trim().toLowerCase();
  return n === PRODUCTION ? '' : n;
}

/** Why a slot name cannot be used, or null. `existing` are the other slots' names. */
export function validateSlotName(name: string, existing: string[]): string | null {
  const n = name.trim();
  if (!n) return 'Enter a name';
  if (n.toLowerCase() === PRODUCTION) return '"production" is the site itself: pick another name';
  if (!SLOT_NAME_RE.test(n)) return "1–32 lower-case letters, digits or '-', not starting or ending with '-'";
  if (existing.includes(n)) return `Another slot is called ${n}`;
  return null;
}

/** A free name to suggest for a new slot. */
export function suggestSlotName(existing: string[]): string {
  for (const n of ['staging', 'testing', 'preview', 'canary']) if (!existing.includes(n)) return n;
  for (let i = 2; ; i++) if (!existing.includes(`staging-${i}`)) return `staging-${i}`;
}

// ---------------------------------------------------------------- warm-up

export interface StatusRange {
  from: number;
  to: number;
}

export type StatusRangesResult = { ok: true; ranges: StatusRange[] } | { ok: false; error: string };

const INT_RE = /^[+-]?\d+$/;

/** Reads "200-399" or "200-299,401,404" like the server (statuses 100..599). */
export function parseStatusRanges(s: string): StatusRangesResult {
  const ranges: StatusRange[] = [];
  for (const raw of s.split(',')) {
    const part = raw.trim();
    if (!part) continue;
    const dash = part.indexOf('-');
    const lo = (dash >= 0 ? part.slice(0, dash) : part).trim();
    const hi = dash >= 0 ? part.slice(dash + 1).trim() : lo;
    const from = INT_RE.test(lo) ? parseInt(lo, 10) : NaN;
    const to = INT_RE.test(hi) ? parseInt(hi, 10) : NaN;
    if (!Number.isFinite(from) || !Number.isFinite(to) || from < 100 || to > 599 || from > to) {
      return { ok: false, error: `"${part}" is not an HTTP status or a range of them (100-599)` };
    }
    ranges.push({ from, to });
  }
  if (ranges.length === 0) return { ok: false, error: 'List at least one status, e.g. 200-399' };
  return { ok: true, ranges };
}

/** The error of an accepted-statuses list, or null. */
export function statusRangesError(s: string): string | null {
  const r = parseStatusRanges(s);
  return r.ok ? null : r.error;
}

export function statusAccepted(code: number, ranges: StatusRange[]): boolean {
  return ranges.some((r) => code >= r.from && code <= r.to);
}

/** Why a warm-up path is not valid, or null. */
export function warmupPathError(p: string): string | null {
  if (!p.startsWith('/')) return 'Must start with /';
  if (/\s/.test(p)) return 'No spaces';
  return null;
}

export function warmupTimeoutError(sec: number): string | null {
  return sec < MIN_WARMUP_TIMEOUT_SEC || sec > MAX_WARMUP_TIMEOUT_SEC
    ? `Between ${MIN_WARMUP_TIMEOUT_SEC} and ${MAX_WARMUP_TIMEOUT_SEC} seconds`
    : null;
}

// ---------------------------------------------------------------- variables

/**
 * The variables a slot's instances run with, as the server merges them:
 * production's variables that are not slot settings, then the slot's own,
 * replacing those of the same name or appended in order.
 */
export function effectiveSlotEnv(prod: EnvVar[] | null | undefined, slot: EnvVar[] | null | undefined): EnvVar[] {
  const out: EnvVar[] = (prod ?? []).filter((e) => !e.slotSetting);
  for (const e of slot ?? []) {
    const v: EnvVar = { ...e };
    delete v.slotSetting;
    const i = out.findIndex((o) => o.name === v.name);
    if (i >= 0) out[i] = v;
    else out.push(v);
  }
  return out;
}

export type EnvOrigin = 'production' | 'overridden' | 'slot';

export interface SlotEnvSummary {
  /** The effective variables with where each comes from. */
  vars: { name: string; secret: boolean; origin: EnvOrigin }[];
  /** Production's slot settings: not given to the slot. */
  sticky: string[];
}

/** Where each of a slot's effective variables comes from. */
export function slotEnvSummary(prod: EnvVar[] | null | undefined, slot: EnvVar[] | null | undefined): SlotEnvSummary {
  const own = new Set((slot ?? []).map((e) => e.name));
  const inherited = new Set((prod ?? []).filter((e) => !e.slotSetting).map((e) => e.name));
  return {
    vars: effectiveSlotEnv(prod, slot).map((e) => ({
      name: e.name,
      secret: !!e.secret,
      origin: own.has(e.name) ? (inherited.has(e.name) ? 'overridden' : 'slot') : 'production',
    })),
    sticky: (prod ?? []).filter((e) => e.slotSetting && e.name).map((e) => e.name),
  };
}

// ---------------------------------------------------------------- releases

/** Whether a site's release id names this deployment. */
export function releaseIs(release: string | undefined | null, dep: Pick<Deployment, 'id' | 'releaseDir'>): boolean {
  if (!release) return false;
  return release === dep.id || (!!dep.releaseDir && dep.releaseDir.endsWith(release));
}

/** The slots ("production" first) that run this deployment's release. */
export function releaseSlots(site: Pick<Site, 'type' | 'activeRelease' | 'slots'>, dep: Pick<Deployment, 'id' | 'releaseDir'>): string[] {
  const out: string[] = [];
  if (releaseIs(site.activeRelease, dep)) out.push(PRODUCTION);
  for (const sl of siteSlots(site)) if (releaseIs(sl.activeRelease, dep)) out.push(sl.name);
  return out;
}

/** Which slot runs a release ("production" first), or null. */
export function releaseSlot(site: Pick<Site, 'type' | 'activeRelease' | 'slots'>, releaseId: string): string | null {
  return releaseSlots(site, { id: releaseId })[0] ?? null;
}

/** The release a slot runs ("" or "production" = the site's own). */
export function slotRelease(site: Pick<Site, 'type' | 'activeRelease' | 'slots'>, slot: string): string | undefined {
  const n = normalizeSlot(slot);
  if (!n) return site.activeRelease;
  return siteSlots(site).find((s) => s.name === n)?.activeRelease;
}

// ---------------------------------------------------------------- selects

/** Production ("") and each slot, for a select. */
export function slotOptions(site: Pick<Site, 'type' | 'slots'>, productionLabel = 'Production'): { value: string; label: string }[] {
  return [{ value: '', label: productionLabel }, ...siteSlots(site).map((s) => ({ value: s.name, label: s.name }))];
}

/** Bindings that route to a slot ("" = production). */
export function slotBindings(bindings: Binding[] | null | undefined, slot: string): Binding[] {
  const n = normalizeSlot(slot);
  return (bindings ?? []).filter((b) => (b.slot ?? '') === n);
}

// ---------------------------------------------------------------- editing

/** The site's slots and bindings without a slot (and the bindings that routed to it). */
export function withoutSlot(site: Pick<Site, 'slots' | 'bindings'>, name: string): Pick<Site, 'slots' | 'bindings'> {
  return {
    slots: (site.slots ?? []).filter((s) => s.name !== name),
    bindings: (site.bindings ?? []).filter((b) => (b.slot ?? '') !== name),
  };
}

// ---------------------------------------------------------------- status and swaps

export const SWAP_PHASES: SwapPhase[] = ['preparing', 'warming', 'swapping'];

const phaseLabels: Record<SwapPhase, string> = {
  preparing: 'Preparing',
  warming: 'Warming up',
  swapping: 'Swapping',
};

const phaseDescriptions: Record<SwapPhase, string> = {
  preparing: "The slot's instances restart with production's settings",
  warming: 'Warm-up requests to every instance',
  swapping: 'Production traffic moves to the warm instances',
};

export function swapPhaseLabel(phase: string | undefined): string {
  return phaseLabels[phase as SwapPhase] ?? (phase || 'Starting');
}

export function swapPhaseDescription(phase: string | undefined): string {
  return phaseDescriptions[phase as SwapPhase] ?? '';
}

/** Position of a phase in SWAP_PHASES, -1 when unknown. */
export function swapStepIndex(phase: string | undefined): number {
  return SWAP_PHASES.indexOf(phase as SwapPhase);
}

/**
 * How a swap that was in progress ended: the view's lastSwap once the swap
 * is gone, or null while it runs (or when the result is of another swap).
 */
export function finishedSwap(prev: SwapProgress | null | undefined, view: SlotsView | undefined): SwapResult | null {
  if (!prev || !view || view.swap) return null;
  const r = view.lastSwap;
  if (!r || r.slot !== prev.slot) return null;
  const started = Date.parse(prev.startedAt);
  const finished = Date.parse(r.finishedAt);
  if (Number.isFinite(started) && Number.isFinite(finished) && finished < started) return null;
  return r;
}

const live = (i: InstanceStatus) => i.state !== 'exited' && i.state !== 'crashed';

/** Ready and running instances of a status. */
export function instanceCounts(status: Pick<SiteStatus, 'instances'> | null | undefined): { ready: number; total: number } {
  const list = (status?.instances ?? []).filter(live);
  return { ready: list.filter((i) => i.state === 'ready').length, total: list.length };
}

// ---------------------------------------------------------------- logs

/** Log filter value for every slot's lines. */
export const ALL_SLOTS = '*';

/**
 * The `slot` parameter of a log request for a filter value (ALL_SLOTS, ""
 * for production or a slot's name). Application lines of every slot need
 * none; an access log is always one slot's, production's by default.
 */
export function logSlotParam(value: string, access: boolean): string | undefined {
  if (value === ALL_SLOTS) return access ? PRODUCTION : undefined;
  return value || PRODUCTION;
}
