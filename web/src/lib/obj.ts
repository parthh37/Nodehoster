/** Deep clone for JSON-compatible data. */
export function clone<T>(v: T): T {
  return typeof structuredClone === 'function' ? structuredClone(v) : (JSON.parse(JSON.stringify(v)) as T);
}

/** Structural equality for JSON-compatible data, treating undefined, null, empty arrays and empty strings as equal to missing. */
export function jsonEqual(a: unknown, b: unknown): boolean {
  return JSON.stringify(normalize(a)) === JSON.stringify(normalize(b));
}

function normalize(v: unknown): unknown {
  if (Array.isArray(v)) return v.length ? v.map(normalize) : undefined;
  if (v && typeof v === 'object') {
    const out: Record<string, unknown> = {};
    for (const k of Object.keys(v as object).sort()) {
      const n = normalize((v as Record<string, unknown>)[k]);
      if (n !== undefined && n !== null && n !== '') out[k] = n;
    }
    return Object.keys(out).length ? out : undefined;
  }
  return v;
}

export function moveItem<T>(list: T[], from: number, to: number): T[] {
  if (to < 0 || to >= list.length) return list;
  const next = list.slice();
  const [item] = next.splice(from, 1);
  next.splice(to, 0, item);
  return next;
}

/** Parse an integer from an input, returning 0 for empty/invalid. */
export function toInt(v: string): number {
  const n = parseInt(v, 10);
  return Number.isFinite(n) ? n : 0;
}

export function toFloat(v: string): number {
  const n = parseFloat(v);
  return Number.isFinite(n) ? n : 0;
}

export function splitLines(v: string): string[] {
  return v
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

export function splitArgs(v: string): string[] {
  // Split a command-line style string honoring simple quotes.
  const out: string[] = [];
  const re = /"([^"]*)"|'([^']*)'|(\S+)/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(v))) out.push(m[1] ?? m[2] ?? m[3]);
  return out;
}

export function joinArgs(args: string[] | undefined): string {
  return (args ?? []).map((a) => (/\s/.test(a) ? `"${a}"` : a)).join(' ');
}
