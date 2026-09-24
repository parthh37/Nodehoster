import type { OCSPStatus } from '@/api/types';
import { Badge } from '@/components/Badge';
import { relativeTime } from '@/lib/format';
import { ocspBadge } from '@/lib/tls';

/** A certificate's OCSP stapling state, with the details in its tooltip. */
export function OcspBadge({ status }: { status: OCSPStatus | undefined }) {
  const b = ocspBadge(status);
  if (!b || !status) return <span className="text-xs text-zinc-400">—</span>;
  const lines = [b.detail];
  if (status.responder) lines.push(`Responder: ${status.responder}`);
  if (status.lastCheck) lines.push(`Last checked ${relativeTime(status.lastCheck)}`);
  if (status.nextCheck) lines.push(`Next check ${relativeTime(status.nextCheck)}`);
  if (status.lastError && status.state !== 'error') lines.push(`Last error: ${status.lastError}`);
  return (
    <div className="flex flex-col items-start gap-0.5" title={lines.join('\n')}>
      <Badge tone={b.tone} dot>
        {b.label}
      </Badge>
      {status.mustStaple && <span className="text-2xs text-zinc-500">Must-Staple</span>}
    </div>
  );
}
