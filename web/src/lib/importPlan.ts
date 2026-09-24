// Review state of a site import (IIS, iisnode web.config, PM2) and the apply
// request built from it. The preview proposes options per item; the review
// keeps what the administrator picked and edited next to it, so the preview
// itself is never mutated and "Back to review" after a partial apply can
// resend only what is left.

import type { ImportApplyRequest, ImportCreated, ImportItem, ImportNote, ImportOption, ImportPreview, SiteView } from '@/api/types';
import { clone } from './obj';
import { runsNode } from './siteDefaults';

/** Prefix of references to another item of the same import (model.ImportRef). */
export const IMPORT_REF = 'import:';

export interface ReviewItem {
  selected: boolean;
  /** Index into the item's options. */
  choice: number;
  /** Per option: the site's or task's name, as edited. */
  names: string[];
  /** Per option: the application folder of a node or worker draft ('' otherwise). */
  appRoots: string[];
  /** The site a task option is added to: "import:<key>", an existing site id, or ''. */
  taskSite: string;
}

/** Item key -> review state. */
export type ImportReview = Record<string, ReviewItem>;

/** Items created by earlier applies of this preview, by item key. Never sent again. */
export type ImportDone = Record<string, ImportCreated>;

export function optionName(o: ImportOption | undefined): string {
  return o?.site?.name ?? o?.task?.name ?? '';
}

/** A node or worker site: something a scheduled task can run on. */
export function runsNodeOption(o: ImportOption | undefined): boolean {
  return o?.kind === 'site' && runsNode(o.site?.type);
}

/**
 * The application folder can be typed in where the source did not say
 * (a web.config on its own, a PM2 app without cwd). The draft's own value
 * decides, so the field stays editable while it is being typed.
 */
export function needsAppRoot(o: ImportOption | undefined): boolean {
  return runsNodeOption(o) && !o?.site?.node?.appRoot;
}

export function initialReview(pv: ImportPreview): ImportReview {
  const r: ImportReview = {};
  for (const it of pv.items) {
    const choice = it.choice >= 0 && it.choice < it.options.length ? it.choice : 0;
    r[it.key] = {
      selected: it.selected,
      choice,
      names: it.options.map(optionName),
      appRoots: it.options.map((o) => (runsNodeOption(o) ? (o.site?.node?.appRoot ?? '') : '')),
      taskSite: it.options.find((o) => o.kind === 'task')?.taskSite ?? '',
    };
  }
  return r;
}

export function chosenOption(it: ImportItem, r: ReviewItem | undefined): ImportOption | undefined {
  return it.options[r?.choice ?? it.choice] ?? it.options[0];
}

/** The name shown for an item: the edited name of its chosen option. */
export function itemName(it: ImportItem, r: ReviewItem | undefined): string {
  const i = r?.choice ?? it.choice;
  return (r?.names[i] ?? optionName(it.options[i])).trim() || it.key;
}

/** The item key of an "import:<key>" reference, or null for anything else. */
export function refKey(v: string | undefined): string | null {
  return v?.startsWith(IMPORT_REF) ? v.slice(IMPORT_REF.length) : null;
}

/** Whether an item is still to be imported (selected and not created by an earlier apply). */
function pending(key: string, review: ImportReview, done: ImportDone): boolean {
  return !!review[key]?.selected && !done[key];
}

/** The id of a site created by an earlier apply for "import:<key>", else the reference as is. */
function resolveRef(v: string, done: ImportDone): string {
  const k = refKey(v);
  const c = k !== null ? done[k] : undefined;
  return c && c.kind === 'site' ? c.siteId : v;
}

export function setAllSelected(pv: ImportPreview, review: ImportReview, selected: boolean, done: ImportDone = {}): ImportReview {
  const next: ImportReview = { ...review };
  for (const it of pv.items) {
    if (!done[it.key] && next[it.key]) next[it.key] = { ...next[it.key], selected };
  }
  return next;
}

export interface RunsOnCandidate {
  value: string;
  label: string;
}

/**
 * Where a task item can run: node and worker sites of this import (other
 * than itself), then the existing ones. A site created by an earlier apply
 * of this import is offered once, as the import item.
 */
export function runsOnCandidates(pv: ImportPreview, review: ImportReview, sites: Pick<SiteView, 'id' | 'name' | 'type'>[], forKey: string, done: ImportDone = {}): RunsOnCandidate[] {
  const out: RunsOnCandidate[] = [];
  for (const it of pv.items) {
    if (it.key === forKey || !runsNodeOption(chosenOption(it, review[it.key]))) continue;
    const state = done[it.key] ? 'imported' : review[it.key]?.selected ? 'this import' : 'not selected';
    out.push({ value: IMPORT_REF + it.key, label: `${itemName(it, review[it.key])} (${state})` });
  }
  const createdIds = new Set(Object.values(done).map((c) => c.siteId));
  const existing = sites
    .filter((s) => runsNode(s.type) && !createdIds.has(s.id))
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((s) => ({ value: s.id, label: s.name }));
  return [...out, ...existing];
}

