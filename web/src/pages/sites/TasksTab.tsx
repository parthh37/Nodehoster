import { useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { CalendarClock, History, Pencil, Play, Plus, Square, Trash2 } from 'lucide-react';
import { tasksApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { ScheduledTask, Site, TaskRun, TaskView } from '@/api/types';
import { Badge } from '@/components/Badge';
import { Button, IconButton } from '@/components/Button';
import { Card, Callout, EmptyState, Mono } from '@/components/Layout';
import { ErrorBox, PathError } from '@/components/Field';
import { Switch } from '@/components/Switch';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { useSitePermissions } from '@/hooks/useAuth';
import { useNow } from '@/hooks/useNow';
import { describeCron } from '@/lib/cron';
import { durationBetween, formatDateTime, relativeTime } from '@/lib/format';
import { jsonEqual } from '@/lib/obj';
import { defaultTask } from '@/lib/siteDefaults';
import type { SiteEditorProps } from './editors/types';
import { TaskDialog } from './tasks/TaskDialog';
import { runtimeOf, taskCommand } from '@/lib/runtimes';
import { RunHistory } from './tasks/RunHistory';
import { RunLogDialog } from './tasks/RunLogDialog';
import { RunStatusBadge } from './tasks/RunStatus';

/**
 * Scheduled tasks of a node or worker site. The definitions are part of the
 * site draft (saved with the SaveBar, administrators only); running and
 * cancelling go straight to the API (operators).
 */
export function TasksTab({ site, update, readOnly, savedSite }: SiteEditorProps & { savedSite: Site }) {
  const { canOperate } = useSitePermissions(site.id);
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const now = useNow(1000);
  const [editing, setEditing] = useState<{ index: number; task: ScheduledTask } | null>(null);
  const [logFor, setLogFor] = useState<TaskRun | null>(null);
  const [historyFilter, setHistoryFilter] = useState('');
  const historyRef = useRef<HTMLDivElement>(null);

  const q = useQuery({
    queryKey: qk.siteTasks(site.id),
    queryFn: () => tasksApi.list(site.id),
    refetchInterval: (query) => ((query.state.data ?? []).some((v) => (v.running ?? []).length > 0 || v.queued) ? 3000 : 10_000),
  });
  const views = q.data ?? [];
  const tasks = site.tasks ?? [];
  const saved = savedSite.tasks ?? [];
  const pendingSave = !jsonEqual(saved, tasks);

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: qk.siteTasks(site.id) });
    void qc.invalidateQueries({ queryKey: qk.siteRuns(site.id) });
  };

  // A save reschedules the tasks: pick up their new ids and next runs now
  // rather than at the next poll.
  useEffect(() => {
    void qc.invalidateQueries({ queryKey: qk.siteTasks(site.id) });
  }, [qc, site.id, savedSite.updatedAt]);

  const run = useMutation({
    mutationFn: (t: ScheduledTask) => tasksApi.run(site.id, t.id),
    onSuccess: (res, t) => {
      refresh();
      if (res.run) {
        toast.success(`${t.name} started`);
        setLogFor(res.run);
      } else {
        toast.success(`${t.name} queued`, 'It runs as soon as the current run ends.');
      }
    },
    onError: (e, t) => toast.error(`Could not run ${t.name}`, e),
  });

  const cancel = useMutation({
    mutationFn: (r: TaskRun) => tasksApi.cancel(site.id, r.id),
    onSuccess: (_res, r) => {
      toast.success(`Cancelling ${r.taskName}`, 'Its process tree is being stopped.');
      refresh();
    },
    onError: (e) => toast.error('Could not cancel the run', e),
  });

  const askCancel = async (r: TaskRun) => {
    const res = await confirm({
      title: `Cancel this run of ${r.taskName}?`,
      message: 'The task and every process it started are stopped. The run is recorded as cancelled.',
      confirmLabel: 'Cancel run',
      cancelLabel: 'Keep running',
      danger: true,
    });
    if (res.ok) cancel.mutate(r);
  };

  const setTasks = (fn: (list: ScheduledTask[]) => ScheduledTask[]) =>
    update((d) => {
      d.tasks = fn(d.tasks ?? []);
    });

  const showHistory = (taskId: string) => {
    setHistoryFilter(taskId);
    historyRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  };

  const addTask = () => setEditing({ index: tasks.length, task: defaultTask() });

  return (
    <div className="space-y-5">
      <Card
        title="Scheduled tasks"
        description={
          <>
            Scripts this site runs on a schedule, like Task Scheduler or cron. A run uses the site's active release, runtime and version, environment
            variables, secrets and Windows identity, with <span className="font-mono">NODEHOSTER_TASK</span> set to the task's name (no PORT). Tasks run
            even while the site is stopped — disable a task to pause it. Schedules use the server's local time; runs missed while the service was
            down are not caught up.
          </>
        }
        actions={
          !readOnly && (
            <Button size="sm" variant="primary" icon={<Plus className="h-3.5 w-3.5" />} onClick={addTask}>
              Add task
            </Button>
          )
        }
        flush
      >
        {pendingSave && !readOnly && (
          <div className="px-4 pt-3">
            <Callout tone="warning">Save your changes to schedule new or edited tasks. Unsaved tasks cannot be run yet.</Callout>
          </div>
        )}
        {q.isError && (
          <div className="px-4 pt-3">
            <ErrorBox>{errorMessage(q.error)}</ErrorBox>
          </div>
        )}
        <PathError path="tasks" className="px-4 pt-3" />
        {tasks.length === 0 ? (
          <EmptyState
            icon={<CalendarClock />}
            title="No scheduled tasks"
            description="Run a clean-up, a report or a sync job from this site's code every few minutes, nightly or on demand."
            action={
              !readOnly && (
                <Button variant="primary" icon={<Plus className="h-4 w-4" />} onClick={addTask}>
                  Add task
                </Button>
              )
            }
          />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th>Task</Th>
                <Th>Schedule</Th>
                <Th>Enabled</Th>
                <Th>Next run</Th>
                <Th>Last run</Th>
                <Th className="w-48" />
              </tr>
            </THead>
            <TBody>
              {tasks.map((t, i) => {
                const savedTask = t.id ? saved.find((x) => x.id === t.id) : undefined;
                const unsaved = !savedTask || !jsonEqual(savedTask, t);
                const view = t.id ? views.find((v) => v.id === t.id) : undefined;
                const current = view?.lastRun?.status === 'running' ? view.lastRun : null;
                return (
                  <Tr key={t.id || `new-${i}`} onClick={() => setEditing({ index: i, task: t })}>
                    <Td className="max-w-[16rem]">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="font-medium">{t.name || <span className="text-zinc-400">Unnamed</span>}</span>
                        {!savedTask ? <Badge tone="amber">new</Badge> : unsaved && <Badge tone="amber">edited</Badge>}
                      </div>
                      <Mono className="block truncate text-xs text-zinc-500" title={taskCommand(site.node, t)}>
                        {taskCommand(site.node, t)}
                      </Mono>
                      <PathError path={`tasks[${i}]`} prefix />
                    </Td>
                    <Td className="max-w-[18rem]">
                      {t.schedule ? <Mono>{t.schedule}</Mono> : <span className="text-xs text-zinc-400">none</span>}
                      <p className="text-xs text-zinc-500">{describeCron(t.schedule)}</p>
                    </Td>
                    <Td onClick={(e) => e.stopPropagation()}>
                      {readOnly ? (
                        <Badge tone={t.enabled ? 'green' : 'gray'}>{t.enabled ? 'enabled' : 'disabled'}</Badge>
                      ) : (
                        <Switch
                          size="sm"
                          checked={t.enabled}
                          onChange={(v) =>
                            setTasks((l) => {
                              l[i] = { ...l[i], enabled: v };
                              return l;
                            })
                          }
                        />
                      )}
                    </Td>
                    <Td className="whitespace-nowrap">
                      <NextRun task={t} view={view} unsaved={unsaved} now={now} />
                    </Td>
                    <Td onClick={(e) => e.stopPropagation()}>
                      <LastRun view={view} now={now} onOpen={setLogFor} />
                    </Td>
                    <Td onClick={(e) => e.stopPropagation()}>
                      <div className="flex items-center justify-end gap-0.5">
                        {canOperate && current && (
                          <Button size="sm" variant="danger-ghost" icon={<Square className="h-3.5 w-3.5" />} onClick={() => askCancel(current)}>
                            Cancel
                          </Button>
                        )}
                        {canOperate && (
                          <span title={unsaved ? 'Save the site first: this task is not scheduled yet' : 'Start a run now'}>
                            <Button
                              size="sm"
                              variant="ghost"
                              icon={<Play className="h-3.5 w-3.5" />}
                              disabled={unsaved}
                              loading={run.isPending && run.variables?.id === t.id}
                              onClick={() => run.mutate(t)}
                            >
                              Run now
                            </Button>
                          </span>
                        )}
                        <IconButton label="History" icon={<History className="h-3.5 w-3.5" />} disabled={!t.id} onClick={() => showHistory(t.id)} />
                        {!readOnly && (
                          <>
                            <IconButton label="Edit" icon={<Pencil className="h-3.5 w-3.5" />} onClick={() => setEditing({ index: i, task: t })} />
                            <IconButton
                              label="Remove"
                              variant="danger-ghost"
                              icon={<Trash2 className="h-3.5 w-3.5" />}
                              onClick={() => setTasks((l) => l.filter((_, j) => j !== i))}
                            />
                          </>
                        )}
                      </div>
                    </Td>
                  </Tr>
                );
              })}
            </TBody>
          </Table>
        )}
      </Card>

      <RunHistory
        ref={historyRef}
        siteId={site.id}
        tasks={views}
        filter={historyFilter}
        onFilter={setHistoryFilter}
        onOpen={setLogFor}
        onCancel={canOperate ? askCancel : undefined}
      />

      <TaskDialog
        value={editing?.task ?? null}
        index={editing?.index ?? 0}
        others={tasks.filter((_, j) => j !== editing?.index)}
        runtime={runtimeOf(site.node)}
        readOnly={readOnly}
        onClose={() => setEditing(null)}
        onApply={(task) => {
          if (editing)
            setTasks((l) => {
              if (editing.index >= l.length) l.push(task);
              else l[editing.index] = task;
              return l;
            });
          setEditing(null);
        }}
      />
      <RunLogDialog siteId={site.id} run={logFor} onClose={() => setLogFor(null)} onCancel={canOperate ? askCancel : undefined} />
    </div>
  );
}

