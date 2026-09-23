import { useRef, useState } from 'react';
import { Eye, EyeOff, RotateCcw } from 'lucide-react';
import { SECRET } from '@/api/types';
import { cn } from '@/lib/cn';
import { useFieldState } from './Field';

/**
 * Input for write-only secrets. When the server returns "__SECRET__" the value
 * is shown as a masked "(unchanged)" placeholder and "__SECRET__" is sent back
 * unless the user types a new value or explicitly clears it.
 */
export function SecretInput({
  value,
  onChange,
  placeholder = 'Not set',
  mono = true,
  disabled,
  className,
  autoComplete = 'new-password',
  allowClear = true,
}: {
  value: string | undefined;
  onChange: (v: string) => void;
  placeholder?: string;
  mono?: boolean;
  disabled?: boolean;
  className?: string;
  autoComplete?: string;
  allowClear?: boolean;
}) {
  const field = useFieldState();
  const had = useRef(value === SECRET);
  if (value === SECRET) had.current = true;
  const [cleared, setCleared] = useState(false);
  const [reveal, setReveal] = useState(false);
  const isSet = value === SECRET;
  const shown = isSet ? '' : (value ?? '');
  const ph = isSet ? '•••••••• (unchanged)' : cleared ? '(will be removed on save)' : placeholder;

  return (
    <div className={cn('relative flex items-center', className)}>
      <input
        id={field.id}
        type={reveal ? 'text' : 'password'}
        value={shown}
        disabled={disabled}
        placeholder={ph}
        autoComplete={autoComplete}
        spellCheck={false}
        aria-invalid={field.invalid || undefined}
        onChange={(e) => {
          const v = e.target.value;
          if (v === '' && had.current && !cleared) onChange(SECRET);
          else onChange(v);
        }}
        className={cn('nh-input pr-16', mono && 'font-mono text-[13px]', field.invalid && 'nh-input-error')}
      />
      <div className="absolute right-1 flex items-center gap-0.5">
        {!isSet && shown && (
          <button
            type="button"
            tabIndex={-1}
            onClick={() => setReveal((r) => !r)}
            className="rounded p-1 text-zinc-400 hover:text-zinc-700 dark:hover:text-zinc-200"
            title={reveal ? 'Hide' : 'Show'}
          >
            {reveal ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
          </button>
        )}
        {allowClear && had.current && !disabled && (isSet ? (
          <button
            type="button"
            onClick={() => {
              setCleared(true);
              onChange('');
            }}
            className="rounded px-1.5 py-0.5 text-2xs font-medium text-zinc-500 hover:bg-zinc-100 hover:text-red-600 dark:hover:bg-zinc-800"
            title="Remove the stored value"
          >
            Clear
          </button>
        ) : (
          <button
            type="button"
            onClick={() => {
              setCleared(false);
              onChange(SECRET);
            }}
            className="rounded p-1 text-zinc-400 hover:text-zinc-700 dark:hover:text-zinc-200"
            title="Keep the stored value"
          >
            <RotateCcw className="h-3.5 w-3.5" />
          </button>
        ))}
      </div>
    </div>
  );
}
