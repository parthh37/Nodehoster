import { useEffect, useRef, useState, type ReactNode } from 'react';
import { cn } from '@/lib/cn';

export interface MenuItem {
  label: ReactNode;
  icon?: ReactNode;
  onSelect: () => void;
  danger?: boolean;
  disabled?: boolean;
  hidden?: boolean;
}

/** Minimal dropdown menu. `items` may contain `'separator'`. */
export function Menu({
  trigger,
  items,
  align = 'right',
  header,
  className,
}: {
  trigger: (props: { onClick: () => void; 'aria-expanded': boolean }) => ReactNode;
  items: Array<MenuItem | 'separator'>;
  align?: 'left' | 'right';
  header?: ReactNode;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false);
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const visible = items.filter((i) => i === 'separator' || !i.hidden);

  return (
    <div ref={ref} className={cn('relative inline-block', className)} onClick={(e) => e.stopPropagation()}>
      {trigger({ onClick: () => setOpen((o) => !o), 'aria-expanded': open })}
      {open && (
        <div
          role="menu"
          className={cn(
            'absolute z-40 mt-1 min-w-[12rem] animate-pop-in rounded-md border border-zinc-200 bg-white py-1 shadow-pop dark:border-zinc-700 dark:bg-zinc-900',
            align === 'right' ? 'right-0' : 'left-0',
          )}
        >
          {header && <div className="border-b border-zinc-200 px-3 py-2 dark:border-zinc-800">{header}</div>}
          {visible.map((item, i) =>
            item === 'separator' ? (
              <div key={`sep-${i}`} className="my-1 border-t border-zinc-200 dark:border-zinc-800" />
            ) : (
              <button
                key={i}
                type="button"
                role="menuitem"
                disabled={item.disabled}
                onClick={() => {
                  setOpen(false);
                  item.onSelect();
                }}
                className={cn(
                  'flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] disabled:cursor-not-allowed disabled:opacity-50',
                  item.danger
                    ? 'text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-500/10'
                    : 'text-zinc-700 hover:bg-zinc-100 dark:text-zinc-200 dark:hover:bg-zinc-800',
                )}
              >
                {item.icon && <span className="text-zinc-400 [&>svg]:h-4 [&>svg]:w-4">{item.icon}</span>}
                {item.label}
              </button>
            ),
          )}
        </div>
      )}
    </div>
  );
}
