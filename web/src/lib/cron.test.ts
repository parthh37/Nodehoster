import { describe, expect, it } from 'vitest';
import { describeCron, localClock, nextRun, nextRuns, parseCron, parseGoDuration, utcClock, type WallClock } from './cron';

// Next-run tests use an explicit clock so they do not depend on the time
// zone of the machine running them; DST cases use America/New_York through
// Intl (clocks go 02:00 -> 03:00 on 8 March 2026 and 02:00 -> 01:00 on
// 1 November 2026), mirroring internal/cron/cron_test.go.

function zoneClock(timeZone: string): WallClock {
  const fmt = new Intl.DateTimeFormat('en-US', {
    timeZone,
    hourCycle: 'h23',
    year: 'numeric',
    month: 'numeric',
    day: 'numeric',
    hour: 'numeric',
    minute: 'numeric',
  });
  const fields = (t: Date) => {
    const p = Object.fromEntries(fmt.formatToParts(t).map((x) => [x.type, x.value]));
    return { y: +p.year, mo: +p.month, d: +p.day, h: +p.hour, mi: +p.minute };
  };
  // Offset of the zone at an instant, in ms (wall - UTC).
  const offset = (ms: number) => {
    const f = fields(new Date(ms));
    return Date.UTC(f.y, f.mo - 1, f.d, f.h, f.mi) - Math.floor(ms / 60_000) * 60_000;
  };
  return {
    fields,
    toInstant: (y, mo, d, h, mi) => {
      const wall = Date.UTC(y, mo - 1, d, h, mi);
      return new Date(wall - offset(wall - offset(wall)));
    },
  };
}

const ny = zoneClock('America/New_York');

/** "2026-03-10 14:08" in the clock's zone. */
function wall(t: Date, clock: WallClock = utcClock): string {
  const f = clock.fields(t);
  const p = (n: number) => String(n).padStart(2, '0');
  return `${f.y}-${p(f.mo)}-${p(f.d)} ${p(f.h)}:${p(f.mi)}`;
}

function runs(spec: string, from: Date, count: number, clock: WallClock = utcClock): string[] {
  return nextRuns(spec, from, count, clock).map((t) => wall(t, clock));
}

function mustParse(spec: string) {
  const p = parseCron(spec);
  if (!p.ok) throw new Error(`parseCron(${spec}): ${p.error}`);
  return p.schedule;
}

describe('parseCron', () => {
  it.each([
    ['', 'the schedule is empty'],
    ['   ', 'the schedule is empty'],
    ['* * * *', 'a cron expression has 5 fields (minute hour day-of-month month day-of-week), this one has 4'],
    ['* * * * * *', 'this one has 6'],
    ['60 * * * *', 'minute: 60 is out of range (0-59)'],
    ['* 24 * * *', 'hour: 24 is out of range (0-23)'],
    ['* * 0 * *', 'day of month: 0 is out of range (1-31)'],
    ['* * 32 * *', 'day of month: 32 is out of range'],
    ['* * * 13 *', 'month: 13 is out of range (1-12)'],
    ['* * * * 8', 'day of week: 8 is out of range (0-7)'],
    ['*/0 * * * *', 'minute: "0" is not a valid step'],
    ['*/x * * * *', 'minute: "x" is not a valid step'],
    ['5-1 * * * *', 'minute: range "5-1" runs backwards'],
    ['1,,2 * * * *', 'minute: empty value in a list'],
    ['* * * foo *', 'month: "foo" is not a number or a name'],
    ['* * * * funday', 'day of week: "funday" is not a number or a name'],
    ['a * * * *', 'minute: "a" is not a number'],
    ['* * * * constructor', 'is not a number or a name'],
    ['@often', 'unknown shorthand "@often" (use @hourly, @daily, @weekly, @monthly, @yearly or @every <duration>)'],
    ['@every', '"@every" needs a duration, e.g. "@every 15m"'],
    ['@every soon', '"soon" is not a duration (use e.g. 90s, 15m, 2h, 1h30m)'],
    ['@every 1h 30m', 'is not a duration'],
    ['@every 30s', 'the shortest interval is 1m0s'],
    ['@every -5m', 'the shortest interval'],
  ])('rejects %j', (spec, want) => {
    const p = parseCron(spec);
    expect(p.ok).toBe(false);
    if (!p.ok) expect(p.error).toContain(want);
  });

  it.each(['* * * * *', '*/15 * * * *', '0 3 * * mon-fri', '0 8 * * SAT,sun', '0 0 1 JAN-mar *', '0 0 * * 7', '5/20 * * * *', '? ? ? ? ?', '  0  3  *  *  *  ', '@daily', '@Midnight', '@every 15m', '@every 1h30m', '@EVERY 90s'])(
    'accepts %j',
    (spec) => expect(parseCron(spec).ok).toBe(true),
  );

  it('folds 7 onto Sunday', () => {
    const s = mustParse('0 0 * * 5-7');
    expect(s.dow.map((v, i) => (v ? i : -1)).filter((i) => i >= 0)).toEqual([0, 5, 6]);
  });

  it('expands "a/n" to the end of the field and "*" of day of week to Saturday', () => {
    const s = mustParse('5/20 * * * */2');
    expect(s.minute.map((v, i) => (v ? i : -1)).filter((i) => i >= 0)).toEqual([5, 25, 45]);
    expect(s.dow.map((v, i) => (v ? i : -1)).filter((i) => i >= 0)).toEqual([0, 2, 4, 6]);
  });

  it('records whether the day fields start with a star', () => {
    expect(mustParse('0 0 */2 * mon')).toMatchObject({ domStar: true, dowStar: false });
    expect(mustParse('0 0 1 * *')).toMatchObject({ domStar: false, dowStar: true });
  });

  it('parses @every intervals, truncated to whole seconds', () => {
    expect(mustParse('@every 1h30m').everySec).toBe(5400);
    expect(mustParse('@every 90.5s').everySec).toBe(90);
    expect(mustParse('@every 1m').everySec).toBe(60);
  });
});

