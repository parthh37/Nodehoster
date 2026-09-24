import { useState, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowRight, Boxes, Cpu, MemoryStick, Network, Pencil, Plus, RefreshCw, ServerOff, Trash2 } from 'lucide-react';
import { errorMessage } from '@/api/client';
import { serverApi, serversApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { ServerView } from '@/api/types';
import { Badge, type Tone } from '@/components/Badge';
import { Button, IconButton } from '@/components/Button';
import { useConfirm } from '@/components/Confirm';
import { ErrorBox } from '@/components/Field';
import { Card, EmptyState, Loading, Mono, PageHeader, ProgressBar } from '@/components/Layout';
import { useToast } from '@/components/Toast';
import { useLocalPermissions } from '@/hooks/useAuth';
import { useNow } from '@/hooks/useNow';
import { useServerTarget } from '@/hooks/useServerTarget';
import { formatBytes, formatPercent, relativeTime } from '@/lib/format';
import { healthLabel, healthState, lacksRoleLimits, memoryPercent, ROLE_LIMITS_NOTE, versionNote, type HealthState } from '@/lib/servers';
import { useServerConnections, useSwitchServer } from '../shell/ServerSwitcher';
import { ServerDialog } from './ServerDialog';

const tone: Record<HealthState, Tone> = { online: 'green', warning: 'amber', offline: 'red', unknown: 'gray' };

/**
 * Servers: the other NodeHoster servers managed from this console, with
 * their health (checked every 30 seconds by this server), like the
 * connections of IIS Manager. Open one to operate it here.
 */
export function ServersPage() {
  const { isAdmin } = useLocalPermissions();
  const servers = useServerConnections();
  const { target } = useServerTarget();
  // This server's version, to compare: the console shows another's while one is open.
  const info = useQuery({ queryKey: qk.serverInfo, queryFn: serverApi.info, staleTime: 30_000, enabled: !target });
  const localVersion = target ? target.localVersion : info.data?.version;
  const [editing, setEditing] = useState<ServerView | null>(null);
  const [adding, setAdding] = useState(false);
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const remove = useMutation({
    mutationFn: (s: ServerView) => serversApi.remove(s.id),
    onSuccess: (_, s) => {
      toast.success(`${s.name} removed`, 'Revoke its API token on that server if nothing else uses it.');
      void qc.invalidateQueries({ queryKey: qk.servers });
    },
    onError: (e) => toast.error('Could not remove the server', e),
  });
  const list = servers.data ?? [];

  return (
    <>
      <PageHeader
        title="Servers"
        icon={<Network className="h-4 w-4" />}
        description="Other NodeHoster servers managed from this console. Open one to operate it here: every page then works on that server, through this one."
        actions={
          <>
            <Button icon={<RefreshCw className="h-3.5 w-3.5" />} onClick={() => void servers.refetch()} loading={servers.isFetching}>
              Refresh
            </Button>
            {isAdmin && (
              <Button variant="primary" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setAdding(true)}>
                Connect to a server
              </Button>
            )}
          </>
        }
      />
      {servers.isPending ? (
        <Loading />
      ) : servers.isError ? (
        <ErrorBox>{errorMessage(servers.error)}</ErrorBox>
      ) : list.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Network />}
            title="No servers connected"
            description={
              isAdmin
                ? 'On the other server, create an API token for this purpose (Account → API tokens, limited to the role this server’s users need there), then connect to its web console with it.'
                : 'An administrator can connect this console to other NodeHoster servers.'
            }
            action={
              isAdmin && (
                <Button variant="primary" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setAdding(true)}>
                  Connect to a server
                </Button>
              )
            }
          />
        </Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {list.map((s) => (
            <ServerCard
              key={s.id}
              s={s}
              current={target?.id === s.id}
              localVersion={localVersion}
              onEdit={isAdmin ? () => setEditing(s) : undefined}
              onRemove={
                isAdmin
                  ? async () => {
                      const r = await confirm({
                        title: `Remove ${s.name}?`,
                        message: 'The console can no longer switch to it and its token is deleted here. Nothing changes on that server.',
                        confirmLabel: 'Remove',
                        danger: true,
                      });
                      if (r.ok) remove.mutate(s);
                    }
                  : undefined
              }
            />
          ))}
        </div>
      )}
      <ServerDialog open={adding || !!editing} server={editing} onClose={() => (setAdding(false), setEditing(null))} />
    </>
  );
}

