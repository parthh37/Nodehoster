import { useQuery } from '@tanstack/react-query';
import { authApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { Role } from '@/api/types';

export function useMe() {
  return useQuery({ queryKey: qk.me, queryFn: authApi.me, retry: false, staleTime: 60_000 });
}

export interface Permissions {
  role: Role | undefined;
  /** operator or admin: start/stop/restart/deploy. */
  canOperate: boolean;
  /** admin: configuration, settings, users, secrets. */
  isAdmin: boolean;
}

export function usePermissions(): Permissions {
  const { data } = useMe();
  const role = data?.user.role;
  return { role, canOperate: role === 'admin' || role === 'operator', isAdmin: role === 'admin' };
}