describe('parseGoDuration', () => {
  it.each([
    ['0', 0],
    ['15m', 900],
    ['1h30m', 5400],
    ['1.5h', 5400],
    ['90s', 90],
    ['500ms', 0.5],
    ['-2m', -120],
    ['+1h', 3600],
    ['.5m', 30],
  ])('%s = %d seconds', (s, want) => expect(parseGoDuration(s)).toBeCloseTo(want, 6));

  it.each(['', '15', 'm', '.m', '1d', '1h 30m', 'soon', '1constructor'])('rejects %j', (s) => expect(parseGoDuration(s)).toBeNull());
});

describe('nextRuns', () => {
  const from = new Date(Date.UTC(2026, 2, 10, 14, 7, 30)); // a Tuesday

  it.each<[string, string[]]>([
    ['* * * * *', ['2026-03-10 14:08', '2026-03-10 14:09']],
    ['*/15 * * * *', ['2026-03-10 14:15', '2026-03-10 14:30', '2026-03-10 14:45', '2026-03-10 15:00']],
    ['5/20 * * * *', ['2026-03-10 14:25', '2026-03-10 14:45', '2026-03-10 15:05']],
    ['0 9-17/4 * * *', ['2026-03-10 17:00', '2026-03-11 09:00', '2026-03-11 13:00']],
    ['30 2 * * *', ['2026-03-11 02:30', '2026-03-12 02:30']],
    ['0 0,12 * * *', ['2026-03-11 00:00', '2026-03-11 12:00']],
    ['0 8 * * mon-fri', ['2026-03-11 08:00', '2026-03-12 08:00', '2026-03-13 08:00', '2026-03-16 08:00']],
    ['0 8 * * SAT,sun', ['2026-03-14 08:00', '2026-03-15 08:00', '2026-03-21 08:00']],
    ['0 8 * * 7', ['2026-03-15 08:00']],
    ['0 0 1 jan-mar *', ['2027-01-01 00:00', '2027-02-01 00:00']],
    // Months without a 31st are skipped.
    ['0 0 31 * *', ['2026-03-31 00:00', '2026-05-31 00:00', '2026-07-31 00:00']],
    ['0 0 29 2 *', ['2028-02-29 00:00', '2032-02-29 00:00']],
    // Both day fields restricted: either matches (Vixie cron).
    ['0 0 13 * fri', ['2026-03-13 00:00', '2026-03-20 00:00', '2026-03-27 00:00', '2026-04-03 00:00', '2026-04-10 00:00', '2026-04-13 00:00']],
    // Day of week restricted, day of month "*": both must match.
    ['0 0 * * fri', ['2026-03-13 00:00', '2026-03-20 00:00']],
    // "*/10" on the day of month starts with a star, so it is AND-ed with the day of week.
    ['0 0 */10 * fri', ['2026-05-01 00:00', '2026-07-31 00:00']],
    ['0 0 */10 * *', ['2026-03-11 00:00', '2026-03-21 00:00', '2026-03-31 00:00', '2026-04-01 00:00']],
    ['@hourly', ['2026-03-10 15:00', '2026-03-10 16:00']],
    ['@daily', ['2026-03-11 00:00']],
    ['@weekly', ['2026-03-15 00:00', '2026-03-22 00:00']],
    ['@monthly', ['2026-04-01 00:00', '2026-05-01 00:00']],
    ['@YEARLY', ['2027-01-01 00:00']],
  ])('%s', (spec, want) => {
    expect(runs(spec, from, want.length)).toEqual(want);
  });

  it('returns nothing for a schedule that never fires', () => {
    expect(nextRuns('0 0 30 2 *', from, 3, utcClock)).toEqual([]);
    expect(nextRun(mustParse('0 0 31 4,6,9,11 *'), from, utcClock)).toBeNull();
  });

  it('returns nothing for an empty or invalid schedule', () => {
    expect(nextRuns('', from, 3)).toEqual([]);
    expect(nextRuns('61 * * * *', from, 3)).toEqual([]);
  });

  it('runs "@every" from the given time, on whole seconds', () => {
    const t = new Date(Date.UTC(2026, 0, 1, 10, 0, 0, 500));
    expect(nextRuns('@every 1h30m', t, 2).map((d) => d.toISOString())).toEqual(['2026-01-01T11:30:00.000Z', '2026-01-01T13:00:00.000Z']);
  });

  it('uses the browser time zone by default', () => {
    // Midday in June: no DST change anywhere at that time.
    const start = new Date(2026, 5, 15, 8, 20);
    expect(nextRuns('0 12 * * *', start, 2)).toEqual([new Date(2026, 5, 15, 12, 0), new Date(2026, 5, 16, 12, 0)]);
    expect(nextRun(mustParse('*/15 * * * *'), start, localClock)).toEqual(new Date(2026, 5, 15, 8, 30));
  });
});

