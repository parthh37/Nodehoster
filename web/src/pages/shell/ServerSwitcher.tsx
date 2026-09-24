// The server switcher (top bar) and the banner shown while the console
// operates a connected server, like IIS Manager's connections tree.
import { useEffect, type ReactNode } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { ArrowLeft, Check, ChevronDown, Monitor, Network, Server } from 'lucide-react';
import { serverApi, serversApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { ServerView } from '@/api/types';
import { Button } from '@/components/Button';
import { EmptyState } from '@/components/Layout';
import { Menu, type MenuItem } from '@/components/Menu';
import { useLocalPermissions } from '@/hooks/useAuth';
import { useServerTarget } from '@/hooks/useServerTarget';
import { cn } from '@/lib/cn';
import { healthState, mayUseServer, versionNote } from '@/lib/servers';

const dot: Record<ReturnType<typeof healthState>, string> = {
  online: 'bg-emerald-500',
  warning: 'bg-amber-500',
  offline: 'bg-red-500',
  unknown: 'bg-zinc-400',
};

/** The connections the signed-in user may use (none for site-scoped users). */
export function useServerConnections() {
  const local = useLocalPermissions();
  const usable = !!local.role && !local.siteScoped;
  return useQuery({ queryKey: qk.servers, queryFn: serversApi.list, enabled: usable, refetchInterval: 30_000, staleTime: 10_000 });
}

/** Switches the console to a connection (null: this server) and goes to its dashboard (or to). */
export function useSwitchServer() {
  const { select } = useServerTarget();
  const navigate = useNavigate();
  const info = useQuery({ queryKey: qk.serverInfo, queryFn: serverApi.info, staleTime: 30_000 });
  const { target } = useServerTarget();
  return async (s: ServerView | null, to = '/') => {
    // This server's version, for comparing: known while it is the one shown.
    const localVersion = target ? target.localVersion : info.data?.version;
    await select(s && { id: s.id, name: s.name, url: s.url, version: s.health.version, localVersion });
    navigate(to);
  };
}

/** The top bar's server name, with a menu of the connected servers. */
export function ServerSwitcher({ fallback }: { fallback: ReactNode }) {
  const { target } = useServerTarget();
  const servers = useServerConnections();
  const switchTo = useSwitchServer();
  const info = useQuery({ queryKey: qk.serverInfo, queryFn: serverApi.info, staleTime: 30_000 });
  const navigate = useNavigate();
  const list = servers.data ?? [];
  if (!target && list.length === 0) return <>{fallback}</>;

  const items: Array<MenuItem | 'separator'> = [
    {
      label: <Row name="This server" detail="" selected={!target} />,
      icon: <Monitor />,
      onSelect: () => void switchTo(null),
    },
    ...list.map((s) => ({
      label: <Row name={s.name} detail={s.health.version ? `v${s.health.version}` : s.url} selected={target?.id === s.id} state={healthState(s.health)} />,
      icon: <Server />,
      onSelect: () => void switchTo(s),
    })),
    'separator',
    { label: 'Servers overview', icon: <Network />, onSelect: () => navigate('/servers') },
  ];
  return (
    <Menu
      align="left"
      items={items}
      header={<p className="text-2xs font-semibold uppercase tracking-wider text-zinc-400">Manage server</p>}
      trigger={(p) => (
        <button
          type="button"
          {...p}
          className="flex min-w-0 items-center gap-2 rounded-md px-1.5 py-1 text-[13px] hover:bg-zinc-100 dark:hover:bg-zinc-800"
          title="Switch to another server"
        >
          {target ? <Server className="h-4 w-4 shrink-0 text-accent-600" /> : <Monitor className="h-4 w-4 shrink-0 text-zinc-400" />}
          <span className="truncate font-mono font-medium text-zinc-800 dark:text-zinc-200">{info.data?.hostname ?? target?.name ?? '…'}</span>
          {target && <span className="hidden truncate rounded bg-accent-50 px-1.5 text-2xs font-medium text-accent-800 dark:bg-accent-500/10 dark:text-accent-300 sm:inline">{target.name}</span>}
          <ChevronDown className="h-3.5 w-3.5 shrink-0 text-zinc-400" />
        </button>
      )}
    />
  );
}

function Row({ name, detail, selected, state }: { name: string; detail: string; selected: boolean; state?: ReturnType<typeof healthState> }) {
  return (
    <span className="flex w-full items-center gap-2">
      {state && <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', dot[state])} />}
      <span className="min-w-0 flex-1 truncate">{name}</span>
      {detail && <span className="truncate font-mono text-2xs text-zinc-400">{detail}</span>}
      {selected && <Check className="h-3.5 w-3.5 text-accent-600" />}
    </span>
  );
}

/**
 * Shown above every page while the console operates a connected server.
 * A connection that was removed, or that the user may no longer use,
 * switches the console back to this server.
 */
export function RemoteBanner() {
  const { target } = useServerTarget();
  const servers = useServerConnections();
  const local = useLocalPermissions();
  const switchTo = useSwitchServer();
  const gone =
    !!target && !!local.role && (local.siteScoped || (servers.isSuccess && !servers.data.some((s) => s.id === target.id && mayUseServer(local.role, s))));
  useEffect(() => {
    if (gone) void switchTo(null);
  }, [gone]); // switchTo is a new function every render
  if (!target) return null;
  const s = servers.data?.find((x) => x.id === target.id);
  const version = s?.health.version ?? target.version;
  const note = versionNote(version, target.localVersion);
  const offline = s && healthState(s.health) === 'offline';
  return (
    <div
      className={cn(
        'flex flex-wrap items-center gap-x-3 gap-y-1 border-b px-4 py-1.5 text-xs sm:px-6 lg:px-8',
        offline
          ? 'border-red-200 bg-red-50 text-red-900 dark:border-red-500/25 dark:bg-red-500/10 dark:text-red-200'
          : 'border-accent-200 bg-accent-50 text-accent-900 dark:border-accent-500/25 dark:bg-accent-500/10 dark:text-accent-200',
      )}
    >
      <Server className="h-3.5 w-3.5 shrink-0" />
      <span className="min-w-0">
        Managing <strong>{target.name}</strong> <span className="font-mono opacity-80">{target.url}</span> through this server
        {version && <> · NodeHoster {version}</>}
        {note && <span className="opacity-80"> ({note})</span>}
        {offline && <> · unreachable: {s.health.error}</>}
      </span>
      <button type="button" onClick={() => void switchTo(null)} className="ml-auto inline-flex items-center gap-1 font-medium underline-offset-2 hover:underline">
        <ArrowLeft className="h-3.5 w-3.5" /> Back to this server
      </button>
    </div>
  );
}

/** Pages about this server only (the account's API tokens), whichever server the console operates. */
export function ThisServerOnly({ what, children }: { what: string; children: ReactNode }) {
  const { target } = useServerTarget();
  const switchTo = useSwitchServer();
  const loc = useLocation();
  if (!target) return <>{children}</>;
  return (
    <EmptyState
      icon={<Monitor />}
      title={`${what} belong to this server`}
      description={`The console is managing ${target.name}. Switch back to this server to manage ${what.toLowerCase()}.`}
      action={
        <>
          <Button variant="primary" onClick={() => void switchTo(null, loc.pathname)}>
            Back to this server
          </Button>
          <Link to="/">
            <Button>Dashboard of {target.name}</Button>
          </Link>
        </>
      }
    />
  );
}
