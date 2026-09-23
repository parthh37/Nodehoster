import { forwardRef, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react';
import { ChevronDown } from 'lucide-react';
import { cn } from '@/lib/cn';
import { useFieldState } from './Field';

export interface InputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'prefix'> {
  mono?: boolean;
  invalid?: boolean;
  prefix?: ReactNode;
  suffix?: ReactNode;
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { mono, invalid, prefix, suffix, className, id, ...rest },
  ref,
) {
  const field = useFieldState();
  const bad = invalid ?? field.invalid;
  const input = (
    <input
      ref={ref}
      id={id ?? field.id}
      aria-invalid={bad || undefined}
      className={cn(
        'nh-input',
        mono && 'font-mono text-[13px]',
        bad && 'nh-input-error',
        prefix ? 'pl-8' : '',
        suffix ? 'pr-12' : '',
        !prefix && !suffix && className,
      )}
      {...rest}
    />
  );
  if (!prefix && !suffix) return input;
  return (
    <div className={cn('relative', className)}>
      {prefix && (
        <span className="pointer-events-none absolute inset-y-0 left-0 flex items-center pl-2.5 text-zinc-400">{prefix}</span>
      )}
      {input}
      {suffix && (
        <span className="pointer-events-none absolute inset-y-0 right-0 flex items-center pr-2.5 text-xs text-zinc-500">
          {suffix}
        </span>
      )}
    </div>
  );
});

export interface NumberInputProps extends Omit<InputProps, 'value' | 'onChange' | 'type'> {
  value: number | undefined | null;
  onChange: (v: number) => void;
  /** Show empty instead of 0. */
  blankZero?: boolean;
  float?: boolean;
}

export function NumberInput({ value, onChange, blankZero, float, ...rest }: NumberInputProps) {
  const shown = value === undefined || value === null || (blankZero && value === 0) ? '' : String(value);
  return (
    <Input
      type="number"
      inputMode={float ? 'decimal' : 'numeric'}
      value={shown}
      onChange={(e) => {
        const raw = e.target.value;
        if (raw === '') return onChange(0);
        const n = float ? parseFloat(raw) : parseInt(raw, 10);
        onChange(Number.isFinite(n) ? n : 0);
      }}
      {...rest}
    />
  );
}

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  mono?: boolean;
  invalid?: boolean;
}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { mono, invalid, className, id, rows = 4, ...rest },
  ref,
) {
  const field = useFieldState();
  const bad = invalid ?? field.invalid;
  return (
    <textarea
      ref={ref}
      id={id ?? field.id}
      rows={rows}
      aria-invalid={bad || undefined}
      className={cn('nh-input resize-y', mono && 'font-mono text-[13px] leading-5', bad && 'nh-input-error', className)}
      {...rest}
    />
  );
});

export interface SelectOption<V extends string | number = string> {
  value: V;
  label: string;
  disabled?: boolean;
}

export interface SelectProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, 'onChange' | 'value'> {
  value: string | number;
  onChange: (value: string) => void;
  options: SelectOption<string | number>[];
  invalid?: boolean;
  mono?: boolean;
  placeholder?: string;
}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { value, onChange, options, invalid, mono, className, id, placeholder, ...rest },
  ref,
) {
  const field = useFieldState();
  const bad = invalid ?? field.invalid;
  return (
    <div className={cn('relative', className)}>
      <select
        ref={ref}
        id={id ?? field.id}
        value={String(value)}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={bad || undefined}
        className={cn('nh-input appearance-none pr-8', mono && 'font-mono text-[13px]', bad && 'nh-input-error')}
        {...rest}
      >
        {placeholder !== undefined && (
          <option value="" disabled>
            {placeholder}
          </option>
        )}
        {options.map((o) => (
          <option key={String(o.value)} value={String(o.value)} disabled={o.disabled}>
            {o.label}
          </option>
        ))}
      </select>
      <ChevronDown className="pointer-events-none absolute right-2 top-1/2 h-4 w-4 -translate-y-1/2 text-zinc-400" />
    </div>
  );
});
