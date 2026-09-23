import { useEffect, useRef, type FormEvent, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { X } from 'lucide-react';
import { cn } from '@/lib/cn';

export interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
  footer?: ReactNode;
  size?: 'sm' | 'md' | 'lg' | 'xl';
  /** Prevent closing by Escape / backdrop / close button. */
  dismissible?: boolean;
  /** Render the body as a <form>; Enter submits. */
  onSubmit?: () => void;
  icon?: ReactNode;
}

const sizes = { sm: 'max-w-md', md: 'max-w-lg', lg: 'max-w-2xl', xl: 'max-w-4xl' };

export function Dialog({ open, onClose, title, description, children, footer, size = 'md', dismissible = true, onSubmit, icon }: DialogProps) {
  const panelRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => {
    if (!open) return;
    const prevFocus = document.activeElement as HTMLElement | null;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && dismissible) {
        e.stopPropagation();
        closeRef.current();
      }
    };
    document.addEventListener('keydown', onKey);
    const t = window.setTimeout(() => {
      const el = panelRef.current?.querySelector<HTMLElement>(
        '[data-autofocus], input:not([type=hidden]):not([disabled]), textarea:not([disabled]), select:not([disabled])',
      );
      (el ?? panelRef.current)?.focus();
    }, 20);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKey);
      window.clearTimeout(t);
      document.body.style.overflow = prevOverflow;
      prevFocus?.focus?.();
    };
  }, [open, dismissible]);

  if (!open) return null;

  const body = (
    <>
      <div className="flex items-start gap-3 border-b border-zinc-200 px-5 py-4 dark:border-zinc-800">
        {icon && <div className="mt-0.5 shrink-0">{icon}</div>}
        <div className="min-w-0 flex-1">
          <h2 className="text-base font-semibold text-zinc-900 dark:text-zinc-50">{title}</h2>
          {description && <p className="mt-0.5 text-[13px] text-zinc-500 dark:text-zinc-400">{description}</p>}
        </div>
        {dismissible && (
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="-mr-1 rounded p-1 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
          >
            <X className="h-4 w-4" />
          </button>
        )}
      </div>
      {children && <div className="max-h-[calc(100vh-12rem)] overflow-y-auto px-5 py-4">{children}</div>}
      {footer && (
        <div className="flex items-center justify-end gap-2 rounded-b-lg border-t border-zinc-200 bg-zinc-50 px-5 py-3 dark:border-zinc-800 dark:bg-zinc-900/60">
          {footer}
        </div>
      )}
    </>
  );

  return createPortal(
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto p-4 pt-[8vh]">
      <div
        className="fixed inset-0 animate-fade-in bg-zinc-950/40 backdrop-blur-[1px] dark:bg-black/60"
        onMouseDown={() => dismissible && onClose()}
      />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        tabIndex={-1}
        className={cn(
          'relative w-full animate-pop-in rounded-lg border border-zinc-200 bg-white shadow-pop outline-none dark:border-zinc-800 dark:bg-zinc-900',
          sizes[size],
        )}
      >
        {onSubmit ? (
          <form
            noValidate
            onSubmit={(e: FormEvent) => {
              e.preventDefault();
              onSubmit();
            }}
          >
            {body}
          </form>
        ) : (
          body
        )}
      </div>
    </div>,
    document.body,
  );
}
