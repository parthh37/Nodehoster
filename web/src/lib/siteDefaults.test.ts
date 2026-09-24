import { describe, expect, it } from 'vitest';
import type { Site, SiteType } from '@/api/types';
import {
  ENV_NAME_RE,
  HHMM_RE,
  HOST_RE,
  NAME_RE,
  SITE_TYPES,
  defaultBinding,
  defaultHealthCheck,
  defaultNode,
  defaultProxy,
  defaultRedirect,
  defaultRouting,
  defaultStatic,
  defaultTask,
  defaultWorker,
  newSite,
  normalizeSite,
  normalizeTask,
  runsNode,
  siteTypeLabel,
  toSite,
} from './siteDefaults';

// Server documents can be partial or contain nulls for fields the TS types declare as required.
const asSite = (v: unknown) => v as Site;

const ALL_TYPES: SiteType[] = ['node', 'worker', 'proxy', 'static', 'redirect'];

// The config block of each type; a worker is configured by `node` like a node site.
const CONFIG_KEY = { node: 'node', worker: 'node', proxy: 'proxy', static: 'static', redirect: 'redirect' } as const;

describe('SITE_TYPES / siteTypeLabel', () => {
  it('lists every site type once', () => {
    expect(SITE_TYPES.map((t) => t.value).sort()).toEqual([...ALL_TYPES].sort());
  });

  it('returns the label for known types and the raw value otherwise', () => {
    expect(siteTypeLabel('node')).toBe('Application');
    expect(siteTypeLabel('redirect')).toBe('Redirect');
    expect(siteTypeLabel('worker')).toBe('Background worker');
    expect(siteTypeLabel('ftp')).toBe('ftp');
  });
});

describe('validation regexes', () => {
  it.each(['a', 'My Site', 'api.v2', 'site_1-prod', 'x'.repeat(64)])('NAME_RE accepts %j', (v) => {
    expect(NAME_RE.test(v)).toBe(true);
  });
  it.each(['', ' leading', '.hidden', '-dash', 'x'.repeat(65), 'a/b', 'émoji'])('NAME_RE rejects %j', (v) => {
    expect(NAME_RE.test(v)).toBe(false);
  });

  it.each(['localhost', 'example.com', 'a.b.c.example.co.uk', '*.example.com', 'xn--bcher-kva.example', '127.0.0.1', `${'a'.repeat(63)}.com`])(
    'HOST_RE accepts %j',
    (v) => expect(HOST_RE.test(v)).toBe(true),
  );
  it.each(['', '*', '*.', 'Example.com', '-a.com', 'a-.com', 'a..b', 'foo_bar.com', 'a.*.com', 'example.com.', `${'a'.repeat(64)}.com`, 'host:80'])(
    'HOST_RE rejects %j',
    (v) => expect(HOST_RE.test(v)).toBe(false),
  );

  it.each(['00:00', '09:30', '23:59'])('HHMM_RE accepts %j', (v) => expect(HHMM_RE.test(v)).toBe(true));
  it.each(['24:00', '9:30', '12:60', '12:5', '1230', ''])('HHMM_RE rejects %j', (v) => expect(HHMM_RE.test(v)).toBe(false));

  it.each(['PORT', '_X', 'a1'])('ENV_NAME_RE accepts %j', (v) => expect(ENV_NAME_RE.test(v)).toBe(true));
  it.each(['1A', 'MY-VAR', 'MY VAR', ''])('ENV_NAME_RE rejects %j', (v) => expect(ENV_NAME_RE.test(v)).toBe(false));
});

