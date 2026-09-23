import { useId, type ReactNode } from 'react';
import { cn } from '@/lib/cn';

export interface SwitchProps {
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
  label?: ReactNode;
  description?: ReactNode;
  size?: 'sm' | 'md';
  className?: string;
  id?: string;
}

export function Switch({ checked, onChange, disabled, label, description, size = 'md', className, id }: SwitchProps) {
  const autoId = useId();
  const sid = id ?? autoId;
  const toggle = (
    <button
      id={sid}
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        'relative inline-flex shrink-0 cursor-pointer items-center rounded-full border border-transparent transition-colors',
        'disabled:cursor-not-allowed disabled:opacity-50',
        size === 'sm' ? 'h-4 w-7' : 'h-5 w-9',
        checked ? 'bg-accent-600 dark:bg-accent-500' : 'bg-zinc-300 dark:bg-zinc-700',
      )}
    >
      <span
        className={cn(
          'inline-block transform rounded-full bg-white shadow ring-0 transition-transform',
          size === 'sm' ? 'h-3 w-3' : 'h-4 w-4',
          checked ? (size === 'sm' ? 'translate-x-3' : 'translate-x-4') : 'translate-x-0.5',
        )}
      />
    </button>
  );
  if (!label && !description) return toggle;
  return (
    <div className={cn('flex items-start gap-3', className)}>
      <div className="pt-0.5">{toggle}</div>
      <div className="min-w-0">
        {label && (
          <label htmlFor={sid} className="cursor-pointer text-[13px] font-medium text-zinc-800 dark:text-zinc-200">
            {label}
          </label>
        )}
        {description && <p className="text-xs text-zinc-500 dark:text-zinc-400">{description}</p>}
      </div>
    </div>
  );
}

export function Checkbox({
  checked,
  onChange,
  label,
  description,
  disabled,
  className,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label?: ReactNode;
  description?: ReactNode;
  disabled?: boolean;
  className?: string;
}) {
  const id = useId();
  return (
    <div className={cn('flex items-start gap-2', className)}>
      <input
        id={id}
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        className="mt-0.5 h-4 w-4 shrink-0 cursor-pointer rounded border-zinc-300 accent-accent-600 disabled:cursor-not-allowed dark:border-zinc-600"
      />
      {(label || description) && (
        <div className="min-w-0">
          {label && (
            <label htmlFor={id} className="cursor-pointer text-[13px] text-zinc-800 dark:text-zinc-200">
              {label}
            </label>
          )}
          {description && <p className="text-xs text-zinc-500 dark:text-zinc-400">{description}</p>}
        </div>
      )}
    </div>
  );
}

export function Radio<V extends string>({
  value,
  onChange,
  options,
  className,
  name,
}: {
  value: V;
  onChange: (v: V) => void;
  options: { value: V; label: ReactNode; description?: ReactNode }[];
  className?: string;
  name?: string;
}) {
  const auto = useId();
  return (
    <div className={cn('flex flex-wrap gap-2', className)} role="radiogroup">
      {options.map((o) => {
        const active = o.value === value;
        return (
          <label
            key={o.value}
            className={cn(
              'flex min-w-0 cursor-pointer items-start gap-2 rounded-md border px-3 py-2 text-[13px] transition-colors',
              active
                ? 'border-accent-600 bg-accent-50 text-accent-900 dark:border-accent-500 dark:bg-accent-500/10 dark:text-accent-100'
                : 'border-zinc-300 hover:border-zinc-400 dark:border-zinc-700 dark:hover:border-zinc-600',
            )}
          >
            <input
              type="radio"
              name={name ?? auto}
              className="mt-0.5 accent-accent-600"
              checked={active}
              onChange={() => onChange(o.value)}
            />
            <span className="min-w-0">
              <span className="font-medium">{o.label}</span>
              {o.description && <span className="block text-xs text-zinc-500 dark:text-zinc-400">{o.description}</span>}
            </span>
          </label>
        );
      })}
    </div>
  );
}