describe('nextRuns around DST (America/New_York)', () => {
  const iso = (d: Date[]) => d.map((x) => x.toISOString().slice(0, 16));

  it('does not run a wall time skipped when clocks go forward', () => {
    // 12:00 EST on 7 March; 02:30 does not exist on the 8th.
    expect(iso(nextRuns('30 2 * * *', new Date('2026-03-07T17:00:00Z'), 2, ny))).toEqual(['2026-03-09T06:30', '2026-03-10T06:30']);
    // 01:45 EST is followed by 03:00 EDT.
    expect(iso(nextRuns('*/15 * * * *', new Date('2026-03-08T06:45:00Z'), 1, ny))).toEqual(['2026-03-08T07:00']);
  });

  it('runs a repeated wall time once, the first time', () => {
    const [first, second] = nextRuns('30 1 * * *', new Date('2026-10-31T16:00:00Z'), 2, ny);
    expect(first.toISOString()).toBe('2026-11-01T05:30:00.000Z'); // 01:30 EDT
    expect(second.toISOString()).toBe('2026-11-02T06:30:00.000Z'); // 01:30 EST the next day

    // Every 20 minutes through the night: each wall time once.
    const night = nextRuns('*/20 * * * *', new Date('2026-11-01T04:30:00Z'), 6, ny);
    expect(night.map((d) => wall(d, ny))).toEqual([
      '2026-11-01 00:40',
      '2026-11-01 01:00',
      '2026-11-01 01:20',
      '2026-11-01 01:40',
      '2026-11-01 02:00',
      '2026-11-01 02:20',
    ]);
    expect(iso(night.slice(3, 5))).toEqual(['2026-11-01T05:40', '2026-11-01T07:00']);
  });

  it('starting inside the repeated hour picks the next instant, not the gone one', () => {
    // 01:10 EST, the second 01:10: the next quarter hour is 01:15 EST.
    const got = nextRun(mustParse('*/15 * * * *'), new Date('2026-11-01T06:10:00Z'), ny);
    expect(got?.toISOString()).toBe('2026-11-01T06:15:00.000Z');
  });

  it('"@every" ignores the wall clock', () => {
    const from = new Date('2026-03-08T06:30:00Z'); // 01:30 EST
    const [got] = nextRuns('@every 1h', from, 1, ny);
    expect(got.getTime() - from.getTime()).toBe(3600_000);
    expect(wall(got, ny)).toBe('2026-03-08 03:30');
  });
});

