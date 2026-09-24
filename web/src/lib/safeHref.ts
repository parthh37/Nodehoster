/**
 * A URL from API data as a link target: only absolute http(s) URLs, so that
 * a value from a connected server (or a webhook payload, a repository)
 * cannot make a `javascript:` or `data:` link in this console. Anything
 * else gives undefined: render it as text.
 */
export function safeHref(url: string | null | undefined): string | undefined {
  if (!url) return undefined;
  let u: URL;
  try {
    u = new URL(url.trim());
  } catch {
    return undefined; // relative, or not a URL at all
  }
  return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : undefined;
}