describe('default factories', () => {
  it('defaultHealthCheck is disabled and uses the given interval', () => {
    expect(defaultHealthCheck(12)).toEqual({ enabled: false, path: '/', intervalSec: 12, timeoutSec: 5, unhealthyThreshold: 3 });
  });

  it('defaultNode has sensible key defaults', () => {
    const n = defaultNode();
    expect(n.script).toBe('server.js');
    expect(n.instances).toBe(1);
    expect(n.portMode).toBe('auto');
    expect(n.restartPolicy).toBe('always');
    expect(n.agentEnabled).toBe(true);
    expect(n.watchFiles).toBe(false);
    expect(n.watchIgnore).toEqual(['node_modules', '.git', 'logs']);
    expect(n.env).toEqual([]);
    expect(n.healthCheck.intervalSec).toBe(30);
    expect(n.healthCheck.path).toBe('/');
    expect(n.runAs.enabled).toBe(false);
  });

  it('defaultProxy has one empty upstream and a 15s health check', () => {
    const p = defaultProxy();
    expect(p.upstreams).toEqual([{ url: '', weight: 1 }]);
    expect(p.loadBalancing).toBe('round_robin');
    expect(p.healthCheck.intervalSec).toBe(15);
  });

  it('defaultStatic and defaultRedirect', () => {
    expect(defaultStatic().indexFiles).toEqual(['index.html', 'index.htm', 'default.htm']);
    expect(defaultStatic().spaFallback).toBe(false);
    expect(defaultRedirect()).toMatchObject({ targetUrl: '', statusCode: 301, preservePath: true });
  });

  it('defaultRouting keeps security features off but compression/websockets on', () => {
    const r = defaultRouting();
    expect(r.httpsRedirect).toBe(false);
    expect(r.hsts.enabled).toBe(false);
    expect(r.hsts.maxAgeSec).toBe(31536000);
    expect(r.basicAuth.enabled).toBe(false);
    expect(r.rateLimit.enabled).toBe(false);
    expect(r.maintenance.enabled).toBe(false);
    expect(r.compression).toBe(true);
    expect(r.webSockets).toBe(true);
    expect(r.accessLog).toBe(true);
  });

  it('factories return fresh objects each call', () => {
    const a = defaultNode();
    const b = defaultNode();
    a.watchIgnore!.push('dist');
    a.healthCheck.path = '/changed';
    expect(b.watchIgnore).toEqual(['node_modules', '.git', 'logs']);
    expect(b.healthCheck.path).toBe('/');
    const r1 = defaultRouting();
    r1.ip.allow!.push('10.0.0.0/8');
    expect(defaultRouting().ip.allow).toEqual([]);
  });

  it('defaultBinding picks port and cert mode by protocol', () => {
    expect(defaultBinding()).toMatchObject({ protocol: 'http', port: 80, host: '', ip: '', certMode: '' });
    expect(defaultBinding('https', 'example.com')).toMatchObject({ protocol: 'https', port: 443, host: 'example.com', certMode: 'auto' });
  });
});

