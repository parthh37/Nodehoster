import { useEffect, useState } from 'react';
import { ChevronRight, Layers, Lock, Plus, Trash2, Webhook } from 'lucide-react';
import type { DeploymentSlot, Site } from '@/api/types';
import { Badge } from '@/components/Badge';
import { Button } from '@/components/Button';
import { CopyField } from '@/components/CopyButton';
import { Dialog } from '@/components/Dialog';
import { Field, PathError } from '@/components/Field';
import { Card, FormSection, Grid, Sections } from '@/components/Layout';
import { Input, NumberInput } from '@/components/Input';
import { ListEditor } from '@/components/ListEditor';
import { Switch } from '@/components/Switch';
import { useConfirm } from '@/components/Confirm';
import {
  MAX_SLOTS,
  MAX_WARMUP_PATHS,
  newSlot,
  slotBindings,
  slotEnvSummary,
  statusRangesError,
  suggestSlotName,
  validateSlotName,
  warmupPathError,
  warmupTimeoutError,
  withoutSlot,
} from '@/lib/slots';
import { pluralize } from '@/lib/format';
import { cn } from '@/lib/cn';
import { EnvVarsEditor } from '../editors/EnvEditor';
import type { SiteEditorProps } from '../editors/types';

/**
 * The slots' settings: part of the site draft, saved with the SaveBar by
 * administrators and read-only for everyone else.
 */
export function SlotSettings({ site, update, readOnly, savedSite }: SiteEditorProps & { savedSite: Site }) {
  const slots = site.slots ?? [];
  const savedNames = (savedSite.slots ?? []).map((s) => s.name);
  const setSlot = (i: number, fn: (s: DeploymentSlot) => void) =>
    update((d) => {
      const list = d.slots ?? [];
      if (list[i]) fn(list[i]);
    });

  return (
    <div className="space-y-5">
      {slots.map((s, i) => (
        <SlotCard
          key={i}
          index={i}
          slot={s}
          site={site}
          saved={savedNames.includes(s.name)}
          readOnly={readOnly}
          set={(fn) => setSlot(i, fn)}
          onRemove={() =>
            update((d) => {
              const r = withoutSlot(d, s.name);
              d.bindings = r.bindings;
              d.slots = r.slots && r.slots.length ? r.slots : undefined;
            })
          }
        />
      ))}
    </div>
  );
}

function SlotCard({
  index,
  slot,
  site,
  saved,
  readOnly,
  set,
  onRemove,
}: {
  index: number;
  slot: DeploymentSlot;
  site: Site;
  /** Saved under this name (else added in this draft). */
  saved: boolean;
  readOnly?: boolean;
  set: (fn: (s: DeploymentSlot) => void) => void;
  onRemove: () => void;
}) {
  const confirm = useConfirm();
  const p = `slots[${index}]`;
  const others = (site.slots ?? []).filter((_, j) => j !== index).map((s) => s.name);
  const nameErr = slot.name ? validateSlotName(slot.name, others) : null;
  const w = slot.warmup ?? { paths: ['/'], statuses: '', timeoutSec: 0 };
  const bindings = slotBindings(site.bindings, slot.name);
  const hookUrl = `${window.location.origin}/hooks/deploy/${site.id}?slot=${encodeURIComponent(slot.name)}`;

  const remove = async () => {
    const r = await confirm({
      title: `Remove the ${slot.name || 'new'} slot?`,
      message: (
        <>
          After saving, its instances stop and its settings are removed
          {bindings.length > 0 && <>, along with its {pluralize(bindings.length, 'binding')}</>}. Production is not affected. Its releases stay in the
          deployment history.
        </>
      ),
      confirmLabel: 'Remove slot',
      danger: true,
    });
    if (r.ok) onRemove();
  };

  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <Layers className="h-4 w-4 text-zinc-400" />
          {slot.name || <span className="text-zinc-400">New slot</span>}
          {!saved && <Badge tone="amber">unsaved</Badge>}
        </span>
      }
      description="Settings that belong to this slot and are never swapped."
      actions={
        !readOnly && (
          <Button size="sm" variant="danger-ghost" icon={<Trash2 className="h-3.5 w-3.5" />} onClick={remove}>
            Remove slot
          </Button>
        )
      }
    >
      <fieldset disabled={readOnly} className="min-w-0">
        <Sections>
          <FormSection title="Slot" description="Its name appears in deployments, logs and bindings.">
            <Grid>
              <Field label="Name" path={`${p}.name`} error={nameErr} hint="A slot cannot be renamed: add another one instead.">
                <Input mono value={slot.name} readOnly />
              </Field>
              <Field label="Instances" path={`${p}.instances`} hint="Empty or 0: as many as production. A swap warms up production's count.">
                <NumberInput
                  min={0}
                  max={64}
                  blankZero
                  value={slot.instances ?? 0}
                  placeholder="As production"
                  onChange={(v) =>
                    set((s) => {
                      s.instances = v || undefined;
                    })
                  }
                />
              </Field>
            </Grid>
            <Switch
              checked={slot.autoSwap}
              onChange={(v) =>
                set((s) => {
                  s.autoSwap = v;
                })
              }
              label="Auto-swap"
              description="Swap this slot into production automatically after each successful deployment to it."
            />
          </FormSection>
          <FormSection
            title="Warm-up"
            description="Before a swap, every path is requested on every instance of the slot until it answers an accepted status, like IIS Application Initialization."
          >
            <Field label="Paths" path={`${p}.warmup.paths`} prefix hint={`Up to ${MAX_WARMUP_PATHS}. Defaults to /.`}>
              <ListEditor
                values={w.paths}
                onChange={(v) =>
                  set((s) => {
                    s.warmup = { ...w, paths: v };
                  })
                }
                placeholder="/health"
                validate={warmupPathError}
                disabled={readOnly || undefined}
              />
            </Field>
            <Grid>
              <Field label="Accepted statuses" path={`${p}.warmup.statuses`} error={statusRangesError(w.statuses)} hint="e.g. 200-399 or 200-299,401">
                <Input
                  mono
                  value={w.statuses}
                  onChange={(e) =>
                    set((s) => {
                      s.warmup = { ...w, statuses: e.target.value };
                    })
                  }
                />
              </Field>
              <Field label="Timeout" path={`${p}.warmup.timeoutSec`} error={warmupTimeoutError(w.timeoutSec)} hint="For all instances and paths together.">
                <NumberInput
                  min={5}
                  max={1800}
                  value={w.timeoutSec}
                  suffix="sec"
                  onChange={(v) =>
                    set((s) => {
                      s.warmup = { ...w, timeoutSec: v };
                    })
                  }
                />
              </Field>
            </Grid>
          </FormSection>
          <FormSection
            title="Variables"
            description={
              <>
                This slot's own variables, added to production's and replacing any of the same name. Production variables marked{' '}
                <em>slot setting</em> on the Environment tab are not given to slots.
              </>
            }
          >
            <EnvVarsEditor
              env={slot.env ?? []}
              onChange={(next) =>
                set((s) => {
                  s.env = next;
                })
              }
              path={`${p}.env`}
              readOnly={readOnly}
              portAssigned={site.type === 'node'}
              emptyDescription="The slot runs with production's variables. Add one to point this slot at another database or API key."
            />
            <EffectiveEnv site={site} slot={slot} />
          </FormSection>
          {saved && (
            <FormSection
              title={
                <span className="flex items-center gap-1.5">
                  <Webhook className="h-3.5 w-3.5" /> Deploy webhook
                </span>
              }
              description="The site's webhook deploys to this slot with ?slot= added. Uses the webhook secret of the Deployments tab."
            >
              <Field label="Payload URL">
                <CopyField value={hookUrl} />
              </Field>
            </FormSection>
          )}
        </Sections>
      </fieldset>
      <PathError path={p} className="mt-3" />
    </Card>
  );
}

