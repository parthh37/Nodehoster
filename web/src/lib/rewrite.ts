// Helpers for the URL Rewrite editors: defaults for new rules, condition
// summaries and merging an import (web.config / .htaccess) into a draft.

import type { OutboundRule, RewriteCondition, RewriteImport, RewriteMap, RewriteRule, RoutingConfig } from '@/api/types';

/** Server variables offered as condition inputs (inbound). */
export const SERVER_VARIABLES = [
  '{HTTP_HOST}',
  '{QUERY_STRING}',
  '{REQUEST_URI}',
  '{URL}',
  '{REQUEST_METHOD}',
  '{REMOTE_ADDR}',
  '{HTTPS}',
  '{SERVER_PORT}',
  '{REQUEST_FILENAME}',
  '{HTTP_USER_AGENT}',
  '{HTTP_REFERER}',
  '{HTTP_COOKIE}',
  '{HTTP_X_FORWARDED_PROTO}',
  '{CACHE_URL}',
];

/** Outbound conditions can also test response headers as {RESPONSE_<HEADER>}. */
export const OUTBOUND_VARIABLES = [...SERVER_VARIABLES, '{RESPONSE_CONTENT_TYPE}', '{RESPONSE_LOCATION}'];

/** HTML tags an outbound rule can filter on (the IIS filterByTags set). */
export const OUTBOUND_TAGS = ['a', 'area', 'base', 'form', 'frame', 'head', 'iframe', 'img', 'input', 'link', 'script'];

/** A rewrite to an absolute http(s) URL proxies the request (like URL Rewrite with ARR). */
export function isProxyTarget(target: string | undefined): boolean {
  return /^https?:\/\//i.test((target ?? '').trim());
}

export function newRewriteRule(): RewriteRule {
  return {
    name: '',
    enabled: true,
    match: '^/old/(.*)$',
    ignoreCase: true,
    action: 'redirect',
    target: '/new/{R:1}',
    statusCode: 301,
    stop: true,
  };
}

export function newOutboundRule(): OutboundRule {
  return {
    name: '',
    enabled: true,
    scope: 'header',
    header: 'Location',
    match: '^https?://localhost(:\\d+)?/(.*)$',
    ignoreCase: true,
    action: 'rewrite',
    value: '/{R:2}',
    stop: false,
  };
}

export function newCondition(): RewriteCondition {
  return { input: '{HTTP_HOST}', matchType: 'pattern', pattern: '', ignoreCase: true };
}

export function newRewriteMap(): RewriteMap {
  return { name: '', defaultValue: '', entries: {} };
}

/** Default status code for an inbound action (0 = none). */
export function defaultStatusFor(action: string): number {
  switch (action) {
    case 'redirect':
      return 301;
    case 'block':
      return 403;
    case 'respond':
      return 200;
    default:
      return 0;
  }
}

/** One-line description of a condition, e.g. `{REQUEST_FILENAME} is not a file`. */
export function describeCondition(c: RewriteCondition): string {
  const input = c.input || '(no input)';
  switch (c.matchType) {
    case 'isFile':
      return `${input} ${c.negate ? 'is not' : 'is'} a file`;
    case 'isDirectory':
      return `${input} ${c.negate ? 'is not' : 'is'} a directory`;
    default:
      return `${input} ${c.negate ? 'does not match' : 'matches'} ${c.pattern || '(anything)'}`;
  }
}

type RewriteParts = Pick<RoutingConfig, 'rewrites' | 'outboundRules' | 'rewriteMaps'>;

export interface ImportSummary {
  rules: number;
  outboundRules: number;
  maps: number;
  /** Names of existing maps the import replaces. */
  replacedMaps: string[];
}

export function summarizeImport(imp: RewriteImport, current: RewriteParts): ImportSummary {
  const existing = new Map((current.rewriteMaps ?? []).map((m) => [m.name.toLowerCase(), m.name]));
  const replaced = new Set<string>();
  for (const m of imp.rewriteMaps ?? []) {
    const name = existing.get(m.name.toLowerCase());
    if (name) replaced.add(name);
  }
  return {
    rules: (imp.rules ?? []).length,
    outboundRules: (imp.outboundRules ?? []).length,
    maps: (imp.rewriteMaps ?? []).length,
    replacedMaps: [...replaced],
  };
}

/**
 * Appends imported inbound and outbound rules after the existing ones. An
 * imported map replaces an existing map with the same name (names are
 * compared without regard to case, like the server), in place; new maps are
 * appended. The input is not modified.
 */
export function mergeRewriteImport<T extends RewriteParts>(current: T, imp: RewriteImport): T {
  const maps = (current.rewriteMaps ?? []).slice();
  for (const m of imp.rewriteMaps ?? []) {
    const incoming: RewriteMap = { ...m, entries: { ...(m.entries ?? {}) } };
    const i = maps.findIndex((x) => x.name.toLowerCase() === m.name.toLowerCase());
    if (i >= 0) maps[i] = incoming;
    else maps.push(incoming);
  }
  return {
    ...current,
    rewrites: [...(current.rewrites ?? []), ...(imp.rules ?? [])],
    outboundRules: [...(current.outboundRules ?? []), ...(imp.outboundRules ?? [])],
    rewriteMaps: maps,
  };
}
