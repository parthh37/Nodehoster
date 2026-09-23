import { describe, expect, it } from 'vitest';
import type { RewriteImport, RewriteMap, RewriteRule, RoutingConfig } from '@/api/types';
import { defaultStatusFor, describeCondition, isProxyTarget, mergeRewriteImport, newRewriteRule, summarizeImport } from './rewrite';

const rule = (name: string): RewriteRule => ({ ...newRewriteRule(), name });
const map = (name: string, entries: Record<string, string> | null = {}): RewriteMap => ({ name, entries });
const imp = (p: Partial<RewriteImport>): RewriteImport => ({ rules: null, outboundRules: null, rewriteMaps: null, warnings: null, ...p });

type Parts = Pick<RoutingConfig, 'rewrites' | 'outboundRules' | 'rewriteMaps'>;

describe('mergeRewriteImport', () => {
  it('appends rules after the existing ones', () => {
    const cur: Parts = { rewrites: [rule('a')], outboundRules: [], rewriteMaps: [] };
    const out = mergeRewriteImport(cur, imp({ rules: [rule('b'), rule('c')] }));
    expect(out.rewrites!.map((r) => r.name)).toEqual(['a', 'b', 'c']);
  });

  it('appends outbound rules and handles missing arrays on both sides', () => {
    const cur: Parts = {};
    const o = { name: 'o', enabled: true, scope: 'body', match: 'x', action: 'none', stop: false };
    const out = mergeRewriteImport(cur, imp({ outboundRules: [o] }));
    expect(out.outboundRules).toEqual([o]);
    expect(out.rewrites).toEqual([]);
    expect(out.rewriteMaps).toEqual([]);
  });

  it('replaces maps with the same name in place, ignoring case, and appends new ones', () => {
    const cur: Parts = { rewriteMaps: [map('Redirects', { '/a': '/b' }), map('Other')] };
    const out = mergeRewriteImport(cur, imp({ rewriteMaps: [map('redirects', { '/x': '/y' }), map('New')] }));
    expect(out.rewriteMaps!.map((m) => m.name)).toEqual(['redirects', 'Other', 'New']);
    expect(out.rewriteMaps![0].entries).toEqual({ '/x': '/y' });
  });

  it('lets the last of several imported maps with one name win', () => {
    const out = mergeRewriteImport({ rewriteMaps: [] } as Parts, imp({ rewriteMaps: [map('m', { a: '1' }), map('M', { b: '2' })] }));
    expect(out.rewriteMaps).toEqual([{ name: 'M', entries: { b: '2' } }]);
  });

  it('turns null entries into an empty object', () => {
    const out = mergeRewriteImport({} as Parts, imp({ rewriteMaps: [map('m', null)] }));
    expect(out.rewriteMaps![0].entries).toEqual({});
  });

  it('keeps other routing fields and does not modify the input', () => {
    const cur = { accessLog: true, rewrites: [rule('a')], rewriteMaps: [map('m', { k: 'v' })] } as Parts & { accessLog: boolean };
    const snapshot = structuredClone(cur);
    const out = mergeRewriteImport(cur, imp({ rules: [rule('b')], rewriteMaps: [map('m', { k: 'w' })] }));
    expect(out.accessLog).toBe(true);
    expect(cur).toEqual(snapshot);
  });
});

describe('summarizeImport', () => {
  it('counts what an import contains and which maps it replaces', () => {
    const s = summarizeImport(imp({ rules: [rule('a'), rule('b')], rewriteMaps: [map('redirects'), map('fresh')] }), {
      rewriteMaps: [map('Redirects')],
    });
    expect(s).toEqual({ rules: 2, outboundRules: 0, maps: 2, replacedMaps: ['Redirects'] });
  });
});

describe('isProxyTarget', () => {
  it('is true only for absolute http(s) URLs', () => {
    expect(isProxyTarget('http://10.0.0.5:3000/{R:1}')).toBe(true);
    expect(isProxyTarget(' HTTPS://api.example.com')).toBe(true);
    expect(isProxyTarget('/index.php?p={R:1}')).toBe(false);
    expect(isProxyTarget('ftp://x')).toBe(false);
    expect(isProxyTarget(undefined)).toBe(false);
  });
});

describe('describeCondition', () => {
  it('describes each match type, negated or not', () => {
    expect(describeCondition({ input: '{HTTP_HOST}', matchType: 'pattern', pattern: '^www\\.' })).toBe('{HTTP_HOST} matches ^www\\.');
    expect(describeCondition({ input: '{HTTP_HOST}', matchType: 'pattern', pattern: 'x', negate: true })).toBe('{HTTP_HOST} does not match x');
    expect(describeCondition({ input: '{REQUEST_FILENAME}', matchType: 'isFile', negate: true })).toBe('{REQUEST_FILENAME} is not a file');
    expect(describeCondition({ input: '{REQUEST_FILENAME}', matchType: 'isDirectory' })).toBe('{REQUEST_FILENAME} is a directory');
  });
});

describe('defaultStatusFor', () => {
  it('matches the server defaults per action', () => {
    expect(defaultStatusFor('redirect')).toBe(301);
    expect(defaultStatusFor('block')).toBe(403);
    expect(defaultStatusFor('respond')).toBe(200);
    expect(defaultStatusFor('rewrite')).toBe(0);
    expect(defaultStatusFor('none')).toBe(0);
  });
});
