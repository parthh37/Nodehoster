import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, ArrowLeftRight, ArrowRight, Ban } from 'lucide-react';
import { slotsApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { SlotsView, SwapProgress } from '@/api/types';
import { Button } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { ErrorBox } from '@/components/Field';
import { Callout, KV, Loading, Mono } from '@/components/Layout';
import { formatSpan } from '@/lib/format';
import { SlotBadge } from './SlotBits';

function Release({ id }: { id: string | undefined }) {
  return id ? <Mono>{id}</Mono> : <span className="text-zinc-400">the configured application path</span>;
}

/**
 * Confirms swapping a slot into production: loads what the swap would do
 * (blockers disable the confirm button), then starts it. Progress is shown
 * by the slots panel, which polls while the swap runs.
 */
export function SwapDialog({
  siteId,
  slot,
  onClose,
  onStarted,
}: {
  siteId: string;
  /** The slot to swap, null when closed. */
  slot: string | null;
  onClose: () => void;
  onStarted: (p: SwapProgress) => void;
}) {
  const qc = useQueryClient();
  const open = !!slot;
  const preview = useQuery({
    queryKey: qk.swapPreview(siteId, slot ?? ''),
    queryFn: () => slotsApi.swapPreview(siteId, slot!),
    enabled: open,
    staleTime: 0,
    gcTime: 0,
    retry: false,
  });

  const swap = useMutation({
    mutationFn: () => slotsApi.swap(siteId, slot!),
    onSuccess: (p) => {
      qc.setQueryData<SlotsView>(qk.slots(siteId), (v) => (v ? { ...v, swap: p } : v));
      void qc.invalidateQueries({ queryKey: qk.slots(siteId) });
      onStarted(p);
      onClose();
    },
  });

  const close = () => {
    swap.reset();
    onClose();
  };

  const p = preview.data;
  const blockers = p?.blockers ?? [];
  const warnings = p?.warnings ?? [];
  const changes = p?.changes ?? [];

  return (
    <Dialog
      open={open}
      onClose={close}
      size="lg"
      icon={
        <span className="flex h-8 w-8 items-center justify-center rounded-full bg-accent-100 text-accent-700 dark:bg-accent-500/15 dark:text-accent-300">
          <ArrowLeftRight className="h-4 w-4" />
        </span>
      }
      title={<>Swap {slot} into production?</>}
      description="The slot is warmed up with production's settings, then production traffic moves onto its instances at once. Requests in flight finish on the old instances."
      onSubmit={() => p && blockers.length === 0 && swap.mutate()}
      footer={
        <>
          <Button onClick={close}>Cancel</Button>
          <Button
            type="submit"
            variant="primary"
            icon={<ArrowLeftRight className="h-3.5 w-3.5" />}
            loading={swap.isPending}
            disabled={!p || blockers.length > 0 || preview.isFetching}
            title={blockers.length > 0 ? blockers[0] : undefined}
          >
            Swap
          </Button>
        </>
      }
    >
      {preview.isPending ? (
        <Loading label="Checking the slots…" className="py-10" />
      ) : preview.isError ? (
        <ErrorBox>{errorMessage(preview.error)}</ErrorBox>
      ) : (
        p && (
          <div className="space-y-4">
            {swap.isError && <ErrorBox>{errorMessage(swap.error)}</ErrorBox>}
            {blockers.length > 0 && (
              <Callout tone="danger" icon={<Ban />} title="The swap cannot run">
                <ul className="list-disc space-y-0.5 pl-4">
                  {blockers.map((b, i) => (
                    <li key={i}>{b}</li>
                  ))}
                </ul>
              </Callout>
            )}
            <div className="grid gap-2 rounded-lg border border-zinc-200 p-3 text-[13px] dark:border-zinc-800">
              <div className="flex flex-wrap items-center gap-2">
                <SlotBadge name={p.slot} />
                <Release id={p.slotRelease} />
                <ArrowRight className="h-3.5 w-3.5 text-zinc-400" aria-label="goes to" />
                <SlotBadge name="production" />
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <SlotBadge name="production" />
                <Release id={p.productionRelease} />
                <ArrowRight className="h-3.5 w-3.5 text-zinc-400" aria-label="goes to" />
                <SlotBadge name={p.slot} />
              </div>
              <p className="text-xs text-zinc-500">Swapping again swaps them back: that is the rollback.</p>
            </div>
            {changes.length > 0 && (
              <div>
                <h3 className="mb-1.5 text-[13px] font-semibold text-zinc-900 dark:text-zinc-100">What happens</h3>
                <ol className="list-decimal space-y-1 pl-5 text-[13px] text-zinc-700 dark:text-zinc-300">
                  {changes.map((c, i) => (
                    <li key={i}>{c}</li>
                  ))}
                </ol>
              </div>
            )}
            <KV
              className="text-xs"
              items={[
                ['Warm-up paths', <Mono key="p">{(p.warmup.paths ?? []).join('  ') || '/'}</Mono>],
                ['Accepted statuses', <Mono key="s">{p.warmup.statuses}</Mono>],
                ['Warm-up timeout', formatSpan(p.warmup.timeoutSec)],
              ]}
            />
            {warnings.length > 0 && (
              <Callout tone="warning" icon={<AlertTriangle />} title="Before you swap">
                <ul className="list-disc space-y-0.5 pl-4">
                  {warnings.map((w, i) => (
                    <li key={i}>{w}</li>
                  ))}
                </ul>
              </Callout>
            )}
          </div>
        )
      )}
    </Dialog>
  );
}