describe('newSite', () => {
  it.each(ALL_TYPES)('creates a %s site with only its own type config', (type) => {
    const s = newSite(type);
    expect(s.type).toBe(type);
    expect(s.autoStart).toBe(true);
    for (const other of ALL_TYPES) {
      if (CONFIG_KEY[other] === CONFIG_KEY[type]) expect(s[CONFIG_KEY[other]]).toBeDefined();
      else expect(s[CONFIG_KEY[other]]).toBeUndefined();
    }
  });

  it.each(ALL_TYPES.filter((t) => t !== 'worker'))('gives a %s site one http binding on port 80', (type) => {
    const s = newSite(type);
    expect(s.bindings).toHaveLength(1);
    expect(s.bindings[0]).toMatchObject({ protocol: 'http', port: 80 });
  });

  it('creates a worker without bindings or health check', () => {
    const s = newSite('worker');
    expect(s.bindings).toEqual([]);
    expect(s.node).toEqual(defaultWorker());
    expect(s.node!.healthCheck.enabled).toBe(false);
    expect(s.node!.portMode).toBe('auto');
    expect(s.node!.loadBalancer.enabled).toBe(false);
  });

  it('starts node and worker sites with an empty task list, others without one', () => {
    expect(newSite('node').tasks).toEqual([]);
    expect(newSite('worker').tasks).toEqual([]);
    expect(newSite('proxy').tasks).toBeUndefined();
    expect(newSite('static').tasks).toBeUndefined();
  });

  it('sets an install command only for node and worker sites', () => {
    expect(newSite('node').deploy.installCommand).toBe('npm ci --omit=dev');
    expect(newSite('worker').deploy.installCommand).toBe('npm ci --omit=dev');
    expect(newSite('static').deploy.installCommand).toBe('');
  });

  it('defaults deploy to main with 5 kept releases', () => {
    const d = newSite('node').deploy;
    expect(d.git.branch).toBe('main');
    expect(d.keepReleases).toBe(5);
  });

  it('does not share state between sites', () => {
    const a = newSite('node');
    const b = newSite('node');
    a.bindings[0].port = 8080;
    a.routing.requestHeaders!.push({ name: 'X', value: 'y' } as never);
    a.node!.env!.push({ name: 'A', value: '1' });
    expect(b.bindings[0].port).toBe(80);
    expect(b.routing.requestHeaders).toEqual([]);
    expect(b.node!.env).toEqual([]);
  });
});

