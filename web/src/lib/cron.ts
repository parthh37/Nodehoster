// Mirror of internal/cron: parses the schedules of scheduled tasks and
// previews their next runs, so the task editor can explain a schedule and
// flag mistakes while it is typed. The server stays authoritative: it
// validates again and reports the real next run (TaskView.nextRunAt) in its
// own local time, which may differ from the browser's.
//
// Syntax: 5 fields (minute hour day-of-month month day-of-week) with
// ranges, steps, lists and jan-dec / sun-sat names (7 is Sunday too), the
// @hourly/@daily/@weekly/@monthly/@yearly shorthands and "@every <Go
// duration>". As in Vixie cron, when both day fields are restricted a day
// matching either runs. Around DST changes each wall-clock minute runs at
// most once: a skipped time does not run, a repeated time runs the first
// time it occurs.
import { formatSpan } from './format';

/** The shortest "@every" interval, in seconds. */
export const MIN_EVERY_SEC = 60;

export interface CronSchedule {
  /** "@every" interval in whole seconds; 0 for a cron expression. */
  everySec: number;
  /** allowed[n] = value n allowed. */
  minute: boolean[];
  hour: boolean[];
  dom: boolean[];
  month: boolean[];
  dow: boolean[];
  /** The day field started with "*" (or "?"). */
  domStar: boolean;
  dowStar: boolean;
}

export type CronParse = { ok: true; schedule: CronSchedule } | { ok: false; error: string };

interface FieldDef {
  name: string;
  min: number;
  max: number;
  names?: Record<string, number>;
}

const MINUTE: FieldDef = { name: 'minute', min: 0, max: 59 };
const HOUR: FieldDef = { name: 'hour', min: 0, max: 23 };
const DOM: FieldDef = { name: 'day of month', min: 1, max: 31 };
const MONTH: FieldDef = {
  name: 'month',
  min: 1,
  max: 12,
  names: { jan: 1, feb: 2, mar: 3, apr: 4, may: 5, jun: 6, jul: 7, aug: 8, sep: 9, oct: 10, nov: 11, dec: 12 },
};
// 7 is accepted for Sunday and folded onto 0.
const DOW: FieldDef = { name: 'day of week', min: 0, max: 7, names: { sun: 0, mon: 1, tue: 2, wed: 3, thu: 4, fri: 5, sat: 6 } };

const MACROS: Record<string, string> = {
  '@yearly': '0 0 1 1 *',
  '@annually': '0 0 1 1 *',
  '@monthly': '0 0 1 * *',
  '@weekly': '0 0 * * 0',
  '@daily': '0 0 * * *',
  '@midnight': '0 0 * * *',
  '@hourly': '0 * * * *',
};

class CronError extends Error {}

// Go's %q, close enough for the strings a schedule contains.
const q = (s: string) => JSON.stringify(s);

/** Parses a schedule. Error texts match the server's. */
export function parseCron(input: string): CronParse {
  try {
    return { ok: true, schedule: parse(input) };
  } catch (e) {
    if (e instanceof CronError) return { ok: false, error: e.message };
    throw e;
  }
}

function parse(input: string): CronSchedule {
  const spec = input.trim();
  if (!spec) throw new CronError('the schedule is empty');
  let lower = spec.toLowerCase();
  if (lower.startsWith('@every')) {
    const rest = lower.slice('@every'.length).trim();
    if (!rest) throw new CronError('"@every" needs a duration, e.g. "@every 15m"');
    const sec = parseGoDuration(rest);
    if (sec === null) throw new CronError(`${q(rest)} is not a duration (use e.g. 90s, 15m, 2h, 1h30m)`);
    if (sec < MIN_EVERY_SEC) throw new CronError('the shortest interval is 1m0s');
    return { ...emptySchedule(), everySec: Math.trunc(sec) };
  }
  if (lower.startsWith('@')) {
    const m = MACROS[lower];
    if (!m) throw new CronError(`unknown shorthand ${q(spec)} (use @hourly, @daily, @weekly, @monthly, @yearly or @every <duration>)`);
    lower = m;
  }
  const parts = lower.split(/\s+/);
  if (parts.length !== 5) {
    throw new CronError(`a cron expression has 5 fields (minute hour day-of-month month day-of-week), this one has ${parts.length}`);
  }
  const s = emptySchedule();
  s.minute = parseField(parts[0], MINUTE).bits;
  s.hour = parseField(parts[1], HOUR).bits;
  ({ bits: s.dom, star: s.domStar } = parseField(parts[2], DOM));
  s.month = parseField(parts[3], MONTH).bits;
  ({ bits: s.dow, star: s.dowStar } = parseField(parts[4], DOW));
  if (s.dow[7]) {
    s.dow[7] = false;
    s.dow[0] = true;
  }
  return s;
}

