import { describe, expect, it } from 'vitest';
import type { ImportItem, ImportOption, ImportPreview, ScheduledTask, Site, SiteType } from '@/api/types';
import {
  buildApplyRequest,
  dependencyHints,
  groupNotes,
  initialReview,
  itemName,
  needsAppRoot,
  refKey,
  reviewProblems,
  runsOnCandidates,
  setAllSelected,
  type ImportDone,
} from './importPlan';
import { defaultTask, newSite } from './siteDefaults';

function site(type: SiteType, name: string, appRoot = 'C:\\apps\\' + name): Site {
  const s = newSite(type);
  s.name = name;
  if (s.node) s.node.appRoot = appRoot;
  return s;
}

const siteOpt = (s: Site, label = 'Node.js application'): ImportOption => ({ label, kind: 'site', site: s });
const task = (name: string): ScheduledTask => ({ ...defaultTask(), name, script: 'jobs/' + name + '.js' });
const taskOpt = (name: string, taskSite = ''): ImportOption => ({ label: 'Scheduled task', kind: 'task', task: task(name), taskSite });

function item(key: string, options: ImportOption[], p: Partial<ImportItem> = {}): ImportItem {
  return { key, source: `test "${key}"`, options, choice: 0, selected: true, notes: [], conflicts: [], ...p };
}

const preview = (items: ImportItem[]): ImportPreview => ({ source: 'iis', items, warnings: [] });

// An IIS site "Shop" mounting its iisnode application /api, which comes first.
function iisPreview(): ImportPreview {
  const api = site('node', 'Shop api');
  api.bindings = [];
  const shop = site('static', 'Shop');
  shop.routing.locations = [{ path: '/api', kind: 'site', siteId: 'import:shop-api', stripPrefix: false }];
  return preview([item('shop-api', [siteOpt(api)]), item('shop', [siteOpt(shop, 'Static site')])]);
}

// A PM2 file: a web app, a cron app that can be a task or a worker.
function pm2Preview(): ImportPreview {
  return {
    source: 'pm2',
    warnings: [],
    items: [
      item('web', [siteOpt(site('node', 'web')), siteOpt(site('worker', 'web'), 'Background worker')]),
      item('cleanup', [taskOpt('cleanup', 'import:web'), siteOpt(site('worker', 'cleanup'), 'Background worker')]),
    ],
  };
}

describe('initialReview', () => {
  it('takes the proposed choice, selection, names, folders and task site', () => {
    const pv = pm2Preview();
    pv.items[0].choice = 1;
    pv.items[1].selected = false;
    const r = initialReview(pv);
    expect(r.web).toEqual({ selected: true, choice: 1, names: ['web', 'web'], appRoots: ['C:\\apps\\web', 'C:\\apps\\web'], taskSite: '' });
    expect(r.cleanup).toEqual({ selected: false, choice: 0, names: ['cleanup', 'cleanup'], appRoots: ['', 'C:\\apps\\cleanup'], taskSite: 'import:web' });
  });

  it('falls back to the first option for an out of range choice', () => {
    const pv = preview([item('a', [siteOpt(site('node', 'a'))], { choice: 5 })]);
    expect(initialReview(pv).a.choice).toBe(0);
  });
});

