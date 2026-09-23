import { useState, type ReactNode } from 'react';
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react';
import { Button, IconButton } from './Button';
import { Input } from './Input';
import { PathError, useFieldError } from './Field';
import { cn } from '@/lib/cn';

/** Editable list of strings. Errors on `${path}[i]` are shown next to the item. */
export function ListEditor({
  values,
  onChange,
  placeholder,
  addLabel = 'Add',
  mono = true,
  path,
  validate,
  emptyText,
  disabled,
  className,
}: {
  values: string[] | undefined | null;
  onChange: (v: string[]) => void;
  placeholder?: string;
  addLabel?: string;
  mono?: boolean;
  path?: string;
  validate?: (v: string) => string | null | undefined;
  emptyText?: ReactNode;
  disabled?: boolean;
  className?: string;
}) {
  const list = values ?? [];
  const [draft, setDraft] = useState('');
  const draftErr = draft.trim() && validate ? validate(draft.trim()) : null;

  const add = () => {
    const parts = draft
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    if (!parts.length) return;
    if (validate && parts.some((p) => validate(p))) return;
    onChange([...list, ...parts.filter((p) => !list.includes(p))]);
    setDraft('');
  };

  return (
    <div className={cn('space-y-1.5', className)}>
      {list.length === 0 && emptyText && <p className="text-xs text-zinc-500 dark:text-zinc-400">{emptyText}</p>}
      {list.map((v, i) => (
        <ListItem
          key={i}
          value={v}
          mono={mono}
          path={path ? `${path}[${i}]` : undefined}
          clientError={validate ? validate(v) : null}
          disabled={disabled}
          onChange={(nv) => onChange(list.map((x, j) => (j === i ? nv : x)))}
          onRemove={() => onChange(list.filter((_, j) => j !== i))}
        />
      ))}
      {!disabled && (
        <div className="flex items-start gap-1.5">
          <div className="min-w-0 flex-1">
            <Input
              value={draft}
              mono={mono}
              placeholder={placeholder}
              invalid={!!draftErr}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  add();
                }
              }}
              onBlur={() => {
                if (draft.trim() && !draftErr) add();
              }}
            />
            {draftErr && <p className="mt-1 text-xs text-red-600 dark:text-red-400">{draftErr}</p>}
          </div>
          <Button variant="secondary" icon={<Plus className="h-3.5 w-3.5" />} onClick={add} disabled={!draft.trim() || !!draftErr}>
            {addLabel}
          </Button>
        </div>
      )}
      {path && <PathError path={path} />}
    </div>
  );
}

function ListItem({
  value,
  onChange,
  onRemove,
  mono,
  path,
  clientError,
  disabled,
}: {
  value: string;
  onChange: (v: string) => void;
  onRemove: () => void;
  mono: boolean;
  path?: string;
  clientError?: string | null;
  disabled?: boolean;
}) {
  const serverErr = useFieldError(path);
  const err = serverErr || clientError;
  return (
    <div>
      <div className="flex items-center gap-1.5">
        <Input value={value} mono={mono} invalid={!!err} onChange={(e) => onChange(e.target.value)} className="flex-1" disabled={disabled} />
        {!disabled && <IconButton label="Remove" icon={<Trash2 className="h-3.5 w-3.5" />} onClick={onRemove} size="md" />}
      </div>
      {err && <p className="mt-1 text-xs text-red-600 dark:text-red-400">{err}</p>}
    </div>
  );
}

/**
 * Generic ordered editor for arrays of objects. Renders a row per item via
 * `render`, with remove and optional move up/down controls.
 */
export function RowsEditor<T>({
  items,
  onChange,
  render,
  create,
  addLabel = 'Add',
  orderable,
  empty,
  header,
  disabled,
  rowClassName,
  path,
}: {
  items: T[] | undefined | null;
  onChange: (items: T[]) => void;
  render: (item: T, update: (patch: Partial<T>) => void, index: number) => ReactNode;
  create: () => T;
  addLabel?: string;
  orderable?: boolean;
  empty?: ReactNode;
  header?: ReactNode;
  disabled?: boolean;
  rowClassName?: string;
  /** Path of the array for row-level errors, e.g. "routing.rewrites". */
  path?: string;
}) {
  const list = items ?? [];
  const update = (i: number, patch: Partial<T>) => onChange(list.map((x, j) => (j === i ? { ...x, ...patch } : x)));
  const move = (i: number, d: number) => {
    const j = i + d;
    if (j < 0 || j >= list.length) return;
    const next = list.slice();
    [next[i], next[j]] = [next[j], next[i]];
    onChange(next);
  };
  return (
    <div className="space-y-2">
      {list.length === 0 && empty}
      {list.length > 0 && header}
      {list.map((item, i) => (
        <div key={i} className={cn('group flex items-start gap-2', rowClassName)}>
          <div className="min-w-0 flex-1">
            {render(item, (p) => update(i, p), i)}
            {path && <PathError path={`${path}[${i}]`} className="mt-1" />}
          </div>
          {!disabled && (
            <div className="flex shrink-0 items-center gap-0.5 pt-0.5">
              {orderable && (
                <>
                  <IconButton label="Move up" icon={<ArrowUp className="h-3.5 w-3.5" />} disabled={i === 0} onClick={() => move(i, -1)} />
                  <IconButton
                    label="Move down"
                    icon={<ArrowDown className="h-3.5 w-3.5" />}
                    disabled={i === list.length - 1}
                    onClick={() => move(i, 1)}
                  />
                </>
              )}
              <IconButton
                label="Remove"
                variant="danger-ghost"
                icon={<Trash2 className="h-3.5 w-3.5" />}
                onClick={() => onChange(list.filter((_, j) => j !== i))}
              />
            </div>
          )}
        </div>
      ))}
      {!disabled && (
        <Button variant="secondary" size="sm" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => onChange([...list, create()])}>
          {addLabel}
        </Button>
      )}
    </div>
  );
}
