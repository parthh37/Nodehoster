import { Link, useSearchParams } from 'react-router-dom';
import { ShieldAlert } from 'lucide-react';
import { usePermissions } from '@/hooks/useAuth';
import { PageHeader } from '@/components/Layout';
import { WafEvents } from './WafEvents';

/** Every firewall event the caller may see, across sites. */
export function WafEventsPage() {
  const [params] = useSearchParams();
  const { isAdmin } = usePermissions();
  return (
    <div>
      <PageHeader
        icon={<ShieldAlert className="h-4 w-4" />}
        title="Web application firewall"
        description={
          <>
            Requests blocked — or, in detect mode, that would have been — for SQL injection, cross-site scripting, path traversal and similar attacks. Each site
            sets its mode and exclusions on its Firewall tab
            {isAdmin && (
              <>
                ; defaults for new sites and banning of repeat offenders are in{' '}
                <Link to="/settings/security" className="nh-link">
                  Settings › Security
                </Link>
              </>
            )}
            .
          </>
        }
      />
      <WafEvents initialSite={params.get('site') ?? ''} />
    </div>
  );
}
