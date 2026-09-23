import { Badge, type Tone } from './Badge';
import type { SiteState } from '@/api/types';
import { siteTypeLabel } from '@/lib/siteDefaults';
import { daysUntil } from '@/lib/format';

export function stateTone(state: string | undefined): Tone {
  switch (state) {
    case 'running':
    case 'ready':
    case 'valid':
    case 'succeeded':
    case 'installed':
      return 'green';
    case 'starting':
    case 'stopping':
    case 'degraded':
    case 'unhealthy':
    case 'pending':
    case 'installing':
    case 'warning':
      return 'amber';
    case 'failed':
    case 'crashed':
    case 'error':
    case 'expired':
      return 'red';
    default:
      return 'gray';
  }
}

const transient = new Set(['starting', 'stopping', 'pending', 'installing', 'running-deploy']);

export function StateBadge({ state, title }: { state: SiteState | string | undefined; title?: string }) {
  const s = state || 'unknown';
  return (
    <Badge tone={stateTone(s)} dot pulse={transient.has(s)} title={title} className="capitalize">
      {s}
    </Badge>
  );
}

const typeTone: Record<string, Tone> = { node: 'green', proxy: 'blue', static: 'violet', redirect: 'gray' };

export function SiteTypeBadge({ type }: { type: string }) {
  return <Badge tone={typeTone[type] ?? 'gray'}>{siteTypeLabel(type)}</Badge>;
}

export function LevelBadge({ level }: { level: string }) {
  const tone: Tone = level === 'error' ? 'red' : level === 'warning' ? 'amber' : 'blue';
  return (
    <Badge tone={tone} className="capitalize">
      {level}
    </Badge>
  );
}

export function RoleBadge({ role }: { role: string }) {
  const tone: Tone = role === 'admin' ? 'accent' : role === 'operator' ? 'blue' : 'gray';
  // "sites": allowed on selected sites only, with a role per site.
  return (
    <Badge tone={tone} className={role === 'sites' ? undefined : 'capitalize'}>
      {role === 'sites' ? 'Selected sites' : role}
    </Badge>
  );
}

export function expiryTone(days: number | null, warnDays = 30): Tone {
  if (days === null) return 'gray';
  if (days < 0) return 'red';
  if (days <= 7) return 'red';
  if (days <= warnDays) return 'amber';
  return 'green';
}

export function DaysLeft({ notAfter, warnDays }: { notAfter?: string | null; warnDays?: number }) {
  const d = daysUntil(notAfter);
  if (d === null) return <span className="text-zinc-400">—</span>;
  const tone = expiryTone(d, warnDays);
  const cls =
    tone === 'red'
      ? 'text-red-600 dark:text-red-400'
      : tone === 'amber'
        ? 'text-amber-600 dark:text-amber-400'
        : 'text-emerald-700 dark:text-emerald-400';
  return <span className={`font-medium tabular ${cls}`}>{d < 0 ? `expired ${-d}d ago` : d === 0 ? 'today' : `${d}d left`}</span>;
}
