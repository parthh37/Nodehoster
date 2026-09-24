import { forwardRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { History, ScrollText, Square } from 'lucide-react';
import { tasksApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { TaskRun, TaskView } from '@/api/types';
import { Button } from '@/components/Button';
import { Select } from '@/components/Input';
import { Card, EmptyState, Loading } from '@/components/Layout';
import { ErrorBox } from '@/components/Field';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { useNow } from '@/hooks/useNow';
import { durationBetween, formatDateTime, relativeTime } from '@/lib/format';
import { ExitCode, RunStatusBadge, triggerLabel } from './RunStatus';

const PAGE = 50;
const MAX = 500;

/** Past and running runs of a site's tasks, newest first. */
export const RunHistory = forwardRef<
  HTMLDivElement,
  {
    siteId: string;
    tasks: TaskView[];
    /** Task id, or '' for every task. */
    filter: string;
    onFilter: (taskId: string) => void;
    onOpen: (r: TaskRun) => void;
    onCancel?: (r: TaskRun) => void;
  }
>(function RunHistory({ siteId, tasks, filter, onFilter, onOpen, onCancel }, ref) {
  const now = useNow(1000);
  const [limit, setLimit] = useState(PAGE);
  const q = useQuery({
    queryKey: qk.taskRuns(siteId, filter, limit),
    queryFn: () => tasksApi.runs(siteId, filter || undefined, limit),
    refetchInterval: (query) => ((query.state.data ?? []).some((r) => r.status === 'running') ? 3000 : 10_000),
  });
  const runs = q.data ?? [];
  const known = tasks.some((t) => t.id === filter);

  return (
    <div ref={ref} className="scroll-mt-4">
      <Card
        title="Run history"
        description="Each run keeps its output. Skipped runs were due while the previous run was still going."
        actions={
          <Select
            className="w-56"
            value={filter}
            onChange={(v) => {
              onFilter(v);
              setLimit(PAGE);
            }}
            options={[
              { value: '', label: 'All tasks' },
              ...tasks.map((t) => ({ value: t.id, label: t.name })),
              ...(filter && !known ? [{ value: filter, label: 'Removed task' }] : []),
            ]}
          />
        }
        flush
      >
        {q.isPending ? (
          <Loading />
        ) : q.isError ? (
          <div className="p-4">
            <ErrorBox>{errorMessage(q.error)}</ErrorBox>
          </div>
        ) : runs.length === 0 ? (
          <EmptyState compact icon={<History />} title="No runs yet" description="Runs appear here when a task runs on its schedule or is started with Run now." />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th>Status</Th>
                {!filter && <Th>Task</Th>}
                <Th>Trigger</Th>
                <Th>Started</Th>
                <Th className="text-right">Duration</Th>
                <Th className="text-right">Exit code</Th>
                <Th>Error</Th>
                <Th className="w-40" />
              </tr>
            </THead>
            <TBody>
              {runs.map((r) => (
                <Tr key={r.id} onClick={() => onOpen(r)}>
                  <Td>
                    <RunStatusBadge status={r.status} />
                  </Td>
                  {!filter && <Td className="font-medium">{r.taskName}</Td>}
                  <Td className="whitespace-nowrap text-zinc-600 dark:text-zinc-300">{triggerLabel(r)}</Td>
                  <Td className="whitespace-nowrap text-zinc-500" title={formatDateTime(r.startedAt)}>
                    {relativeTime(r.startedAt, now)}
                  </Td>
                  <Td className="text-right tabular text-zinc-500">{r.status === 'skipped' ? '—' : durationBetween(r.startedAt, r.finishedAt, now)}</Td>
                  <Td className="text-right tabular">
                    <ExitCode run={r} />
                  </Td>
                  <Td className="max-w-xs truncate text-xs text-red-600 dark:text-red-400" title={r.error}>
                    {r.status === 'skipped' ? <span className="text-zinc-500">{r.error || 'Previous run still going'}</span> : r.error || <span className="text-zinc-400">—</span>}
                  </Td>
                  <Td>
                    <div className="flex justify-end gap-1" onClick={(e) => e.stopPropagation()}>
                      {r.status !== 'skipped' && (
                        <Button size="sm" variant="ghost" icon={<ScrollText className="h-3.5 w-3.5" />} onClick={() => onOpen(r)}>
                          Log
                        </Button>
                      )}
                      {r.status === 'running' && onCancel && (
                        <Button size="sm" variant="danger-ghost" icon={<Square className="h-3.5 w-3.5" />} onClick={() => onCancel(r)}>
                          Cancel
                        </Button>
                      )}
                    </div>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        )}
        {runs.length >= limit && limit < MAX && (
          <div className="border-t border-zinc-200 px-4 py-2 text-center dark:border-zinc-800">
            <Button size="sm" variant="ghost" onClick={() => setLimit((l) => Math.min(MAX, l + 100))}>
              Show more
            </Button>
          </div>
        )}
      </Card>
    </div>
  );
});
