import { useEffect, useMemo, useState } from 'react';
import type { ScheduledTask } from '@/api/types';
import { Button } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { Field } from '@/components/Field';
import { Input, NumberInput } from '@/components/Input';
import { Radio, Switch } from '@/components/Switch';
import { Grid } from '@/components/Layout';
import { clone } from '@/lib/obj';
import { describeCron, nextRuns, parseCron } from '@/lib/cron';
import { formatSpan } from '@/lib/format';
import { defaultTask, MAX_TASK_TIMEOUT_SEC, NAME_RE } from '@/lib/siteDefaults';
import { cn } from '@/lib/cn';
import { ArgsInput } from '../editors/ArgsInput';
import { EnvVarsEditor } from '../editors/EnvEditor';

const PRESETS = [
  { label: 'Every 5 minutes', value: '*/5 * * * *' },
  { label: 'Hourly', value: '0 * * * *' },
  { label: 'Daily at 03:00', value: '0 3 * * *' },
  { label: 'Weekly, Monday 03:00', value: '0 3 * * mon' },
  { label: 'On demand only', value: '' },
];

export const OVERLAP_OPTIONS = [
  { value: 'skip', label: 'Skip', description: 'Record a skipped run and wait for the next time.' },
  { value: 'queue', label: 'Queue', description: 'Run once right after the current run ends (at most one waits).' },
  { value: 'allow', label: 'Run in parallel', description: 'Start another process alongside (up to 10).' },
];

const hm = new Intl.DateTimeFormat(undefined, { weekday: 'short', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false });

/** Client-side checks; the server validates again. */
function taskErrors(t: ScheduledTask, others: ScheduledTask[]) {
  const schedule = t.schedule.trim() ? parseCron(t.schedule) : null;
  return {
    name: !NAME_RE.test(t.name.trim())
      ? "1–64 characters: letters, digits, space, '.', '_' or '-'"
      : others.some((o) => o.name.trim().toLowerCase() === t.name.trim().toLowerCase())
        ? `Another task is called "${t.name.trim()}"`
        : null,
    schedule: schedule && !schedule.ok ? schedule.error : schedule && nextRuns(t.schedule, new Date(), 1).length === 0 ? 'This schedule never runs' : null,
    script: !t.script?.trim() && !t.npmScript?.trim() ? 'Set a script or an npm script' : null,
    timeout: t.timeoutSec < 1 || t.timeoutSec > MAX_TASK_TIMEOUT_SEC ? `Between 1 second and ${MAX_TASK_TIMEOUT_SEC} seconds (7 days)` : null,
  };
}

