import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { AlertTriangle, Cpu } from 'lucide-react';
import { sitesApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { SiteStatus, SiteView } from '@/api/types';
import { Card, Callout, EmptyState, Loading, Mono, Stat } from '@/components/Layout';
import { Table, TableMessage, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { StateBadge } from '@/components/StatusBadges';
import { Badge } from '@/components/Badge';
import { AreaChart } from '@/components/Charts';
import { Segmented } from '@/components/Tabs';
import { useNow } from '@/hooks/useNow';
import { formatBytes, formatCompact, formatMs, formatNumber, formatPercent, formatUptime, relativeTime } from '@/lib/format';
import { cn } from '@/lib/cn';

const RANGES = [
  { value: '15', label: '15m' },
  { value: '60', label: '1h' },
  { value: '360', label: '6h' },
  { value: '1440', label: '24h' },
] as const;

export function OverviewTab({ site, status }: { site: SiteView; status: SiteStatus | undefined }) {
  const now = useNow(1000);
  const t = status?.traffic;
  const total = t?.requests ?? 0;
  const pct = (n: number | undefined) => (total ? `${((100 * (n ?? 0)) / total).toFixed(1)}%` : '');

  return (
    <div className="space-y-5">
      {status?.message && (
        <Callout tone={status.state === 'failed' ? 'danger' : status.state === 'degraded' ? 'warning' : 'info'} icon={<AlertTriangle />}>
          {status.message}
        </Callout>
      )}

      <div className="grid gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <Stat label="Requests/s" value={formatNumber(t?.rps ?? 0, 2)} sub="last minute" />
        <Stat label="Requests" value={formatCompact(total)} sub={`${formatBytes(t?.bytesIn)} in · ${formatBytes(t?.bytesOut)} out`} />
        <Stat label="Avg latency" value={formatMs(t?.avgLatencyMs ?? 0)} />
        <Stat label="2xx / 3xx" value={<span className="text-emerald-700 dark:text-emerald-400">{formatCompact(t?.status2xx ?? 0)}</span>} sub={`${pct(t?.status2xx)} · 3xx ${formatCompact(t?.status3xx ?? 0)}`} />
        <Stat label="4xx" value={formatCompact(t?.status4xx ?? 0)} tone={(t?.status4xx ?? 0) > 0 ? 'amber' : 'default'} sub={pct(t?.status4xx)} />
        <Stat label="5xx" value={formatCompact(t?.status5xx ?? 0)} tone={(t?.status5xx ?? 0) > 0 ? 'red' : 'default'} sub={pct(t?.status5xx)} />
      </div>

      {site.type === 'node' && <InstancesCard site={site} status={status} now={now} />}
      {site.type === 'proxy' && <UpstreamsCard status={status} />}
      {site.type === 'node' && site.node?.loadBalancer?.enabled && <UpstreamsCard status={status} title="Servers" />}

      <MetricsCard site={site} />
    </div>
  );
}

function InstancesCard({ site, status, now }: { site: SiteView; status: SiteStatus | undefined; now: number }) {
  const inst = [...(status?.instances ?? [])].sort((a, b) => a.index - b.index);
  const agent = site.node?.agentEnabled;
  return (
    <Card title="Instances" description={`${site.node?.instances ?? 1} configured · ${site.node?.portMode === 'fixed' ? `fixed port ${site.node.fixedPort}` : 'automatic ports'}`} flush>
      <Table>
        <THead>
          <tr>
            <Th>#</Th>
            <Th>PID</Th>
            <Th>Port</Th>
            <Th>State</Th>
            <Th>Health</Th>
            <Th className="text-right">Uptime</Th>
            <Th className="text-right">Restarts</Th>
            <Th className="text-right">CPU</Th>
            <Th className="text-right">Memory</Th>
            <Th className="text-right" title="Reported by the NodeHoster agent">Heap</Th>
            <Th className="text-right" title="Reported by the NodeHoster agent">Loop lag</Th>
            <Th className="text-right">Requests</Th>
            <Th>Last exit</Th>
          </tr>
        </THead>
        <TBody>
          {inst.length === 0 && (
            <TableMessage colSpan={13}>
              {status?.state === 'stopped' || !status ? 'The site is stopped. Start it to launch its processes.' : 'Waiting for instances…'}
            </TableMessage>
          )}
          {inst.map((i) => {
            const running = i.state !== 'exited' && i.state !== 'crashed';
            return (
              <Tr key={i.index}>
                <Td className="font-mono text-zinc-500">{i.index}</Td>
                <Td>
                  <Mono>{i.pid || '—'}</Mono>
                </Td>
                <Td>
                  <Mono>{i.port || '—'}</Mono>
                </Td>
                <Td>
                  <StateBadge state={i.state} />
                </Td>
                <Td>
                  {running ? (
                    i.healthy ? (
                      <Badge tone="green">healthy</Badge>
                    ) : (
                      <Badge tone="amber">unhealthy</Badge>
                    )
                  ) : (
                    <span className="text-zinc-400">—</span>
                  )}
                </Td>
                <Td className="text-right tabular">{running ? formatUptime(i.startedAt, now) : '—'}</Td>
                <Td className={cn('text-right tabular', i.restarts > 0 && 'text-amber-600 dark:text-amber-400')}>{i.restarts}</Td>
                <Td className="text-right tabular">{running ? formatPercent(i.cpuPercent) : '—'}</Td>
                <Td className="text-right tabular">{running ? formatBytes(i.memoryBytes) : '—'}</Td>
                <Td className="text-right tabular">
                  {i.heapUsedBytes ? (
                    <span title={`of ${formatBytes(i.heapTotalBytes)}`}>{formatBytes(i.heapUsedBytes)}</span>
                  ) : (
                    <span className="text-zinc-400" title={agent ? 'Not reported yet' : 'Enable the agent to collect heap metrics'}>—</span>
                  )}
                </Td>
                <Td className={cn('text-right tabular', (i.eventLoopLagMs ?? 0) > 100 && 'text-amber-600 dark:text-amber-400')}>
                  {i.eventLoopLagMs ? formatMs(i.eventLoopLagMs) : <span className="text-zinc-400">—</span>}
                </Td>
                <Td className="text-right tabular">{formatCompact(i.requests)}</Td>
                <Td className="whitespace-nowrap text-xs">
                  {i.lastExitCode !== undefined && i.lastExitCode !== null ? (
                    <span title={i.lastExitAt ? new Date(i.lastExitAt).toLocaleString() : undefined}>
                      <Mono className={i.lastExitCode === 0 ? 'text-zinc-500' : 'text-red-600 dark:text-red-400'}>code {i.lastExitCode}</Mono>
                      {i.lastExitAt && <span className="ml-1 text-zinc-500">{relativeTime(i.lastExitAt, now)}</span>}
                    </span>
                  ) : (
                    <span className="text-zinc-400">—</span>
                  )}
                </Td>
              </Tr>
            );
          })}
        </TBody>
      </Table>
      {inst.some((i) => i.nodeVersion) && (
        <div className="border-t border-zinc-200 px-4 py-2 text-xs text-zinc-500 dark:border-zinc-800">
          Node.js <Mono>{inst.find((i) => i.nodeVersion)?.nodeVersion}</Mono>
        </div>
      )}
    </Card>
  );
}

function UpstreamsCard({ status, title = 'Upstream health' }: { status: SiteStatus | undefined; title?: string }) {
  const ups = status?.upstreams ?? [];
  return (
    <Card title={title} flush>
      <Table>
        <THead>
          <tr>
            <Th>Upstream</Th>
            <Th>Health</Th>
            <Th className="text-right">Active connections</Th>
            <Th>Last error</Th>
          </tr>
        </THead>
        <TBody>
          {ups.length === 0 && <TableMessage colSpan={4}>No upstream status reported. Start the site to begin health checking.</TableMessage>}
          {ups.map((u) => (
            <Tr key={u.url}>
              <Td>
                {u.local ? <span className="font-medium">{u.url}</span> : <Mono>{u.url}</Mono>}
              </Td>
              <Td>
                {u.healthy ? (
                  <Badge tone="green" dot>
                    healthy
                  </Badge>
                ) : (
                  <Badge tone="red" dot>
                    {u.local ? 'not ready' : 'down'}
                  </Badge>
                )}
              </Td>
              <Td className="text-right tabular">{u.activeConns}</Td>
              <Td className="max-w-md truncate text-xs text-red-600 dark:text-red-400" title={u.lastError}>
                {u.lastError || <span className="text-zinc-400">—</span>}
              </Td>
            </Tr>
          ))}
        </TBody>
      </Table>
    </Card>
  );
}

function MetricsCard({ site }: { site: SiteView }) {
  const [range, setRange] = useState<(typeof RANGES)[number]['value']>('60');
  const minutes = Number(range);
  const q = useQuery({
    queryKey: qk.siteMetrics(site.id, minutes),
    queryFn: () => sitesApi.metrics(site.id, minutes),
    refetchInterval: 60_000,
  });
  const pts = q.data ?? [];
  const times = pts.map((p) => p.t);
  const isNode = site.type === 'node';

  return (
    <Card title="Metrics" description="One point per minute" actions={<Segmented options={[...RANGES]} value={range} onChange={setRange} />}>
      {q.isPending ? (
        <Loading />
      ) : pts.length === 0 ? (
        <EmptyState compact icon={<Cpu />} title="No metrics yet" description="Metrics are recorded every minute while the site is running." />
      ) : (
        <div className={cn('grid gap-6', isNode ? 'lg:grid-cols-2' : 'lg:grid-cols-2')}>
          <ChartBlock title="Requests & errors">
            <AreaChart
              times={times}
              format={formatCompact}
              series={[
                { label: 'Requests', values: pts.map((p) => p.req), className: 'text-accent-600 dark:text-accent-400' },
                { label: 'Errors', values: pts.map((p) => p.err), className: 'text-red-500' },
              ]}
            />
          </ChartBlock>
          <ChartBlock title="Average latency">
            <AreaChart times={times} format={(v) => formatMs(v)} series={[{ label: 'Latency', values: pts.map((p) => p.lat), className: 'text-violet-500' }]} />
          </ChartBlock>
          {isNode && (
            <>
              <ChartBlock title="CPU">
                <AreaChart times={times} format={(v) => `${Math.round(v)}%`} series={[{ label: 'CPU', values: pts.map((p) => p.cpu), className: 'text-sky-500' }]} />
              </ChartBlock>
              <ChartBlock title="Memory">
                <AreaChart times={times} format={(v) => formatBytes(v, 0)} series={[{ label: 'Memory', values: pts.map((p) => p.mem), className: 'text-amber-500' }]} />
              </ChartBlock>
            </>
          )}
        </div>
      )}
    </Card>
  );
}

function ChartBlock({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <h3 className="mb-2 text-xs font-medium text-zinc-500 dark:text-zinc-400">{title}</h3>
      {children}
    </div>
  );
}