describe('buildApplyRequest', () => {
  it('sends selected items with the edited name and folder, without touching the preview', () => {
    const pv = pm2Preview();
    pv.items[0].options[0].site!.node!.appRoot = '';
    const before = JSON.stringify(pv);
    const r = initialReview(pv);
    r.web.names[0] = '  web-app ';
    r.web.appRoots[0] = ' D:\\sites\\web ';
    const req = buildApplyRequest(pv, r, true);
    expect(req.source).toBe('pm2');
    expect(req.start).toBe(true);
    expect(req.items).toHaveLength(2);
    expect(req.items[0]).toMatchObject({ key: 'web', kind: 'site' });
    expect(req.items[0].site!.name).toBe('web-app');
    expect(req.items[0].site!.node!.appRoot).toBe('D:\\sites\\web');
    expect(req.items[1]).toMatchObject({ key: 'cleanup', kind: 'task', taskSite: 'import:web' });
    expect(req.items[1].task!.name).toBe('cleanup');
    expect(JSON.stringify(pv)).toBe(before);
    // The drafts are copies.
    req.items[0].site!.bindings.push({ id: '', protocol: 'http', ip: '', port: 1, host: '' });
    expect(pv.items[0].options[0].site!.bindings).toHaveLength(1);
  });

  it('uses the chosen option and its own edits', () => {
    const pv = pm2Preview();
    const r = initialReview(pv);
    r.cleanup.choice = 1;
    r.cleanup.names[0] = 'task name';
    r.cleanup.names[1] = 'cleanup-worker';
    const req = buildApplyRequest(pv, r, false);
    expect(req.items[1].kind).toBe('site');
    expect(req.items[1].site!.type).toBe('worker');
    expect(req.items[1].site!.name).toBe('cleanup-worker');
    expect(req.items[1].task).toBeUndefined();
    expect(req.items[1].taskSite).toBeUndefined();
  });

  it('leaves out unselected items and those created by an earlier apply', () => {
    const pv = pm2Preview();
    const r = initialReview(pv);
    r.web.selected = false;
    expect(buildApplyRequest(pv, r, false).items.map((i) => i.key)).toEqual(['cleanup']);
    const done: ImportDone = { cleanup: { key: 'cleanup', kind: 'task', siteId: 's1', name: 'cleanup' } };
    expect(buildApplyRequest(pv, setAllSelected(pv, r, true, done), false, done).items.map((i) => i.key)).toEqual(['web']);
  });

  it('points references to items created earlier at the created sites', () => {
    const pv = iisPreview();
    const r = initialReview(pv);
    const done: ImportDone = { 'shop-api': { key: 'shop-api', kind: 'site', siteId: 'site-42', name: 'Shop api' } };
    const req = buildApplyRequest(pv, r, false, done);
    expect(req.items.map((i) => i.key)).toEqual(['shop']);
    expect(req.items[0].site!.routing.locations![0].siteId).toBe('site-42');
    // The preview keeps its reference for the next attempt.
    expect(pv.items[1].options[0].site!.routing.locations![0].siteId).toBe('import:shop-api');

    const pm2 = pm2Preview();
    const done2: ImportDone = { web: { key: 'web', kind: 'site', siteId: 'site-7', name: 'web' } };
    expect(buildApplyRequest(pm2, initialReview(pm2), false, done2).items[0]).toMatchObject({ key: 'cleanup', taskSite: 'site-7' });
  });

  it('keeps references to items of the same request for the server to resolve', () => {
    const pv = iisPreview();
    const req = buildApplyRequest(pv, initialReview(pv), false);
    expect(req.items[1].site!.routing.locations![0].siteId).toBe('import:shop-api');
  });
});

describe('setAllSelected', () => {
  it('selects or clears everything except created items', () => {
    const pv = pm2Preview();
    const done: ImportDone = { web: { key: 'web', kind: 'site', siteId: 'x', name: 'web' } };
    const r = setAllSelected(pv, initialReview(pv), false, done);
    expect(r.web.selected).toBe(true);
    expect(r.cleanup.selected).toBe(false);
    expect(setAllSelected(pv, r, true).cleanup.selected).toBe(true);
  });
});

describe('runsOnCandidates', () => {
  const sites = [
    { id: 's-b', name: 'beta', type: 'worker' as SiteType },
    { id: 's-p', name: 'proxy', type: 'proxy' as SiteType },
    { id: 's-a', name: 'alpha', type: 'node' as SiteType },
  ];

  it('lists node and worker items of the import, then existing node and worker sites by name', () => {
    const pv = pm2Preview();
    const r = initialReview(pv);
    expect(runsOnCandidates(pv, r, sites, 'cleanup')).toEqual([
      { value: 'import:web', label: 'web (this import)' },
      { value: 's-a', label: 'alpha' },
      { value: 's-b', label: 'beta' },
    ]);
  });

  it('follows the chosen option and the selection, and never offers the item itself', () => {
    const pv = pm2Preview();
    const r = initialReview(pv);
    r.web.selected = false;
    r.cleanup.choice = 1;
    expect(runsOnCandidates(pv, r, [], 'cleanup')).toEqual([{ value: 'import:web', label: 'web (not selected)' }]);
    expect(runsOnCandidates(pv, r, [], 'web')).toEqual([{ value: 'import:cleanup', label: 'cleanup (this import)' }]);

    const pv2 = iisPreview();
    expect(runsOnCandidates(pv2, initialReview(pv2), [], 'x').map((c) => c.value)).toEqual(['import:shop-api']);
  });

  it('offers a site created by an earlier apply once, as the import item', () => {
    const pv = pm2Preview();
    const done: ImportDone = { web: { key: 'web', kind: 'site', siteId: 's-web', name: 'web' } };
    const withCreated = [...sites, { id: 's-web', name: 'web', type: 'node' as SiteType }];
    expect(runsOnCandidates(pv, initialReview(pv), withCreated, 'cleanup', done).map((c) => c.label)).toEqual(['web (imported)', 'alpha', 'beta']);
  });
});

