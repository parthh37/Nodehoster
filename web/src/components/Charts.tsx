// Small dependency-free SVG charts.
import { useMemo, useState } from 'react';
import { useElementSize } from '@/hooks/useElementSize';
import { cn } from '@/lib/cn';
import { formatHourMinute, formatTime } from '@/lib/format';

export interface ChartSeries {
  label: string;
  values: number[];
  /** Tailwind text color classes; the chart draws with currentColor. */
  className: string;
  /** Draw as filled area (default) or line only. */
  line?: boolean;
}

function niceMax(v: number): number {
  if (v <= 0) return 1;
  const exp = Math.pow(10, Math.floor(Math.log10(v)));
  const f = v / exp;
  const nice = f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10;
  return nice * exp;
}

function buildPath(values: number[], x: (i: number) => number, y: (v: number) => number): string {
  let d = '';
  values.forEach((v, i) => {
    d += `${i === 0 ? 'M' : 'L'}${x(i).toFixed(1)},${y(v).toFixed(1)}`;
  });
  return d;
}

export function AreaChart({
  times,
  series,
  height = 160,
  format = (v) => String(Math.round(v)),
  className,
  emptyText = 'No data yet',
}: {
  times: (string | Date)[];
  series: ChartSeries[];
  height?: number;
  format?: (v: number) => string;
  className?: string;
  emptyText?: string;
}) {
  const [ref, { width }] = useElementSize<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const padL = 44;
  const padR = 8;
  const padT = 8;
  const padB = 20;
  const n = times.length;
  const innerW = Math.max(1, width - padL - padR);
  const innerH = Math.max(1, height - padT - padB);

  const max = useMemo(() => {
    let m = 0;
    for (const s of series) for (const v of s.values) if (Number.isFinite(v) && v > m) m = v;
    return niceMax(m);
  }, [series]);

  const x = (i: number) => padL + (n <= 1 ? innerW / 2 : (i / (n - 1)) * innerW);
  const y = (v: number) => padT + innerH - (Math.max(0, Number.isFinite(v) ? v : 0) / max) * innerH;

  const ticks = [0, 0.5, 1].map((f) => f * max);
  const xLabels = n > 1 ? [0, Math.floor((n - 1) / 2), n - 1] : n === 1 ? [0] : [];

  return (
    <div ref={ref} className={cn('relative w-full select-none', className)} style={{ height }}>
      {n === 0 ? (
        <div className="flex h-full items-center justify-center text-xs text-zinc-400">{emptyText}</div>
      ) : width > 0 ? (
        <svg
          width={width}
          height={height}
          className="overflow-visible"
          onMouseLeave={() => setHover(null)}
          onMouseMove={(e) => {
            const rect = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
            const px = e.clientX - rect.left - padL;
            const i = n <= 1 ? 0 : Math.round((px / innerW) * (n - 1));
            setHover(Math.max(0, Math.min(n - 1, i)));
          }}
        >
          {ticks.map((t, i) => (
            <g key={i}>
              <line
                x1={padL}
                x2={width - padR}
                y1={y(t)}
                y2={y(t)}
                className="stroke-zinc-200 dark:stroke-zinc-800"
                strokeDasharray={i === 0 ? undefined : '3 3'}
              />
              <text x={padL - 6} y={y(t)} dy="0.32em" textAnchor="end" className="fill-zinc-400 text-[10px] tabular">
                {format(t)}
              </text>
            </g>
          ))}
          {xLabels.map((i) => (
            <text
              key={i}
              x={x(i)}
              y={height - 4}
              textAnchor={i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle'}
              className="fill-zinc-400 text-[10px] tabular"
            >
              {formatHourMinute(times[i])}
            </text>
          ))}
          {series.map((s, si) => {
            const line = buildPath(s.values, x, y);
            const area = `${line}L${x(s.values.length - 1).toFixed(1)},${y(0)}L${x(0).toFixed(1)},${y(0)}Z`;
            return (
              <g key={si} className={s.className}>
                {!s.line && <path d={area} fill="currentColor" fillOpacity={0.12} />}
                <path d={line} fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" />
              </g>
            );
          })}
          {hover !== null && (
            <g>
              <line x1={x(hover)} x2={x(hover)} y1={padT} y2={padT + innerH} className="stroke-zinc-400 dark:stroke-zinc-500" strokeDasharray="2 2" />
              {series.map((s, si) => (
                <circle key={si} cx={x(hover)} cy={y(s.values[hover] ?? 0)} r={3} className={cn(s.className, 'fill-current stroke-white dark:stroke-zinc-900')} strokeWidth={1.5} />
              ))}
            </g>
          )}
        </svg>
      ) : null}
      {hover !== null && n > 0 && width > 0 && (
        <div
          className="pointer-events-none absolute top-0 z-10 rounded-md border border-zinc-200 bg-white/95 px-2 py-1.5 text-xs shadow-pop dark:border-zinc-700 dark:bg-zinc-900/95"
          style={{
            left: Math.min(Math.max(x(hover) + 10, 0), width - 150),
          }}
        >
          <div className="mb-0.5 font-medium text-zinc-500 tabular">{formatTime(times[hover])}</div>
          {series.map((s, si) => (
            <div key={si} className="flex items-center gap-2 whitespace-nowrap">
              <span className={cn('inline-block h-2 w-2 rounded-full bg-current', s.className)} />
              <span className="text-zinc-600 dark:text-zinc-300">{s.label}</span>
              <span className="ml-auto pl-3 font-medium tabular text-zinc-900 dark:text-zinc-100">{format(s.values[hover] ?? 0)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

export function Sparkline({
  values,
  className = 'text-accent-600 dark:text-accent-400',
  height = 28,
  width = 96,
  fill = true,
}: {
  values: number[];
  className?: string;
  height?: number;
  width?: number;
  fill?: boolean;
}) {
  if (values.length < 2) {
    return <svg width={width} height={height} />;
  }
  const max = Math.max(...values, 1e-9);
  const x = (i: number) => (i / (values.length - 1)) * width;
  const y = (v: number) => height - 1 - (Math.max(0, v) / max) * (height - 2);
  const line = buildPath(values, x, y);
  return (
    <svg width={width} height={height} className={className} aria-hidden>
      {fill && <path d={`${line}L${width},${height}L0,${height}Z`} fill="currentColor" fillOpacity={0.12} />}
      <path d={line} fill="none" stroke="currentColor" strokeWidth={1.25} strokeLinejoin="round" />
    </svg>
  );
}

export function ChartLegend({ series }: { series: { label: string; className: string }[] }) {
  return (
    <div className="flex flex-wrap items-center gap-3 text-xs text-zinc-500 dark:text-zinc-400">
      {series.map((s) => (
        <span key={s.label} className="inline-flex items-center gap-1.5">
          <span className={cn('inline-block h-2 w-2 rounded-full bg-current', s.className)} />
          {s.label}
        </span>
      ))}
    </div>
  );
}
