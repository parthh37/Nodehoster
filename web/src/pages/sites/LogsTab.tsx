import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ArrowDownToLine, Download, Eraser, Pause, Play, Search, Trash2 } from 'lucide-react';
import { sitesApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { openLogStream } from '@/api/sse';
import type { LogLine, LogType, SiteView } from '@/api/types';
import { Button } from '@/components/Button';
import { Input } from '@/components/Input';
import { Segmented } from '@/components/Tabs';
import { Badge } from '@/components/Badge';
import { ErrorBox } from '@/components/Field';
import { Checkbox } from '@/components/Switch';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { useSitePermissions } from '@/hooks/useAuth';
import { formatTime } from '@/lib/format';
import { runsNode } from '@/lib/siteDefaults';
import { cn } from '@/lib/cn';

const MAX_LINES = 5000;
const RENDER_LINES = 2000;

export function LogsTab({ site }: { site: SiteView }) {
  const hasApp = runsNode(site.type);
  // A background worker serves no HTTP, so it has no access log.
  const hasAccess = site.type !== 'worker';
  const [type, setType] = useState<LogType>(hasApp ? 'app' : 'access');
  const [lines, setLines] = useState<LogLine[]>([]);
  const [paused, setPaused] = useState(false);
  const [buffered, setBuffered] = useState(0);
  const [filter, setFilter] = useState('');
  const [autoScroll, setAutoScroll] = useState(true);
  const [showStdout, setShowStdout] = useState(true);
  const [showStderr, setShowStderr] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [connected, setConnected] = useState(false);
  const [loading, setLoading] = useState(true);
  const pausedRef = useRef(false);
  const bufferRef = useRef<LogLine[]>([]);
  const boxRef = useRef<HTMLDivElement>(null);
  const { canOperate } = useSitePermissions(site.id);
  const confirm = useConfirm();
  const toast = useToast();

  pausedRef.current = paused;

  const append = useCallback((incoming: LogLine[]) => {
    setLines((prev) => {
      const next = prev.concat(incoming);
      return next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next;
    });
  }, []);

  useEffect(() => {
    let cancelled = false;
    setLines([]);
    setError(null);
    setLoading(true);
    bufferRef.current = [];
    setBuffered(0);
    const pending: LogLine[] = [];
    let ready = false;

    const h = openLogStream(site.id, type, {
      onOpen: () => setConnected(true),
      onError: () => setConnected(false),
      onLine: (l) => {
        if (!ready) {
          pending.push(l);
          return;
        }
        if (pausedRef.current) {
          bufferRef.current.push(l);
          setBuffered(bufferRef.current.length);
        } else {
          append([l]);
        }
      },
    });

    sitesApi
      .logs(site.id, type, 500)
      .then((initial) => {
        if (cancelled) return;
        const list = initial ?? [];
        // Drop streamed lines already present in the initial batch.
        const last = list.length ? list[list.length - 1].t : '';
        setLines(list.concat(pending.filter((p) => p.t > last)));
      })
      .catch((e) => !cancelled && setError(errorMessage(e)))
      .finally(() => {
        ready = true;
        if (!cancelled) setLoading(false);
      });

    return () => {
      cancelled = true;
      h.close();
    };
  }, [site.id, type, append]);

  const resume = () => {
    setPaused(false);
    append(bufferRef.current);
    bufferRef.current = [];
    setBuffered(0);
  };

  const visible = useMemo(() => {
    const term = filter.trim().toLowerCase();
    const out = lines.filter((l) => {
      if (type === 'app') {
        if (l.s === 'stdout' && !showStdout) return false;
        if (l.s === 'stderr' && !showStderr) return false;
      }
      return !term || l.m.toLowerCase().includes(term);
    });
    return out.length > RENDER_LINES ? out.slice(out.length - RENDER_LINES) : out;
  }, [lines, filter, showStdout, showStderr, type]);

  useEffect(() => {
    const el = boxRef.current;
    if (el && autoScroll) el.scrollTop = el.scrollHeight;
  }, [visible, autoScroll]);

  const clearFiles = async () => {
    const r = await confirm({
      title: 'Delete log files?',
      message: `This permanently deletes the ${type === 'app' ? 'application' : 'access'} log files for ${site.name} on the server.`,
      confirmLabel: 'Delete logs',
      danger: true,
    });
    if (!r.ok) return;
    try {
      await sitesApi.clearLogs(site.id);
      setLines([]);
      toast.success('Log files cleared');
    } catch (e) {
      toast.error('Could not clear logs', e);
    }
  };

  const multiInstance = type === 'app' && (site.node?.instances ?? 1) > 1;

  return (
    <div className="nh-card flex h-[calc(100vh-17rem)] min-h-[420px] flex-col overflow-hidden">
      <div className="flex flex-wrap items-center gap-2 border-b border-zinc-200 px-3 py-2 dark:border-zinc-800">
        <Segmented<LogType>
          value={type}
          onChange={setType}
          options={[
            ...(hasApp ? [{ value: 'app' as const, label: 'Application' }] : []),
            ...(hasAccess ? [{ value: 'access' as const, label: 'Access log' }] : []),
          ]}
        />
        <Input className="w-56" prefix={<Search className="h-3.5 w-3.5" />} placeholder="Filter…" value={filter} onChange={(e) => setFilter(e.target.value)} />
        {type === 'app' && (
          <div className="flex items-center gap-3 px-1">
            <Checkbox checked={showStdout} onChange={setShowStdout} label="stdout" />
            <Checkbox checked={showStderr} onChange={setShowStderr} label={<span className="text-red-600 dark:text-red-400">stderr</span>} />
          </div>
        )}
        <div className="ml-auto flex items-center gap-1.5">
          {connected ? (
            paused ? (
              <Badge tone="amber">paused{buffered > 0 && ` · ${buffered} new`}</Badge>
            ) : (
              <Badge tone="green" dot pulse>
                live
              </Badge>
            )
          ) : (
            <Badge tone="gray">connecting…</Badge>
          )}
          {paused ? (
            <Button size="sm" icon={<Play className="h-3.5 w-3.5" />} onClick={resume}>
              Resume
            </Button>
          ) : (
            <Button size="sm" icon={<Pause className="h-3.5 w-3.5" />} onClick={() => setPaused(true)}>
              Pause
            </Button>
          )}
          <Button
            size="sm"
            variant={autoScroll ? 'secondary' : 'ghost'}
            icon={<ArrowDownToLine className="h-3.5 w-3.5" />}
            onClick={() => setAutoScroll((a) => !a)}
            title="Auto-scroll"
            className={cn(autoScroll && 'text-accent-700 dark:text-accent-400')}
          >
            Follow
          </Button>
          <Button size="sm" variant="ghost" icon={<Eraser className="h-3.5 w-3.5" />} onClick={() => setLines([])} title="Clear the view (files are kept)">
            Clear
          </Button>
          <a href={sitesApi.logsDownloadUrl(site.id, type)} download>
            <Button size="sm" variant="ghost" icon={<Download className="h-3.5 w-3.5" />}>
              Download
            </Button>
          </a>
          {canOperate && (
            <Button size="sm" variant="danger-ghost" icon={<Trash2 className="h-3.5 w-3.5" />} onClick={clearFiles} title="Delete log files on the server" />
          )}
        </div>
      </div>
      {error && <ErrorBox className="m-3">{error}</ErrorBox>}
      <div
        ref={boxRef}
        onScroll={(e) => {
          const el = e.currentTarget;
          const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 30;
          if (!atBottom && autoScroll) setAutoScroll(false);
          if (atBottom && !autoScroll) setAutoScroll(true);
        }}
        className="scrollbar-thin flex-1 overflow-auto bg-zinc-950 py-2 font-mono text-[12px] leading-[1.35rem] text-zinc-200"
      >
        {loading ? (
          <p className="px-3 text-zinc-500">Loading…</p>
        ) : visible.length === 0 ? (
          <p className="px-3 text-zinc-500">
            {lines.length === 0
              ? type === 'app'
                ? 'No output yet. Anything the app writes to stdout or stderr appears here.'
                : 'No requests logged yet.'
              : 'No lines match the filter.'}
          </p>
        ) : (
          visible.map((l, i) => (
            <div
              key={i}
              className={cn(
                'flex gap-3 whitespace-pre-wrap break-all px-3 hover:bg-white/[0.04]',
                l.s === 'stderr' && 'bg-red-500/[0.06] text-red-300',
                l.s === 'system' && 'text-accent-300',
              )}
            >
              <span className="shrink-0 select-none text-zinc-500">{formatTime(l.t)}</span>
              {multiInstance && <span className="w-5 shrink-0 select-none text-right text-zinc-500">#{l.i}</span>}
              <span className="min-w-0 flex-1">{l.m}</span>
            </div>
          ))
        )}
      </div>
      {lines.length > RENDER_LINES && (
        <div className="border-t border-zinc-800 bg-zinc-950 px-3 py-1 text-2xs text-zinc-500">
          Showing the last {RENDER_LINES.toLocaleString()} of {lines.length.toLocaleString()} lines. Download for the full log.
        </div>
      )}
    </div>
  );
}
