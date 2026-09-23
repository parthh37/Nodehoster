import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { AlertCircle, CheckCircle2, Info, X } from 'lucide-react';
import { errorMessage } from '@/api/client';
import { cn } from '@/lib/cn';

type Kind = 'success' | 'error' | 'info';

interface ToastItem {
  id: number;
  kind: Kind;
  title: string;
  message?: string;
}

interface ToastApi {
  success: (title: string, message?: string) => void;
  error: (title: string, err?: unknown) => void;
  info: (title: string, message?: string) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const seq = useRef(0);

  const dismiss = useCallback((id: number) => setItems((l) => l.filter((t) => t.id !== id)), []);

  const push = useCallback(
    (kind: Kind, title: string, message?: string) => {
      const id = ++seq.current;
      setItems((l) => [...l.slice(-4), { id, kind, title, message }]);
      window.setTimeout(() => dismiss(id), kind === 'error' ? 8000 : 4000);
    },
    [dismiss],
  );

  const api = useMemo<ToastApi>(
    () => ({
      success: (t, m) => push('success', t, m),
      info: (t, m) => push('info', t, m),
      error: (t, e) => push('error', t, e === undefined ? undefined : errorMessage(e)),
    }),
    [push],
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      {createPortal(
        <div className="pointer-events-none fixed right-4 top-16 z-[60] flex w-[22rem] max-w-[calc(100vw-2rem)] flex-col gap-2">
          {items.map((t) => (
            <div
              key={t.id}
              role="status"
              className={cn(
                'pointer-events-auto flex animate-slide-in items-start gap-2.5 rounded-lg border bg-white px-3 py-2.5 shadow-pop dark:bg-zinc-900',
                t.kind === 'error' ? 'border-red-200 dark:border-red-500/30' : 'border-zinc-200 dark:border-zinc-700',
              )}
            >
              {t.kind === 'success' && <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-emerald-600 dark:text-emerald-400" />}
              {t.kind === 'error' && <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-red-600 dark:text-red-400" />}
              {t.kind === 'info' && <Info className="mt-0.5 h-4 w-4 shrink-0 text-accent-600 dark:text-accent-400" />}
              <div className="min-w-0 flex-1">
                <p className="text-[13px] font-medium text-zinc-900 dark:text-zinc-100">{t.title}</p>
                {t.message && <p className="mt-0.5 break-words text-xs text-zinc-600 dark:text-zinc-400">{t.message}</p>}
              </div>
              <button
                type="button"
                onClick={() => dismiss(t.id)}
                className="rounded p-0.5 text-zinc-400 hover:text-zinc-700 dark:hover:text-zinc-200"
                aria-label="Dismiss"
              >
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
          ))}
        </div>,
        document.body,
      )}
    </ToastContext.Provider>
  );
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error('useToast outside ToastProvider');
  return ctx;
}
