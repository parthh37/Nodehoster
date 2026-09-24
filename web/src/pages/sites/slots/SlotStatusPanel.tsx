import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeftRight, CheckCircle2, Circle, Layers, Loader2, Play, RefreshCw, Square, XCircle, Zap } from 'lucide-react';
import { sitesApi, slotsApi, type SlotAction } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { Deployment, Site, SiteState, SlotStatus, SlotsView, SwapProgress, SwapResult } from '@/api/types';
import { Badge } from '@/components/Badge';
import { Button } from '@/components/Button';
import { ErrorBox } from '@/components/Field';
import { Callout, Card, KV, Loading, Mono, ProgressBar } from '@/components/Layout';
import { StateBadge } from '@/components/StatusBadges';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { useSitePermissions } from '@/hooks/useAuth';
import { useNow } from '@/hooks/useNow';
import { durationBetween, formatCompact, formatDateTime, formatNumber, relativeTime } from '@/lib/format';
import { finishedSwap, instanceCounts, PRODUCTION, releaseIs, swapPhaseDescription, swapPhaseLabel, SWAP_PHASES, swapStepIndex } from '@/lib/slots';
import { cn } from '@/lib/cn';
import { BindingLink } from '../shared';
import { SwapDialog } from './SwapDialog';

const canStart = (s: SiteState | undefined) => !s || s === 'stopped' || s === 'failed';
const canStop = (s: SiteState | undefined) => !!s && s !== 'stopped';

const verbs: Record<SlotAction, string> = { start: 'started', stop: 'stopped', recycle: 'recycled' };

/**
 * Live state of production and every saved slot, with start/stop/recycle
 * and swapping for operators. Polls every few seconds, every second while
 * a swap runs, and says how a swap it watched ended.
 */
