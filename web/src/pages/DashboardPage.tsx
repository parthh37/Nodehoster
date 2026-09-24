import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Activity, BadgeCheck, Boxes, Clock, Cpu, HardDrive, MemoryStick, Plus, Server, Tag } from 'lucide-react';
import { certsApi, serverApi, sitesApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { NHEvent, SiteStatus } from '@/api/types';
import { useLive } from '@/hooks/useLive';
import { usePermissions } from '@/hooks/useAuth';
import { useNow } from '@/hooks/useNow';
import { Card, EmptyState, Loading, PageHeader, ProgressBar, Stat } from '@/components/Layout';
import { AreaChart, ChartLegend } from '@/components/Charts';
import { Button } from '@/components/Button';
import { DaysLeft, LevelBadge, StateBadge } from '@/components/StatusBadges';
import { Dot } from '@/components/Badge';
import { daysUntil, formatBytes, formatCompact, formatDate, formatNumber, formatPercent, formatUptime, relativeTime } from '@/lib/format';
import { cn } from '@/lib/cn';
import { AlertsBanner } from './alerts/AlertsBanner';

export function DashboardPage() {
  const now = useNow(10_000);
  const info = useQuery({ queryKey: qk.serverInfo, queryFn: serverApi.info, refetchInterval: 10_000 });
  const metrics = useQuery({ queryKey: qk.serverMetrics(60), queryFn: () => serverApi.metrics(60), refetchInterval: 60_000 });
  const sites = useQuery({ queryKey: qk.sites, queryFn: sitesApi.list });
  const certs = useQuery({ queryKey: qk.certs, queryFn: certsApi.list });
  const { statuses, events: liveEvents } = useLive();
  const { isAdmin } = usePermissions();

  const s = info.data;
  const memPct = s && s.memTotal ? (s.memUsed / s.memTotal) * 100 : 0;
  const diskUsed = s ? s.diskTotal - s.diskFree : 0;
  const diskPct = s && s.diskTotal ? (diskUsed / s.diskTotal) * 100 : 0;

  const counts = useMemo(() => {
    const c = { running: 0, transitional: 0, stopped: 0, failed: 0, total: 0 };
    for (const site of sites.data ?? []) {
      const st: SiteStatus | undefined = statuses[site.id] ?? site.status;
      c.total++;
      switch (st?.state) {
        case 'running':
          c.running++;
          break;
        case 'failed':
          c.failed++;
          break;
        case 'starting':
        case 'stopping':
        case 'degraded':
          c.transitional++;
          break;
        default:
          c.stopped++;
      }
    }
    return c;
  }, [sites.data, statuses]);

  const expiring = useMemo(
    () =>
      (certs.data ?? [])
        .filter((c) => {
          const d = daysUntil(c.notAfter);
          return c.status !== 'pending' && d !== null && d <= 30;
        })
        .sort((a, b) => (daysUntil(a.notAfter) ?? 0) - (daysUntil(b.notAfter) ?? 0)),
    [certs.data],
  );

  const points = metrics.data ?? [];
  const totalReq = points.reduce((a, p) => a + p.req, 0);
  const totalErr = points.reduce((a, p) => a + p.err, 0);

  const siteNames = useMemo(() => Object.fromEntries((sites.data ?? []).map((x) => [x.id, x.name])), [sites.data]);

  return (
    <div className="space-y-5">
      <PageHeader title="Dashboard" description={s ? <>Overview of <span className="font-mono">{s.hostname}</span></> : 'Server overview'} />
      <AlertsBanner />

      {/* Server tiles */}
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
        <Stat
          label="CPU"
          icon={<Cpu />}
          value={s ? formatPercent(s.cpuPercent) : '—'}
          sub={s ? `${s.cpuCount} logical processors` : undefined}
          tone={s && s.cpuPercent > 90 ? 'red' : s && s.cpuPercent > 70 ? 'amber' : 'default'}
        >
          <ProgressBar className="mt-2" value={s?.cpuPercent ?? 0} tone={s && s.cpuPercent > 90 ? 'red' : s && s.cpuPercent > 70 ? 'amber' : 'accent'} />
        </Stat>
        <Stat label="Memory" icon={<MemoryStick />} value={s ? formatPercent(memPct, 0) : '—'} sub={s ? `${formatBytes(s.memUsed)} of ${formatBytes(s.memTotal)}` : undefined}>
          <ProgressBar className="mt-2" value={memPct} tone={memPct > 90 ? 'red' : memPct > 75 ? 'amber' : 'accent'} />
        </Stat>
        <Stat label="Disk (data)" icon={<HardDrive />} value={s ? formatPercent(diskPct, 0) : '—'} sub={s ? `${formatBytes(s.diskFree)} free of ${formatBytes(s.diskTotal)}` : undefined}>
          <ProgressBar className="mt-2" value={diskPct} tone={diskPct > 90 ? 'red' : diskPct > 80 ? 'amber' : 'accent'} />
        </Stat>
        <Stat label="Uptime" icon={<Clock />} value={s ? formatUptime(s.startedAt, now) : '—'} sub={s ? `since ${formatDate(s.startedAt)}` : undefined} />
        <Stat
          label="Version"
          icon={<Tag />}
          value={s ? <span className="font-mono text-lg">{s.version}</span> : '—'}
          sub={s ? <span className="font-mono">{[s.commit?.slice(0, 7), s.goVersion].filter(Boolean).join(' · ')}</span> : undefined}
        />
        <Stat
          label="Host"
          icon={<Server />}
          value={<span className="font-mono text-lg">{s?.hostname ?? '—'}</span>}
          sub={s ? `${s.os}${s.isService ? ' · Windows service' : ''}` : undefined}
        />
      </div>

      <div className="grid gap-5 lg:grid-cols-3">
        {/* Sites summary */}
        <Card
          title="Sites"
          actions={
            <Link to="/sites" className="nh-link text-xs">
              View all
            </Link>
          }
        >
          {sites.isPending ? (
            <Loading />
          ) : counts.total === 0 ? (
            <EmptyState
              compact
              icon={<Boxes />}
              title="No sites yet"
              description="Create your first site to host a Node.js app, a reverse proxy, static files or a redirect."
              action={
                isAdmin && (
                  <Link to="/sites/new">
                    <Button variant="primary" icon={<Plus className="h-4 w-4" />}>
                      New site
                    </Button>
                  </Link>
                )
              }
            />
          ) : (
            <div>
              <div className="flex items-baseline gap-2">
                <span className="text-3xl font-semibold tabular">{counts.total}</span>
                <span className="text-[13px] text-zinc-500">sites configured</span>
              </div>
              <div className="mt-4 flex h-2 overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800">
                {counts.running > 0 && <div className="bg-emerald-500" style={{ width: `${(counts.running / counts.total) * 100}%` }} />}
                {counts.transitional > 0 && <div className="bg-amber-500" style={{ width: `${(counts.transitional / counts.total) * 100}%` }} />}
                {counts.failed > 0 && <div className="bg-red-500" style={{ width: `${(counts.failed / counts.total) * 100}%` }} />}
              </div>
              <dl className="mt-4 grid grid-cols-2 gap-3 text-[13px]">
                <SummaryItem tone="green" label="Running" value={counts.running} />
                <SummaryItem tone="amber" label="Starting / degraded" value={counts.transitional} />
                <SummaryItem tone="gray" label="Stopped" value={counts.stopped} />
                <SummaryItem tone="red" label="Failed" value={counts.failed} />
              </dl>
              {counts.failed > 0 && (
                <div className="mt-4 space-y-1 border-t border-zinc-200 pt-3 dark:border-zinc-800">
                  {(sites.data ?? [])
                    .filter((x) => (statuses[x.id] ?? x.status)?.state === 'failed')
                    .map((x) => (
                      <Link key={x.id} to={`/sites/${x.id}`} className="flex items-center justify-between rounded px-1 py-0.5 text-[13px] hover:bg-zinc-50 dark:hover:bg-zinc-800/50">
                        <span className="font-medium">{x.name}</span>
                        <StateBadge state="failed" />
                      </Link>
                    ))}
                </div>
              )}
            </div>
          )}
        </Card>

        {/* Server metrics */}
        <Card
          className="lg:col-span-2"
          title="Traffic — last hour"
          description={
            points.length ? (
              <>
                {formatNumber(totalReq)} requests · {formatNumber(totalErr)} errors
                {totalReq > 0 && <> ({formatPercent((totalErr / totalReq) * 100)})</>}
              </>
            ) : undefined
          }
          actions={
            <ChartLegend
              series={[
                { label: 'Requests/min', className: 'text-accent-600 dark:text-accent-400' },
                { label: 'Errors/min', className: 'text-red-500' },
              ]}
            />
          }
        >
          {metrics.isPending ? (
            <Loading />
          ) : (
            <AreaChart
              height={200}
              times={points.map((p) => p.t)}
              format={formatCompact}
              emptyText="No traffic recorded in the last hour"
              series={[
                { label: 'Requests', values: points.map((p) => p.req), className: 'text-accent-600 dark:text-accent-400' },
                { label: 'Errors', values: points.map((p) => p.err), className: 'text-red-500' },
              ]}
            />
          )}
        </Card>
      </div>

      <div className="grid gap-5 lg:grid-cols-3">
        {/* Expiring certificates */}
        <Card
          title="Certificates expiring soon"
          description="Within 30 days"
          actions={
            <Link to="/certificates" className="nh-link text-xs">
              Manage
            </Link>
          }
          flush
        >
          {certs.isPending ? (
            <Loading />
          ) : expiring.length === 0 ? (
            <EmptyState compact icon={<BadgeCheck />} title="All certificates are healthy" description="Nothing expires in the next 30 days." />
          ) : (
            <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
              {expiring.map((c) => (
                <li key={c.id} className="flex items-center justify-between gap-3 px-4 py-2.5">
                  <div className="min-w-0">
                    <p className="truncate text-[13px] font-medium">{c.name}</p>
                    <p className="truncate font-mono text-xs text-zinc-500">{(c.domains ?? []).join(', ')}</p>
                  </div>
                  <div className="shrink-0 text-right text-xs">
                    <DaysLeft notAfter={c.notAfter} />
                    <p className="text-zinc-500">{c.autoRenew ? 'auto-renew on' : 'manual renewal'}</p>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Card>

        {/* Recent events */}
        <RecentEvents className="lg:col-span-2" live={liveEvents} siteNames={siteNames} />
      </div>
    </div>
  );
}

function SummaryItem({ tone, label, value }: { tone: 'green' | 'amber' | 'gray' | 'red'; label: string; value: number }) {
  return (
    <div className="flex items-center gap-2">
      <Dot tone={tone} />
      <dt className="text-zinc-500 dark:text-zinc-400">{label}</dt>
      <dd className="ml-auto font-semibold tabular">{value}</dd>
    </div>
  );
}

function RecentEvents({ live, siteNames, className }: { live: NHEvent[]; siteNames: Record<string, string>; className?: string }) {
  useNow(30_000);
  const q = useQuery({ queryKey: qk.events(15), queryFn: () => serverApi.events(15) });
  const merged = useMemo(() => {
    const seen = new Set<number>();
    const out: NHEvent[] = [];
    for (const e of [...live, ...(q.data ?? [])]) {
      if (seen.has(e.id)) continue;
      seen.add(e.id);
      out.push(e);
    }
    return out.sort((a, b) => b.time.localeCompare(a.time)).slice(0, 15);
  }, [live, q.data]);

  return (
    <Card
      className={className}
      title="Recent events"
      actions={
        <Link to="/events" className="nh-link text-xs">
          All events
        </Link>
      }
      flush
    >
      {q.isPending ? (
        <Loading />
      ) : merged.length === 0 ? (
        <EmptyState compact icon={<Activity />} title="No events yet" description="Site starts, crashes, deployments and certificate renewals appear here as they happen." />
      ) : (
        <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
          {merged.map((e) => (
            <li key={e.id} className={cn('flex items-start gap-3 px-4 py-2', live.some((l) => l.id === e.id) && 'animate-fade-in')}>
              <div className="w-16 shrink-0 pt-px">
                <LevelBadge level={e.level} />
              </div>
              <div className="min-w-0 flex-1">
                <p className="text-[13px] text-zinc-800 dark:text-zinc-200">{e.message}</p>
                <p className="mt-0.5 flex flex-wrap gap-x-2 text-xs text-zinc-500">
                  <span className="font-mono">{e.type}</span>
                  {e.siteId && (
                    <Link to={`/sites/${e.siteId}`} className="nh-link">
                      {siteNames[e.siteId] ?? e.siteId}
                    </Link>
                  )}
                </p>
              </div>
              <time className="shrink-0 text-xs text-zinc-500" title={new Date(e.time).toLocaleString()}>
                {relativeTime(e.time)}
              </time>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