function ServerCard({
  s,
  current,
  localVersion,
  onEdit,
  onRemove,
}: {
  s: ServerView;
  current: boolean;
  localVersion?: string;
  onEdit?: () => void;
  onRemove?: () => void;
}) {
  const h = s.health;
  const state = healthState(h);
  const now = useNow(15_000);
  const qc = useQueryClient();
  const toast = useToast();
  const switchTo = useSwitchServer();
  const check = useMutation({
    mutationFn: () => serversApi.check(s.id),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.servers }),
    onError: (e) => toast.error(`Could not check ${s.name}`, e),
  });
  const mem = memoryPercent(h);
  const note = versionNote(h.version, localVersion);
  return (
    <Card
      className={current ? 'ring-2 ring-accent-500/40' : undefined}
      title={
        <span className="flex items-center gap-2">
          <span className="truncate">{s.name}</span>
          <Badge tone={tone[state]} dot pulse={state === 'offline'}>
            {healthLabel(h)}
          </Badge>
          {current && <Badge tone="accent">Open</Badge>}
        </span>
      }
      description={<Mono className="text-xs">{s.url}</Mono>}
      actions={
        <>
          <IconButton label="Check now" icon={<RefreshCw className="h-3.5 w-3.5" />} onClick={() => check.mutate()} disabled={check.isPending} />
          {onEdit && <IconButton label="Edit" icon={<Pencil className="h-3.5 w-3.5" />} onClick={onEdit} />}
          {onRemove && <IconButton label="Remove" variant="danger-ghost" icon={<Trash2 className="h-3.5 w-3.5" />} onClick={onRemove} />}
        </>
      }
      footer={
        <div className="flex items-center justify-between gap-2 text-xs text-zinc-500">
          <span>
            {h.checkedAt ? `Checked ${relativeTime(h.checkedAt, now)}` : 'Not checked yet'}
            {h.reachable && h.latencyMs > 0 && ` · ${h.latencyMs} ms`}
          </span>
          <Button size="sm" variant={current ? 'secondary' : 'primary'} iconRight={<ArrowRight className="h-3.5 w-3.5" />} onClick={() => void switchTo(s)}>
            {current ? 'Dashboard' : 'Open'}
          </Button>
        </div>
      }
    >
      {state === 'offline' ? (
        <div className="flex items-start gap-2 text-[13px] text-red-700 dark:text-red-300">
          <ServerOff className="mt-0.5 h-4 w-4 shrink-0" />
          <div className="min-w-0">
            <p className="break-words">{h.error}</p>
            {h.since && <p className="mt-1 text-xs opacity-80">Unreachable since {relativeTime(h.since, now)}</p>}
          </div>
        </div>
      ) : state === 'unknown' ? (
        <p className="text-[13px] text-zinc-500">Waiting for the first check…</p>
      ) : (
        <div className="space-y-3 text-[13px]">
          <p className="text-zinc-600 dark:text-zinc-300">
            <span className="font-mono">{h.hostname}</span>
            {h.version && <> · NodeHoster {h.version}</>}
            {h.os && <span className="text-zinc-400"> · {h.os}</span>}
          </p>
          {note && <p className="text-xs text-amber-700 dark:text-amber-400">Version {note}.</p>}
          <div className="grid grid-cols-3 gap-3">
            <Metric icon={<Boxes />} label="Sites" value={`${h.running}/${h.sites}`} sub={h.failed ? `${h.failed} failed` : h.degraded ? `${h.degraded} degraded` : 'running'} bad={h.failed > 0} />
            <Metric icon={<Cpu />} label="CPU" value={formatPercent(h.cpuPercent, 0)} sub={h.cpuCount ? `${h.cpuCount} cores` : undefined}>
              <ProgressBar value={h.cpuPercent} tone={h.cpuPercent > 85 ? 'red' : h.cpuPercent > 60 ? 'amber' : 'accent'} />
            </Metric>
            <Metric icon={<MemoryStick />} label="Memory" value={mem === undefined ? '—' : formatPercent(mem, 0)} sub={h.memTotal ? `of ${formatBytes(h.memTotal)}` : undefined}>
              {mem !== undefined && <ProgressBar value={mem} tone={mem > 90 ? 'red' : mem > 75 ? 'amber' : 'accent'} />}
            </Metric>
          </div>
          {h.user && (
            <p className="text-xs text-zinc-500">
              Token user {h.user} ({h.role === 'sites' ? 'limited to some sites' : h.role})
            </p>
          )}
          {lacksRoleLimits(h) && <p className="text-xs text-amber-700 dark:text-amber-400">{ROLE_LIMITS_NOTE}</p>}
        </div>
      )}
    </Card>
  );
}

function Metric({ icon, label, value, sub, bad, children }: { icon: ReactNode; label: string; value: string; sub?: string; bad?: boolean; children?: ReactNode }) {
  return (
    <div className="min-w-0 space-y-1">
      <p className="flex items-center gap-1 text-2xs font-medium uppercase tracking-wide text-zinc-400 [&>svg]:h-3 [&>svg]:w-3">
        {icon}
        {label}
      </p>
      <p className={bad ? 'font-semibold tabular text-red-600 dark:text-red-400' : 'font-semibold tabular'}>{value}</p>
      {children}
      {sub && <p className="truncate text-2xs text-zinc-500">{sub}</p>}
    </div>
  );
}
