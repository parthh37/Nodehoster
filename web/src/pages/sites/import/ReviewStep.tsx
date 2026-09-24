import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { AlertTriangle, ChevronRight, CircleAlert, ExternalLink } from 'lucide-react';
import type { ImportFailed, ImportItem, ImportPreview, SiteView } from '@/api/types';
import { Badge } from '@/components/Badge';
import { Button } from '@/components/Button';
import { Card, Callout, EmptyState } from '@/components/Layout';
import { Input, Select } from '@/components/Input';
import { Checkbox, Switch } from '@/components/Switch';
import { bindingLabel } from '@/lib/bindings';
import { cn } from '@/lib/cn';
import { describeCron } from '@/lib/cron';
import { pluralize } from '@/lib/format';
import {
  chosenOption,
  groupNotes,
  needsAppRoot,
  runsOnCandidates,
  setAllSelected,
  type ImportDone,
  type ImportReview,
  type ReviewItem,
} from '@/lib/importPlan';
import { runsNode } from '@/lib/siteDefaults';

export interface ReviewStepProps {
  preview: ImportPreview;
  review: ImportReview;
  onReview: (r: ImportReview) => void;
  done: ImportDone;
  /** Failures of the last apply, by item key. */
  failed: Record<string, ImportFailed>;
  /** Blocking problems (missing names, folders, task sites), shown once the user tried to import. */
  problems: Record<string, string[]>;
  hints: Record<string, string[]>;
  sites: SiteView[];
  start: boolean;
  onStart: (v: boolean) => void;
}

