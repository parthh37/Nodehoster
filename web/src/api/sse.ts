// Server-Sent Events helpers. EventSource reconnects automatically; the
// helpers only add typed parsing and a single close function.

import type { Deployment, LogLine, LogType, NHEvent, SiteStatus, TaskRun } from './types';
import { routePath } from './target';

export interface StreamHandle {
  close(): void;
}

type Handlers = Record<string, (data: string) => void>;

interface StreamOptions {
  onOpen?: () => void;
  onError?: (closed: boolean) => void;
}

/**
 * Opens an EventSource. The browser retries dropped connections itself, but
 * gives up for good after an HTTP error (e.g. 403 or a restart returning 502);
 * in that case we reopen with a capped backoff until closed by the caller.
 */
function open(url: string, handlers: Handlers, opts: StreamOptions = {}): StreamHandle {
  let es: EventSource | null = null;
  let closed = false;
  let retry = 0;
  let timer: number | undefined;

  const connect = () => {
    if (closed) return;
    es = new EventSource(routePath(url), { withCredentials: true });
    es.onopen = () => {
      retry = 0;
      opts.onOpen?.();
    };
    es.onerror = () => {
      const dead = es?.readyState === EventSource.CLOSED;
      opts.onError?.(dead);
      if (dead && !closed) {
        es?.close();
        retry = Math.min(retry + 1, 6);
        timer = window.setTimeout(connect, Math.min(30_000, 1000 * 2 ** retry));
      }
    };
    for (const [name, fn] of Object.entries(handlers)) {
      es.addEventListener(name, (ev) => fn((ev as MessageEvent<string>).data));
    }
  };
  connect();

  return {
    close: () => {
      closed = true;
      window.clearTimeout(timer);
      es?.close();
    },
  };
}

function parse<T>(data: string): T | undefined {
  try {
    return JSON.parse(data) as T;
  } catch {
    return undefined;
  }
}

/** /api/stream: `status` (SiteStatus[] every 2s) and `event` (Event). */
export function openServerStream(
  h: { onStatus: (s: SiteStatus[]) => void; onEvent: (e: NHEvent) => void } & StreamOptions,
): StreamHandle {
  return open(
    '/api/stream',
    {
      status: (d) => {
        const v = parse<SiteStatus[]>(d);
        if (Array.isArray(v)) h.onStatus(v);
      },
      event: (d) => {
        const v = parse<NHEvent>(d);
        if (v) h.onEvent(v);
      },
    },
    h,
  );
}

/** /api/sites/{id}/logs/stream: `log` (LogLine). */
export function openLogStream(
  siteId: string,
  type: LogType,
  h: { onLine: (l: LogLine) => void } & StreamOptions,
): StreamHandle {
  return open(
    `/api/sites/${encodeURIComponent(siteId)}/logs/stream?type=${type}`,
    {
      log: (d) => {
        const v = parse<LogLine>(d);
        if (v) h.onLine(v);
      },
    },
    h,
  );
}

/** Deployment log stream: `log` (string) and `done` (Deployment). */
export function openDeploymentLogStream(
  siteId: string,
  depId: string,
  h: { onLine: (line: string) => void; onDone: (d: Deployment | undefined) => void } & StreamOptions,
): StreamHandle {
  let handle: StreamHandle | null = null;
  handle = open(
    `/api/sites/${encodeURIComponent(siteId)}/deployments/${encodeURIComponent(depId)}/log/stream`,
    {
      log: (d) => {
        // The payload is a plain string, possibly JSON-encoded.
        const v = d.startsWith('"') ? parse<string>(d) : undefined;
        h.onLine(v ?? d);
      },
      done: (d) => {
        handle?.close();
        h.onDone(parse<Deployment>(d));
      },
    },
    h,
  );
  return handle;
}

/**
 * Task run log stream: `log` (a chunk of raw output; the first one is
 * everything written so far, so a reconnect starts over) and `done`
 * (TaskRun, sent at once for a finished run).
 */
export function openRunLogStream(
  siteId: string,
  runId: string,
  h: { onChunk: (text: string) => void; onDone: (r: TaskRun | undefined) => void } & StreamOptions,
): StreamHandle {
  let handle: StreamHandle | null = null;
  handle = open(
    `/api/sites/${encodeURIComponent(siteId)}/runs/${encodeURIComponent(runId)}/log/stream`,
    {
      log: (d) => {
        const v = d.startsWith('"') ? parse<string>(d) : undefined;
        h.onChunk(v ?? d);
      },
      done: (d) => {
        handle?.close();
        h.onDone(parse<TaskRun>(d));
      },
    },
    h,
  );
  return handle;
}