function emptySchedule(): CronSchedule {
  return { everySec: 0, minute: [], hour: [], dom: [], month: [], dow: [], domStar: false, dowStar: false };
}

function parseField(expr: string, f: FieldDef): { bits: boolean[]; star: boolean } {
  const star = expr.startsWith('*') || expr.startsWith('?');
  const bits: boolean[] = new Array(f.max + 1).fill(false);
  for (const part of expr.split(',')) parseRange(part, f, bits);
  return { bits, star };
}

function parseRange(part: string, f: FieldDef, bits: boolean[]) {
  if (part === '') throw new CronError(`${f.name}: empty value in a list`);
  const slash = part.indexOf('/');
  const rng = slash < 0 ? part : part.slice(0, slash);
  let step = 1;
  if (slash >= 0) {
    const stepStr = part.slice(slash + 1);
    const n = atoi(stepStr);
    if (n === null || n < 1) throw new CronError(`${f.name}: ${q(stepStr)} is not a valid step`);
    step = n;
  }
  let lo: number;
  let hi: number;
  if (rng === '*' || rng === '?') {
    lo = f.min;
    // "*" is Sunday to Saturday; 7 would be Sunday again.
    hi = f === DOW ? 6 : f.max;
  } else if (rng.includes('-')) {
    const dash = rng.indexOf('-');
    lo = value(rng.slice(0, dash), f);
    hi = value(rng.slice(dash + 1), f);
    if (lo > hi) throw new CronError(`${f.name}: range ${q(rng)} runs backwards`);
  } else {
    lo = hi = value(rng, f);
    if (slash >= 0) hi = f.max; // "5/15" = from 5 to the end, every 15
  }
  for (let v = lo; v <= hi; v += step) bits[v] = true;
}

function value(s: string, f: FieldDef): number {
  if (f.names && Object.hasOwn(f.names, s)) return f.names[s];
  const v = atoi(s);
  if (v === null) throw new CronError(f.names ? `${f.name}: ${q(s)} is not a number or a name` : `${f.name}: ${q(s)} is not a number`);
  if (v < f.min || v > f.max) throw new CronError(`${f.name}: ${v} is out of range (${f.min}-${f.max})`);
  return v;
}

/** strconv.Atoi. */
function atoi(s: string): number | null {
  return /^[+-]?\d+$/.test(s) ? parseInt(s, 10) : null;
}

const UNIT_SEC: Record<string, number> = { ns: 1e-9, us: 1e-6, 'µs': 1e-6, 'μs': 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 };

/** Go's time.ParseDuration, in seconds; null when invalid. */
export function parseGoDuration(input: string): number | null {
  let s = input;
  let sign = 1;
  if (s[0] === '-' || s[0] === '+') {
    if (s[0] === '-') sign = -1;
    s = s.slice(1);
  }
  if (s === '0') return 0;
  if (s === '') return null;
  let total = 0;
  while (s) {
    const m = /^(\d*)(?:\.(\d*))?([^\d.]*)/.exec(s);
    if (!m || (!m[1] && !m[2]) || !Object.hasOwn(UNIT_SEC, m[3])) return null;
    total += parseFloat(`${m[1] || '0'}.${m[2] || '0'}`) * UNIT_SEC[m[3]];
    s = s.slice(m[0].length);
  }
  return sign * total;
}

// ---------------------------------------------------------------- next runs

/** Converts between instants and wall-clock time in one time zone. Months are 1-12. */
export interface WallClock {
  /** An instant with these local fields (any one for a repeated time, a nearby one for a skipped time). */
  toInstant(y: number, mo: number, d: number, h: number, mi: number): Date;
  fields(t: Date): { y: number; mo: number; d: number; h: number; mi: number };
}

/** The browser's time zone. */
export const localClock: WallClock = {
  toInstant: (y, mo, d, h, mi) => new Date(y, mo - 1, d, h, mi, 0, 0),
  fields: (t) => ({ y: t.getFullYear(), mo: t.getMonth() + 1, d: t.getDate(), h: t.getHours(), mi: t.getMinutes() }),
};

export const utcClock: WallClock = {
  toInstant: (y, mo, d, h, mi) => new Date(Date.UTC(y, mo - 1, d, h, mi)),
  fields: (t) => ({ y: t.getUTCFullYear(), mo: t.getUTCMonth() + 1, d: t.getUTCDate(), h: t.getUTCHours(), mi: t.getUTCMinutes() }),
};

const MIN_MS = 60_000;
// Bounds the search: "0 0 30 2 *" (February 30th) never matches.
const SEARCH_LIMIT_MS = 5 * 366 * 24 * 3600_000;

