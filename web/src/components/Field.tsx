import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { AlertCircle } from 'lucide-react';
import { ApiError } from '@/api/client';
import { cn } from '@/lib/cn';

// ---------------------------------------------------------------- form errors
//
// A form wraps its content in <FormErrors error={apiError}>. Fields declare the
// server field path they edit (e.g. "bindings[0].host"); when ApiError.field
// matches, the message is shown next to that field. <FormErrorBanner/> shows
// the error at the top only when no mounted field claimed it.

interface Registration {
  path: string;
  prefix: boolean;
}

interface FormErrorsCtx {
  error: ApiError | Error | null;
  register: (r: Registration) => () => void;
  claimed: boolean;
}

const FormErrorsContext = createContext<FormErrorsCtx | null>(null);

export function fieldMatches(path: string, errField: string | undefined, prefix = false): boolean {
  if (!errField) return false;
  if (errField === path) return true;
  if (prefix) return errField.startsWith(path + '[') || errField.startsWith(path + '.');
  return false;
}

export function FormErrors({ error, children }: { error: ApiError | Error | null | undefined; children: ReactNode }) {
  const [regs, setRegs] = useState<Registration[]>([]);
  const register = useCallback((r: Registration) => {
    setRegs((prev) => [...prev, r]);
    return () => setRegs((prev) => prev.filter((x) => x !== r));
  }, []);
  const err = error ?? null;
  const field = err instanceof ApiError ? err.field : undefined;
  const claimed = !!field && regs.some((r) => fieldMatches(r.path, field, r.prefix));
  const value = useMemo(() => ({ error: err, register, claimed }), [err, register, claimed]);
  return <FormErrorsContext.Provider value={value}>{children}</FormErrorsContext.Provider>;
}

/** Returns the server error message for a field path, registering the path as displayable. */
export function useFieldError(path: string | undefined, prefix = false): string | undefined {
  const ctx = useContext(FormErrorsContext);
  const register = ctx?.register;
  useLayoutEffect(() => {
    if (!path || !register) return;
    return register({ path, prefix });
  }, [path, prefix, register]);
  if (!path || !ctx?.error || !(ctx.error instanceof ApiError)) return undefined;
  return fieldMatches(path, ctx.error.field, prefix) ? ctx.error.message : undefined;
}

export function FormErrorBanner({ className }: { className?: string }) {
  const ctx = useContext(FormErrorsContext);
  if (!ctx?.error || ctx.claimed) return null;
  const e = ctx.error;
  const field = e instanceof ApiError ? e.field : undefined;
  return (
    <ErrorBox className={className}>
      {field && <span className="mr-1 font-mono text-xs opacity-80">{field}:</span>}
      {e.message}
    </ErrorBox>
  );
}

export function ErrorBox({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      role="alert"
      className={cn(
        'flex items-start gap-2 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300',
        className,
      )}
    >
      <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
      <div className="min-w-0 break-words">{children}</div>
    </div>
  );
}

// ---------------------------------------------------------------- field

interface FieldState {
  id?: string;
  invalid: boolean;
}

const FieldStateContext = createContext<FieldState>({ invalid: false });

export function useFieldState(): FieldState {
  return useContext(FieldStateContext);
}

export interface FieldProps {
  label?: ReactNode;
  hint?: ReactNode;
  /** Server field path (ApiError.field) this input edits. */
  path?: string;
  /** Also claim errors on nested paths (path[..] / path.x). */
  prefix?: boolean;
  /** Client-side error, shown when there is no server error. */
  error?: string | false | null;
  required?: boolean;
  className?: string;
  children: ReactNode;
  /** Label to the left of the control. */
  inline?: boolean;
  labelAction?: ReactNode;
}

export function Field({ label, hint, path, prefix, error, required, className, children, inline, labelAction }: FieldProps) {
  const id = useId();
  const serverError = useFieldError(path, prefix);
  const message = serverError || error || undefined;
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (serverError) ref.current?.scrollIntoView({ block: 'center', behavior: 'smooth' });
  }, [serverError]);
  const state = useMemo(() => ({ id, invalid: !!message }), [id, message]);
  return (
    <div ref={ref} className={cn(inline ? 'grid grid-cols-[minmax(0,12rem)_1fr] items-start gap-3' : 'space-y-1', className)}>
      {label && (
        <div className={cn('flex items-center justify-between gap-2', inline && 'pt-1.5')}>
          <label htmlFor={id} className="text-[13px] font-medium text-zinc-700 dark:text-zinc-300">
            {label}
            {required && <span className="ml-0.5 text-red-500">*</span>}
          </label>
          {labelAction}
        </div>
      )}
      <div className={cn(inline && 'space-y-1')}>
        <FieldStateContext.Provider value={state}>{children}</FieldStateContext.Provider>
        {message ? (
          <p className="flex items-start gap-1 text-xs text-red-600 dark:text-red-400">
            <AlertCircle className="mt-px h-3.5 w-3.5 shrink-0" />
            <span>{message}</span>
          </p>
        ) : hint ? (
          <p className="text-xs text-zinc-500 dark:text-zinc-400">{hint}</p>
        ) : null}
      </div>
    </div>
  );
}

/** Inline error for a row or group (e.g. "bindings[0]"). */
export function PathError({ path, prefix, className }: { path: string; prefix?: boolean; className?: string }) {
  const msg = useFieldError(path, prefix);
  if (!msg) return null;
  return (
    <p className={cn('flex items-start gap-1 text-xs text-red-600 dark:text-red-400', className)}>
      <AlertCircle className="mt-px h-3.5 w-3.5 shrink-0" />
      <span>{msg}</span>
    </p>
  );
}
