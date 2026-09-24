import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { BellRing } from 'lucide-react';
import { alertsApi } from '@/api/alerts';
import { qk } from '@/api/queryKeys';
import { alertCounts, alertSubject } from '@/lib/alerts';
import { Callout } from '@/components/Layout';

/** The dashboard's banner: alerts firing, most severe first (nothing when none are). */
export function AlertsBanner({ className }: { className?: string }) {
  const q = useQuery({ queryKey: qk.alertList(), queryFn: () => alertsApi.list(), refetchInterval: 15_000 });
  const firing = (q.data?.firing ?? []).filter((a) => !a.silence);
  if (firing.length === 0) return null;
  const c = alertCounts(q.data);
  const parts = [c.critical && `${c.critical} critical`, c.warning && `${c.warning} warning${c.warning === 1 ? '' : 's'}`].filter(Boolean);
  return (
    <Callout
      className={className}
      tone={c.critical ? 'danger' : 'warning'}
      icon={<BellRing />}
      title={`${firing.length} alert${firing.length === 1 ? '' : 's'} firing (${parts.join(', ')})`}
      actions={
        <Link to="/alerts" className="nh-link text-xs font-medium">
          View alerts
        </Link>
      }
    >
      <ul className="mt-1 space-y-0.5">
        {firing.slice(0, 3).map((a) => (
          <li key={a.id} className="truncate">
            <span className="font-medium">{alertSubject(a)}:</span> {a.message}
          </li>
        ))}
        {firing.length > 3 && <li className="text-xs opacity-80">and {firing.length - 3} more</li>}
      </ul>
    </Callout>
  );
}
