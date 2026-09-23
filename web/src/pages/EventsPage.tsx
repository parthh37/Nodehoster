import { useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Activity, RefreshCw, Search } from 'lucide-react';
import { serverApi, sitesApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { NHEvent } from '@/api/types';
import { useLive } from '@/hooks/useLive';
import { useNow } from '@/hooks/useNow';
import { Card, EmptyState, Loading, PageHeader } from '@/components/Layout';
import { Table, TableMessage, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Input, Select } from '@/components/Input';
import { Button } from '@/components/Button';
import { LevelBadge } from '@/components/StatusBadges';
import { Badge } from '@/components/Badge';
import { ErrorBox } from '@/components/Field';
import { formatDateTime, relativeTime } from '@/lib/format';
import { cn } from '@/lib/cn';

const LIMIT = 500;

export function EventsPage() {
  const [params, setParams] = useSearchParams();
  const level = params.get('level') ?? '';
  const siteId = params.get('site') ?? '';
  const [search, setSearch] = useState('');
  const now = useNow(30_000);
  const { events: live, connected } = useLive();

  const q = useQuery({ queryKey: qk.events(LIMIT, siteId || undefined), queryFn: () => serverApi.events(LIMIT, siteId || undefined) });
  const sites = useQuery({ queryKey: qk.sites, queryFn: sitesApi.list, staleTime: 30_000 });
  const siteNames = useMemo(() => Object.fromEntries((sites.data ?? []).map((s) => [s.id, s.name])), [sites.data]);

  const setParam = (k: string, v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v);
    else next.delete(k);
    setParams(next, { replace: true });
  };

  const fetchedIds = useMemo(() => new Set((q.data ?? []).map((e) => e.id)), [q.data]);

  const rows = useMemo(() => {
    const seen = new Set<number>();
    const all: NHEvent[] = [];
    for (const e of [...live.filter((e) => !siteId || e.siteId === siteId), ...(q.data ?? [])]) {
      if (seen.has(e.id)) continue;
      seen.add(e.id);
      all.push(e);
    }
    const term = search.trim().toLowerCase();
    return all
      .filter((e) => (!level || e.level === level) && (!term || e.message.toLowerCase().includes(term) || e.type.toLowerCase().includes(term)))
      .sort((a, b) => b.time.localeCompare(a.time) || b.id - a.id);
  }, [live, q.data, level, siteId, search]);

  return (
    <div>
      <PageHeader
        title="Events"
        description="Operational history: starts, stops, crashes, recycles, deployments and certificate activity."
        actions={
          <>
            <Badge tone={connected ? 'green' : 'amber'} dot pulse={connected}>
              {connected ? 'Live' : 'Reconnecting'}
            </Badge>
            <Button icon={<RefreshCw className="h-3.5 w-3.5" />} onClick={() => void q.refetch()} loading={q.isFetching}>
              Refresh
            </Button>
          </>
        }
      />
      {q.isError && <ErrorBox className="mb-4">{errorMessage(q.error)}</ErrorBox>}
      <Card flush>
        <div className="flex flex-wrap items-center gap-2 border-b border-zinc-200 px-4 py-2.5 dark:border-zinc-800">
          <Input className="w-64" prefix={<Search className="h-3.5 w-3.5" />} placeholder="Search messages or types…" value={search} onChange={(e) => setSearch(e.target.value)} />
          <Select
            className="w-36"
            value={level}
            onChange={(v) => setParam('level', v)}
            options={[
              { value: '', label: 'All levels' },
              { value: 'info', label: 'Info' },
              { value: 'warning', label: 'Warning' },
              { value: 'error', label: 'Error' },
            ]}
          />
          <Select
            className="w-52"
            value={siteId}
            onChange={(v) => setParam('site', v)}
            options={[{ value: '', label: 'All sites' }, ...(sites.data ?? []).map((s) => ({ value: s.id, label: s.name }))]}
          />
          <span className="ml-auto text-xs text-zinc-500">{rows.length} events</span>
        </div>
        {q.isPending ? (
          <Loading />
        ) : rows.length === 0 && !level && !search && !siteId ? (
          <EmptyState icon={<Activity />} title="No events recorded" description="Events appear here as sites start, stop, crash, deploy and renew certificates." />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th className="w-44">Time</Th>
                <Th className="w-24">Level</Th>
                <Th className="w-40">Type</Th>
                <Th className="w-44">Site</Th>
                <Th>Message</Th>
              </tr>
            </THead>
            <TBody>
              {rows.length === 0 && <TableMessage colSpan={5}>No events match the filters.</TableMessage>}
              {rows.map((e) => (
                <Tr key={e.id} className={cn(!fetchedIds.has(e.id) && 'animate-fade-in bg-accent-50/40 dark:bg-accent-500/[0.04]')}>
                  <Td className="whitespace-nowrap text-xs text-zinc-500" title={formatDateTime(e.time)}>
                    <div>{formatDateTime(e.time)}</div>
                    <div className="text-2xs">{relativeTime(e.time, now)}</div>
                  </Td>
                  <Td>
                    <LevelBadge level={e.level} />
                  </Td>
                  <Td className="font-mono text-xs text-zinc-600 dark:text-zinc-400">{e.type}</Td>
                  <Td>
                    {e.siteId ? (
                      <Link to={`/sites/${e.siteId}`} className="nh-link text-[13px]">
                        {siteNames[e.siteId] ?? e.siteId}
                      </Link>
                    ) : (
                      <span className="text-xs text-zinc-400">Server</span>
                    )}
                  </Td>
                  <Td className="text-[13px]">{e.message}</Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        )}
      </Card>
    </div>
  );
}
