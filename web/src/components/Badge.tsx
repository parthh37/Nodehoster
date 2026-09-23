import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

export type Tone = 'gray' | 'green' | 'amber' | 'red' | 'blue' | 'accent' | 'violet';

const tones: Record<Tone, string> = {
  gray: 'bg-zinc-100 text-zinc-700 ring-zinc-200 dark:bg-zinc-800 dark:text-zinc-300 dark:ring-zinc-700',
  green: 'bg-emerald-50 text-emerald-700 ring-emerald-200 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-500/25',
  amber: 'bg-amber-50 text-amber-800 ring-amber-200 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-500/25',
  red: 'bg-red-50 text-red-700 ring-red-200 dark:bg-red-500/10 dark:text-red-300 dark:ring-red-500/25',
  blue: 'bg-sky-50 text-sky-700 ring-sky-200 dark:bg-sky-500/10 dark:text-sky-300 dark:ring-sky-500/25',
  accent: 'bg-accent-50 text-accent-800 ring-accent-200 dark:bg-accent-500/10 dark:text-accent-300 dark:ring-accent-500/25',
  violet: 'bg-violet-50 text-violet-700 ring-violet-200 dark:bg-violet-500/10 dark:text-violet-300 dark:ring-violet-500/25',
};

const dots: Record<Tone, string> = {
  gray: 'bg-zinc-400',
  green: 'bg-emerald-500',
  amber: 'bg-amber-500',
  red: 'bg-red-500',
  blue: 'bg-sky-500',
  accent: 'bg-accent-500',
  violet: 'bg-violet-500',
};

export function Badge({
  tone = 'gray',
  children,
  dot,
  pulse,
  className,
  title,
  mono,
}: {
  tone?: Tone;
  children: ReactNode;
  dot?: boolean;
  pulse?: boolean;
  className?: string;
  title?: string;
  mono?: boolean;
}) {
  return (
    <span
      title={title}
      className={cn(
        'inline-flex max-w-full items-center gap-1.5 whitespace-nowrap rounded px-1.5 py-px text-xs font-medium ring-1 ring-inset',
        mono && 'font-mono',
        tones[tone],
        className,
      )}
    >
      {dot && (
        <span className="relative flex h-1.5 w-1.5">
          {pulse && <span className={cn('absolute inline-flex h-full w-full animate-ping rounded-full opacity-60', dots[tone])} />}
          <span className={cn('relative inline-flex h-1.5 w-1.5 rounded-full', dots[tone])} />
        </span>
      )}
      <span className="flex min-w-0 items-center gap-1 overflow-hidden text-ellipsis whitespace-nowrap">{children}</span>
    </span>
  );
}

export function Dot({ tone, className }: { tone: Tone; className?: string }) {
  return <span className={cn('inline-block h-2 w-2 shrink-0 rounded-full', dots[tone], className)} />;
}
