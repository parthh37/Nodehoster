import { useEffect, useState } from 'react';
import { RotateCcw } from 'lucide-react';
import type { Deployment, Site } from '@/api/types';
import { Button } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { Callout } from '@/components/Layout';
import { formatDateTime } from '@/lib/format';
import { releaseSlots, siteSlots, slotLabel } from '@/lib/slots';
import { SlotTargetField } from './SlotBits';

/**
 * Activates a release in production or in a deployment slot (a site with
 * slots): any successful release can run in any slot.
 */
export function ActivateReleaseDialog({
  site,
  dep,
  pending,
  onClose,
  onConfirm,
}: {
  site: Site;
  /** The release to activate, null when closed. */
  dep: Deployment | null;
  pending: boolean;
  onClose: () => void;
  /** `slot` is "" for production. */
  onConfirm: (dep: Deployment, slot: string) => void;
}) {
  const [slot, setSlot] = useState('');
  useEffect(() => {
    if (!dep) return;
    // Default to where the release was deployed, while that slot exists.
    const own = dep.slot && siteSlots(site).some((s) => s.name === dep.slot) ? dep.slot : '';
    setSlot(own);
    // Only when opening.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dep]);

  const runningIn = dep ? releaseSlots(site, dep) : [];
  const already = runningIn.includes(slotLabel(slot));
  const prod = slot === '';

  return (
    <Dialog
      open={!!dep}
      onClose={onClose}
      size="sm"
      title="Activate this release?"
      description={
        dep && (
          <>
            The release from {formatDateTime(dep.startedAt)}
            {dep.commit && (
              <>
                {' '}
                (<span className="font-mono">{dep.commit.slice(0, 8)}</span>)
              </>
            )}
            .
          </>
        )
      }
      onSubmit={() => dep && !already && onConfirm(dep, slot)}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant={prod ? 'danger' : 'primary'} icon={<RotateCcw className="h-3.5 w-3.5" />} loading={pending} disabled={already}>
            Activate in {slotLabel(slot)}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <SlotTargetField site={site} value={slot} onChange={setSlot} label="Activate in" />
        {already ? (
          <Callout tone="info">{slotLabel(slot)} already runs this release.</Callout>
        ) : (
          <p className="text-[13px] text-zinc-600 dark:text-zinc-300">
            {prod
              ? 'Production switches to this release. Node instances are recycled without downtime.'
              : `The ${slot} slot switches to this release; production is not affected. Swap it into production when it is ready.`}
          </p>
        )}
      </div>
    </Dialog>
  );
}