/** The first time after `after` that the schedule fires, or null if it never does. */
export function nextRun(s: CronSchedule, after: Date, clock: WallClock = localClock): Date | null {
  if (s.everySec > 0) return new Date(Math.floor((after.getTime() + s.everySec * 1000) / 1000) * 1000);
  // The search runs on the wall clock, in UTC where every day has 24 hours;
  // each match is then placed in the real time zone.
  const f = clock.fields(after);
  let wall = Date.UTC(f.y, f.mo - 1, f.d, f.h, f.mi) + MIN_MS;
  const end = wall + SEARCH_LIMIT_MS;
  while (wall < end) {
    const w = new Date(wall);
    const [y, mo, d, h] = [w.getUTCFullYear(), w.getUTCMonth(), w.getUTCDate(), w.getUTCHours()];
    if (!s.month[mo + 1]) {
      wall = Date.UTC(y, mo + 1, 1);
      continue;
    }
    if (!dayMatches(s, w)) {
      wall = Date.UTC(y, mo, d + 1);
      continue;
    }
    if (!s.hour[h]) {
      wall = Date.UTC(y, mo, d, h + 1);
      continue;
    }
    if (!s.minute[w.getUTCMinutes()]) {
      wall += MIN_MS;
      continue;
    }
    const at = place(w, clock, after);
    if (at) return at;
    wall += MIN_MS;
  }
  return null;
}

function dayMatches(s: CronSchedule, wall: Date): boolean {
  const dom = !!s.dom[wall.getUTCDate()];
  const dow = !!s.dow[wall.getUTCDay()];
  return s.domStar || s.dowStar ? dom && dow : dom || dow;
}

// A wall time that does not exist (clocks go forward) is not placed; one
// that exists twice (clocks go back) is the earlier instant after `after`.
function place(wall: Date, clock: WallClock, after: Date): Date | null {
  const [y, mo, d, h, mi] = [wall.getUTCFullYear(), wall.getUTCMonth() + 1, wall.getUTCDate(), wall.getUTCHours(), wall.getUTCMinutes()];
  const same = (x: Date) => {
    const f = clock.fields(x);
    return f.y === y && f.mo === mo && f.d === d && f.h === h && f.mi === mi;
  };
  const at = clock.toInstant(y, mo, d, h, mi);
  if (!same(at)) return null;
  // Look for the other occurrence of a repeated time (shifts are 30 or 60 minutes).
  let best = at;
  for (const shift of [-60, -30, 30, 60]) {
    const o = new Date(at.getTime() + shift * MIN_MS);
    if (same(o) && o > after && (!(best > after) || o < best)) best = o;
  }
  return best > after ? best : null;
}

/** The next `count` runs after `from`; fewer when the schedule runs out, none when it is invalid or empty. */
export function nextRuns(spec: string, from: Date, count: number, clock: WallClock = localClock): Date[] {
  const p = parseCron(spec);
  if (!p.ok) return [];
  const out: Date[] = [];
  let at: Date | null = from;
  while (out.length < count && (at = nextRun(p.schedule, at, clock))) out.push(at);
  return out;
}

// ---------------------------------------------------------------- description