function NextRun({ task, view, unsaved, now }: { task: ScheduledTask; view: TaskView | undefined; unsaved: boolean; now: number }) {
  if (unsaved) return <span className="text-xs text-amber-600 dark:text-amber-400">Save to schedule</span>;
  if (!task.enabled) return <span className="text-xs text-zinc-400">Disabled</span>;
  if (!task.schedule) return <span className="text-xs text-zinc-400">On demand</span>;
  if (!view?.nextRunAt) return <span className="text-zinc-400">—</span>;
  return (
    <div title="Server time, shown in your time zone">
      <div className="text-[13px]">{formatDateTime(view.nextRunAt)}</div>
      <div className="text-xs text-zinc-500">{relativeTime(view.nextRunAt, now)}</div>
    </div>
  );
}

function LastRun({ view, now, onOpen }: { view: TaskView | undefined; now: number; onOpen: (r: TaskRun) => void }) {
  const r = view?.lastRun;
  const more = (view?.running ?? []).length - 1;
  const queued = view?.queued && (
    <Badge tone="amber" title="Runs as soon as the current run ends">
      queued
    </Badge>
  );
  if (!r) return queued || <span className="text-zinc-400">—</span>;
  const exit = r.exitCode !== undefined && r.exitCode !== null && r.status !== 'running' ? ` · exit ${r.exitCode}` : '';
  return (
    <button type="button" className="text-left" onClick={() => onOpen(r)} title="Show the output">
      <span className="flex flex-wrap items-center gap-1.5">
        <RunStatusBadge status={r.status} title={r.error} />
        {more > 0 && <Badge tone="blue">+{more} running</Badge>}
        {queued}
      </span>
      <span className="mt-0.5 block whitespace-nowrap text-xs text-zinc-500" title={formatDateTime(r.startedAt)}>
        {relativeTime(r.startedAt, now)} · {durationBetween(r.startedAt, r.finishedAt, now)}
        {exit}
      </span>
    </button>
  );
}
