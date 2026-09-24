import { useQuery } from '@tanstack/react-query';
import { authApi, serversApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { Access, Role } from '@/api/types';
import { accessOf, canOperate, canOperateServer, isServerAdmin, isSiteScoped } from '@/lib/access';
import { capAccess } from '@/lib/servers';
import { useServerTarget } from './useServerTarget';

export function useMe() {
  return useQuery({ queryKey: qk.me, queryFn: authApi.me, retry: false, staleTime: 60_000 });
}

export interface Permissions {
  role: Role | undefined;
  access: Access | undefined;
  /** Allowed on selected sites only: no server-wide pages. */
  siteScoped: boolean;
  /** Server-wide operator or admin. For a site's actions use useSitePermissions. */
  canOperate: boolean;
  /** Server administrator: configuration, settings, users, secrets. */
  isAdmin: boolean;
}

function permissionsOf(access: Access | undefined, role: Role | undefined): Permissions {
  return {
    role,
    access,
    siteScoped: isSiteScoped(access),
    canOperate: canOperateServer(access),
    isAdmin: isServerAdmin(access),
  };
}

/** The signed-in user's permissions on this server, whichever server the console operates. */
export function useLocalPermissions(): Permissions {
  const { data } = useMe();
  return permissionsOf(accessOf(data), data?.user.role);
}

/**
 * The permissions on the server the console operates: on a connected
 * server, what its token may do there, never above the user's role here.
 */
export function usePermissions(): Permissions {
  const local = useLocalPermissions();
  const { target } = useServerTarget();
  const remote = useQuery({
    queryKey: qk.remoteMe(target?.id ?? ''),
    queryFn: () => serversApi.remoteMe(target!.id),
    enabled: !!target,
    retry: false,
    staleTime: 60_000,
  });
  if (!target) return local;
  if (remote.isPending) return permissionsOf(undefined, undefined);
  // Unreachable: show the pages read-only; they explain the failure.
  const access = capAccess(remote.isError ? { role: 'viewer' } : accessOf(remote.data), local.role);
  return permissionsOf(access, access?.role);
}

export interface SitePermissions {
  /** Start, stop, restart, recycle, deploy, clear logs. */
  canOperate: boolean;
  /** Change the site's configuration or delete it: server administrators only. */
  canConfigure: boolean;
}

/** What the caller may do on one site (a grant, or the server-wide role). */
export function useSitePermissions(siteId: string | undefined): SitePermissions {
  const { access } = usePermissions();
  return { canOperate: !!siteId && canOperate(access, siteId), canConfigure: isServerAdmin(access) };
}
