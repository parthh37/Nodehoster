import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// client.ts keeps a module-level "already redirecting to /login" flag, so each test gets a
// fresh module instance.
type Client = typeof import('./client');
let client: Client;

let fetchMock: ReturnType<typeof vi.fn<typeof fetch>>;
let assign: ReturnType<typeof vi.fn<(url: string) => void>>;

function stubLocation(pathname = '/sites/abc', search = '?tab=logs') {
  assign = vi.fn<(url: string) => void>();
  vi.stubGlobal('window', { location: { pathname, search, assign } });
}

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init.headers as Record<string, string>) },
  });
}

/** The RequestInit passed to the n-th fetch call. */
function sentInit(n = 0): RequestInit & { headers: Record<string, string> } {
  return fetchMock.mock.calls[n][1] as RequestInit & { headers: Record<string, string> };
}

beforeEach(async () => {
  vi.resetModules();
  fetchMock = vi.fn<typeof fetch>();
  vi.stubGlobal('fetch', fetchMock);
  stubLocation();
  client = await import('./client');
});

describe('ApiError / errorMessage', () => {
  it('carries status, field and body', () => {
    const e = new client.ApiError(422, 'Bad name', 'name', { error: 'Bad name', field: 'name' });
    expect(e).toBeInstanceOf(Error);
    expect(e.name).toBe('ApiError');
    expect(e.status).toBe(422);
    expect(e.field).toBe('name');
    expect(e.body).toEqual({ error: 'Bad name', field: 'name' });
  });

  it('normalizes an empty field to undefined and defaults the body', () => {
    const e = new client.ApiError(400, 'x', '');
    expect(e.field).toBeUndefined();
    expect(e.body).toEqual({});
  });

  it('extracts messages from errors and other values', () => {
    expect(client.errorMessage(new client.ApiError(500, 'boom'))).toBe('boom');
    expect(client.errorMessage(new TypeError('type'))).toBe('type');
    expect(client.errorMessage('plain')).toBe('plain');
    expect(client.errorMessage(42)).toBe('42');
  });
});

describe('request headers and body', () => {
  it('GET sends Accept, same-origin credentials and no CSRF header or body', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ ok: true }));
    await expect(client.http.get('/api/sites')).resolves.toEqual({ ok: true });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0][0]).toBe('/api/sites');
    const init = sentInit();
    expect(init.method).toBe('GET');
    expect(init.credentials).toBe('same-origin');
    expect(init.body).toBeUndefined();
    expect(init.headers).toEqual({ Accept: 'application/json' });
  });

  it.each([
    ['POST', () => client.http.post('/api/x', { a: 1 })],
    ['PUT', () => client.http.put('/api/x', { a: 1 })],
  ])('%s sends the CSRF header and a JSON body', async (method, call) => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await call();
    const init = sentInit();
    expect(init.method).toBe(method);
    expect(init.headers['X-Requested-With']).toBe('NodeHoster');
    expect(init.headers['Content-Type']).toBe('application/json');
    expect(init.body).toBe('{"a":1}');
  });

  it('DELETE sends the CSRF header without a body', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await client.http.del('/api/sites/1');
    const init = sentInit();
    expect(init.method).toBe('DELETE');
    expect(init.headers['X-Requested-With']).toBe('NodeHoster');
    expect(init.headers['Content-Type']).toBeUndefined();
    expect(init.body).toBeUndefined();
  });

  it('POST without a body sends no Content-Type', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await client.http.post('/api/sites/1/start');
    const init = sentInit();
    expect(init.headers['X-Requested-With']).toBe('NodeHoster');
    expect(init.headers['Content-Type']).toBeUndefined();
    expect(init.body).toBeUndefined();
  });

  it('serializes falsy JSON bodies', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await client.http.put('/api/flag', false);
    expect(sentInit().body).toBe('false');
  });

  it('passes FormData through and lets the browser set the multipart Content-Type', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    const fd = new FormData();
    fd.append('file', new Blob(['hello']), 'a.zip');
    await client.http.post('/api/upload', fd);
    const init = sentInit();
    expect(init.body).toBe(fd);
    expect(init.headers['Content-Type']).toBeUndefined();
    expect(init.headers['X-Requested-With']).toBe('NodeHoster');
  });

  it('merges custom headers but never lets them drop the CSRF header', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await client.http.post('/api/x', { a: 1 }, { headers: { 'X-Custom': '1', 'X-Requested-With': 'evil' } });
    const init = sentInit();
    expect(init.headers['X-Custom']).toBe('1');
    expect(init.headers['X-Requested-With']).toBe('NodeHoster');
  });

  it('forwards the abort signal', async () => {
    fetchMock.mockResolvedValue(jsonResponse([]));
    const ctrl = new AbortController();
    await client.http.get('/api/x', { signal: ctrl.signal });
    expect(sentInit().signal).toBe(ctrl.signal);
  });
});

