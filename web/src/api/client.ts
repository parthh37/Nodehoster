// Thin fetch wrapper around the NodeHoster REST API.

import { remoteError, routePath } from './target';

export class ApiError extends Error {
  readonly status: number;
  readonly field?: string;
  readonly body: Record<string, unknown>;

  constructor(status: number, message: string, field?: string, body: Record<string, unknown> = {}) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.field = field || undefined;
    this.body = body;
  }
}

export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return String(err);
}

const CSRF_HEADER = 'X-Requested-With';
const CSRF_VALUE = 'NodeHoster';

export interface RequestOptions {
  /** Do not redirect to /login on 401 (used by the login call itself). */
  noAuthRedirect?: boolean;
  signal?: AbortSignal;
  headers?: Record<string, string>;
}

let redirecting = false;

function redirectToLogin() {
  if (redirecting) return;
  const { pathname, search } = window.location;
  if (pathname === '/login') return;
  redirecting = true;
  const next = encodeURIComponent(pathname + search);
  window.location.assign(`/login?next=${next}`);
}

async function parseError(res: Response): Promise<ApiError> {
  let body: Record<string, unknown> = {};
  let message = res.statusText || `HTTP ${res.status}`;
  const text = await res.text().catch(() => '');
  if (text) {
    try {
      const parsed = JSON.parse(text) as unknown;
      if (parsed && typeof parsed === 'object') {
        body = parsed as Record<string, unknown>;
        if (typeof body.error === 'string' && body.error) message = body.error;
      }
    } catch {
      message = text.length > 300 ? text.slice(0, 300) + '…' : text;
    }
  }
  if (res.status === 403 && !body.error) message = 'You do not have permission to do this.';
  const field = typeof body.field === 'string' ? body.field : undefined;
  return new ApiError(res.status, message, field, body);
}

async function send(method: string, path: string, body: unknown, opts: RequestOptions = {}): Promise<Response> {
  const headers: Record<string, string> = { Accept: 'application/json', ...opts.headers };
  let payload: BodyInit | undefined;
  if (method !== 'GET' && method !== 'HEAD') headers[CSRF_HEADER] = CSRF_VALUE;
  if (body instanceof FormData) {
    payload = body;
  } else if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    payload = JSON.stringify(body);
  }
  let res: Response;
  // Another server's API when the console operates one (target.ts).
  const url = routePath(path);
  try {
    res = await fetch(url, {
      method,
      headers,
      body: payload,
      credentials: 'same-origin',
      signal: opts.signal,
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e;
    throw new ApiError(0, 'Cannot reach the NodeHoster server. Check that the service is running.');
  }
  if (res.status === 401 && !opts.noAuthRedirect) {
    redirectToLogin();
  }
  if (!res.ok) throw remoteError(await parseError(res), url);
  return res;
}

async function json<T>(res: Response): Promise<T> {
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  if (!text) return undefined as T;
  const type = res.headers.get('Content-Type') || '';
  if (type.includes('json') || text.startsWith('{') || text.startsWith('[')) {
    return JSON.parse(text) as T;
  }
  return text as unknown as T;
}

export const http = {
  get: async <T>(path: string, opts?: RequestOptions) => json<T>(await send('GET', path, undefined, opts)),
  post: async <T = void>(path: string, body?: unknown, opts?: RequestOptions) =>
    json<T>(await send('POST', path, body, opts)),
  put: async <T = void>(path: string, body?: unknown, opts?: RequestOptions) =>
    json<T>(await send('PUT', path, body, opts)),
  del: async <T = void>(path: string, opts?: RequestOptions) => json<T>(await send('DELETE', path, undefined, opts)),
  text: async (path: string, opts?: RequestOptions) => (await send('GET', path, undefined, opts)).text(),
  /** Performs a request whose response is a file, and saves it in the browser. */
  download: async (method: 'GET' | 'POST', path: string, body?: unknown, fallbackName = 'download') => {
    const res = await send(method, path, body);
    const blob = await res.blob();
    const name = filenameFromDisposition(res.headers.get('Content-Disposition')) || fallbackName;
    saveBlob(blob, name);
  },
};

export function filenameFromDisposition(header: string | null): string | null {
  if (!header) return null;
  const star = /filename\*\s*=\s*(?:UTF-8'')?([^;]+)/i.exec(header);
  if (star) {
    try {
      return decodeURIComponent(star[1].trim().replace(/^"|"$/g, ''));
    } catch {
      /* fall through */
    }
  }
  const plain = /filename\s*=\s*"?([^";]+)"?/i.exec(header);
  return plain ? plain[1].trim() : null;
}

export function saveBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** Build a query string, skipping empty values. */
export function qs(params: Record<string, string | number | boolean | undefined | null>): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue;
    sp.set(k, String(v));
  }
  const s = sp.toString();
  return s ? `?${s}` : '';
}
