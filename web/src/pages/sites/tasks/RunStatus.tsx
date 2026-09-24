import { Loader2 } from 'lucide-react';
import type { TaskRun } from '@/api/types';
import { Badge, type Tone } from '@/components/Badge';
import { Mono } from '@/components/Layout';
import { cn } from '@/lib/cn';

const STATUS: Record<string, { tone: Tone; label: string }> = {
  running: { tone: 'blue', label: 'Running' },
  succeeded: { tone: 'green', label: 'Succeeded' },
  failed: { tone: 'red', label: 'Failed' },
  timeout: { tone: 'amber', label: 'Timed out' },
  cancelled: { tone: 'gray', label: 'Cancelled' },
  skipped: { tone: 'gray', label: 'Skipped' },
};

export function RunStatusBadge({ status, title }: { status: string; title?: string }) {
  const s = STATUS[status] ?? { tone: 'gray' as Tone, label: status };
  return (
    <Badge tone={s.tone} title={title} className={cn(status === 'skipped' && 'opacity-70')}>
      {status === 'running' && <Loader2 className="h-3 w-3 animate-spin" />}
      {s.label}
    </Badge>
  );
}

export function triggerLabel(r: TaskRun): string {
  if (r.trigger === 'manual') return r.user ? `Manual · ${r.user}` : 'Manual';
  return 'Schedule';
}

export function ExitCode({ run }: { run: TaskRun }) {
  if (run.exitCode === undefined || run.exitCode === null) return <span className="text-zinc-400">—</span>;
  return <Mono className={run.exitCode === 0 ? 'text-zinc-500' : 'text-red-600 dark:text-red-400'}>{run.exitCode}</Mono>;
}
