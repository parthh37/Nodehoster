// Which server the console operates: this one, or a connected server
// reached through this one's proxy (/api/servers/{id}/proxy/...). The
// choice lives in the tab's session storage, so that tabs can operate
// different servers and a reload keeps it; the API client and the event
// streams route every request through routePath.

import { isProxiedUrl, missingEndpointMessage, routeApiPath } from '@/lib/servers';

export interface Target {
  id: string;
  name: string;
  url: string;
  /** The server's NodeHoster version when it was chosen, for messages. */
  version?: string;
  /** This server's version then, to compare. */
  localVersion?: string;
}

const KEY = 'nodehoster.server';

function load(): Target | null {
  try {
    const raw = window.sessionStorage.getItem(KEY);
    if (!raw) return null;
    const t = JSON.parse(raw) as Target;
    return t && typeof t.id === 'string' && t.id ? t : null;
  } catch {
    return null;
  }
}

let current: Target | null = typeof window === 'undefined' ? null : load();
const listeners = new Set<() => void>();

/** The connected server the console operates, or null for this server. */
export function currentTarget(): Target | null {
  return current;
}

/** Switches the console to a connected server (null: this server). */
export function setTarget(t: Target | null) {
  current = t;
  try {
    if (t) window.sessionStorage.setItem(KEY, JSON.stringify(t));
    else window.sessionStorage.removeItem(KEY);
  } catch {
    /* storage unavailable: the choice lasts until reload */
  }
  for (const fn of listeners) fn();
}

export function subscribeTarget(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** The URL of an API path on the server the console operates. */
export function routePath(path: string): string {
  return routeApiPath(path, current?.id ?? null);
}

/**
 * Explains an error of a connected server: an endpoint it does not have
 * comes from an older version. Other errors are the server's own words.
 */
export function remoteError<E extends Error & { status: number; body: Record<string, unknown> }>(err: E, url: string): E {
  if (current && isProxiedUrl(url) && err.status === 404 && err.body.error === 'no such endpoint') {
    err.message = missingEndpointMessage(current.name, current.version);
  }
  return err;
}
