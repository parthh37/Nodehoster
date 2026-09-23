import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Download, Hexagon, MoreHorizontal, Search, Star, Terminal, Trash2 } from 'lucide-react';
import { nodeApi, settingsApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { AvailableNode, InstalledNode } from '@/api/types';
import { usePermissions } from '@/hooks/useAuth';
import { Button, IconButton } from '@/components/Button';
import { Card, EmptyState, Loading, Mono, PageHeader, ProgressBar } from '@/components/Layout';
import { Table, TableMessage, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Badge } from '@/components/Badge';
import { StateBadge } from '@/components/StatusBadges';
import { Dialog } from '@/components/Dialog';
import { ErrorBox } from '@/components/Field';
import { Input } from '@/components/Input';
import { Segmented } from '@/components/Tabs';
import { Menu } from '@/components/Menu';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { formatDate } from '@/lib/format';

const v = (s: string) => (s.startsWith('v') ? s : `v${s}`);

export function NodePage() {
  const { isAdmin } = usePermissions();
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [installOpen, setInstallOpen] = useState(false);
  const q = useQuery({
    queryKey: qk.nodeVersions,
    queryFn: nodeApi.versions,
    refetchInterval: (query) => ((query.state.data?.installed ?? []).some((i) => i.status === 'installing') ? 1500 : 30_000),
  });

  const setDefault = useMutation({
    mutationFn: async (version: string) => {
      const s = await settingsApi.get();
      return settingsApi.put({ ...s, defaultNodeVersion: version });
    },
    onSuccess: (s, version) => {
      qc.setQueryData(qk.settings, s);
      void qc.invalidateQueries({ queryKey: qk.nodeVersions });
      toast.success(version ? `Default set to ${v(version)}` : 'Default set to the system Node.js');
    },
    onError: (e) => toast.error('Could not change the default version', e),
  });

  const remove = useMutation({
    mutationFn: (version: string) => nodeApi.remove(version),
    onSuccess: (_r, version) => {
      toast.success(`Removed ${v(version)}`);
      void qc.invalidateQueries({ queryKey: qk.nodeVersions });
    },
    onError: (e) => toast.error('Could not remove version', e),
  });

  const installed = useMemo(
    () => [...(q.data?.installed ?? [])].sort((a, b) => b.version.localeCompare(a.version, undefined, { numeric: true })),
    [q.data],
  );
  const system = q.data?.system ?? null;
  const anyDefault = installed.some((i) => i.isDefault);

  return (
    <div>
      <PageHeader
        title="Node.js runtimes"
        description="Versions installed side by side on this server. Each site can pin a version; otherwise it uses the default."
        actions={
          isAdmin && (
            <Button variant="primary" icon={<Download className="h-4 w-4" />} onClick={() => setInstallOpen(true)}>
              Install version
            </Button>
          )
        }
      />
      {q.isError && <ErrorBox className="mb-4">{errorMessage(q.error)}</ErrorBox>}
      <div className="space-y-5">
        <Card title="System Node.js" description="Found on the server's PATH.">
          {q.isPending ? (
            <Loading />
          ) : system ? (
            <div className="flex flex-wrap items-center gap-4">
              <Terminal className="h-5 w-5 text-zinc-400" />
              <div>
                <Mono className="text-sm font-semibold">{v(system.version)}</Mono>
                <p className="font-mono text-xs text-zinc-500">{system.path}</p>
              </div>
              {!anyDefault ? (
                <Badge tone="accent" className="ml-auto">
                  <Star className="h-3 w-3" /> default
                </Badge>
              ) : (
                isAdmin && (
                  <Button className="ml-auto" size="sm" loading={setDefault.isPending && setDefault.variables === ''} onClick={() => setDefault.mutate('')}>
                    Use as default
                  </Button>
                )
              )}
            </div>
          ) : (
            <p className="text-[13px] text-zinc-500">
              No Node.js on PATH. {anyDefault ? '' : 'Install a version below and make it the default so sites can start.'}
            </p>
          )}
        </Card>

        <Card title="Installed versions" flush>
          {q.isPending ? (
            <Loading />
          ) : installed.length === 0 ? (
            <EmptyState
              icon={<Hexagon />}
              title="No managed versions installed"
              description="Install an LTS release from nodejs.org. NodeHoster downloads, verifies and unpacks it into its data folder — no system-wide installer needed."
              action={
                isAdmin && (
                  <Button variant="primary" icon={<Download className="h-4 w-4" />} onClick={() => setInstallOpen(true)}>
                    Install version
                  </Button>
                )
              }
            />
          ) : (
            <Table>
              <THead>
                <tr>
                  <Th>Version</Th>
                  <Th>Status</Th>
                  <Th>Path</Th>
                  <Th className="w-10" />
                </tr>
              </THead>
              <TBody>
                {installed.map((n) => (
                  <InstalledRow
                    key={n.version}
                    n={n}
                    isAdmin={isAdmin}
                    onDefault={() => setDefault.mutate(n.version)}
                    onRemove={async () => {
                      const r = await confirm({
                        title: `Remove Node.js ${v(n.version)}?`,
                        message: 'Sites pinned to this version must be switched first; the server refuses to remove a version in use.',
                        confirmLabel: 'Remove',
                        danger: true,
                      });
                      if (r.ok) remove.mutate(n.version);
                    }}
                  />
                ))}
              </TBody>
            </Table>
          )}
        </Card>
      </div>
      <InstallDialog open={installOpen} onClose={() => setInstallOpen(false)} installed={installed} />
    </div>
  );
}

function InstalledRow({ n, isAdmin, onDefault, onRemove }: { n: InstalledNode; isAdmin: boolean; onDefault: () => void; onRemove: () => void }) {
  return (
    <Tr>
      <Td>
        <div className="flex items-center gap-2">
          <Mono className="font-semibold">{v(n.version)}</Mono>
          {n.isDefault && (
            <Badge tone="accent">
              <Star className="h-3 w-3" /> default
            </Badge>
          )}
        </div>
      </Td>
      <Td className="w-72">
        {n.status === 'installing' ? (
          <div className="flex items-center gap-2">
            <ProgressBar className="w-40" value={n.progress} indeterminate={!n.progress} />
            <span className="text-xs tabular text-zinc-500">{n.progress ? `${Math.round(n.progress)}%` : 'starting…'}</span>
          </div>
        ) : n.status === 'error' ? (
          <div>
            <StateBadge state="error" />
            {n.error && <p className="mt-1 text-xs text-red-600 dark:text-red-400">{n.error}</p>}
          </div>
        ) : (
          <StateBadge state={n.status} />
        )}
      </Td>
      <Td>
        <Mono className="text-zinc-500">{n.path || '—'}</Mono>
      </Td>
      <Td>
        {isAdmin && n.status !== 'installing' && (
          <Menu
            trigger={(p) => <IconButton label="Actions" icon={<MoreHorizontal className="h-4 w-4" />} {...p} />}
            items={[
              { label: 'Make default', icon: <Star />, hidden: n.isDefault || n.status !== 'installed', onSelect: onDefault },
              { label: 'Remove', icon: <Trash2 />, danger: true, onSelect: onRemove },
            ]}
          />
        )}
      </Td>
    </Tr>
  );
}

function InstallDialog({ open, onClose, installed }: { open: boolean; onClose: () => void; installed: InstalledNode[] }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [filter, setFilter] = useState<'lts' | 'all'>('lts');
  const [search, setSearch] = useState('');
  const q = useQuery({ queryKey: qk.nodeAvailable, queryFn: nodeApi.available, enabled: open, staleTime: 10 * 60_000 });
  const install = useMutation({
    mutationFn: (version: string) => nodeApi.install(version),
    onSuccess: (_r, version) => {
      toast.info(`Installing Node.js ${v(version)}`, 'Progress is shown in the installed versions list.');
      void qc.invalidateQueries({ queryKey: qk.nodeVersions });
      onClose();
    },
    onError: (e) => toast.error('Could not start the installation', e),
  });

  const have = new Set(installed.map((i) => i.version.replace(/^v/, '')));
  const list = useMemo(() => {
    const term = search.trim().replace(/^v/, '');
    let rows: AvailableNode[] = q.data ?? [];
    if (filter === 'lts') rows = rows.filter((r) => r.lts);
    if (term) rows = rows.filter((r) => r.version.replace(/^v/, '').startsWith(term) || (typeof r.lts === 'string' && r.lts.toLowerCase().includes(term.toLowerCase())));
    return rows.slice(0, 200);
  }, [q.data, filter, search]);

  const latestPerMajor = useMemo(() => {
    const seen = new Set<string>();
    const out = new Set<string>();
    for (const r of q.data ?? []) {
      const major = r.version.replace(/^v/, '').split('.')[0];
      if (!seen.has(major)) {
        seen.add(major);
        out.add(r.version);
      }
    }
    return out;
  }, [q.data]);

  return (
    <Dialog open={open} onClose={onClose} size="lg" title="Install Node.js" description="Official releases from nodejs.org. LTS versions are recommended for production.">
      <div className="space-y-3">
        <div className="flex items-center gap-2">
          <Segmented
            value={filter}
            onChange={setFilter}
            options={[
              { value: 'lts', label: 'LTS only' },
              { value: 'all', label: 'All releases' },
            ]}
          />
          <Input className="flex-1" prefix={<Search className="h-3.5 w-3.5" />} placeholder="Filter, e.g. 22 or jod" value={search} onChange={(e) => setSearch(e.target.value)} />
        </div>
        {q.isError && <ErrorBox>{errorMessage(q.error)}</ErrorBox>}
        <div className="scrollbar-thin max-h-[50vh] overflow-y-auto rounded-md border border-zinc-200 dark:border-zinc-800">
          <Table dense>
            <THead>
              <tr>
                <Th>Version</Th>
                <Th>Release</Th>
                <Th>Date</Th>
                <Th className="w-28" />
              </tr>
            </THead>
            <TBody>
              {q.isPending && <TableMessage colSpan={4}>Loading releases…</TableMessage>}
              {!q.isPending && list.length === 0 && <TableMessage colSpan={4}>No releases match.</TableMessage>}
              {list.map((r) => {
                const isInstalled = have.has(r.version.replace(/^v/, ''));
                return (
                  <Tr key={r.version}>
                    <Td>
                      <div className="flex items-center gap-2">
                        <Mono className="font-semibold">{v(r.version)}</Mono>
                        {latestPerMajor.has(r.version) && <span className="text-2xs text-zinc-500">latest</span>}
                      </div>
                    </Td>
                    <Td>
                      <div className="flex items-center gap-1.5">
                        {r.lts ? <Badge tone="green">LTS {r.lts}</Badge> : <Badge tone="gray">Current</Badge>}
                        {r.security && <Badge tone="amber">security</Badge>}
                      </div>
                    </Td>
                    <Td className="text-zinc-500">{formatDate(r.date)}</Td>
                    <Td className="text-right">
                      {isInstalled ? (
                        <span className="text-xs text-zinc-500">Installed</span>
                      ) : (
                        <Button size="sm" icon={<Download className="h-3.5 w-3.5" />} loading={install.isPending && install.variables === r.version} disabled={install.isPending} onClick={() => install.mutate(r.version)}>
                          Install
                        </Button>
                      )}
                    </Td>
                  </Tr>
                );
              })}
            </TBody>
          </Table>
        </div>
      </div>
    </Dialog>
  );
}