describe('response parsing', () => {
  it('returns undefined for 204 and empty bodies', async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    await expect(client.http.post('/api/x')).resolves.toBeUndefined();
    fetchMock.mockResolvedValueOnce(new Response('', { status: 200 }));
    await expect(client.http.get('/api/x')).resolves.toBeUndefined();
  });

  it('parses JSON by content type', async () => {
    fetchMock.mockResolvedValue(new Response('"quoted"', { headers: { 'Content-Type': 'application/json; charset=utf-8' } }));
    await expect(client.http.get('/api/x')).resolves.toBe('quoted');
  });

  it('parses JSON-looking bodies even without a JSON content type', async () => {
    fetchMock.mockResolvedValueOnce(new Response('{"a":1}', { headers: { 'Content-Type': 'text/plain' } }));
    await expect(client.http.get('/api/x')).resolves.toEqual({ a: 1 });
    fetchMock.mockResolvedValueOnce(new Response('[1,2]', { headers: { 'Content-Type': 'text/plain' } }));
    await expect(client.http.get('/api/x')).resolves.toEqual([1, 2]);
  });

  it('returns plain text for non-JSON responses', async () => {
    fetchMock.mockResolvedValue(new Response('hello', { headers: { 'Content-Type': 'text/plain' } }));
    await expect(client.http.get('/api/x')).resolves.toBe('hello');
  });

  it('http.text returns the raw body even if it is JSON', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ a: 1 }));
    await expect(client.http.text('/api/logs')).resolves.toBe('{"a":1}');
  });
});

describe('error handling', () => {
  async function failWith(res: Response): Promise<InstanceType<Client['ApiError']>> {
    fetchMock.mockResolvedValue(res);
    const err = await client.http.get('/api/x').then(
      () => {
        throw new Error('expected rejection');
      },
      (e: unknown) => e,
    );
    expect(err).toBeInstanceOf(client.ApiError);
    return err as InstanceType<Client['ApiError']>;
  }

  it('uses the JSON error message, field and body', async () => {
    const e = await failWith(jsonResponse({ error: 'Name is taken', field: 'name', code: 'dup' }, { status: 409, statusText: 'Conflict' }));
    expect(e.status).toBe(409);
    expect(e.message).toBe('Name is taken');
    expect(e.field).toBe('name');
    expect(e.body).toEqual({ error: 'Name is taken', field: 'name', code: 'dup' });
  });

  it('ignores a non-string field', async () => {
    const e = await failWith(jsonResponse({ error: 'bad', field: 3 }, { status: 400 }));
    expect(e.field).toBeUndefined();
  });

  it('falls back to statusText when the JSON has no usable error', async () => {
    const e = await failWith(jsonResponse({ error: '' }, { status: 500, statusText: 'Internal Server Error' }));
    expect(e.message).toBe('Internal Server Error');
  });

  it('falls back to "HTTP <status>" without a status text or body', async () => {
    const e = await failWith(new Response('', { status: 502 }));
    expect(e.message).toBe('HTTP 502');
    expect(e.body).toEqual({});
  });

  it('uses a plain-text body as the message', async () => {
    const e = await failWith(new Response('upstream timed out', { status: 504, statusText: 'Gateway Timeout' }));
    expect(e.message).toBe('upstream timed out');
  });

  it('truncates long plain-text bodies to 300 characters', async () => {
    const e = await failWith(new Response('x'.repeat(1000), { status: 500 }));
    expect(e.message).toBe('x'.repeat(300) + '…');
  });

  it('keeps statusText when the JSON body is not an object', async () => {
    const e = await failWith(new Response('null', { status: 500, statusText: 'Oops' }));
    expect(e.message).toBe('Oops');
    expect(e.body).toEqual({});
  });

  it('uses a friendly message for 403 without an error body', async () => {
    const e = await failWith(new Response('', { status: 403, statusText: 'Forbidden' }));
    expect(e.message).toBe('You do not have permission to do this.');
  });

  it('prefers the server message for 403 when there is one', async () => {
    const e = await failWith(jsonResponse({ error: 'Read-only user' }, { status: 403 }));
    expect(e.message).toBe('Read-only user');
  });

  it('turns network failures into an ApiError with status 0', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));
    const err = await client.http.get('/api/x').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(client.ApiError);
    expect((err as InstanceType<Client['ApiError']>).status).toBe(0);
    expect((err as Error).message).toMatch(/Cannot reach the NodeHoster server/);
  });

  it('rethrows aborts unchanged', async () => {
    const abort = new DOMException('The operation was aborted.', 'AbortError');
    fetchMock.mockRejectedValue(abort);
    await expect(client.http.get('/api/x')).rejects.toBe(abort);
  });
});

