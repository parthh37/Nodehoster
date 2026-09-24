import { useMemo, useState, type ReactNode } from 'react';
import { ClipboardPaste, EyeOff, Layers, Lock, Plus, Search, Trash2, Unlock } from 'lucide-react';
import type { EnvVar } from '@/api/types';
import { SECRET } from '@/api/types';
import { Button, IconButton } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { Field, PathError, useFieldError } from '@/components/Field';
import { Input, Textarea } from '@/components/Input';
import { SecretInput } from '@/components/SecretInput';
import { Checkbox } from '@/components/Switch';
import { Callout, EmptyState } from '@/components/Layout';
import { Badge } from '@/components/Badge';
import { ENV_NAME_RE } from '@/lib/siteDefaults';
import { looksSecret, parseDotEnv } from '@/lib/dotenv';
import { cn } from '@/lib/cn';
import type { SiteEditorProps } from './types';

export function EnvEditor({ site, update, readOnly }: SiteEditorProps) {
  return (
    <EnvVarsEditor
      env={site.node?.env ?? []}
      onChange={(next) =>
        update((d) => {
          d.node!.env = next;
        })
      }
      path="node.env"
      readOnly={readOnly}
      portAssigned={site.type === 'node'}
      slotSettings={(site.slots ?? []).length > 0}
    />
  );
}

// With slot settings the rows get another column.
const ROW_COLS = 'grid-cols-[minmax(10rem,18rem)_1fr_5.5rem_2.25rem]';
const ROW_COLS_SLOTS = 'grid-cols-[minmax(10rem,18rem)_1fr_5.5rem_6.5rem_2.25rem]';

function SlotSettingToggle({ on, disabled, onChange }: { on: boolean; disabled?: boolean; onChange: (v: boolean) => void }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      aria-label="Slot setting"
      disabled={disabled}
      onClick={() => onChange(!on)}
      title={
        on
          ? 'Slot setting: stays in production and is not given to deployment slots. Click to share it with every slot.'
          : 'Shared: every deployment slot inherits it unless the slot sets its own. Click to keep it in production only.'
      }
      className={cn(
        'inline-flex items-center gap-1 rounded px-1.5 py-1 text-xs font-medium transition-colors disabled:cursor-not-allowed',
        on ? 'bg-violet-100 text-violet-800 dark:bg-violet-500/15 dark:text-violet-300' : 'text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800',
      )}
    >
      <Layers className="h-3.5 w-3.5" />
      {on ? 'Production' : 'All slots'}
    </button>
  );
}

/**
 * Edits a list of environment variables with secrets and .env import: a
 * site's variables, or the extra ones of a scheduled task.
 */
