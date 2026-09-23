import type { Binding } from '@/api/types';

export function defaultPort(protocol: string): number {
  return protocol === 'https' ? 443 : 80;
}

/** Display text, always with the port: https://example.com:443 */
export function bindingLabel(b: Binding): string {
  const host = b.host || (b.ip ? b.ip : '*');
  return `${b.protocol}://${formatHost(host)}:${b.port}`;
}

function formatHost(h: string): string {
  return h.includes(':') && !h.startsWith('[') ? `[${h}]` : h;
}

/** A browsable URL for a binding, or null for wildcard host names. */
export function bindingHref(b: Binding, fallbackHost = window.location.hostname): string | null {
  if (b.host.startsWith('*')) return null;
  const host = b.host || b.ip || fallbackHost;
  const port = b.port === defaultPort(b.protocol) ? '' : `:${b.port}`;
  return `${b.protocol}://${formatHost(host)}${port}/`;
}

/** IIS-style binding info string: http *:80:host */
export function bindingInfo(b: Binding): string {
  return `${b.protocol} ${b.ip || '*'}:${b.port}:${b.host}`;
}
