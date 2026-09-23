import { useQuery } from '@tanstack/react-query';
import { authApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { Access, Role } from '@/api/types';
import { accessOf, canOperate, canOperateServer, isServerAdmin, isSiteScoped } from '@/lib/access';

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

export function usePermissions(): Permissions {
  const { data } = useMe();
  const access = accessOf(data);
  return {
    role: data?.user.role,
    access,
    siteScoped: isSiteScoped(access),
    canOperate: canOperateServer(access),
    isAdmin: isServerAdmin(access),
  };
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