describe('reviewProblems', () => {
  it('reports empty names, folders and task sites of selected items only', () => {
    const pv = pm2Preview();
    const r = initialReview(pv);
    expect(reviewProblems(pv, r)).toEqual({});
    r.web.names[0] = ' ';
    r.web.appRoots[0] = '';
    r.cleanup.taskSite = '';
    expect(reviewProblems(pv, r)).toEqual({
      web: ['Enter a name for the site.', 'Enter the application folder.'],
      cleanup: ['Choose the site that runs this task.'],
    });
    r.web.selected = false;
    expect(Object.keys(reviewProblems(pv, r))).toEqual(['cleanup']);
    expect(reviewProblems(pv, r, { cleanup: { key: 'cleanup', kind: 'task', siteId: 'x', name: 'cleanup' } })).toEqual({});
  });

  it('names a task as a task', () => {
    const pv = pm2Preview();
    const r = initialReview(pv);
    r.cleanup.names[0] = '';
    expect(reviewProblems(pv, r).cleanup).toEqual(['Enter a name for the task.']);
  });
});

describe('dependencyHints', () => {
  it('is empty when everything needed is selected', () => {
    const pv = iisPreview();
    expect(dependencyHints(pv, initialReview(pv))).toEqual({});
    const pm2 = pm2Preview();
    expect(dependencyHints(pm2, initialReview(pm2))).toEqual({});
  });

  it('warns both ways when a mounted application is not selected', () => {
    const pv = iisPreview();
    const r = initialReview(pv);
    r['shop-api'].selected = false;
    const h = dependencyHints(pv, r);
    expect(h.shop).toHaveLength(1);
    expect(h.shop[0]).toContain('Mounts "Shop api" at /api, which is not selected');
    expect(h['shop-api']).toEqual(['"Shop" mounts this application at /api: it will fail unless this is imported too.']);
  });

  it('says nothing when the unselected site is not needed, or was created before', () => {
    const pv = iisPreview();
    const r = initialReview(pv);
    r.shop.selected = false;
    expect(dependencyHints(pv, r)).toEqual({});
    r.shop.selected = true;
    r['shop-api'].selected = false;
    expect(dependencyHints(pv, r, { 'shop-api': { key: 'shop-api', kind: 'site', siteId: 'x', name: 'Shop api' } })).toEqual({});
  });

  it('warns when the site a task runs on is not selected or not a node or worker site', () => {
    const pv = pm2Preview();
    const r = initialReview(pv);
    r.web.selected = false;
    const h = dependencyHints(pv, r);
    expect(h.cleanup[0]).toContain('Runs on "web", which is not selected');
    expect(h.web[0]).toContain('The task "cleanup" runs on this site');

    // The target picked as something that cannot run tasks.
    const staticWeb = site('static', 'web');
    pv.items[0].options.push(siteOpt(staticWeb, 'Static site'));
    const r2 = initialReview(pv);
    r2.web.choice = 2;
    expect(dependencyHints(pv, r2)).toEqual({
      cleanup: ['Runs on "web", which is not imported as a Node.js application or background worker: the task will fail.'],
    });
  });

  it('ignores tasks on existing sites and items not selected', () => {
    const pv = pm2Preview();
    const r = initialReview(pv);
    r.cleanup.taskSite = 'existing-id';
    r.web.selected = false;
    expect(dependencyHints(pv, r)).toEqual({});
    r.cleanup.taskSite = 'import:web';
    r.cleanup.selected = false;
    expect(dependencyHints(pv, r)).toEqual({});
  });
});

describe('helpers', () => {
  it('needsAppRoot is true for node and worker drafts without a folder', () => {
    expect(needsAppRoot(siteOpt(site('node', 'a', '')))).toBe(true);
    expect(needsAppRoot(siteOpt(site('worker', 'a', '')))).toBe(true);
    expect(needsAppRoot(siteOpt(site('node', 'a')))).toBe(false);
    expect(needsAppRoot(siteOpt(site('static', 'a')))).toBe(false);
    expect(needsAppRoot(taskOpt('t'))).toBe(false);
  });

  it('refKey reads import references only', () => {
    expect(refKey('import:shop')).toBe('shop');
    expect(refKey('site-1')).toBeNull();
    expect(refKey('')).toBeNull();
    expect(refKey(undefined)).toBeNull();
  });

  it('itemName uses the edited name of the chosen option, falling back to the key', () => {
    const pv = pm2Preview();
    const r = initialReview(pv);
    r.cleanup.choice = 1;
    r.cleanup.names[1] = ' worker ';
    expect(itemName(pv.items[1], r.cleanup)).toBe('worker');
    r.cleanup.names[1] = '';
    expect(itemName(pv.items[1], r.cleanup)).toBe('cleanup');
    expect(itemName(pv.items[0], undefined)).toBe('web');
  });

  it('groupNotes groups by level and tolerates null', () => {
    expect(
      groupNotes([
        { level: 'converted', text: 'a' },
        { level: 'skipped', text: 'b' },
        { level: 'approximated', text: 'c' },
        { level: 'converted', text: 'd' },
      ]),
    ).toEqual({ converted: ['a', 'd'], approximated: ['c'], skipped: ['b'] });
    expect(groupNotes(null)).toEqual({ converted: [], approximated: [], skipped: [] });
  });
});
