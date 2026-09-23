import type { ReactNode } from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { ApiError, errorMessage } from '@/api/client';
import { useMe } from '@/hooks/useAuth';
import { Button } from '@/components/Button';
import { Logo } from './Logo';
import { Spinner } from '@/components/Layout';

export function AuthGate({ children }: { children: ReactNode }) {
  const me = useMe();
  const loc = useLocation();

  if (me.isPending) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <div className="flex flex-col items-center gap-3 text-zinc-500">
          <Logo className="h-8 w-8" />
          <Spinner />
        </div>
      </div>
    );
  }
  if (me.isError) {
    if (me.error instanceof ApiError && me.error.status === 401) {
      return <Navigate to={`/login?next=${encodeURIComponent(loc.pathname + loc.search)}`} replace />;
    }
    return (
      <div className="flex min-h-screen items-center justify-center p-6">
        <div className="nh-card w-full max-w-md p-6 text-center">
          <Logo className="mx-auto h-8 w-8" />
          <h1 className="mt-3 text-base font-semibold">Cannot load NodeHoster</h1>
          <p className="mt-1 text-[13px] text-zinc-500">{errorMessage(me.error)}</p>
          <Button className="mt-4" variant="primary" onClick={() => void me.refetch()} loading={me.isFetching}>
            Try again
          </Button>
        </div>
      </div>
    );
  }
  return <>{children}</>;
}