describe('describeCron', () => {
  it.each([
    ['', 'Only when started manually'],
    ['  ', 'Only when started manually'],
    ['* * * * *', 'Every minute'],
    ['*/1 * * * *', 'Every minute'],
    ['*/15 * * * *', 'Every 15 minutes'],
    ['0 * * * *', 'Every hour'],
    ['0 */2 * * *', 'Every 2 hours'],
    ['5 * * * *', 'At minute 5 past every hour'],
    ['30 */6 * * *', 'At minute 30 past every 6 hours'],
    ['0 3 * * *', 'At 03:00'],
    ['30 2 * * *', 'At 02:30'],
    ['0 3 * * 1-5', 'At 03:00 on Monday through Friday'],
    ['0 3 * * MON-FRI', 'At 03:00 on Monday through Friday'],
    ['0 8,20 * * *', 'At 08:00 and 20:00'],
    ['0,30 9 * * *', 'At 09:00 and 09:30'],
    ['0 6,12,18 * * sat,sun', 'At 06:00, 12:00 and 18:00 on Saturday and Sunday'],
    ['0 8 * * 7', 'At 08:00 on Sunday'],
    ['0 8 * * 1,3,5', 'At 08:00 on Monday, Wednesday and Friday'],
    ['0 0 1 * *', 'At 00:00 on day 1 of the month'],
    ['0 0 1,15 * *', 'At 00:00 on days 1 and 15 of the month'],
    ['0 0 1-7 * *', 'At 00:00 on days 1 through 7 of the month'],
    ['0 0 13 * fri', 'At 00:00 on day 13 of the month and on Friday'],
    ['0 0 */2 * mon', 'At 00:00 on days */2 of the month, if it is Monday'],
    ['0 0 1 1 *', 'At 00:00 on day 1 of the month in January'],
    ['0 12 * jun-aug *', 'At 12:00 in June through August'],
    ['*/15 9-17 * * 1-5', 'Every 15 minutes from 09:00 through 17:59 on Monday through Friday'],
    ['* 9 * * *', 'Every minute from 09:00 through 09:59'],
    ['*/5 */2 * * *', 'Every 5 minutes during every 2 hours'],
    ['0,30 9-17 * * 1-5', 'At minutes 0,30 past hours 9-17 on Monday through Friday'],
    ['0,30 9-17 * jan-mar mon-fri', 'At minutes 0,30 past hours 9-17 on Monday through Friday in January through March'],
    ['0 9-17/4 * * *', 'At minute 0 past hours 9-17/4'],
    ['0 0 */10 * *', 'At 00:00 on days */10 of the month'],
    ['0 0 * */3 *', 'At 00:00 in months */3'],
    ['0 0 * * */2', 'At 00:00 on days of the week */2'],
    ['@hourly', 'Hourly'],
    ['@daily', 'Daily at 00:00'],
    ['@midnight', 'Daily at 00:00'],
    ['@weekly', 'Weekly on Sunday at 00:00'],
    ['@monthly', 'Monthly on day 1 at 00:00'],
    ['@Yearly', 'Yearly on January 1 at 00:00'],
    ['@annually', 'Yearly on January 1 at 00:00'],
    ['@every 1m', 'Every minute'],
    ['@every 1h', 'Every hour'],
    ['@every 15m', 'Every 15 minutes'],
    ['@every 1h30m', 'Every 1 hour 30 minutes'],
    ['@every 90s', 'Every 1 minute 30 seconds'],
    ['@every 48h', 'Every 2 days'],
    ['61 * * * *', 'Invalid schedule'],
  ])('%j -> %s', (spec, want) => expect(describeCron(spec)).toBe(want));
});