/** What the slot's instances actually run with. */
function EffectiveEnv({ site, slot }: { site: Site; slot: DeploymentSlot }) {
  const [open, setOpen] = useState(false);
  const sum = slotEnvSummary(site.node?.env, slot.env);
  const vars = sum.vars.filter((v) => v.name);
  const inherited = vars.filter((v) => v.origin === 'production').length;
  return (
    <div className="rounded-md border border-zinc-200 text-xs dark:border-zinc-800">
      <button
        type="button"
        className="flex w-full items-center gap-1.5 px-3 py-2 text-left text-zinc-600 hover:bg-zinc-50 dark:text-zinc-300 dark:hover:bg-zinc-800/50"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <ChevronRight className={cn('h-3.5 w-3.5 transition-transform', open && 'rotate-90')} />
        Runs with {pluralize(vars.length, 'variable')}: {inherited} from production
        {sum.sticky.length > 0 && <>, {sum.sticky.length} production slot {sum.sticky.length === 1 ? 'setting' : 'settings'} left out</>}
      </button>
      {open && (
        <div className="flex flex-wrap gap-1 border-t border-zinc-200 px-3 py-2 dark:border-zinc-800">
          {vars.length === 0 && <span className="text-zinc-500">No variables.</span>}
          {vars.map((v) => (
            <Badge
              key={v.name}
              mono
              tone={v.origin === 'production' ? 'gray' : v.origin === 'overridden' ? 'amber' : 'violet'}
              title={v.origin === 'production' ? 'From production' : v.origin === 'overridden' ? "Overrides production's value" : 'This slot only'}
            >
              {v.secret && <Lock className="h-3 w-3" />}
              {v.name}
            </Badge>
          ))}
          {sum.sticky.map((n) => (
            <Badge key={`sticky-${n}`} mono tone="gray" className="line-through opacity-60" title="Slot setting: stays in production">
              {n}
            </Badge>
          ))}
        </div>
      )}
    </div>
  );
}

/** Asks for the name of another slot. */
export function AddSlotButton({ site, update }: Pick<SiteEditorProps, 'site' | 'update'>) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const existing = (site.slots ?? []).map((s) => s.name);
  const full = existing.length >= MAX_SLOTS;
  useEffect(() => {
    if (open) setName(suggestSlotName(existing));
    // Only when opening.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  const err = validateSlotName(name, existing);
  const add = () => {
    if (err) return;
    update((d) => {
      d.slots = [...(d.slots ?? []), newSlot(name.trim())];
    });
    setOpen(false);
  };
  return (
    <>
      <Button
        size="sm"
        icon={<Plus className="h-3.5 w-3.5" />}
        disabled={full}
        title={full ? `At most ${MAX_SLOTS} slots besides production` : undefined}
        onClick={() => setOpen(true)}
      >
        Add slot
      </Button>
      <Dialog
        open={open}
        onClose={() => setOpen(false)}
        size="sm"
        title="Add a deployment slot"
        description="It starts once something is deployed to it. Save the site to create it."
        onSubmit={add}
        footer={
          <>
            <Button onClick={() => setOpen(false)}>Cancel</Button>
            <Button type="submit" variant="primary" disabled={!!err}>
              Add slot
            </Button>
          </>
        }
      >
        <Field label="Name" error={name ? err : null} hint="e.g. staging, testing, preview">
          <Input mono value={name} onChange={(e) => setName(e.target.value.trim().toLowerCase())} />
        </Field>
      </Dialog>
    </>
  );
}