export function EnvVarsEditor({
  env,
  onChange: setEnv,
  path,
  readOnly,
  portAssigned,
  emptyDescription,
  importable = true,
  slotSettings,
}: {
  env: EnvVar[];
  onChange: (next: EnvVar[]) => void;
  /** Server field path of the list, e.g. "node.env". */
  path: string;
  readOnly?: boolean;
  /** NodeHoster sets PORT for these processes (node sites, not workers or tasks). */
  portAssigned?: boolean;
  emptyDescription?: ReactNode;
  /** Offer "Paste .env" (off inside another dialog: Escape would close both). */
  importable?: boolean;
  /** Production's variables of a site with deployment slots: offer the per-variable "slot setting" toggle. */
  slotSettings?: boolean;
}) {
  const [filter, setFilter] = useState('');
  const [importing, setImporting] = useState(false);

  const setVar = (i: number, patch: Partial<EnvVar>) => setEnv(env.map((e, j) => (j === i ? { ...e, ...patch } : e)));

  const dupes = useMemo(() => {
    const seen = new Map<string, number>();
    for (const e of env) seen.set(e.name, (seen.get(e.name) ?? 0) + 1);
    return new Set([...seen].filter(([, n]) => n > 1).map(([k]) => k));
  }, [env]);

  const term = filter.trim().toLowerCase();
  const visible = env.map((e, i) => ({ e, i })).filter(({ e }) => !term || e.name.toLowerCase().includes(term));

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          className="w-64"
          prefix={<Search className="h-3.5 w-3.5" />}
          placeholder="Filter variables…"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
        <span className="text-xs text-zinc-500">
          {env.length} variables · {env.filter((e) => e.secret).length} secret
        </span>
        {!readOnly && (
          <div className="ml-auto flex gap-2">
            {importable && (
              <Button size="sm" icon={<ClipboardPaste className="h-3.5 w-3.5" />} onClick={() => setImporting(true)}>
                Paste .env
              </Button>
            )}
            <Button size="sm" variant="primary" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setEnv([...env, { name: '', value: '', secret: false }])}>
              Add variable
            </Button>
          </div>
        )}
      </div>

      {env.length === 0 ? (
        <div className="rounded-lg border border-dashed border-zinc-300 dark:border-zinc-700">
          <EmptyState
            compact
            icon={<Lock />}
            title="No environment variables"
            description={
              emptyDescription ?? (
                <>
                  Variables are passed to every instance.{portAssigned && <> <span className="font-mono">PORT</span> is set automatically.</>} Mark API
                  keys and passwords as secret so they are encrypted at rest and never shown again.
                </>
              )
            }
            action={
              !readOnly && (
                <>
                  {importable && (
                    <Button icon={<ClipboardPaste className="h-4 w-4" />} onClick={() => setImporting(true)}>
                      Paste a .env file
                    </Button>
                  )}
                  <Button variant="primary" icon={<Plus className="h-4 w-4" />} onClick={() => setEnv([{ name: '', value: '', secret: false }])}>
                    Add variable
                  </Button>
                </>
              )
            }
          />
        </div>
      ) : (
        <div className="overflow-hidden rounded-lg border border-zinc-200 dark:border-zinc-800">
          {slotSettings && (
            <p className="border-b border-zinc-200 bg-violet-50/60 px-3 py-1.5 text-xs text-violet-900 dark:border-zinc-800 dark:bg-violet-500/10 dark:text-violet-200">
              This site has deployment slots: they inherit these variables unless marked <b>Production</b> (a slot setting, which stays in
              production). A slot's own variables are set on the Slots tab.
            </p>
          )}
          <div className={cn('grid gap-2 border-b border-zinc-200 bg-zinc-50 px-3 py-1.5 text-2xs font-semibold uppercase tracking-wide text-zinc-500 dark:border-zinc-800 dark:bg-zinc-900/60', slotSettings ? ROW_COLS_SLOTS : ROW_COLS)}>
            <span>Name</span>
            <span>Value</span>
            <span className="text-center">Secret</span>
            {slotSettings && (
              <span className="text-center" title="Which deployment slots get the variable">
                Slots
              </span>
            )}
            <span />
          </div>
          <div className="divide-y divide-zinc-100 dark:divide-zinc-800/80">
            {visible.map(({ e, i }) => (
              <EnvRow
                key={i}
                path={`${path}[${i}]`}
                portAssigned={portAssigned}
                v={e}
                dupe={dupes.has(e.name) && !!e.name}
                readOnly={readOnly}
                slotSettings={slotSettings}
                onChange={(p) => setVar(i, p)}
                onRemove={() => setEnv(env.filter((_, j) => j !== i))}
              />
            ))}
            {visible.length === 0 && <p className="px-3 py-4 text-center text-xs text-zinc-500">No variables match “{filter}”.</p>}
          </div>
        </div>
      )}
      <PathError path={path} />
      <ImportDialog
        open={importing}
        onClose={() => setImporting(false)}
        existing={env}
        onImport={(next) => {
          setEnv(next);
          setImporting(false);
        }}
      />
    </div>
  );
}

