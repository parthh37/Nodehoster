import { useCallback, useSyncExternalStore } from 'react';
import { useQueryClient, type Query } from '@tanstack/react-query';
import { LOCAL_QUERY_ROOTS } from '@/api/queryKeys';
import { currentTarget, setTarget, subscribeTarget, type Target } from '@/api/target';

const remoteQuery = (q: Query) => !LOCAL_QUERY_ROOTS.includes(String(q.queryKey[0]));

/**
 * The server the console operates (null: this one) and a function to
 * switch. Switching drops every cached answer of the previous server
 * (cancelling those in flight, so none lands after the switch); the
 * layout remounts its pages and live stream for the new one.
 */
export function useServerTarget(): { target: Target | null; select: (t: Target | null) => Promise<void> } {
  const target = useSyncExternalStore(subscribeTarget, currentTarget, () => null);
  const qc = useQueryClient();
  const select = useCallback(
    async (t: Target | null) => {
      await qc.cancelQueries({ predicate: remoteQuery });
      qc.removeQueries({ predicate: remoteQuery });
      setTarget(t);
    },
    [qc],
  );
  return { target, select };
}
