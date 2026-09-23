import type { HTMLAttributes, ReactNode, TdHTMLAttributes, ThHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

export function Table({ children, className, dense }: { children: ReactNode; className?: string; dense?: boolean }) {
  return (
    <div className={cn('scrollbar-thin overflow-x-auto', className)}>
      <table className={cn('w-full border-collapse text-left text-[13px]', dense && '[&_td]:py-1.5')}>{children}</table>
    </div>
  );
}

export function THead({ children }: { children: ReactNode }) {
  return (
    <thead className="border-b border-zinc-200 bg-zinc-50/80 text-2xs font-semibold uppercase tracking-wide text-zinc-500 dark:border-zinc-800 dark:bg-zinc-900/60 dark:text-zinc-400">
      {children}
    </thead>
  );
}

export function TBody({ children }: { children: ReactNode }) {
  return <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800/80">{children}</tbody>;
}

export function Th({ className, children, ...rest }: ThHTMLAttributes<HTMLTableCellElement>) {
  return (
    <th className={cn('whitespace-nowrap px-3 py-2 font-semibold first:pl-4 last:pr-4', className)} {...rest}>
      {children}
    </th>
  );
}

export function Td({ className, children, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return (
    <td className={cn('px-3 py-2 align-middle first:pl-4 last:pr-4', className)} {...rest}>
      {children}
    </td>
  );
}

export function Tr({ className, children, onClick, ...rest }: HTMLAttributes<HTMLTableRowElement>) {
  return (
    <tr
      className={cn(
        'transition-colors hover:bg-zinc-50 dark:hover:bg-zinc-800/40',
        onClick && 'cursor-pointer',
        className,
      )}
      onClick={onClick}
      {...rest}
    >
      {children}
    </tr>
  );
}

/** A full-width row used for loading / empty messages inside a table. */
export function TableMessage({ colSpan, children }: { colSpan: number; children: ReactNode }) {
  return (
    <tr>
      <td colSpan={colSpan} className="px-4 py-8 text-center text-[13px] text-zinc-500 dark:text-zinc-400">
        {children}
      </td>
    </tr>
  );
}
