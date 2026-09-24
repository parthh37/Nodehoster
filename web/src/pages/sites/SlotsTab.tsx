import { useState } from 'react';
import { Link } from 'react-router-dom';
import { ChevronRight, Info, Layers, Plus } from 'lucide-react';
import type { DeploymentSlot, Site } from '@/api/types';
import { Button } from '@/components/Button';
import { Callout, Card, EmptyState } from '@/components/Layout';
import { PathError } from '@/components/Field';
import { jsonEqual } from '@/lib/obj';
import { newSlot } from '@/lib/slots';
import { cn } from '@/lib/cn';
import type { SiteEditorProps } from './editors/types';
import { SlotStatusPanel } from './slots/SlotStatusPanel';
import { AddSlotButton, SlotSettings } from './slots/SlotSettings';

const settingsOf = (list: DeploymentSlot[] | undefined) => (list ?? []).map((s) => ({ ...s, activeRelease: undefined }));

/**
 * Deployment slots of a node or worker site. The live part (status,
 * start/stop/recycle, swap) acts on the saved slots and is for operators;
 * the slots' settings are part of the site draft, saved by administrators.
 */
export function SlotsTab({ site, update, readOnly, savedSite }: SiteEditorProps & { savedSite: Site }) {
  const slots = site.slots ?? [];
  const saved = savedSite.slots ?? [];
  const fixedPort = site.node?.portMode === 'fixed';
  const pendingSave = !jsonEqual(settingsOf(saved), settingsOf(slots));

  const addStaging = () =>
    update((d) => {
      d.slots = [...(d.slots ?? []), newSlot('staging')];
    });

  return (
    <div className="space-y-5">
      <HowSlotsWork defaultOpen={saved.length === 0} />

      {fixedPort && slots.length > 0 && (
        <Callout tone="warning" title="Slots need automatic ports">
          Two slots cannot listen on one fixed port. Switch the port mode to automatic on the{' '}
          <Link to={`/sites/${site.id}/settings`} className="nh-link">
            Settings tab
          </Link>{' '}
          before saving.
        </Callout>
      )}

      {saved.length > 0 && (
        <section className="space-y-3" aria-labelledby="slots-status">
          <h2 id="slots-status" className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">
            Status
          </h2>
          <SlotStatusPanel site={savedSite} />
        </section>
      )}

      {slots.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Layers />}
            title={saved.length > 0 ? 'All slots removed' : 'No deployment slots'}
            description={
              saved.length > 0 ? (
                'Save to stop and remove them, or discard your changes to keep them.'
              ) : fixedPort ? (
                <>
                  Deploy to a staging copy of this site, test it on its own host name, then swap it into production without a cold start. Slots
                  need automatic ports: this site uses a fixed port, so change that on the{' '}
                  <Link to={`/sites/${site.id}/settings`} className="nh-link">
                    Settings tab
                  </Link>{' '}
                  first.
                </>
              ) : (
                'Deploy to a staging copy of this site, test it on its own host name, then swap it into production without a cold start. Swapping back is the rollback.'
              )
            }
            action={
              !readOnly &&
              saved.length === 0 && (
                <Button variant="primary" icon={<Plus className="h-4 w-4" />} disabled={fixedPort} onClick={addStaging}>
                  Add a staging slot
                </Button>
              )
            }
          />
          <PathError path="slots" className="px-4 pb-3" />
        </Card>
      ) : (
        <section className="space-y-3" aria-labelledby="slots-settings">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <h2 id="slots-settings" className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">
                Slot settings
              </h2>
              <p className="text-xs text-zinc-500 dark:text-zinc-400">
                Everything else — script, Node.js version, limits, routing — comes from production. Bindings are assigned to a slot on the Bindings tab.
              </p>
            </div>
            {!readOnly && <AddSlotButton site={site} update={update} />}
          </div>
          {pendingSave && !readOnly && <Callout tone="warning">Save your changes to apply them. New slots can be deployed to once saved.</Callout>}
          <PathError path="slots" />
          <SlotSettings site={site} update={update} readOnly={readOnly} savedSite={savedSite} />
        </section>
      )}
    </div>
  );
}

function HowSlotsWork({ defaultOpen }: { defaultOpen: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <Callout tone="info" icon={<Info />}>
      <button type="button" className="flex items-center gap-1 font-semibold" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        How deployment slots work
        <ChevronRight className={cn('h-3.5 w-3.5 transition-transform', open && 'rotate-90')} />
      </button>
      {open && (
        <ul className="mt-1.5 list-disc space-y-1 pl-4">
          <li>
            Like Azure App Service deployment slots: a slot runs its own release on its own instances and its own bindings (e.g.{' '}
            <span className="font-mono">staging.example.com</span>). Deploy to it from the Deployments tab.
          </li>
          <li>
            A slot uses production's configuration. Its own variables override production's; production variables marked <em>slot setting</em>{' '}
            stay in production and are not given to slots.
          </li>
          <li>
            <b>Swap</b> restarts the slot with production's settings, warms it up, then moves production traffic onto those warm instances at once:
            no cold start, and requests in flight finish. Production's old release becomes the slot's, so swapping again is the rollback.
          </li>
          <li>
            <b>Auto-swap</b> swaps a slot into production after each successful deployment to it.
          </li>
          <li>Bindings stay with their slot on a swap. Scheduled tasks run in production only, and a slot is never load balanced to other servers.</li>
        </ul>
      )}
    </Callout>
  );
}
