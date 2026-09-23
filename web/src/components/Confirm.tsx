import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react';
import { AlertTriangle } from 'lucide-react';
import { Button } from './Button';
import { Dialog } from './Dialog';
import { Checkbox } from './Switch';

export interface ConfirmOptions {
  title: ReactNode;
  message?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  /** Optional checkbox; its value is returned as `checked`. */
  checkbox?: { label: ReactNode; description?: ReactNode; defaultChecked?: boolean };
  /** Require typing this text to confirm. */
  typeToConfirm?: string;
}

export interface ConfirmResult {
  ok: boolean;
  checked: boolean;
}

type ConfirmFn = (o: ConfirmOptions) => Promise<ConfirmResult>;

const ConfirmContext = createContext<ConfirmFn | null>(null);

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [opts, setOpts] = useState<ConfirmOptions | null>(null);
  const [checked, setChecked] = useState(false);
  const [typed, setTyped] = useState('');
  const resolver = useRef<((r: ConfirmResult) => void) | null>(null);

  const confirm = useCallback<ConfirmFn>((o) => {
    setOpts(o);
    setChecked(!!o.checkbox?.defaultChecked);
    setTyped('');
    return new Promise<ConfirmResult>((resolve) => {
      resolver.current = resolve;
    });
  }, []);

  const close = (ok: boolean) => {
    resolver.current?.({ ok, checked });
    resolver.current = null;
    setOpts(null);
  };

  const blocked = !!opts?.typeToConfirm && typed !== opts.typeToConfirm;

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      <Dialog
        open={!!opts}
        onClose={() => close(false)}
        size="sm"
        title={opts?.title}
        icon={
          opts?.danger ? (
            <span className="flex h-8 w-8 items-center justify-center rounded-full bg-red-100 text-red-600 dark:bg-red-500/15 dark:text-red-400">
              <AlertTriangle className="h-4 w-4" />
            </span>
          ) : undefined
        }
        onSubmit={() => !blocked && close(true)}
        footer={
          <>
            <Button variant="secondary" onClick={() => close(false)}>
              {opts?.cancelLabel ?? 'Cancel'}
            </Button>
            <Button type="submit" variant={opts?.danger ? 'danger' : 'primary'} disabled={blocked} data-autofocus>
              {opts?.confirmLabel ?? 'Confirm'}
            </Button>
          </>
        }
      >
        {(opts?.message || opts?.checkbox || opts?.typeToConfirm) && (
          <div className="space-y-3 text-[13px] text-zinc-600 dark:text-zinc-300">
            {opts?.message && <div>{opts.message}</div>}
            {opts?.checkbox && (
              <Checkbox checked={checked} onChange={setChecked} label={opts.checkbox.label} description={opts.checkbox.description} />
            )}
            {opts?.typeToConfirm && (
              <div className="space-y-1">
                <p>
                  Type <span className="font-mono font-semibold text-zinc-900 dark:text-zinc-100">{opts.typeToConfirm}</span> to
                  confirm.
                </p>
                <input className="nh-input font-mono" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
              </div>
            )}
          </div>
        )}
      </Dialog>
    </ConfirmContext.Provider>
  );
}

export function useConfirm(): ConfirmFn {
  const ctx = useContext(ConfirmContext);
  if (!ctx) throw new Error('useConfirm outside ConfirmProvider');
  return ctx;
}
