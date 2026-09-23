// Page-level layout primitives: headers, cards, sections, empty states, spinners.
import type { ReactNode } from 'react';
import { Loader2 } from 'lucide-react';
import { cn } from '@/lib/cn';

export function PageHeader({
  title,
  description,
  actions,
  icon,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  icon?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="mb-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-3">
          {icon && (
            <div className="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-zinc-200 bg-white text-zinc-600 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-300">
              {icon}
            </div>
          )}
          <div className="min-w-0">
            <h1 className="truncate text-xl font-semibold tracking-tight text-zinc-900 dark:text-zinc-50">{title}</h1>
            {description && <div className="mt-0.5 text-[13px] text-zinc-500 dark:text-zinc-400">{description}</div>}
          </div>
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
      </div>
      {children}
    </div>
  );
}

export function Card({
  title,
  description,
  actions,
  children,
  className,
  bodyClassName,
  flush,
  footer,
  tone,
}: {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
  className?: string;
  bodyClassName?: string;
  /** No body padding (for tables). */
  flush?: boolean;
  footer?: ReactNode;
  tone?: 'warning' | 'danger';
}) {
  return (
    <section
      className={cn(
        'nh-card overflow-hidden',
        tone === 'warning' && 'border-amber-300 dark:border-amber-500/40',
        tone === 'danger' && 'border-red-200 dark:border-red-500/30',
        className,
      )}
    >
      {(title || actions) && (
        <header className="flex flex-wrap items-start justify-between gap-2 border-b border-zinc-200 px-4 py-3 dark:border-zinc-800">
          <div className="min-w-0">
            {title && <h2 className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">{title}</h2>}
            {description && <div className="mt-0.5 text-xs text-zinc-500 dark:text-zinc-400">{description}</div>}
          </div>
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </header>
      )}
      {children !== undefined && children !== null && children !== false && (
        <div className={cn(!flush && 'p-4', bodyClassName)}>{children}</div>
      )}
      {footer && (
        <footer className="border-t border-zinc-200 bg-zinc-50/60 px-4 py-2.5 dark:border-zinc-800 dark:bg-zinc-900/40">{footer}</footer>
      )}
    </section>
  );
}

/** Two-column row inside a settings card: title/description left, controls right. */
export function FormSection({ title, description, children, className }: { title: ReactNode; description?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <div className={cn('grid gap-4 py-5 first:pt-0 last:pb-0 md:grid-cols-[minmax(0,15rem)_1fr]', className)}>
      <div>
        <h3 className="text-[13px] font-semibold text-zinc-900 dark:text-zinc-100">{title}</h3>
        {description && <div className="mt-1 text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">{description}</div>}
      </div>
      <div className="min-w-0 space-y-4">{children}</div>
    </div>
  );
}

export function Sections({ children }: { children: ReactNode }) {
  return <div className="divide-y divide-zinc-200 dark:divide-zinc-800">{children}</div>;
}

export function Grid({ children, cols = 2, className }: { children: ReactNode; cols?: 2 | 3 | 4; className?: string }) {
  const c = cols === 4 ? 'sm:grid-cols-2 lg:grid-cols-4' : cols === 3 ? 'sm:grid-cols-3' : 'sm:grid-cols-2';
  return <div className={cn('grid gap-4', c, className)}>{children}</div>;
}

export function EmptyState({
  icon,
  title,
  description,
  action,
  className,
  compact,
}: {
  icon?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  className?: string;
  compact?: boolean;
}) {
  return (
    <div className={cn('flex flex-col items-center justify-center text-center', compact ? 'px-4 py-8' : 'px-6 py-14', className)}>
      {icon && (
        <div className="mb-3 flex h-11 w-11 items-center justify-center rounded-full bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400 [&>svg]:h-5 [&>svg]:w-5">
          {icon}
        </div>
      )}
      <h3 className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">{title}</h3>
      {description && <div className="mt-1 max-w-md text-[13px] text-zinc-500 dark:text-zinc-400">{description}</div>}
      {action && <div className="mt-4 flex flex-wrap justify-center gap-2">{action}</div>}
    </div>
  );
}

export function Spinner({ className }: { className?: string }) {
  return <Loader2 className={cn('h-4 w-4 animate-spin text-zinc-400', className)} />;
}

export function Loading({ label = 'Loading…', className }: { label?: string; className?: string }) {
  return (
    <div className={cn('flex items-center justify-center gap-2 py-16 text-[13px] text-zinc-500', className)}>
      <Spinner /> {label}
    </div>
  );
}

export function ProgressBar({ value, tone = 'accent', className, indeterminate }: { value: number; tone?: 'accent' | 'amber' | 'red' | 'green'; className?: string; indeterminate?: boolean }) {
  const pct = Math.max(0, Math.min(100, value));
  const color = { accent: 'bg-accent-600 dark:bg-accent-500', amber: 'bg-amber-500', red: 'bg-red-500', green: 'bg-emerald-500' }[tone];
  return (
    <div className={cn('relative h-1.5 w-full overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800', className)}>
      {indeterminate ? (
        <div className={cn('absolute inset-y-0 w-1/3 animate-indeterminate rounded-full', color)} />
      ) : (
        <div className={cn('h-full rounded-full transition-[width] duration-500', color)} style={{ width: `${pct}%` }} />
      )}
    </div>
  );
}

export function Callout({
  tone = 'info',
  title,
  children,
  icon,
  className,
  actions,
}: {
  tone?: 'info' | 'warning' | 'danger' | 'success';
  title?: ReactNode;
  children?: ReactNode;
  icon?: ReactNode;
  className?: string;
  actions?: ReactNode;
}) {
  const styles = {
    info: 'border-sky-200 bg-sky-50 text-sky-900 dark:border-sky-500/25 dark:bg-sky-500/10 dark:text-sky-200',
    warning: 'border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-500/25 dark:bg-amber-500/10 dark:text-amber-200',
    danger: 'border-red-200 bg-red-50 text-red-900 dark:border-red-500/25 dark:bg-red-500/10 dark:text-red-200',
    success: 'border-emerald-200 bg-emerald-50 text-emerald-900 dark:border-emerald-500/25 dark:bg-emerald-500/10 dark:text-emerald-200',
  }[tone];
  return (
    <div className={cn('flex items-start gap-2.5 rounded-md border px-3 py-2.5 text-[13px]', styles, className)}>
      {icon && <div className="mt-0.5 shrink-0 [&>svg]:h-4 [&>svg]:w-4">{icon}</div>}
      <div className="min-w-0 flex-1">
        {title && <p className="font-semibold">{title}</p>}
        {children && <div className={cn(title && 'mt-0.5', 'opacity-90')}>{children}</div>}
      </div>
      {actions && <div className="shrink-0">{actions}</div>}
    </div>
  );
}

/** Label/value pair list. */
export function KV({ items, className }: { items: [ReactNode, ReactNode][]; className?: string }) {
  return (
    <dl className={cn('grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1.5 text-[13px]', className)}>
      {items.map(([k, v], i) => (
        <div key={i} className="contents">
          <dt className="text-zinc-500 dark:text-zinc-400">{k}</dt>
          <dd className="min-w-0 break-words text-zinc-900 dark:text-zinc-100">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export function Mono({ children, className, title }: { children: ReactNode; className?: string; title?: string }) {
  return (
    <span title={title} className={cn('font-mono text-[12.5px]', className)}>
      {children}
    </span>
  );
}

export function Stat({
  label,
  value,
  sub,
  icon,
  className,
  tone,
  children,
}: {
  label: ReactNode;
  value: ReactNode;
  sub?: ReactNode;
  icon?: ReactNode;
  className?: string;
  tone?: 'default' | 'green' | 'amber' | 'red';
  children?: ReactNode;
}) {
  const valueTone = {
    default: 'text-zinc-900 dark:text-zinc-50',
    green: 'text-emerald-700 dark:text-emerald-400',
    amber: 'text-amber-600 dark:text-amber-400',
    red: 'text-red-600 dark:text-red-400',
  }[tone ?? 'default'];
  return (
    <div className={cn('nh-card px-4 py-3', className)}>
      <div className="flex items-center justify-between gap-2 text-xs font-medium text-zinc-500 dark:text-zinc-400">
        <span className="truncate">{label}</span>
        {icon && <span className="text-zinc-400 [&>svg]:h-4 [&>svg]:w-4">{icon}</span>}
      </div>
      <div className={cn('mt-1 truncate text-xl font-semibold tabular tracking-tight', valueTone)}>{value}</div>
      {sub && <div className="mt-0.5 truncate text-xs text-zinc-500 dark:text-zinc-400">{sub}</div>}
      {children}
    </div>
  );
}
