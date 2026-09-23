import { useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Boxes, Plus, Search } from 'lucide-react';
import { sitesApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { SiteView } from '@/api/types';
import { useLive } from '@/hooks/useLive';
import { usePermissions } from '@/hooks/useAuth';
import { Button } from '@/components/Button';
import { Input, Select } from '@/components/Input';
import { Card, EmptyState, Loading, PageHeader } from '@/components/Layout';
import { Table, TableMessage, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { SiteTypeBadge, StateBadge } from '@/components/StatusBadges';
import { ErrorBox } from '@/components/Field';
import { formatNumber } from '@/lib/format';
import { runsNode, SITE_TYPES } from '@/lib/siteDefaults';
import { BindingList, SiteRowActions } from './shared';

export function SitesPage() {
  const q = useQuery({ queryKey: qk.sites, queryFn: sitesApi.list, refetchInterval: 30_000 });
  const { statuses } = useLive();
  const { isAdmin } = usePermissions();
  const navigate = useNavigate();
  const [search, setSearch] = useState('');
  const [type, setType] = useState('');
  const [state, setState] = useState('');

  const rows = useMemo(() => {
    const term = search.trim().toLowerCase();
    return (q.data ?? [])
      .map((s) => ({ site: s, status: statuses[s.id] ?? s.status }))
      .filter(({ site, status }) => {
        if (type && site.type !== type) return false;
        if (state && status?.state !== state) return false;
        if (!term) return true;
        return (
          site.name.toLowerCase().includes(term) ||
          (site.description ?? '').toLowerCase().includes(term) ||
          (site.bindings ?? []).some((b) => `${b.host}:${b.port}`.includes(term))
        );
      })
      .sort((a, b) => a.site.name.localeCompare(b.site.name));
  }, [q.data, statuses, search, type, state]);

  const total = q.data?.length ?? 0;

  return (
    <div>
      <PageHeader
        title="Sites"
        description="Each site is a set of bindings plus what answers the requests arriving on them."
        actions={
          isAdmin && (
            <Link to="/sites/new">
              <Button variant="primary" icon={<Plus className="h-4 w-4" />}>
                New site
              </Button>
            </Link>
          )
        }
      />
      {q.isError && <ErrorBox className="mb-4">{errorMessage(q.error)}</ErrorBox>}
      {q.isPending ? (
        <Card>
          <Loading />
        </Card>
      ) : total === 0 ? (
        <Card>
          <EmptyState
            icon={<Boxes />}
            title="No sites yet"
            description={
              isAdmin
                ? 'Create a site to run a Node.js app or background worker, proxy to another server, serve static files or redirect a domain. You will pick bindings (host name and port) in the wizard.'
                : 'No sites have been configured. An administrator can create one.'
            }
            action={
              isAdmin && (
                <Link to="/sites/new">
                  <Button variant="primary" icon={<Plus className="h-4 w-4" />}>
                    Create your first site
                  </Button>
                </Link>
              )
            }
          />
        </Card>
      ) : (
        <Card flush>
          <div className="flex flex-wrap items-center gap-2 border-b border-zinc-200 px-4 py-2.5 dark:border-zinc-800">
            <Input
              className="w-64"
              prefix={<Search className="h-3.5 w-3.5" />}
              placeholder="Filter by name or host…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
            <Select className="w-40" value={type} onChange={setType} options={[{ value: '', label: 'All types' }, ...SITE_TYPES.map((t) => ({ value: t.value, label: t.label }))]} />
            <Select
              className="w-36"
              value={state}
              onChange={setState}
              options={[
                { value: '', label: 'Any state' },
                { value: 'running', label: 'Running' },
                { value: 'degraded', label: 'Degraded' },
                { value: 'starting', label: 'Starting' },
                { value: 'stopped', label: 'Stopped' },
                { value: 'failed', label: 'Failed' },
              ]}
            />
            <span className="ml-auto text-xs text-zinc-500">
              {rows.length === total ? `${total} sites` : `${rows.length} of ${total} sites`}
            </span>
          </div>
          <Table>
            <THead>
              <tr>
                <Th>Name</Th>
                <Th>Type</Th>
                <Th>State</Th>
                <Th>Bindings</Th>
                <Th className="text-right">Instances</Th>
                <Th className="text-right">Req/s</Th>
                <Th className="w-28" />
              </tr>
            </THead>
            <TBody>
              {rows.length === 0 && <TableMessage colSpan={7}>No sites match the filter.</TableMessage>}
              {rows.map(({ site, status }) => (
                <Tr key={site.id} onClick={() => navigate(`/sites/${site.id}`)}>
                  <Td className="max-w-[16rem]">
                    <Link to={`/sites/${site.id}`} className="font-medium text-zinc-900 hover:underline dark:text-zinc-100" onClick={(e) => e.stopPropagation()}>
                      {site.name}
                    </Link>
                    {site.description && <p className="truncate text-xs text-zinc-500">{site.description}</p>}
                  </Td>
                  <Td>
                    <SiteTypeBadge type={site.type} />
                  </Td>
                  <Td>
                    <div className="flex items-center gap-1.5">
                      <StateBadge state={status?.state} title={status?.message} />
                      {site.routing?.maintenance?.enabled && (
                        <span className="rounded bg-amber-100 px-1 text-2xs font-medium text-amber-800 dark:bg-amber-500/15 dark:text-amber-300" title="Maintenance mode is on">
                          maint
                        </span>
                      )}
                    </div>
                  </Td>
                  <Td>
                    {site.type === 'worker' ? (
                      <span className="text-xs text-zinc-400" title="Background workers are not reachable over HTTP">
                        —
                      </span>
                    ) : (
                      <BindingList bindings={site.bindings} />
                    )}
                  </Td>
                  <Td className="text-right tabular">
                    <Instances site={site} status={status} />
                  </Td>
                  <Td className="text-right tabular">{site.type !== 'worker' && status && status.state !== 'stopped' ? formatNumber(status.traffic?.rps ?? 0, 1) : <span className="text-zinc-400">—</span>}</Td>
                  <Td>
                    <SiteRowActions site={site} state={status?.state} />
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        </Card>
      )}
    </div>
  );
}

function Instances({ site, status }: { site: SiteView; status: SiteView['status'] | undefined }) {
  if (!runsNode(site.type)) return <span className="text-zinc-400">—</span>;
  const inst = status?.instances ?? [];
  const ready = inst.filter((i) => i.state === 'ready').length;
  const total = Math.max(inst.length, site.node?.instances ?? 1);
  const stopped = !status || status.state === 'stopped';
  const tone = stopped
    ? 'text-zinc-400'
    : ready === total
      ? 'text-emerald-700 dark:text-emerald-400'
      : ready === 0
        ? 'text-red-600 dark:text-red-400'
        : 'text-amber-600 dark:text-amber-400';
  return (
    <span className={`font-mono text-[12.5px] ${tone}`}>
      {ready}/{total}
    </span>
  );
}