export function ReviewStep({ preview, review, onReview, done, failed, problems, hints, sites, start, onStart }: ReviewStepProps) {
  const open = preview.items.filter((it) => !done[it.key]);
  const selected = open.filter((it) => review[it.key]?.selected).length;
  const warnings = preview.warnings ?? [];

  const setItem = (key: string, fn: (r: ReviewItem) => ReviewItem) => onReview({ ...review, [key]: fn(review[key]) });

  if (preview.items.length === 0) {
    return (
      <div className="space-y-4">
        <Warnings warnings={warnings} />
        <Card>
          <EmptyState title="Nothing to import" description="No sites or applications were found in this configuration." />
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <Warnings warnings={warnings} />
      <Card
        flush
        title="Found"
        description="Choose what to import and how. Bindings and everything else can be changed after importing, on each site's pages."
        actions={
          open.length > 0 && (
            <>
              <span className="text-xs text-zinc-500">
                {selected} of {pluralize(open.length, 'item')} selected
              </span>
              <Button size="sm" onClick={() => onReview(setAllSelected(preview, review, selected < open.length, done))}>
                {selected < open.length ? 'Select all' : 'Select none'}
              </Button>
            </>
          )
        }
      >
        <ul className="divide-y divide-zinc-100 dark:divide-zinc-800/80">
          {preview.items.map((it) => (
            <ItemRow
              key={it.key}
              preview={preview}
              item={it}
              review={review}
              r={review[it.key]}
              set={(fn) => setItem(it.key, fn)}
              done={done}
              failed={failed[it.key]}
              problems={problems[it.key]}
              hints={hints[it.key]}
              sites={sites}
            />
          ))}
        </ul>
      </Card>
      <Card>
        <Switch
          checked={start}
          onChange={onStart}
          label="Start the imported sites"
          description={
            preview.source === 'pm2'
              ? 'Otherwise they are created stopped. Stop the apps in PM2 first (pm2 stop all): both cannot listen on the same port.'
              : 'Otherwise they are created stopped. Stop the sites in IIS first: IIS and NodeHoster cannot listen on the same port.'
          }
        />
      </Card>
    </div>
  );
}

function Warnings({ warnings }: { warnings: string[] }) {
  if (!warnings.length) return null;
  return (
    <Callout tone="warning" icon={<AlertTriangle />} title={pluralize(warnings.length, 'note') + ' about this configuration'}>
      <ul className="mt-1 list-disc space-y-0.5 pl-4">
        {warnings.map((w, i) => (
          <li key={i} className="break-words">
            {w}
          </li>
        ))}
      </ul>
    </Callout>
  );
}

function ItemRow({
  preview,
  item,
  review,
  r,
  set,
  done,
  failed,
  problems,
  hints,
  sites,
}: {
  preview: ImportPreview;
  item: ImportItem;
  review: ImportReview;
  r: ReviewItem;
  set: (fn: (r: ReviewItem) => ReviewItem) => void;
  done: ImportDone;
  failed?: ImportFailed;
  problems?: string[];
  hints?: string[];
  sites: SiteView[];
}) {
  const [expanded, setExpanded] = useState(false);
  const created = done[item.key];
  const opt = chosenOption(item, r);
  const site = opt?.kind === 'site' ? opt.site : undefined;
  const task = opt?.kind === 'task' ? opt.task : undefined;
  const locked = !!created;
  const active = r.selected && !locked;
  const notes = useMemo(() => groupNotes(item.notes), [item.notes]);
  const candidates = useMemo(
    () => (task ? runsOnCandidates(preview, review, sites, item.key, done) : []),
    [task, preview, review, sites, item.key, done],
  );
  // A target that is not offered (a site that was deleted meanwhile) stays visible.
  const runsOnOptions =
    r.taskSite && !candidates.some((c) => c.value === r.taskSite) ? [...candidates, { value: r.taskSite, label: `${r.taskSite} (not found)` }] : candidates;

  const setName = (v: string) => set((x) => ({ ...x, names: x.names.map((n, i) => (i === x.choice ? v : n)) }));
  const setAppRoot = (v: string) => set((x) => ({ ...x, appRoots: x.appRoots.map((n, i) => (i === x.choice ? v : n)) }));
  const noteCount = item.notes?.length ?? 0;

  return (
    <li className={cn('px-4 py-3', !active && !locked && 'bg-zinc-50/60 dark:bg-zinc-900/30')}>
      <div className="flex items-start gap-3">
        <Checkbox className="pt-1.5" checked={locked || r.selected} disabled={locked} onChange={(v) => set((x) => ({ ...x, selected: v }))} />
        <div className="min-w-0 flex-1 space-y-2">
          <div className="flex flex-wrap items-center gap-2">
            <Input
              aria-label={task ? 'Task name' : 'Site name'}
              className="w-60"
              maxLength={64}
              value={r.names[r.choice] ?? ''}
              disabled={locked}
              invalid={!!problems?.some((p) => p.startsWith('Enter a name'))}
              onChange={(e) => setName(e.target.value)}
            />
            {item.options.length > 1 && !locked ? (
              <Select
                aria-label="Import as"
                className="w-52"
                value={r.choice}
                onChange={(v) => set((x) => ({ ...x, choice: Number(v) }))}
                options={item.options.map((o, i) => ({ value: i, label: o.label }))}
              />
            ) : (
              <Badge tone="accent">{opt?.label}</Badge>
            )}
            <span className="text-xs text-zinc-500">{item.source}</span>
            {created && (
              <Link to={`/sites/${created.siteId}`} className="inline-flex items-center gap-1">
                <Badge tone="green">
                  Imported <ExternalLink className="h-3 w-3" />
                </Badge>
              </Link>
            )}
          </div>

          {/* What the chosen option creates. */}
          <div className="space-y-1.5 text-xs text-zinc-600 dark:text-zinc-400">
            {site && (
              <>
                {site.type !== 'worker' && (
                  <Detail label="Bindings">
                    {site.bindings?.length ? (
                      <span className="font-mono text-[12px]">{site.bindings.map(bindingLabel).join(', ')}</span>
                    ) : (
                      <span>None</span>
                    )}
                  </Detail>
                )}
                {runsNode(site.type) && (
                  <Detail label="Application folder">
                    {needsAppRoot(opt) ? (
                      <Input
                        aria-label="Application folder"
                        mono
                        className="w-96 max-w-full"
                        placeholder="C:\sites\my-app"
                        value={r.appRoots[r.choice] ?? ''}
                        disabled={locked}
                        invalid={!!problems?.includes('Enter the application folder.')}
                        onChange={(e) => setAppRoot(e.target.value)}
                      />
                    ) : (
                      <span className="font-mono text-[12px]">{site.node?.appRoot}</span>
                    )}
                  </Detail>
                )}
                {(site.routing?.locations ?? []).length > 0 && (
                  <Detail label="Locations">
                    <span className="font-mono text-[12px]">{(site.routing.locations ?? []).map((l) => l.path).join(', ')}</span>
                  </Detail>
                )}
              </>
            )}
            {task && (
              <>
                <Detail label="Schedule">
                  {task.schedule ? (
                    <>
                      <span className="font-mono text-[12px]">{task.schedule}</span>
                      <span className="ml-2">{describeCron(task.schedule)}</span>
                    </>
                  ) : (
                    'On demand only'
                  )}
                </Detail>
                <Detail label="Runs on">
                  <Select
                    aria-label="Runs on"
                    className="w-72 max-w-full"
                    value={r.taskSite}
                    placeholder="Choose a Node.js or worker site…"
                    disabled={locked}
                    invalid={!!problems?.some((p) => p.startsWith('Choose the site'))}
                    onChange={(v) => set((x) => ({ ...x, taskSite: v }))}
                    options={runsOnOptions}
                  />
                </Detail>
              </>
            )}
          </div>

          {!locked && failed && <Message tone="red">Import failed: {failed.field && <span className="font-mono">{failed.field}: </span>}{failed.error}</Message>}
          {active && problems?.map((p, i) => <Message key={i} tone="red">{p}</Message>)}
          {!locked && (item.conflicts ?? []).length > 0 && (
            <div className="space-y-0.5">
              <p className="text-2xs font-semibold uppercase tracking-wide text-red-600 dark:text-red-400">Problems with the proposed import</p>
              {item.conflicts.map((c, i) => (
                <Message key={i} tone="red">
                  {c}
                </Message>
              ))}
            </div>
          )}
          {!locked && hints?.map((h, i) => <Message key={i} tone="amber">{h}</Message>)}

          {noteCount > 0 && (
            <div>
              <button
                type="button"
                onClick={() => setExpanded((v) => !v)}
                aria-expanded={expanded}
                className="inline-flex items-center gap-1.5 text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200"
              >
                <ChevronRight className={cn('h-3.5 w-3.5 transition-transform', expanded && 'rotate-90')} />
                Conversion notes
                {notes.converted.length > 0 && <Badge tone="green">✓ {notes.converted.length}</Badge>}
                {notes.approximated.length > 0 && <Badge tone="amber">≈ {notes.approximated.length}</Badge>}
                {notes.skipped.length > 0 && <Badge>– {notes.skipped.length}</Badge>}
              </button>
              {expanded && (
                <div className="mt-2 space-y-2 border-l-2 border-zinc-200 pl-3 dark:border-zinc-800">
                  <NoteGroup title="Converted" glyph="✓" className="text-emerald-700 dark:text-emerald-400" notes={notes.converted} />
                  <NoteGroup title="Approximated" glyph="≈" className="text-amber-700 dark:text-amber-400" notes={notes.approximated} />
                  <NoteGroup title="Not converted" glyph="–" className="text-zinc-500 dark:text-zinc-400" notes={notes.skipped} />
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </li>
  );
}

function Detail({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
      <span className="w-32 shrink-0 text-zinc-500">{label}</span>
      <div className="min-w-0">{children}</div>
    </div>
  );
}

function Message({ tone, children }: { tone: 'red' | 'amber'; children: React.ReactNode }) {
  return (
    <p className={cn('flex items-start gap-1 text-xs', tone === 'red' ? 'text-red-600 dark:text-red-400' : 'text-amber-700 dark:text-amber-400')}>
      <CircleAlert className="mt-px h-3.5 w-3.5 shrink-0" />
      <span className="min-w-0 break-words">{children}</span>
    </p>
  );
}

function NoteGroup({ title, glyph, className, notes }: { title: string; glyph: string; className: string; notes: string[] }) {
  if (!notes.length) return null;
  return (
    <div>
      <p className="text-2xs font-semibold uppercase tracking-wide text-zinc-500">{title}</p>
      <ul className="mt-0.5 space-y-0.5 text-xs text-zinc-700 dark:text-zinc-300">
        {notes.map((n, i) => (
          <li key={i} className="flex items-start gap-1.5">
            <span className={cn('w-3 shrink-0 text-center font-semibold', className)} aria-hidden>
              {glyph}
            </span>
            <span className="min-w-0 break-words">{n}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