export function SlotStatusPanel({ site }: { site: Site }) {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const { canOperate } = useSitePermissions(site.id);
  const now = useNow(1000);
  const [swapFor, setSwapFor] = useState<string | null>(null);
  const worker = site.type === 'worker';

  const q = useQuery({
    queryKey: qk.slots(site.id),
    queryFn: () => slotsApi.list(site.id),
    refetchInterval: (query) => (query.state.data?.swap ? 1000 : 5000),
  });
  // Shares the Deployments tab's cache: labels the releases.
  const deps = useQuery({ queryKey: qk.deployments(site.id), queryFn: () => sitesApi.deployments(site.id), staleTime: 15_000 });

  const view = q.data;
  const swap = view?.swap ?? null;

  // A swap this page watched has ended: say how, and pick up the new releases.
  const watching = useRef<SwapProgress | null>(null);
  useEffect(() => {
    if (!view) return;
    const done = finishedSwap(watching.current, view);
    if (done) {
      if (done.succeeded) toast.success(`${done.slot} swapped into production`, done.message || undefined);
      else toast.error(`Swap of ${done.slot} failed`, done.message || 'The swap did not complete.');
      void qc.invalidateQueries({ queryKey: qk.site(site.id), exact: true });
      void qc.invalidateQueries({ queryKey: qk.deployments(site.id) });
    }
    watching.current = view.swap ?? null;
  }, [view, qc, site.id, toast]);

  const action = useMutation({
    mutationFn: ({ slot, action }: { slot: string; action: SlotAction }) => slotsApi.action(site.id, slot, action),
    onSuccess: (v, { slot, action }) => {
      qc.setQueryData<SlotsView>(qk.slots(site.id), v);
      toast.success(`${slot === PRODUCTION ? site.name : slot} ${verbs[action]}`);
      if (slot === PRODUCTION) {
        void qc.invalidateQueries({ queryKey: qk.site(site.id), exact: true });
        void qc.invalidateQueries({ queryKey: qk.sites, exact: true });
      }
    },
    onError: (e, { slot, action }) => toast.error(`Could not ${action} ${slot}`, e),
  });

  const run = async (slot: string, a: SlotAction) => {
    if (a === 'stop') {
      const prod = slot === PRODUCTION;
      const r = await confirm({
        title: prod ? `Stop ${site.name}?` : `Stop ${slot}?`,
        message: prod
          ? 'Production stops answering requests on its bindings until it is started again. Other slots keep running.'
          : `The ${slot} slot stops answering on its bindings. Production is not affected.`,
        confirmLabel: prod ? 'Stop production' : 'Stop slot',
        danger: true,
      });
      if (!r.ok) return;
    }
    action.mutate({ slot, action: a });
  };

  if (q.isPending) return <Loading />;
  if (q.isError) return <ErrorBox>{errorMessage(q.error)}</ErrorBox>;

  const slots = view?.slots ?? [];
  const busy = (slot: string) => action.isPending && action.variables?.slot === slot;
  const findDep = (release: string | undefined) => (release ? (deps.data ?? []).find((d) => releaseIs(release, d)) : undefined);

  return (
    <div className="space-y-4">
      {swap && <SwapProgressCard swap={swap} now={now} />}
      {!swap && view?.lastSwap && <LastSwap result={view.lastSwap} now={now} />}
      <div className="grid gap-4 lg:grid-cols-2">
        {slots.map((s) => (
          <SlotCard
            key={s.name}
            siteId={site.id}
            slot={s}
            dep={findDep(s.release)}
            worker={worker}
            now={now}
            actions={
              canOperate && (
                <div className="flex flex-wrap items-center gap-1.5">
                  {canStart(s.status.state) ? (
                    <Button
                      size="sm"
                      icon={<Play className="h-3.5 w-3.5" />}
                      loading={busy(s.name) && action.variables?.action === 'start'}
                      disabled={busy(s.name) || !!swap || (s.name !== PRODUCTION && !s.release)}
                      title={s.name !== PRODUCTION && !s.release ? 'Deploy to this slot first' : undefined}
                      onClick={() => run(s.name, 'start')}
                    >
                      Start
                    </Button>
                  ) : (
                    canStop(s.status.state) && (
                      <Button
                        size="sm"
                        icon={<Square className="h-3.5 w-3.5" />}
                        loading={busy(s.name) && action.variables?.action === 'stop'}
                        disabled={busy(s.name) || !!swap}
                        onClick={() => run(s.name, 'stop')}
                      >
                        Stop
                      </Button>
                    )
                  )}
                  <Button
                    size="sm"
                    icon={<RefreshCw className="h-3.5 w-3.5" />}
                    loading={busy(s.name) && action.variables?.action === 'recycle'}
                    disabled={busy(s.name) || !!swap || canStart(s.status.state)}
                    title="Zero-downtime rolling restart"
                    onClick={() => run(s.name, 'recycle')}
                  >
                    Recycle
                  </Button>
                  {s.name !== PRODUCTION && (
                    <Button
                      size="sm"
                      variant="primary"
                      className="ml-auto"
                      icon={<ArrowLeftRight className="h-3.5 w-3.5" />}
                      disabled={!!swap}
                      title={swap ? 'A swap is in progress' : `Warm ${s.name} up and move production traffic onto it`}
                      onClick={() => setSwapFor(s.name)}
                    >
                      Swap into production
                    </Button>
                  )}
                </div>
              )
            }
          />
        ))}
      </div>
      <SwapDialog siteId={site.id} slot={swapFor} onClose={() => setSwapFor(null)} onStarted={(p) => (watching.current = p)} />
    </div>
  );
}

function releaseText(release: string | undefined, dep: Deployment | undefined, now: number, production: boolean) {
  if (!release) return <span className="text-zinc-400">{production ? 'The configured application path' : 'Nothing deployed yet'}</span>;
  return (
    <span className="flex min-w-0 flex-wrap items-center gap-x-2">
      <Mono className="truncate" title={release}>
        {dep?.commit ? dep.commit.slice(0, 8) : release}
      </Mono>
      {dep?.message && (
        <span className="truncate text-xs text-zinc-500" title={dep.message}>
          {dep.message}
        </span>
      )}
      {dep && (
        <span className="text-xs text-zinc-500" title={formatDateTime(dep.startedAt)}>
          {relativeTime(dep.startedAt, now)}
        </span>
      )}
    </span>
  );
}

