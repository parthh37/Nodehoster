import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { BellRing, Settings } from 'lucide-react';
import { alertsApi } from '@/api/alerts';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import { usePermissions } from '@/hooks/useAuth';
import { alertCounts } from '@/lib/alerts';
import { Button } from '@/components/Button';
import { Callout, Card, Loading, PageHeader, Stat } from '@/components/Layout';
import { ErrorBox } from '@/components/Field';
import { Select } from '@/components/Input';
import { ActiveAlerts, AlertHistory } from './AlertViews';

const HISTORY_FILTERS = [
  { value: 'all', label: 'All alerts' },
  { value: 'server', label: 'Server alerts only' },
];

/** Resource alerts: what is firing or pending now, and what fired recently. */
export function AlertsPage() {
  const { isAdmin, siteScoped } = usePermissions();
  const [filter, setFilter] = useState('all');
  const server = filter === 'server';
  const list = useQuery({ queryKey: qk.alertList(), queryFn: () => alertsApi.list(), refetchInterval: 15_000 });
  const history = useQuery({ queryKey: qk.alertHistory(undefined, server), queryFn: () => alertsApi.history({ server, limit: 200 }), refetchInterval: 60_000 });
  const counts = alertCounts(list.data);
  const firing = list.data?.firing ?? [];
  const pending = list.data?.pending ?? [];

  return (
    <div className="space-y-5">
      <PageHeader
        title="Alerts"
        icon={<BellRing className="h-4 w-4" />}
        description="Notifications when a metric stays past a limit: CPU, memory, event-loop lag, errors, response times, instances down, and the server's CPU, memory and disks."
        actions={
          isAdmin && (
            <Link to="/settings/alerts">
              <Button icon={<Settings className="h-4 w-4" />}>Rules</Button>
            </Link>
          )
        }
      />
      {list.isPending && <Loading />}
      {list.isError && <ErrorBox>{errorMessage(list.error)}</ErrorBox>}
      {list.data && !list.data.enabled && (
        <Callout tone="info" title="Alerts are off">
          No rule is evaluated. {isAdmin ? (
            <Link to="/settings/alerts" className="nh-link">
              Turn them on in Settings › Alerts
            </Link>
          ) : (
            'An administrator turns them on in Settings › Alerts.'
          )}
          .
        </Callout>
      )}
      {list.data && (
        <>
          <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
            <Stat label="Critical" value={counts.critical} tone={counts.critical ? 'red' : 'default'} sub="firing" />
            <Stat label="Warnings" value={counts.warning} tone={counts.warning ? 'amber' : 'default'} sub="firing" />
            <Stat label="Silenced" value={counts.silenced} sub="firing, quiet" />
            <Stat label="Pending" value={pending.length} sub="past a limit, not yet long enough" />
          </div>
          <Card title="Firing" flush>
            <ActiveAlerts alerts={firing} />
          </Card>
          {pending.length > 0 && (
            <Card title="Pending" description="Past their limit, not yet for their rule's whole period: they fire if it lasts." flush>
              <ActiveAlerts alerts={pending} />
            </Card>
          )}
        </>
      )}
      <Card
        title="History"
        description="Alerts that fired, kept as long as events (Settings › Process & logs)."
        actions={!siteScoped && <Select className="w-48" value={filter} onChange={setFilter} options={HISTORY_FILTERS} />}
        flush
      >
        {history.isPending ? <Loading className="p-4" /> : history.isError ? <ErrorBox className="m-4">{errorMessage(history.error)}</ErrorBox> : <AlertHistory alerts={history.data ?? []} />}
      </Card>
    </div>
  );
}