/** What stops a selected item from being sent: obviously missing input. The server validates the rest. */
export function reviewProblems(pv: ImportPreview, review: ImportReview, done: ImportDone = {}): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const it of pv.items) {
    const r = review[it.key];
    if (!r || !pending(it.key, review, done)) continue;
    const o = chosenOption(it, r);
    const p: string[] = [];
    if (!r.names[r.choice]?.trim()) p.push(o?.kind === 'task' ? 'Enter a name for the task.' : 'Enter a name for the site.');
    if (runsNodeOption(o) && !r.appRoots[r.choice]?.trim()) p.push('Enter the application folder.');
    if (o?.kind === 'task' && !r.taskSite) p.push('Choose the site that runs this task.');
    if (p.length) out[it.key] = p;
  }
  return out;
}

function addHint(out: Record<string, string[]>, key: string, text: string) {
  (out[key] ??= []).push(text);
}

/**
 * Items that will fail because something they need is not imported: a site
 * mounting an IIS application ("import:<key>" location) or a task running on
 * a site of this import. The hint goes on both the dependent item and the
 * unselected one it needs.
 */
export function dependencyHints(pv: ImportPreview, review: ImportReview, done: ImportDone = {}): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  const byKey = new Map(pv.items.map((it) => [it.key, it]));
  const available = (k: string) => !!done[k] || !!review[k]?.selected;
  for (const it of pv.items) {
    if (!pending(it.key, review, done)) continue;
    const r = review[it.key];
    const o = chosenOption(it, r);
    const name = itemName(it, r);
    if (o?.kind === 'site') {
      for (const l of o.site?.routing?.locations ?? []) {
        const k = l.kind === 'site' ? refKey(l.siteId) : null;
        if (k === null || done[k]) continue;
        const target = byKey.get(k);
        const tName = target ? itemName(target, review[k]) : k;
        if (!target || !available(k)) {
          addHint(out, it.key, `Mounts "${tName}" at ${l.path}, which is not selected: this site will fail unless you import that too, or remove the location after importing.`);
          if (target) addHint(out, k, `"${name}" mounts this application at ${l.path}: it will fail unless this is imported too.`);
        } else if (chosenOption(target, review[k])?.kind !== 'site') {
          addHint(out, it.key, `Mounts "${tName}" at ${l.path}, which is not imported as a site: this site will fail.`);
        }
      }
    } else if (o?.kind === 'task') {
      const k = refKey(r?.taskSite);
      if (k === null || done[k]) continue;
      const target = byKey.get(k);
      const tName = target ? itemName(target, review[k]) : k;
      if (!target || !available(k)) {
        addHint(out, it.key, `Runs on "${tName}", which is not selected: the task will fail unless you import that too.`);
        if (target) addHint(out, k, `The task "${name}" runs on this site: it will fail unless this is imported too.`);
      } else if (!runsNodeOption(chosenOption(target, review[k]))) {
        addHint(out, it.key, `Runs on "${tName}", which is not imported as a Node.js application or background worker: the task will fail.`);
      }
    }
  }
  return out;
}

/**
 * The apply request for the selected items that were not created yet, with
 * the chosen option's draft (deep-cloned) carrying the edited name and
 * application folder. References to items created by an earlier apply become
 * those sites' ids: the server only resolves "import:" within one request.
 */
export function buildApplyRequest(pv: ImportPreview, review: ImportReview, start: boolean, done: ImportDone = {}): ImportApplyRequest {
  const items: ImportApplyRequest['items'] = [];
  for (const it of pv.items) {
    if (!pending(it.key, review, done)) continue;
    const r = review[it.key];
    const o = chosenOption(it, r);
    if (!o) continue;
    const name = (r.names[r.choice] ?? optionName(o)).trim();
    if (o.kind === 'site' && o.site) {
      const site = clone(o.site);
      site.name = name;
      if (runsNode(site.type) && site.node) site.node.appRoot = (r.appRoots[r.choice] ?? '').trim();
      for (const l of site.routing?.locations ?? []) {
        if (l.kind === 'site' && l.siteId) l.siteId = resolveRef(l.siteId, done);
      }
      items.push({ key: it.key, kind: 'site', site });
    } else if (o.kind === 'task' && o.task) {
      const task = clone(o.task);
      task.name = name;
      items.push({ key: it.key, kind: 'task', task, taskSite: resolveRef(r.taskSite, done) });
    }
  }
  return { source: pv.source, items, start };
}

/** Notes grouped by level, in display order. */
export function groupNotes(notes: ImportNote[] | null | undefined): { converted: string[]; approximated: string[]; skipped: string[] } {
  const g = { converted: [] as string[], approximated: [] as string[], skipped: [] as string[] };
  for (const n of notes ?? []) {
    if (n.level === 'approximated') g.approximated.push(n.text);
    else if (n.level === 'skipped') g.skipped.push(n.text);
    else g.converted.push(n.text);
  }
  return g;
}