describe('normalizeSite', () => {
  it('leaves a complete new site unchanged', () => {
    for (const type of ALL_TYPES) {
      const s = newSite(type);
      expect(normalizeSite(s)).toEqual(s);
    }
  });

  it('does not mutate the input', () => {
    const input = asSite({ id: 'x', type: 'node', bindings: [{ protocol: 'http', port: 80 }], routing: { hsts: { enabled: true } } });
    const snapshot = structuredClone(input);
    normalizeSite(input);
    expect(input).toEqual(snapshot);
  });

  it('fills missing routing, bindings and deploy for old documents', () => {
    const s = normalizeSite(asSite({ id: 'x', name: 'old', type: 'static' }));
    expect(s.bindings).toEqual([]);
    expect(s.routing).toEqual(defaultRouting());
    expect(s.deploy.keepReleases).toBe(5);
    expect(s.deploy.git).toEqual({});
    expect(s.static).toMatchObject({ root: '', spaFallback: false, directoryBrowsing: false });
  });

  it('replaces null ip/host on bindings with empty strings', () => {
    const s = normalizeSite(asSite({ type: 'redirect', bindings: [{ id: 'b', protocol: 'http', port: 80, ip: null, host: null }] }));
    expect(s.bindings[0]).toEqual({ id: 'b', protocol: 'http', port: 80, ip: '', host: '' });
  });

  it('deep-merges nested routing objects, keeping explicit values', () => {
    const s = normalizeSite(
      asSite({
        type: 'proxy',
        routing: {
          compression: false,
          hsts: { enabled: true },
          ip: { allow: ['10.0.0.0/8'] },
          basicAuth: { enabled: true, users: null },
          rateLimit: { requestsPerSecond: 50 },
          maintenance: { enabled: true },
        },
      }),
    );
    const d = defaultRouting();
    expect(s.routing.compression).toBe(false);
    expect(s.routing.webSockets).toBe(true);
    expect(s.routing.hsts).toEqual({ ...d.hsts, enabled: true });
    expect(s.routing.ip).toEqual({ allow: ['10.0.0.0/8'], deny: [] });
    expect(s.routing.basicAuth).toMatchObject({ enabled: true, realm: 'Restricted', users: [] });
    expect(s.routing.rateLimit).toEqual({ ...d.rateLimit, requestsPerSecond: 50 });
    expect(s.routing.maintenance).toEqual({ ...d.maintenance, enabled: true });
  });

  it('keeps an explicit keepReleases of 0 and existing git settings', () => {
    const s = normalizeSite(asSite({ type: 'static', deploy: { keepReleases: 0, git: { repo: 'r', branch: 'dev' } } }));
    expect(s.deploy.keepReleases).toBe(0);
    expect(s.deploy.git).toEqual({ repo: 'r', branch: 'dev' });
  });

  it('fills a missing node config with defaults', () => {
    const s = normalizeSite(asSite({ type: 'node' }));
    expect(s.node).toEqual(defaultNode());
  });

  it('fills a missing worker config with worker defaults (no health check)', () => {
    const s = normalizeSite(asSite({ type: 'worker' }));
    expect(s.node).toEqual(defaultWorker());
    expect(s.tasks).toEqual([]);
  });

  it('merges a partial worker config like a node one', () => {
    const s = normalizeSite(asSite({ type: 'worker', node: { script: 'consumer.js', env: null, recycle: { memoryLimitMB: 256 } } }));
    expect(s.node!.script).toBe('consumer.js');
    expect(s.node!.env).toEqual([]);
    expect(s.node!.recycle).toEqual({ memoryLimitMB: 256 });
    expect(s.node!.healthCheck.enabled).toBe(false);
  });

  it('normalizes tasks of node and worker sites', () => {
    const s = normalizeSite(
      asSite({
        type: 'node',
        tasks: [
          { id: 't1', name: 'cleanup', schedule: '', script: 'cleanup.js', enabled: true, timeoutSec: 0, overlap: '' },
          { id: 't2', name: 'report', schedule: '@daily', npmScript: 'report', args: null, env: [{ name: 'A', value: '1' }], enabled: false, timeoutSec: 60, overlap: 'queue' },
        ],
      }),
    );
    expect(s.tasks![0]).toEqual({ ...defaultTask(), id: 't1', name: 'cleanup', schedule: '', script: 'cleanup.js' });
    expect(s.tasks![1]).toMatchObject({ npmScript: 'report', script: '', args: [], env: [{ name: 'A', value: '1' }], enabled: false, timeoutSec: 60, overlap: 'queue' });
    expect(normalizeSite(asSite({ type: 'node', tasks: null })).tasks).toEqual([]);
  });

  it('leaves tasks alone on other site types', () => {
    expect(normalizeSite(asSite({ type: 'proxy' })).tasks).toBeUndefined();
  });

  it('merges a partial node config over defaults', () => {
    const s = normalizeSite(
      asSite({
        type: 'node',
        node: { script: 'app.js', instances: 4, env: null, healthCheck: { enabled: true, path: '/healthz' }, runAs: { enabled: true, user: 'svc' } },
      }),
    );
    const n = s.node!;
    const d = defaultNode();
    expect(n.script).toBe('app.js');
    expect(n.instances).toBe(4);
    expect(n.restartPolicy).toBe(d.restartPolicy);
    expect(n.maxRestarts).toBe(d.maxRestarts);
    expect(n.watchIgnore).toEqual(d.watchIgnore);
    expect(n.env).toEqual([]);
    expect(n.healthCheck).toEqual({ ...d.healthCheck, enabled: true, path: '/healthz' });
    expect(n.runAs).toMatchObject({ enabled: true, user: 'svc' });
    expect(n.recycle).toEqual({});
    expect(n.limits).toEqual({});
  });

  it('copies node recycle/limits rather than sharing them with the input', () => {
    const input = asSite({ type: 'node', node: { recycle: { dailyAt: '03:00' }, limits: { memoryMB: 512 } } });
    const s = normalizeSite(input);
    expect(s.node!.recycle).toEqual({ dailyAt: '03:00' });
    expect(s.node!.limits).toEqual({ memoryMB: 512 });
    expect(s.node!.recycle).not.toBe(input.node!.recycle);
    expect(s.node!.limits).not.toBe(input.node!.limits);
  });

  it('merges proxy config and replaces null upstreams with an empty list', () => {
    const s = normalizeSite(asSite({ type: 'proxy', proxy: { upstreams: null, preserveHost: true, healthCheck: { enabled: true } } }));
    expect(s.proxy!.upstreams).toEqual([]);
    expect(s.proxy!.preserveHost).toBe(true);
    expect(s.proxy!.loadBalancing).toBe('round_robin');
    expect(s.proxy!.healthCheck).toEqual({ ...defaultHealthCheck(15), enabled: true });
  });

  it('keeps explicit proxy upstreams', () => {
    const upstreams = [{ url: 'http://127.0.0.1:3000', weight: 2 }];
    expect(normalizeSite(asSite({ type: 'proxy', proxy: { upstreams } })).proxy!.upstreams).toEqual(upstreams);
  });

  it('keeps other static settings when indexFiles is null', () => {
    const s = normalizeSite(asSite({ type: 'static', static: { root: 'C:\\www', indexFiles: null, spaFallback: true } }));
    expect(s.static!.root).toBe('C:\\www');
    expect(s.static!.spaFallback).toBe(true);
    expect(s.static!.cacheControl).toBe('');
  });

  it('keeps explicit static indexFiles', () => {
    const s = normalizeSite(asSite({ type: 'static', static: { indexFiles: ['app.html'] } }));
    expect(s.static!.indexFiles).toEqual(['app.html']);
  });

  // BUG: normalizeSite replaces missing/null static.indexFiles with [] (static branch at the end of normalizeSite), while
  // the server's ApplyDefaults (internal/model/validate.go:132) fills the default index documents.
  // An old static site without indexFiles therefore shows an empty list in the editor, and saving
  // it relies on the server to silently restore the defaults.
  it.skip('fills default index documents when indexFiles is missing, like the server', () => {
    const d = defaultStatic().indexFiles;
    expect(normalizeSite(asSite({ type: 'static' })).static!.indexFiles).toEqual(d);
    expect(normalizeSite(asSite({ type: 'static', static: { indexFiles: null } })).static!.indexFiles).toEqual(d);
  });

  it('merges redirect config', () => {
    const s = normalizeSite(asSite({ type: 'redirect', redirect: { targetUrl: 'https://x.test', statusCode: 308 } }));
    expect(s.redirect).toEqual({ targetUrl: 'https://x.test', statusCode: 308, preservePath: true });
  });

  it('only fills the config block matching the site type', () => {
    const s = normalizeSite(asSite({ type: 'static' }));
    expect(s.node).toBeUndefined();
    expect(s.proxy).toBeUndefined();
    expect(s.redirect).toBeUndefined();
  });
});

