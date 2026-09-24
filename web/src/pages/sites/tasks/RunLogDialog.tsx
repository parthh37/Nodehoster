import { useEffect, useMemo, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Download, Square } from 'lucide-react';
import { tasksApi } from '@/api/endpoints';
import { ApiError, errorMessage } from '@/api/client';
import { openRunLogStream } from '@/api/sse';
import { qk } from '@/api/queryKeys';
import type { TaskRun } from '@/api/types';
import { Button } from '@/components/Button';
import { Badge } from '@/components/Badge';
import { Dialog } from '@/components/Dialog';
import { ErrorBox } from '@/components/Field';
import { Callout } from '@/components/Layout';
import { useNow } from '@/hooks/useNow';
import { durationBetween, formatDateTime } from '@/lib/format';
import { cn } from '@/lib/cn';
import { RunStatusBadge, triggerLabel } from './RunStatus';

// Rendering is capped; the download has everything.
const RENDER_LINES = 5000;

/**
 * Output of one task run: loaded once for a finished run, streamed while it
 * is running (the same pattern as the deployment log).
 */
export function RunLogDialog({
  siteId,
  run,
  onClose,
  onCancel,
}: {
  siteId: string;
  run: TaskRun | null;
  onClose: () => void;
  /** Offered while the run is in progress (operators). */
  onCancel?: (r: TaskRun) => void;
}) {
  const qc = useQueryClient();
  const now = useNow(1000);
  const [text, setText] = useState('');
  const [current, setCurrent] = useState<TaskRun | null>(run);
  const [error, setError] = useState<string | null>(null);
  const [live, setLive] = useState(false);
  const boxRef = useRef<HTMLPreElement>(null);
  const stick = useRef(true);

  useEffect(() => {
    setCurrent(run);
    setText('');
    setError(null);
    setLive(false);
    stick.current = true;
    if (!run || run.status === 'skipped') return;
    if (run.status === 'running') {
      setLive(true);
      const h = openRunLogStream(siteId, run.id, {
        // The first chunk after (re)connecting is everything written so far.
        onOpen: () => setText(''),
        onChunk: (c) => setText((t) => t + c),
        onDone: (r) => {
          setLive(false);
          if (r) setCurrent(r);
          void qc.invalidateQueries({ queryKey: qk.siteTasks(siteId) });
          void qc.invalidateQueries({ queryKey: qk.siteRuns(siteId) });
        },
      });
      return () => h.close();
    }
    let cancelled = false;
    tasksApi
      .log(siteId, run.id)
      .then((t) => !cancelled && setText(t))
      .catch((e) => !cancelled && setError(e instanceof ApiError && e.status === 404 ? 'This run has no log.' : errorMessage(e)));
    return () => {
      cancelled = true;
    };
  }, [run, siteId, qc]);

  const lines = useMemo(() => {
    const all = text.replace(/\n$/, '').split('\n');
    return { total: text ? all.length : 0, shown: text ? all.slice(-RENDER_LINES) : [] };
  }, [text]);

  useEffect(() => {
    const el = boxRef.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [lines]);

  const r = current;
  return (
    <Dialog
      open={!!run}
      onClose={onClose}
      size="xl"
      title={
        <span className="flex items-center gap-2">
          {r?.taskName ?? 'Task'} run
          {r && <RunStatusBadge status={r.status} />}
          {live && (
            <Badge tone="accent" dot pulse>
              live
            </Badge>
          )}
        </span>
      }
      description={
        r
          ? `${triggerLabel(r)} · started ${formatDateTime(r.startedAt)} · ${durationBetween(r.startedAt, r.finishedAt, now)}${
              r.exitCode !== undefined && r.exitCode !== null ? ` · exit code ${r.exitCode}` : ''
            }`
          : undefined
      }
      footer={
        <>
          {r && r.status !== 'skipped' && (
            <a href={tasksApi.logDownloadUrl(siteId, r.id)} download className="mr-auto">
              <Button icon={<Download className="h-3.5 w-3.5" />}>Download</Button>
            </a>
          )}
          {r?.status === 'running' && onCancel && (
            <Button variant="danger-ghost" icon={<Square className="h-3.5 w-3.5" />} onClick={() => onCancel(r)}>
              Cancel run
            </Button>
          )}
          <Button onClick={onClose}>Close</Button>
        </>
      }
    >
      {r?.status === 'skipped' ? (
        <Callout tone="info">{r.error || 'This run was skipped because the previous run of the task was still going.'}</Callout>
      ) : error ? (
        <ErrorBox>{error}</ErrorBox>
      ) : (
        <>
          <pre
            ref={boxRef}
            onScroll={(e) => {
              const el = e.currentTarget;
              stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
            }}
            className="scrollbar-thin h-[55vh] overflow-auto rounded-md bg-zinc-950 p-3 font-mono text-xs leading-5 text-zinc-200"
          >
            {lines.shown.length === 0 ? (
              <span className="text-zinc-500">{live ? 'Waiting for output…' : 'No output.'}</span>
            ) : (
              lines.shown.map((l, i) => (
                <div key={i} className={cn(/error|fail|err!/i.test(l) && 'text-red-400')}>
                  {l || ' '}
                </div>
              ))
            )}
          </pre>
          {lines.total > RENDER_LINES && (
            <p className="mt-1 text-2xs text-zinc-500">
              Showing the last {RENDER_LINES.toLocaleString()} of {lines.total.toLocaleString()} lines. Download for the full log.
            </p>
          )}
        </>
      )}
      {r && r.status !== 'skipped' && r.status !== 'succeeded' && r.status !== 'running' && r.error && <ErrorBox className="mt-3">{r.error}</ErrorBox>}
    </Dialog>
  );
}
