import { useEffect, useState, type ReactNode } from 'react';
import { Input, Textarea } from './Input';
import { RowsEditor } from './ListEditor';

interface Pair {
  key: string;
  value: string;
}

function toPairs(r: Record<string, string> | undefined | null): Pair[] {
  return Object.entries(r ?? {}).map(([key, value]) => ({ key, value }));
}

function toRecord(pairs: Pair[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const p of pairs) if (p.key.trim()) out[p.key.trim()] = p.value;
  return out;
}

/**
 * Edits a Record<string,string>. Rows with empty keys are kept locally while
 * being edited but not emitted.
 */
export function KeyValueEditor({
  value,
  onChange,
  keyLabel = 'Key',
  valueLabel = 'Value',
  keyPlaceholder,
  valuePlaceholder,
  multiline,
  addLabel = 'Add',
  empty,
  disabled,
  keyWidth = 'w-40',
  renderValue,
}: {
  value: Record<string, string> | undefined | null;
  onChange: (v: Record<string, string>) => void;
  keyLabel?: string;
  valueLabel?: string;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  multiline?: boolean;
  addLabel?: string;
  empty?: ReactNode;
  disabled?: boolean;
  keyWidth?: string;
  renderValue?: (value: string, onChange: (v: string) => void, key: string) => ReactNode;
}) {
  const [pairs, setPairs] = useState<Pair[]>(() => toPairs(value));

  // Resync when the value changes from outside (e.g. reset).
  useEffect(() => {
    const current = toRecord(pairs);
    if (JSON.stringify(current) !== JSON.stringify(value ?? {})) setPairs(toPairs(value));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);

  const emit = (next: Pair[]) => {
    setPairs(next);
    onChange(toRecord(next));
  };

  return (
    <RowsEditor<Pair>
      items={pairs}
      onChange={emit}
      disabled={disabled}
      addLabel={addLabel}
      empty={empty}
      create={() => ({ key: '', value: '' })}
      header={
        <div className="flex gap-2 pr-8 text-2xs font-semibold uppercase tracking-wide text-zinc-500">
          <span className={keyWidth}>{keyLabel}</span>
          <span className="flex-1">{valueLabel}</span>
        </div>
      }
      render={(p, update) => (
        <div className="flex items-start gap-2">
          <Input className={`${keyWidth} shrink-0`} mono value={p.key} placeholder={keyPlaceholder} onChange={(e) => update({ key: e.target.value })} />
          <div className="min-w-0 flex-1">
            {renderValue ? (
              renderValue(p.value, (v) => update({ value: v }), p.key)
            ) : multiline ? (
              <Textarea mono rows={3} value={p.value} placeholder={valuePlaceholder} onChange={(e) => update({ value: e.target.value })} />
            ) : (
              <Input mono value={p.value} placeholder={valuePlaceholder} onChange={(e) => update({ value: e.target.value })} />
            )}
          </div>
        </div>
      )}
    />
  );
}