describe('401 handling', () => {
  it('redirects to /login with the current path as next and still rejects', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ error: 'Not signed in' }, { status: 401 }));
    await expect(client.http.get('/api/me')).rejects.toMatchObject({ status: 401, message: 'Not signed in' });
    expect(assign).toHaveBeenCalledWith('/login?next=%2Fsites%2Fabc%3Ftab%3Dlogs');
  });

  it('redirects only once while a redirect is in progress', async () => {
    fetchMock.mockImplementation(async () => jsonResponse({ error: 'x' }, { status: 401 }));
    await Promise.allSettled([client.http.get('/api/a'), client.http.get('/api/b'), client.http.get('/api/c')]);
    expect(assign).toHaveBeenCalledTimes(1);
  });

  it('does not redirect when noAuthRedirect is set', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ error: 'Invalid password' }, { status: 401 }));
    await expect(client.http.post('/api/login', { u: 'a' }, { noAuthRedirect: true })).rejects.toMatchObject({ status: 401 });
    expect(assign).not.toHaveBeenCalled();
  });

  it('does not redirect when already on the login page', async () => {
    stubLocation('/login', '?next=%2F');
    fetchMock.mockResolvedValue(jsonResponse({ error: 'x' }, { status: 401 }));
    await expect(client.http.get('/api/me')).rejects.toMatchObject({ status: 401 });
    expect(assign).not.toHaveBeenCalled();
  });

  it('does not redirect on other errors', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ error: 'x' }, { status: 403 }));
    await expect(client.http.get('/api/x')).rejects.toMatchObject({ status: 403 });
    expect(assign).not.toHaveBeenCalled();
  });
});

describe('filenameFromDisposition', () => {
  it.each<[string | null, string | null]>([
    [null, null],
    ['', null],
    ['inline', null],
    ['attachment; filename="report.csv"', 'report.csv'],
    ['attachment; filename=report.csv', 'report.csv'],
    ['attachment; filename = "spaced name.txt" ; size=10', 'spaced name.txt'],
    ["attachment; filename*=UTF-8''na%C3%AFve%20file.txt", 'naïve file.txt'],
    ["attachment; FILENAME*=utf-8''x.zip", 'x.zip'],
    ['attachment; filename*="quoted%20star.txt"', 'quoted star.txt'],
  ])('%j -> %j', (header, name) => {
    expect(client.filenameFromDisposition(header)).toBe(name);
  });

  it('prefers filename* over filename', () => {
    expect(client.filenameFromDisposition(`attachment; filename="fallback.txt"; filename*=UTF-8''r%C3%A9sum%C3%A9.pdf`)).toBe('résumé.pdf');
  });

  it('falls back to filename when filename* is not valid percent-encoding', () => {
    expect(client.filenameFromDisposition(`attachment; filename*=UTF-8''bad%E0%A4%A; filename="fallback.txt"`)).toBe('fallback.txt');
  });
});

describe('qs', () => {
  it('returns an empty string when nothing is set', () => {
    expect(client.qs({})).toBe('');
    expect(client.qs({ a: undefined, b: null, c: '' })).toBe('');
  });

  it('keeps 0 and false and encodes values', () => {
    expect(client.qs({ page: 0, live: false, q: 'a b&c', tail: 200 })).toBe('?page=0&live=false&q=a+b%26c&tail=200');
  });
});

describe('http.download', () => {
  let anchor: { href: string; download: string; click: ReturnType<typeof vi.fn>; remove: ReturnType<typeof vi.fn> };
  let appendChild: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.useFakeTimers();
    anchor = { href: '', download: '', click: vi.fn(), remove: vi.fn() };
    appendChild = vi.fn();
    vi.stubGlobal('document', { createElement: vi.fn(() => anchor), body: { appendChild } });
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:mock');
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('saves the response with the Content-Disposition file name', async () => {
    fetchMock.mockResolvedValue(new Response('zipdata', { headers: { 'Content-Disposition': 'attachment; filename="backup.zip"' } }));
    await client.http.download('POST', '/api/backup', { full: true });
    expect(sentInit().method).toBe('POST');
    expect(sentInit().headers['X-Requested-With']).toBe('NodeHoster');
    expect(anchor.href).toBe('blob:mock');
    expect(anchor.download).toBe('backup.zip');
    expect(appendChild).toHaveBeenCalledWith(anchor);
    expect(anchor.click).toHaveBeenCalled();
    expect(anchor.remove).toHaveBeenCalled();
    expect(URL.revokeObjectURL).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1000);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:mock');
  });

  it('uses the fallback name without a Content-Disposition header', async () => {
    fetchMock.mockResolvedValue(new Response('data'));
    await client.http.download('GET', '/api/logs/download', undefined, 'site.log');
    expect(anchor.download).toBe('site.log');
  });

  it('does not save anything when the request fails', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ error: 'nope' }, { status: 500 }));
    await expect(client.http.download('GET', '/api/x')).rejects.toMatchObject({ message: 'nope' });
    expect(anchor.click).not.toHaveBeenCalled();
  });
});
