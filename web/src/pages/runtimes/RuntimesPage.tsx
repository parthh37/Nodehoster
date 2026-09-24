import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Download, ExternalLink, Layers, MoreHorizontal, RefreshCw, Star, Terminal, Trash2 } from 'lucide-react';
import { runtimesApi, settingsApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { InstalledNode, ManagedRuntime, RuntimeDefaults, RuntimeReport } from '@/api/types';
import { usePermissions } from '@/hooks/useAuth';
import { Button, IconButton } from '@/components/Button';
import { Callout, Card, EmptyState, Loading, Mono, PageHeader, ProgressBar } from '@/components/Layout';
import { Table, TableMessage, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Badge } from '@/components/Badge';
import { StateBadge } from '@/components/StatusBadges';
import { Dialog } from '@/components/Dialog';
import { ErrorBox } from '@/components/Field';
import { Menu } from '@/components/Menu';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { formatDate } from '@/lib/format';
import { DOTNET_DOWNLOAD, PYTHON_DOWNLOAD } from '@/lib/runtimes';

type Managed = 'bun' | 'deno';
const LABEL: Record<Managed, string> = { bun: 'Bun', deno: 'Deno' };

/**
 * The runtimes besides Node.js: Bun and Deno versions NodeHoster installs
 * side by side (like the Node.js page), and the Python interpreters and
 * .NET runtimes it finds on the server.
 */
export function RuntimesPage() {
  const { isAdmin } = usePermissions();
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({
    queryKey: qk.runtimes,
    queryFn: runtimesApi.report,
    refetchInterval: (query) => {
      const r = query.state.data;
      const installing = [...(r?.bun.installed ?? []), ...(r?.deno.installed ?? [])].some((i) => i.status === 'installing');
      return installing ? 1500 : 30_000;
    },
  });
  const refresh = useMutation({
    mutationFn: runtimesApi.refresh,
    onSuccess: (r) => {
      qc.setQueryData(qk.runtimes, r);
      toast.success('Looked for runtimes again');
    },
    onError: (e) => toast.error('Could not refresh', e),
  });
  const setDefault = useMutation({
    mutationFn: async (patch: RuntimeDefaults) => {
      const s = await settingsApi.get();
      return settingsApi.put({ ...s, runtimes: { ...s.runtimes, ...patch } });
    },
    onSuccess: (s) => {
      qc.setQueryData(qk.settings, s);
      void qc.invalidateQueries({ queryKey: qk.runtimes });
      toast.success('Default changed');
    },
    onError: (e) => toast.error('Could not change the default', e),
  });
  const r = q.data;

  return (
    <div>
      <PageHeader
        title="Runtimes"
        description="What application sites run with besides Node.js. Each site picks its runtime and can pin a version; otherwise it uses the default."
        actions={
          isAdmin && (
            <Button icon={<RefreshCw className="h-4 w-4" />} loading={refresh.isPending} onClick={() => refresh.mutate()}>
              Look again
            </Button>
          )
        }
      />
      {q.isError && <ErrorBox className="mb-4">{errorMessage(q.error)}</ErrorBox>}
      {q.isPending ? (
        <Loading />
      ) : r ? (
        <div className="space-y-5">
          {(['bun', 'deno'] as Managed[]).map((rt) => (
            <ManagedCard key={rt} rt={rt} m={r[rt]} isAdmin={isAdmin} onDefault={(v) => setDefault.mutate({ [rt]: v })} />
          ))}
          <PythonCard report={r} isAdmin={isAdmin} onDefault={(v) => setDefault.mutate({ python: v })} />
          <DotnetCard report={r} />
        </div>
      ) : null}
    </div>
  );
}

function ManagedCard({ rt, m, isAdmin, onDefault }: { rt: Managed; m: ManagedRuntime; isAdmin: boolean; onDefault: (v: string) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [installOpen, setInstallOpen] = useState(false);
  const installed = useMemo(() => [...(m.installed ?? [])].sort((a, b) => b.version.localeCompare(a.version, undefined, { numeric: true })), [m.installed]);
  const anyDefault = installed.some((i) => i.isDefault);
  const remove = useMutation({
    mutationFn: (v: string) => runtimesApi.remove(rt, v),
    onSuccess: (_r, v) => {
      toast.success(`Removed ${LABEL[rt]} ${v}`);
      void qc.invalidateQueries({ queryKey: qk.runtimes });
    },
    onError: (e) => toast.error('Could not remove the version', e),
  });

  return (
    <Card
      title={LABEL[rt]}
      description={
        rt === 'bun'
          ? 'Official builds from github.com/oven-sh/bun, checked against their published SHA-256.'
          : 'Official builds from github.com/denoland/deno, checked against their published SHA-256.'
      }
      actions={
        isAdmin && (
          <Button size="sm" icon={<Download className="h-3.5 w-3.5" />} onClick={() => setInstallOpen(true)}>
            Install version
          </Button>
        )
      }
      flush
    >
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
          {m.system && (
            <Tr>
              <Td>
                <div className="flex items-center gap-2">
                  <Terminal className="h-3.5 w-3.5 text-zinc-400" />
                  <Mono className="font-semibold">{m.system.version}</Mono>
                  {!anyDefault && (
                    <Badge tone="accent">
                      <Star className="h-3 w-3" /> default
                    </Badge>
                  )}
                </div>
              </Td>
              <Td className="text-xs text-zinc-500">on the server's PATH</Td>
              <Td>
                <Mono className="text-zinc-500">{m.system.path}</Mono>
              </Td>
              <Td>
                {isAdmin && anyDefault && (
                  <Menu
                    trigger={(p) => <IconButton label="Actions" icon={<MoreHorizontal className="h-4 w-4" />} {...p} />}
                    items={[{ label: 'Make default', icon: <Star />, onSelect: () => onDefault('') }]}
                  />
                )}
              </Td>
            </Tr>
          )}
          {installed.map((n) => (
            <InstalledRow
              key={n.version}
              n={n}
              isAdmin={isAdmin}
              onDefault={() => onDefault(n.version)}
              onRemove={async () => {
                const ok = await confirm({
                  title: `Remove ${LABEL[rt]} ${n.version}?`,
                  message: 'Sites pinned to this version must be switched first; the server refuses to remove a version in use.',
                  confirmLabel: 'Remove',
                  danger: true,
                });
                if (ok.ok) remove.mutate(n.version);
              }}
            />
          ))}
          {!m.system && installed.length === 0 && (
            <TableMessage colSpan={4}>
              {LABEL[rt]} is not installed. {isAdmin ? 'Install a version: NodeHoster downloads, verifies and unpacks it into its data folder.' : ''}
            </TableMessage>
          )}
        </TBody>
      </Table>
      <InstallDialog rt={rt} open={installOpen} onClose={() => setInstallOpen(false)} installed={installed} />
    </Card>
  );
}

function InstalledRow({ n, isAdmin, onDefault, onRemove }: { n: InstalledNode; isAdmin: boolean; onDefault: () => void; onRemove: () => void }) {
  return (
    <Tr>
      <Td>
        <div className="flex items-center gap-2">
          <Mono className="font-semibold">{n.version}</Mono>
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

function InstallDialog({ rt, open, onClose, installed }: { rt: Managed; open: boolean; onClose: () => void; installed: InstalledNode[] }) {
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: qk.runtimeAvailable(rt), queryFn: () => runtimesApi.available(rt), enabled: open, staleTime: 10 * 60_000 });
  const install = useMutation({
    mutationFn: (version: string) => runtimesApi.install(rt, version),
    onSuccess: (_r, version) => {
      toast.info(`Installing ${LABEL[rt]} ${version}`, 'Progress is shown in the list.');
      void qc.invalidateQueries({ queryKey: qk.runtimes });
      onClose();
    },
    onError: (e) => toast.error('Could not start the installation', e),
  });
  const have = new Set(installed.map((i) => i.version));
  const list = (q.data ?? []).slice(0, 100);
  return (
    <Dialog open={open} onClose={onClose} size="lg" title={`Install ${LABEL[rt]}`} description="Stable releases published for this server's platform, newest first.">
      <div className="space-y-3">
        {q.isError && <ErrorBox>{errorMessage(q.error)}</ErrorBox>}
        <div className="scrollbar-thin max-h-[50vh] overflow-y-auto rounded-md border border-zinc-200 dark:border-zinc-800">
          <Table dense>
            <THead>
              <tr>
                <Th>Version</Th>
                <Th>Released</Th>
                <Th className="w-28" />
              </tr>
            </THead>
            <TBody>
              {q.isPending && <TableMessage colSpan={3}>Loading releases…</TableMessage>}
              {!q.isPending && list.length === 0 && <TableMessage colSpan={3}>No releases found.</TableMessage>}
              {list.map((a, i) => (
                <Tr key={a.version}>
                  <Td>
                    <div className="flex items-center gap-2">
                      <Mono className="font-semibold">{a.version}</Mono>
                      {i === 0 && <span className="text-2xs text-zinc-500">latest</span>}
                    </div>
                  </Td>
                  <Td className="text-zinc-500">{a.date ? formatDate(a.date) : '—'}</Td>
                  <Td className="text-right">
                    {have.has(a.version) ? (
                      <span className="text-xs text-zinc-500">Installed</span>
                    ) : (
                      <Button
                        size="sm"
                        icon={<Download className="h-3.5 w-3.5" />}
                        loading={install.isPending && install.variables === a.version}
                        disabled={install.isPending}
                        onClick={() => install.mutate(a.version)}
                      >
                        Install
                      </Button>
                    )}
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        </div>
      </div>
    </Dialog>
  );
}

function PythonCard({ report, isAdmin, onDefault }: { report: RuntimeReport; isAdmin: boolean; onDefault: (v: string) => void }) {
  const list = report.python ?? [];
  const pinned = report.defaults.python ?? '';
  return (
    <Card
      title="Python"
      description="Interpreters found through the py launcher, on PATH and in the standard folders. Sites run in their own virtual environment made from one of them."
      flush
    >
      {list.length === 0 ? (
        <EmptyState
          icon={<Layers />}
          title="Python was not found"
          description="NodeHoster does not install Python. Install it for all users (so the service can use it), then look again."
          action={
            <a href={PYTHON_DOWNLOAD} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-sm font-medium text-accent-700 hover:underline dark:text-accent-400">
              python.org downloads <ExternalLink className="h-3.5 w-3.5" />
            </a>
          }
        />
      ) : (
        <Table>
          <THead>
            <tr>
              <Th>Version</Th>
              <Th>Found by</Th>
              <Th>Path</Th>
              <Th className="w-10" />
            </tr>
          </THead>
          <TBody>
            {list.map((p) => (
              <Tr key={p.path}>
                <Td>
                  <div className="flex items-center gap-2">
                    <Mono className="font-semibold">{p.version}</Mono>
                    {p.isDefault && (
                      <Badge tone="accent">
                        <Star className="h-3 w-3" /> default
                      </Badge>
                    )}
                  </div>
                </Td>
                <Td className="text-xs text-zinc-500">
                  {p.source === 'py' ? 'py launcher' : p.source === 'registry' ? 'registry' : p.source === 'path' ? 'PATH' : 'standard folder'}
                </Td>
                <Td>
                  <Mono className="text-zinc-500">{p.path}</Mono>
                </Td>
                <Td>
                  {isAdmin && (
                    <Menu
                      trigger={(t) => <IconButton label="Actions" icon={<MoreHorizontal className="h-4 w-4" />} {...t} />}
                      items={[
                        { label: 'Make default', icon: <Star />, hidden: pinned === p.path, onSelect: () => onDefault(p.path) },
                        { label: 'Default: the newest found', icon: <RefreshCw />, hidden: !pinned, onSelect: () => onDefault('') },
                      ]}
                    />
                  )}
                </Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      )}
    </Card>
  );
}

function DotnetCard({ report }: { report: RuntimeReport }) {
  const d = report.dotnet;
  return (
    <Card title=".NET" description="ASP.NET Core apps run on Kestrel behind the reverse proxy; the app's runtimeconfig.json picks the runtime version." flush>
      {!d ? (
        <EmptyState
          icon={<Layers />}
          title=".NET was not found"
          description="Install the ASP.NET Core Runtime (Windows Hosting Bundle) for framework-dependent apps, or deploy self-contained .exe apps, which need none."
          action={
            <a href={DOTNET_DOWNLOAD} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-sm font-medium text-accent-700 hover:underline dark:text-accent-400">
              .NET downloads <ExternalLink className="h-3.5 w-3.5" />
            </a>
          }
        />
      ) : (
        <>
          <div className="border-b border-zinc-200 px-4 py-2 text-xs text-zinc-500 dark:border-zinc-800">
            Host <Mono>{d.host}</Mono>
          </div>
          <Table>
            <THead>
              <tr>
                <Th>Runtime</Th>
                <Th>Version</Th>
                <Th>Path</Th>
              </tr>
            </THead>
            <TBody>
              {(d.runtimes ?? []).length === 0 && <TableMessage colSpan={3}>The host lists no runtimes.</TableMessage>}
              {(d.runtimes ?? []).map((r) => (
                <Tr key={r.name + r.version}>
                  <Td className="font-medium">{r.name}</Td>
                  <Td>
                    <Mono>{r.version}</Mono>
                  </Td>
                  <Td>
                    <Mono className="text-zinc-500">{r.path}</Mono>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
          {!(d.runtimes ?? []).some((r) => r.name === 'Microsoft.AspNetCore.App') && (
            <div className="p-4">
              <Callout tone="warning">No ASP.NET Core runtime is installed: web apps that are not self-contained will not start.</Callout>
            </div>
          )}
        </>
      )}
    </Card>
  );
}