/** Adds or edits one task of the site draft. */
export function TaskDialog({
  value,
  index,
  others,
  readOnly,
  onClose,
  onApply,
}: {
  /** The task to edit; null = closed. */
  value: ScheduledTask | null;
  /** Its position in site.tasks (for server field errors). */
  index: number;
  /** The site's other tasks, for the unique-name check. */
  others: ScheduledTask[];
  readOnly?: boolean;
  onClose: () => void;
  onApply: (t: ScheduledTask) => void;
}) {
  const [t, setT] = useState<ScheduledTask>(defaultTask);
  const [touched, setTouched] = useState(false);
  // Held separately so clearing the npm script does not flip the choice.
  const [mode, setMode] = useState<'script' | 'npm'>('script');
  useEffect(() => {
    if (value) {
      setT(clone(value));
      setMode(value.npmScript ? 'npm' : 'script');
    }
    setTouched(false);
  }, [value]);

  const set = (patch: Partial<ScheduledTask>) => setT((x) => ({ ...x, ...patch }));
  const errs = taskErrors(t, others);
  const p = `tasks[${index}]`;
  const preview = useMemo(() => (t.schedule.trim() ? nextRuns(t.schedule, new Date(), 5) : []), [t.schedule]);
  const isNew = !value?.id && !value?.name;

  return (
    <Dialog
      open={!!value}
      onClose={onClose}
      size="lg"
      title={readOnly ? t.name || 'Task' : isNew ? 'Add task' : `Edit ${value?.name || 'task'}`}
      description="Changes take effect when you save the site."
      onSubmit={() => {
        if (readOnly) return onClose();
        setTouched(true);
        if (errs.name || errs.schedule || errs.script || errs.timeout) return;
        onApply({ ...t, name: t.name.trim(), schedule: t.schedule.trim(), script: t.script?.trim(), npmScript: t.npmScript?.trim() });
      }}
      footer={
        readOnly ? (
          <Button onClick={onClose}>Close</Button>
        ) : (
          <>
            <Button onClick={onClose}>Cancel</Button>
            <Button type="submit" variant="primary">
              Apply
            </Button>
          </>
        )
      }
    >
      <fieldset disabled={readOnly} className="min-w-0 space-y-4">
        <Grid>
          <Field label="Name" path={`${p}.name`} required error={touched || t.name ? errs.name : null}>
            <Input value={t.name} maxLength={64} placeholder="nightly-cleanup" onChange={(e) => set({ name: e.target.value })} />
          </Field>
          <Switch
            className="sm:pt-6"
            checked={t.enabled}
            onChange={(v) => set({ enabled: v })}
            label="Enabled"
            description="A disabled task only runs when started with Run now."
          />
        </Grid>

        <Field
          label="Schedule"
          path={`${p}.schedule`}
          error={errs.schedule}
          hint={
            <>
              minute hour day-of-month month day-of-week, in the server's local time, e.g. <span className="font-mono">*/15 * * * *</span> or{' '}
              <span className="font-mono">0 3 * * mon-fri</span>. Also @hourly, @daily, @weekly, @monthly and <span className="font-mono">@every 30m</span>. Empty
              = only when started manually.
            </>
          }
        >
          <Input mono value={t.schedule} placeholder="(on demand only)" onChange={(e) => set({ schedule: e.target.value })} />
        </Field>
        {!readOnly && (
          <div className="-mt-2 flex flex-wrap gap-1.5">
            {PRESETS.map((pr) => (
              <button
                key={pr.label}
                type="button"
                onClick={() => set({ schedule: pr.value })}
                className={cn(
                  'rounded-full border px-2 py-0.5 text-xs transition-colors',
                  t.schedule.trim() === pr.value
                    ? 'border-accent-600 bg-accent-50 text-accent-800 dark:border-accent-500 dark:bg-accent-500/10 dark:text-accent-200'
                    : 'border-zinc-300 text-zinc-600 hover:border-zinc-400 dark:border-zinc-700 dark:text-zinc-300 dark:hover:border-zinc-600',
                )}
              >
                {pr.label}
              </button>
            ))}
          </div>
        )}
        {!errs.schedule && (
          <div className="rounded-md border border-zinc-200 bg-zinc-50 px-3 py-2 text-[13px] dark:border-zinc-800 dark:bg-zinc-900/60">
            <p className="font-medium">{describeCron(t.schedule)}</p>
            {preview.length > 0 && (
              <>
                <p className="mt-1.5 text-xs text-zinc-500">Next runs (server time):</p>
                <ul className="mt-0.5 grid gap-x-4 font-mono text-xs text-zinc-600 dark:text-zinc-300 sm:grid-cols-2">
                  {preview.map((d) => (
                    <li key={d.getTime()}>{hm.format(d)}</li>
                  ))}
                </ul>
              </>
            )}
          </div>
        )}

        <Field label="Run">
          <Radio
            value={mode}
            onChange={(m) => {
              setMode(m);
              set(m === 'npm' ? { npmScript: t.npmScript || 'start', script: '' } : { script: t.script || '', npmScript: '' });
            }}
            options={[
              { value: 'script', label: 'Script', description: 'node <file>' },
              { value: 'npm', label: 'npm script', description: 'npm run <script>' },
            ]}
          />
        </Field>
        <Grid>
          {mode === 'script' ? (
            <Field label="Script" path={`${p}.script`} error={touched ? errs.script : null} hint="Relative to the application folder of the active release.">
              <Input mono value={t.script ?? ''} placeholder="scripts/cleanup.js" onChange={(e) => set({ script: e.target.value })} />
            </Field>
          ) : (
            <Field label="npm script" path={`${p}.npmScript`} error={touched ? errs.script : null} hint="A script from package.json.">
              <Input mono value={t.npmScript ?? ''} placeholder="cleanup" onChange={(e) => set({ npmScript: e.target.value })} />
            </Field>
          )}
          <Field label="Arguments" path={`${p}.args`} prefix>
            <ArgsInput value={t.args} onChange={(v) => set({ args: v })} placeholder="--dry-run" disabled={readOnly} />
          </Field>
        </Grid>

        <Grid>
          <Field
            label="Timeout"
            path={`${p}.timeoutSec`}
            error={errs.timeout}
            hint={errs.timeout ? undefined : `${formatSpan(t.timeoutSec)}. Then the whole process tree is killed.`}
          >
            <NumberInput min={1} max={MAX_TASK_TIMEOUT_SEC} value={t.timeoutSec} onChange={(v) => set({ timeoutSec: v })} suffix="sec" />
          </Field>
        </Grid>
        <Field label="When the previous run is still going" path={`${p}.overlap`}>
          <Radio value={t.overlap} onChange={(v) => set({ overlap: v })} options={OVERLAP_OPTIONS} />
        </Field>

        <Field label="Extra environment variables" hint="Added to the site's variables for this task only; a name used by both takes this value.">
          <EnvVarsEditor
            env={t.env ?? []}
            onChange={(env) => set({ env })}
            path={`${p}.env`}
            readOnly={readOnly}
            importable={false}
            emptyDescription={
              <>
                The task already gets the site's variables and secrets, plus <span className="font-mono">NODEHOSTER_TASK</span> (its name) and{' '}
                <span className="font-mono">NODEHOSTER_TASK_RUN</span> (the run id).
              </>
            }
          />
        </Field>
      </fieldset>
    </Dialog>
  );
}