function SlotCard({
  siteId,
  slot,
  dep,
  worker,
  now,
  actions,
}: {
  siteId: string;
  slot: SlotStatus;
  dep: Deployment | undefined;
  worker: boolean;
  now: number;
  actions: ReactNode;
}) {
  const production = slot.name === PRODUCTION;
  const { ready, total } = instanceCounts(slot.status);
  const t = slot.status.traffic;
  const bindings = slot.bindings ?? [];
  const items: [ReactNode, ReactNode][] = [
    [
      'Release',
      <Link key="r" to={`/sites/${siteId}/deployments`} className="block min-w-0 hover:underline">
        {releaseText(slot.release, dep, now, production)}
      </Link>,
    ],
    [
      'Instances',
      <span key="i" className={cn('tabular', total > 0 && ready < total && 'text-amber-600 dark:text-amber-400')}>
        {ready}/{total} ready
      </span>,
    ],
  ];
  if (!worker) {
    items.push([
      'Traffic',
      <span key="t" className="tabular">
        {formatNumber(t?.rps ?? 0, 2)} req/s · {formatCompact(t?.requests ?? 0)} requests
      </span>,
    ]);
    items.push([
      'Bindings',
      bindings.length === 0 ? (
        <span key="b" className="text-xs text-amber-600 dark:text-amber-400">
          {production ? 'None: production is unreachable' : 'None: add a binding for this slot on the Bindings tab'}
        </span>
      ) : (
        <div key="b" className="flex flex-col gap-0.5">
          {bindings.map((b, i) => (
            <BindingLink key={b.id || i} b={b} hideSlot />
          ))}
        </div>
      ),
    ]);
  }
  return (
    <Card
      className={cn(production && 'border-accent-300 dark:border-accent-500/40')}
      title={
        <span className="flex flex-wrap items-center gap-2">
          <Layers className="h-4 w-4 text-zinc-400" />
          {slot.name}
          <StateBadge state={slot.status.state} title={slot.status.message} />
          {slot.autoSwap && (
            <Badge tone="blue" title="Swaps into production after each successful deployment to this slot">
              <Zap className="h-3 w-3" />
              auto-swap
            </Badge>
          )}
        </span>
      }
      description={production ? 'Serves the site’s production bindings.' : 'Runs its own release on its own instances.'}
      footer={actions || undefined}
    >
      {slot.status.message && slot.status.state !== 'running' && <p className="mb-2 text-xs text-zinc-500">{slot.status.message}</p>}
      <KV items={items} />
    </Card>
  );
}

function SwapProgressCard({ swap, now }: { swap: SwapProgress; now: number }) {
  const step = swapStepIndex(swap.phase);
  return (
    <Card
      className="border-accent-300 dark:border-accent-500/40"
      title={
        <span className="flex items-center gap-2">
          <Loader2 className="h-4 w-4 animate-spin text-accent-600 dark:text-accent-400" />
          Swapping {swap.slot} into production
        </span>
      }
      description={
        <>
          {swap.auto ? 'Auto-swap after a deployment' : swap.user ? `Started by ${swap.user}` : 'Started'} · {durationBetween(swap.startedAt, null, now)}
        </>
      }
    >
      <ol className="grid gap-3 sm:grid-cols-3" aria-label="Swap progress">
        {SWAP_PHASES.map((p, i) => {
          const done = step > i;
          const current = step === i;
          return (
            <li key={p} className="flex items-start gap-2" aria-current={current ? 'step' : undefined}>
              {done ? (
                <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-emerald-600 dark:text-emerald-400" />
              ) : current ? (
                <Loader2 className="mt-0.5 h-4 w-4 shrink-0 animate-spin text-accent-600 dark:text-accent-400" />
              ) : (
                <Circle className="mt-0.5 h-4 w-4 shrink-0 text-zinc-300 dark:text-zinc-600" />
              )}
              <div className="min-w-0">
                <p className={cn('text-[13px] font-medium', !done && !current && 'text-zinc-400 dark:text-zinc-500')}>{swapPhaseLabel(p)}</p>
                <p className="text-xs text-zinc-500">{swapPhaseDescription(p)}</p>
              </div>
            </li>
          );
        })}
      </ol>
      <ProgressBar className="mt-3" value={0} indeterminate />
      {swap.message && <p className="mt-2 text-xs text-zinc-600 dark:text-zinc-300">{swap.message}</p>}
    </Card>
  );
}

function LastSwap({ result: r, now }: { result: SwapResult; now: number }) {
  return (
    <Callout
      tone={r.succeeded ? 'success' : 'danger'}
      icon={r.succeeded ? <CheckCircle2 /> : <XCircle />}
      title={r.succeeded ? `Last swap: ${r.slot} into production` : `Last swap of ${r.slot} failed`}
    >
      {r.message && <p>{r.message}</p>}
      <p className="mt-0.5 text-xs">
        <span title={formatDateTime(r.finishedAt)}>{relativeTime(r.finishedAt, now)}</span> · took {durationBetween(r.startedAt, r.finishedAt, now)} ·{' '}
        {r.auto ? 'auto-swap after a deployment' : r.user ? `by ${r.user}` : 'by NodeHoster'}
        {r.productionRelease && (
          <>
            {' '}
            · production runs <span className="font-mono">{r.productionRelease}</span>
          </>
        )}
      </p>
    </Callout>
  );
}