function EnvRow({
  path,
  portAssigned,
  v,
  dupe,
  readOnly,
  slotSettings,
  onChange,
  onRemove,
}: {
  /** Server field path of the variable, e.g. "node.env[2]". */
  path: string;
  portAssigned?: boolean;
  v: EnvVar;
  dupe: boolean;
  readOnly?: boolean;
  slotSettings?: boolean;
  onChange: (p: Partial<EnvVar>) => void;
  onRemove: () => void;
}) {
  const nameErr = useFieldError(`${path}.name`);
  const rowErr = useFieldError(path, false);
  const valueErr = useFieldError(`${path}.value`);
  const clientNameErr =
    v.name && !ENV_NAME_RE.test(v.name) ? 'Letters, digits and _; cannot start with a digit' : dupe ? 'Duplicate name' : null;
  const err = nameErr || clientNameErr || rowErr || valueErr;
  const reserved = !!portAssigned && v.name.toUpperCase() === 'PORT';
  const isStoredSecret = v.value === SECRET;

  return (
    <div className="px-3 py-2">
      <div className={cn('grid items-center gap-2', slotSettings ? ROW_COLS_SLOTS : ROW_COLS)}>
        <Input
          mono
          value={v.name}
          readOnly={isStoredSecret}
          title={isStoredSecret ? 'Renaming a stored secret would discard its value. Remove it and add a new variable instead.' : undefined}
          placeholder="NAME"
          invalid={!!(nameErr || clientNameErr)}
          onChange={(e) => onChange({ name: e.target.value.replace(/\s/g, '') })}
          onBlur={() => !v.secret && v.name && looksSecret(v.name) && v.value !== SECRET && !v.value && onChange({ secret: true })}
        />
        {v.secret ? (
          <SecretInput value={v.value} onChange={(val) => onChange({ value: val })} placeholder="value" allowClear={false} />
        ) : (
          <Input mono value={v.value} placeholder="value" onChange={(e) => onChange({ value: e.target.value })} />
        )}
        <div className="flex justify-center">
          <button
            type="button"
            disabled={readOnly || (isStoredSecret && v.secret)}
            onClick={() => onChange({ secret: !v.secret })}
            title={isStoredSecret ? 'Stored secrets cannot be revealed. Enter a new value to change it.' : v.secret ? 'Secret — click to make plain' : 'Plain — click to mark secret'}
            className={cn(
              'inline-flex items-center gap-1 rounded px-1.5 py-1 text-xs font-medium transition-colors disabled:cursor-not-allowed',
              v.secret
                ? 'bg-amber-100 text-amber-800 dark:bg-amber-500/15 dark:text-amber-300'
                : 'text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800',
            )}
          >
            {v.secret ? <Lock className="h-3.5 w-3.5" /> : <Unlock className="h-3.5 w-3.5" />}
            {v.secret ? 'Secret' : 'Plain'}
          </button>
        </div>
        {slotSettings && (
          <div className="flex justify-center">
            <SlotSettingToggle on={!!v.slotSetting} disabled={readOnly} onChange={(on) => onChange({ slotSetting: on || undefined })} />
          </div>
        )}
        {!readOnly ? (
          <IconButton label="Remove variable" variant="danger-ghost" icon={<Trash2 className="h-3.5 w-3.5" />} onClick={onRemove} />
        ) : (
          <span />
        )}
      </div>
      {(err || reserved) && (
        <p className={cn('mt-1 text-xs', err ? 'text-red-600 dark:text-red-400' : 'text-amber-600 dark:text-amber-400')}>
          {err || 'PORT is assigned by NodeHoster per instance and will be overridden.'}
        </p>
      )}
    </div>
  );
}

function ImportDialog({
  open,
  onClose,
  existing,
  onImport,
}: {
  open: boolean;
  onClose: () => void;
  existing: EnvVar[];
  onImport: (next: EnvVar[]) => void;
}) {
  const [text, setText] = useState('');
  const [overwrite, setOverwrite] = useState(true);
  const [autoSecret, setAutoSecret] = useState(true);
  const parsed = useMemo(() => parseDotEnv(text), [text]);
  const existingNames = new Set(existing.map((e) => e.name));
  const added = parsed.vars.filter((v) => !existingNames.has(v.name));
  const replaced = parsed.vars.filter((v) => existingNames.has(v.name));

  const apply = () => {
    const incoming = parsed.vars.map((v) => ({ ...v, secret: autoSecret ? v.secret : false }));
    const byName = new Map(incoming.map((v) => [v.name, v]));
    const next = existing.map((e) => (overwrite && byName.has(e.name) ? byName.get(e.name)! : e));
    for (const v of incoming) if (!existingNames.has(v.name)) next.push(v);
    onImport(next);
    setText('');
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="lg"
      title="Import from .env"
      description="Paste the contents of a .env file. Comments, quotes and export prefixes are handled."
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={parsed.vars.length === 0} onClick={apply}>
            Import {parsed.vars.length ? parsed.vars.length : ''} variables
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Field label=".env contents">
          <Textarea mono rows={10} value={text} onChange={(e) => setText(e.target.value)} placeholder={'# Example\nNODE_ENV=production\nDATABASE_URL="postgres://…"\nAPI_KEY=sk_live_…'} spellCheck={false} />
        </Field>
        <div className="flex flex-wrap gap-x-6 gap-y-2">
          <Checkbox checked={overwrite} onChange={setOverwrite} label="Overwrite existing variables with the same name" />
          <Checkbox
            checked={autoSecret}
            onChange={setAutoSecret}
            label={
              <span className="inline-flex items-center gap-1">
                <EyeOff className="h-3.5 w-3.5" /> Mark keys that look sensitive as secret
              </span>
            }
          />
        </div>
        {parsed.vars.length > 0 && (
          <Callout tone="info">
            {added.length} new, {replaced.length} {overwrite ? 'to overwrite' : 'skipped (already defined)'}
            {parsed.skipped > 0 && `, ${parsed.skipped} unparseable lines ignored`}.
            {autoSecret && parsed.vars.some((v) => v.secret) && (
              <div className="mt-1 flex flex-wrap gap-1">
                {parsed.vars
                  .filter((v) => v.secret)
                  .map((v) => (
                    <Badge key={v.name} tone="amber" mono>
                      {v.name}
                    </Badge>
                  ))}
              </div>
            )}
          </Callout>
        )}
      </div>
    </Dialog>
  );
}
