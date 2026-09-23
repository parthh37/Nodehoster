import type { ReactNode } from 'react';
import { NavLink } from 'react-router-dom';
import { cn } from '@/lib/cn';

export interface TabItem<K extends string = string> {
  key: K;
  label: ReactNode;
  icon?: ReactNode;
  badge?: ReactNode;
  hidden?: boolean;
}

const tabClass = (active: boolean) =>
  cn(
    '-mb-px inline-flex items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2 text-[13px] font-medium transition-colors',
    active
      ? 'border-accent-600 text-zinc-900 dark:border-accent-400 dark:text-zinc-50'
      : 'border-transparent text-zinc-500 hover:border-zinc-300 hover:text-zinc-800 dark:text-zinc-400 dark:hover:border-zinc-600 dark:hover:text-zinc-200',
  );

/** Controlled tabs (state held by the parent). */
export function Tabs<K extends string>({
  tabs,
  value,
  onChange,
  className,
}: {
  tabs: TabItem<K>[];
  value: K;
  onChange: (k: K) => void;
  className?: string;
}) {
  return (
    <div className={cn('scrollbar-thin flex overflow-x-auto border-b border-zinc-200 dark:border-zinc-800', className)} role="tablist">
      {tabs
        .filter((t) => !t.hidden)
        .map((t) => (
          <button key={t.key} type="button" role="tab" aria-selected={t.key === value} className={tabClass(t.key === value)} onClick={() => onChange(t.key)}>
            {t.icon}
            {t.label}
            {t.badge}
          </button>
        ))}
    </div>
  );
}

/** Tabs that are routes: each tab links to `${base}/${key}`. */
export function RouteTabs<K extends string>({
  tabs,
  base,
  value,
  className,
}: {
  tabs: TabItem<K>[];
  base: string;
  value: K;
  className?: string;
}) {
  return (
    <div className={cn('scrollbar-thin flex overflow-x-auto border-b border-zinc-200 dark:border-zinc-800', className)} role="tablist">
      {tabs
        .filter((t) => !t.hidden)
        .map((t) => (
          <NavLink key={t.key} to={`${base}/${t.key}`} replace role="tab" aria-selected={t.key === value} className={tabClass(t.key === value)}>
            {t.icon}
            {t.label}
            {t.badge}
          </NavLink>
        ))}
    </div>
  );
}

/** Small segmented control. */
export function Segmented<K extends string>({
  options,
  value,
  onChange,
  className,
}: {
  options: { value: K; label: ReactNode }[];
  value: K;
  onChange: (k: K) => void;
  className?: string;
}) {
  return (
    <div className={cn('inline-flex rounded-md border border-zinc-300 bg-zinc-100 p-0.5 dark:border-zinc-700 dark:bg-zinc-800/60', className)}>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          onClick={() => onChange(o.value)}
          className={cn(
            'rounded px-2.5 py-0.5 text-xs font-medium transition-colors',
            o.value === value
              ? 'bg-white text-zinc-900 shadow-sm dark:bg-zinc-700 dark:text-zinc-50'
              : 'text-zinc-600 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100',
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