const DAY_NAMES = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday'];
const MONTH_NAMES = ['', 'January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];

const SHORTHANDS: Record<string, string> = {
  '@hourly': 'Hourly',
  '@daily': 'Daily at 00:00',
  '@midnight': 'Daily at 00:00',
  '@weekly': 'Weekly on Sunday at 00:00',
  '@monthly': 'Monthly on day 1 at 00:00',
  '@yearly': 'Yearly on January 1 at 00:00',
  '@annually': 'Yearly on January 1 at 00:00',
};

/** One comma-separated item of a valid field: a value, a range or anything with a step. */
type Item = { v: number } | { a: number; b: number } | { step: true };

function items(expr: string, f: FieldDef): Item[] {
  return expr.split(',').map((p): Item => {
    if (p.includes('/') || p === '*' || p === '?') return { step: true };
    const num = (s: string) => (f.names && Object.hasOwn(f.names, s) ? f.names[s] : parseInt(s, 10));
    if (p.includes('-')) {
      const i = p.indexOf('-');
      return { a: num(p.slice(0, i)), b: num(p.slice(i + 1)) };
    }
    return { v: num(p) };
  });
}

/** The values of a field made only of single values, else null. */
function singles(expr: string, f: FieldDef): number[] | null {
  const list = items(expr, f);
  return list.every((i) => 'v' in i) ? list.map((i) => (i as { v: number }).v) : null;
}

function joinList(list: string[]): string {
  return list.length <= 1 ? (list[0] ?? '') : `${list.slice(0, -1).join(', ')} and ${list[list.length - 1]}`;
}

/** "Monday through Friday", "1 and 15"; null when the field has steps or "*". */
function simpleList(expr: string, f: FieldDef, name: (n: number) => string): string | null {
  const list = items(expr, f);
  if (list.some((i) => 'step' in i)) return null;
  return joinList(list.map((i) => ('v' in i ? name(i.v) : `${name((i as { a: number }).a)} through ${name((i as { b: number }).b)}`)));
}

const pad = (n: number) => String(n).padStart(2, '0');
const every = (n: number, unit: string) => (n === 1 ? `Every ${unit}` : `Every ${n} ${unit}s`);
const stepOf = (expr: string) => {
  const m = /^[*?]\/(\d+)$/.exec(expr);
  return m ? parseInt(m[1], 10) : null;
};
const isStar = (expr: string) => expr === '*' || expr === '?';

function timePhrase(min: string, hour: string): string {
  const mStar = isStar(min);
  const hStar = isStar(hour);
  const mStep = stepOf(min);
  const hStep = stepOf(hour);
  const mVals = singles(min, MINUTE);
  const hVals = singles(hour, HOUR);
  const hRange = /^(\d+)-(\d+)$/.exec(hour);

  if (mStar && hStar) return 'Every minute';
  if (mStep && hStar) return every(mStep, 'minute');
  if (mVals?.length === 1 && (hStar || hStep)) {
    const n = hStep ?? 1;
    if (mVals[0] === 0) return every(n, 'hour');
    return `At minute ${mVals[0]} past ${n === 1 ? 'every hour' : `every ${n} hours`}`;
  }
  if (mVals && hVals && mVals.length * hVals.length <= 6) {
    const times = hVals.flatMap((h) => mVals.map((m) => h * 60 + m)).sort((a, b) => a - b);
    return `At ${joinList(times.map((t) => `${pad(Math.floor(t / 60))}:${pad(t % 60)}`))}`;
  }
  if (mStar || mStep) {
    const lead = mStep ? every(mStep, 'minute') : 'Every minute';
    const span = hRange ? [+hRange[1], +hRange[2]] : hVals?.length === 1 ? [hVals[0], hVals[0]] : null;
    if (span) return `${lead} from ${pad(span[0])}:00 through ${pad(span[1])}:59`;
    return `${lead} during ${hStep ? `every ${hStep} hours` : `hours ${hour}`}`;
  }
  const mp = mVals?.length === 1 ? `minute ${mVals[0]}` : `minutes ${min}`;
  const hp = hStar ? 'every hour' : hStep ? `every ${hStep} hours` : hVals?.length === 1 ? `hour ${hVals[0]}` : `hours ${hour}`;
  return `At ${mp} past ${hp}`;
}

function dayPhrase(dom: string, dow: string): string {
  const domText = isStar(dom)
    ? ''
    : (() => {
        const v = singles(dom, DOM);
        if (v?.length === 1) return `on day ${v[0]} of the month`;
        const l = simpleList(dom, DOM, String);
        return `on days ${l ?? dom} of the month`;
      })();
  const dowList = isStar(dow) ? '' : simpleList(dow, DOW, (n) => DAY_NAMES[n]);
  const dowText = isStar(dow) ? '' : dowList ? `on ${dowList}` : `on days of the week ${dow}`;
  if (!domText || !dowText) return domText || dowText;
  const star = (e: string) => e.startsWith('*') || e.startsWith('?');
  // Both restricted: a day matching either runs. Otherwise both must match.
  if (!star(dom) && !star(dow)) return `${domText} and ${dowText}`;
  return `${domText}, if it is ${dowList ?? `day of the week ${dow}`}`;
}

function monthPhrase(month: string): string {
  if (isStar(month)) return '';
  const l = simpleList(month, MONTH, (n) => MONTH_NAMES[n]);
  return l ? `in ${l}` : `in months ${month}`;
}

/** A schedule in plain English ("At 03:00 on Monday through Friday"). */
export function describeCron(spec: string): string {
  const s = spec.trim().toLowerCase();
  if (!s) return 'Only when started manually';
  const p = parseCron(s);
  if (!p.ok) return 'Invalid schedule';
  if (p.schedule.everySec > 0) {
    const sec = p.schedule.everySec;
    return sec === 60 ? 'Every minute' : sec === 3600 ? 'Every hour' : `Every ${formatSpan(sec)}`;
  }
  if (SHORTHANDS[s]) return SHORTHANDS[s];
  const [min, hour, dom, month, dow] = s.split(/\s+/);
  return [timePhrase(min, hour), dayPhrase(dom, dow), monthPhrase(month)].filter(Boolean).join(' ');
}
