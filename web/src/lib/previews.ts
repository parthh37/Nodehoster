// Preview deployments: the Previews tab's settings form and list, and the
// sites list, which shows previews under their parent rather than as
// sites of their own. Naming mirrors internal/preview (names.go).

import type { PreviewConfig, PreviewInfo, PreviewKind, Site } from '@/api/types';

/** What the console proposes when previews are turned on. */
export function defaultPreviewConfig(): PreviewConfig {
  return {
    enabled: false,
    hostPattern: '',
    pullRequests: true,
    branches: [],
    allowForks: false,
    maxPreviews: 10,
    expireDays: 7,
    protocol: 'http',
    ip: '',
    port: 80,
    certMode: '',
    env: [],
    basicAuth: { enabled: false, realm: 'Preview', users: [] },
    allowIps: [],
    reportStatus: false,
  };
}

/** A site's preview settings, with defaults for a site saved before previews existed. */
export function previewConfigOf(site: Pick<Site, 'deploy'>): PreviewConfig {
  const d = defaultPreviewConfig();
  const p = site.deploy.previews;
  if (!p) return d;
  return { ...d, ...p, basicAuth: { ...d.basicAuth, ...p.basicAuth, users: p.basicAuth?.users ?? [] } };
}

/** The label part and the domain part of a host pattern. */
export function splitHostPattern(p: string): [label: string, suffix: string] {
  const i = p.indexOf('.');
  return i < 0 ? [p, ''] : [p.slice(0, i), p.slice(i + 1)];
}

const HOST_RE = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

/** Validation of a host pattern, as the server does it; null when valid. */
export function hostPatternError(raw: string): string | null {
  const p = raw.trim().toLowerCase();
  if (!p) return 'Enter a host pattern such as pr-{number}.preview.example.com';
  const [label, suffix] = splitHostPattern(p);
  if (!suffix) return 'Add the domain the previews live under, e.g. {branch}.preview.example.com';
  if (!label.includes('{number}') && !label.includes('{branch}')) return 'The first label must contain {number} or {branch}';
  if (/[{}*]/.test(suffix)) return 'Placeholders go in the first label only';
  const fixed = label.replaceAll('{number}', '').replaceAll('{branch}', '');
  if (!/^[a-z0-9-]*$/.test(fixed)) return "The first label may only have letters, digits and '-' around the placeholders";
  if (fixed.length > 40) return "The first label leaves too little room for the preview's name";
  if (!HOST_RE.test(suffix)) return `"${suffix}" is not a valid host name`;
  return null;
}

/** A branch as a DNS label: "Feature/Login_Page" is "feature-login-page". */
export function slugBranch(branch: string): string {
  return branch
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
}

/**
 * The host a preview gets under a pattern, for examples in the form. The
 * server also shortens long branch names with a hash and adds one when a
 * name is taken; examples do not need that.
 */
export function exampleHost(pattern: string, kind: PreviewKind, number: number, branch: string): string {
  const [pl, suffix] = splitHostPattern(pattern.trim().toLowerCase());
  let label: string;
  if (kind === 'pr') label = pl.replaceAll('{number}', String(number));
  else if (pl.includes('{branch}')) label = pl.replaceAll('{number}', '');
  else label = '{branch}';
  label = label.replaceAll('{branch}', slugBranch(branch) || 'branch');
  label = label.replace(/-{2,}/g, '-').replace(/^-+|-+$/g, '').slice(0, 63) || 'preview';
  return suffix ? `${label}.${suffix}` : label;
}

function globMatch(p: string, s: string): boolean {
  if (p === '') return s === '';
  if (p.startsWith('**')) {
    const rest = p.replace(/^\*+/, '');
    for (let i = 0; i <= s.length; i++) if (globMatch(rest, s.slice(i))) return true;
    return false;
  }
  if (p[0] === '*') {
    for (let i = 0; i <= s.length; i++) {
      if (globMatch(p.slice(1), s.slice(i))) return true;
      if (s[i] === '/') return false;
    }
    return false;
  }
  if (s === '') return false;
  if (p[0] === '?' ? s[0] === '/' : p[0] !== s[0]) return false;
  return globMatch(p.slice(1), s.slice(1));
}

/** Whether a branch matches one of the patterns (* within a path segment, ** across). */
export function matchBranch(patterns: string[] | undefined, branch: string): boolean {
  return (patterns ?? []).some((p) => globMatch(p.trim(), branch));
}

/** Validation of one branch pattern; null when valid. */
export function branchPatternError(p: string): string | null {
  if (!/^[A-Za-z0-9._/*?-]+$/.test(p) || p.startsWith('-') || p.startsWith('/') || p.includes('..')) {
    return 'Letters, digits, . _ - / and the wildcards * ** ?';
  }
  return null;
}

/** "#42 fix/login" or "branch feature/x". */
export function describePreview(p: Pick<PreviewInfo, 'kind' | 'number' | 'branch'>): string {
  return p.kind === 'pr' ? `#${p.number} ${p.branch}` : `branch ${p.branch}`;
}

/**
 * Why a deployment failed, from its message ("<commit subject> — <error>";
 * no subject when nothing was fetched).
 */
export function failureText(message: string | undefined): string {
  return (message ?? '').replace(/^[\s—]+/, '');
}

/** What a pull request is called on the host. */
export function pullRequestWord(provider: string | undefined): string {
  return provider === 'gitlab' ? 'merge request' : 'pull request';
}

/**
 * Sites as the sites list shows them: previews under their parent. A
 * preview whose parent is not in the list (not visible to the user, or
 * being deleted) stays a row of its own.
 */
export function groupSites<T extends { id: string; previewOf?: string }>(sites: T[]): { roots: T[]; previews: Map<string, T[]> } {
  const ids = new Set(sites.map((s) => s.id));
  const roots: T[] = [];
  const previews = new Map<string, T[]>();
  for (const s of sites) {
    if (s.previewOf && ids.has(s.previewOf)) {
      const list = previews.get(s.previewOf) ?? [];
      list.push(s);
      previews.set(s.previewOf, list);
    } else {
      roots.push(s);
    }
  }
  return { roots, previews };
}

/** Whether a site can have previews at all (git-deployed node or static site). */
export function canHavePreviews(site: Pick<Site, 'type' | 'previewOf'>): boolean {
  return !site.previewOf && (site.type === 'node' || site.type === 'static');
}

/** The webhook events to select on each git host for previews. */
export const WEBHOOK_EVENTS: { host: string; events: string }[] = [
  { host: 'GitHub', events: 'Pushes, Pull requests, and Branch or tag deletion' },
  { host: 'GitLab', events: 'Push events and Merge request events' },
  { host: 'Gitea / Forgejo', events: 'Push, Delete and Pull Request events' },
];