describe('runsNode', () => {
  it('is true for node and worker sites only', () => {
    expect(ALL_TYPES.filter(runsNode)).toEqual(['node', 'worker']);
    expect(runsNode(undefined)).toBe(false);
  });
});

describe('tasks', () => {
  it('defaultTask is enabled, runs daily at 03:00 and skips overlapping runs', () => {
    expect(defaultTask()).toEqual({ id: '', name: '', schedule: '0 3 * * *', script: '', npmScript: '', args: [], enabled: true, timeoutSec: 3600, overlap: 'skip', env: [] });
    const a = defaultTask();
    a.env!.push({ name: 'X', value: '1' });
    expect(defaultTask().env).toEqual([]);
  });

  it('normalizeTask keeps an empty schedule and explicit values', () => {
    const t = normalizeTask({ id: 'x', name: 'n', schedule: '', enabled: false, timeoutSec: 604800, overlap: 'allow' });
    expect(t).toEqual({ ...defaultTask(), id: 'x', name: 'n', schedule: '', enabled: false, timeoutSec: 604800, overlap: 'allow' });
  });
});

describe('toSite', () => {
  it('strips the live status and keeps everything else', () => {
    const site = newSite('static');
    const view = { ...site, status: { state: 'running' } };
    const out = toSite(view);
    expect(out).not.toHaveProperty('status');
    expect(out).toEqual(site);
  });
});
