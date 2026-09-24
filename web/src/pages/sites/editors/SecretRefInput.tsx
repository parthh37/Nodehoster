import { useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { CheckCircle2, Loader2, Vault, XCircle } from 'lucide-react';
import { secretStoresApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { SecretRef, SecretStoreStatus, SecretTestResult } from '@/api/types';
import { IconButton } from '@/components/Button';
import { Input, Select } from '@/components/Input';
import { useFieldError } from '@/components/Field';
import { formatRef, refHint, refPlaceholder, refProblem } from '@/lib/secretStores';
import { cn } from '@/lib/cn';

/**
 * The secret stores for pickers. Only administrators may list them, and
 * only administrators edit what references them: for others this stays
 * empty and references show as text.
 */
export function useSecretStores(enabled = true): SecretStoreStatus[] {
  const q = useQuery({ queryKey: qk.secretStores, queryFn: secretStoresApi.status, enabled, retry: false, staleTime: 30_000 });
  return q.data ?? [];
}

/**
 * Picks a store and a secret in it, with a Test that asks the server to
 * resolve the reference (the value is never sent back).
 */
export function SecretRefInput({
  value,
  onChange,
  readOnly,
  path,
  stores,
  compact,
}: {
  value: SecretRef;
  onChange: (r: SecretRef) => void;
  readOnly?: boolean;
  /** Server field path of the reference, e.g. "node.env[2].from". */
  path?: string;
  stores: SecretStoreStatus[];
  /** Inside a table row: no hint line. */
  compact?: boolean;
}) {
  const [result, setResult] = useState<SecretTestResult | null>(null);
  const storeErr = useFieldError(path ? `${path}.store` : undefined);
  const refErr = useFieldError(path ? `${path}.ref` : undefined);
  const store = stores.find((s) => s.name === value.store);
  const problem = value.ref.trim() && store ? refProblem(store.type, value.ref) : null;
  const test = useMutation({
    mutationFn: () => secretStoresApi.resolve({ store: value.store, ref: value.ref.trim() }),
    onSuccess: setResult,
    onError: (e) => setResult({ ok: false, error: errorMessage(e) }),
  });
  const set = (p: Partial<SecretRef>) => {
    setResult(null);
    onChange({ ...value, ...p });
  };

  if (readOnly) {
    return (
      <span className="inline-flex min-w-0 items-center gap-1.5 font-mono text-[13px] text-zinc-600 dark:text-zinc-300" title="Read from a secret store when the process starts">
        <Vault className="h-3.5 w-3.5 shrink-0 text-violet-500" />
        <span className="truncate">{formatRef(value)}</span>
      </span>
    );
  }
  const options = stores.map((s) => ({ value: s.name, label: s.name }));
  if (value.store && !store) options.unshift({ value: value.store, label: `${value.store} (missing)` });
  const err = storeErr || refErr || problem || (value.store && stores.length > 0 && !store ? `No secret store is named ${value.store}` : null);

  return (
    <div className="min-w-0">
      <div className="flex min-w-0 items-center gap-1.5">
        <Select
          className="w-36 shrink-0"
          mono
          value={value.store}
          placeholder={stores.length ? 'Store…' : 'No stores'}
          options={options}
          invalid={!!storeErr}
          onChange={(v) => set({ store: v })}
        />
        <Input
          mono
          className="min-w-0 flex-1"
          value={value.ref}
          invalid={!!(refErr || problem)}
          placeholder={refPlaceholder(store?.type)}
          title={refHint(store?.type)}
          onChange={(e) => set({ ref: e.target.value })}
        />
        <IconButton
          label="Test: resolve this reference without showing the value"
          icon={test.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : result?.ok ? <CheckCircle2 className="h-3.5 w-3.5 text-emerald-600" /> : result ? <XCircle className="h-3.5 w-3.5 text-red-600" /> : <CheckCircle2 className="h-3.5 w-3.5" />}
          disabled={!value.store || !value.ref.trim() || !!problem || test.isPending}
          onClick={() => test.mutate()}
        />
      </div>
      {(err || result) && (
        <p className={cn('mt-1 text-xs', err || !result?.ok ? 'text-red-600 dark:text-red-400' : 'text-emerald-700 dark:text-emerald-400')}>
          {err || (result?.ok ? 'Resolves. The value is read when the process starts and never shown.' : result?.error)}
        </p>
      )}
      {!compact && !err && !result && store && <p className="mt-1 text-xs text-zinc-500 dark:text-zinc-400">{refHint(store.type)}</p>}
      {!compact && stores.length === 0 && <p className="mt-1 text-xs text-zinc-500 dark:text-zinc-400">Add a store in Settings → Secret stores first.</p>}
    </div>
  );
}
