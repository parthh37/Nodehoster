import { useState, type ComponentType } from 'react';
import { Link } from 'react-router-dom';
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, CheckCircle2, Globe, Info, MailCheck, Play, Plus, Server, Wrench, X, XCircle } from 'lucide-react';
import { mailApi } from '@/api/endpoints';
import { ApiError, errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { MailCheck as Check, MailCheckStatus, MailHealth } from '@/api/types';
import { usePermissions } from '@/hooks/useAuth';
import { useNow } from '@/hooks/useNow';
import { Callout, Card, Mono, ProgressBar, Spinner } from '@/components/Layout';
import { ErrorBox, Field } from '@/components/Field';
import { Input } from '@/components/Input';
import { Button } from '@/components/Button';
import { Badge, type Tone } from '@/components/Badge';
import { CopyButton, CopyField } from '@/components/CopyButton';
import { cn } from '@/lib/cn';
import { jsonEqual } from '@/lib/obj';
import { formatDateTime, relativeTime } from '@/lib/format';
import { addDomains, countChecks, fixView, summarizeHealth, summaryText, worstOfCounts, worstStatus, type CheckCounts } from '@/lib/mailHealth';

const statusMeta: Record<MailCheckStatus, { tone: Tone; label: string; icon: ComponentType<{ className?: string }>; iconClass: string }> = {
  pass: { tone: 'green', label: 'Pass', icon: CheckCircle2, iconClass: 'text-emerald-600 dark:text-emerald-400' },
  warn: { tone: 'amber', label: 'Warning', icon: AlertTriangle, iconClass: 'text-amber-500 dark:text-amber-400' },
  fail: { tone: 'red', label: 'Fail', icon: XCircle, iconClass: 'text-red-600 dark:text-red-400' },
  info: { tone: 'gray', label: 'Info', icon: Info, iconClass: 'text-zinc-400 dark:text-zinc-500' },
};

const metaFor = (status: string) => statusMeta[status as MailCheckStatus] ?? statusMeta.info;

const deliveryLabel: Record<string, string> = { direct: 'Direct delivery', smarthost: 'Smart host' };

export function MailHealthTab() {
  const qc = useQueryClient();
  const { isAdmin } = usePermissions();
  // Extra domains typed by the user, and the list the shown report was run with.
  const [domains, setDomains] = useState<string[]>([]);
  const [ran, setRan] = useState<string[]>([]);
  const [draft, setDraft] = useState('');
  const [draftError, setDraftError] = useState<string | null>(null);

  // Runs when the tab first opens; after that only from the button. A
  // report from earlier in the session is shown until it is garbage-collected.
  const health = useQuery({
    queryKey: qk.mailHealth(ran),
    queryFn: ({ signal }) => mailApi.health(ran, signal),
    staleTime: Infinity,
    retry: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    placeholderData: keepPreviousData,
  });

  const addDraft = (): string[] | null => {
    if (!draft.trim()) return domains;
    const r = addDomains(domains, draft);
    if (r.error) {
      setDraftError(r.error);
      return null;
    }
    setDomains(r.domains);
    setDraft('');
    setDraftError(null);
    return r.domains;
  };

  const run = () => {
    const list = addDraft();
    if (!list) return;
    if (jsonEqual(list, ran)) {
      void health.refetch();
    } else {
      // A cached report for this list would be shown without running again.
      qc.removeQueries({ queryKey: qk.mailHealth(list), exact: true });
      setRan(list);
    }
  };

  const domainError = health.error instanceof ApiError && health.error.field === 'domain' ? health.error.message : null;
  const liveDraftError = draft.trim() ? draftError : null;
  const pending = !jsonEqual(domains, ran);
  const data = health.data;

  return (
    <div className="space-y-5">
      <Card
        title={
          <span className="flex items-center gap-2">
            <MailCheck className="h-4 w-4 text-zinc-400" />
            Deliverability
          </span>
        }
        description="Checks whether mail sent from this server reaches the inbox: SPF, DKIM, DMARC, reverse DNS, the host name, outbound port 25 and blacklists. Each problem comes with the record to publish or the change to make."
      >
        <div className="space-y-3">
          <Field
            label="Also check"
            error={liveDraftError || domainError}
            hint="Domains with a DKIM key or listed as allowed sender domains in the settings are always checked."
          >
            <div className="flex flex-wrap items-start gap-1.5">
              <Input
                mono
                className="min-w-0 flex-1 sm:max-w-sm"
                value={draft}
                placeholder="example.com"
                invalid={!!liveDraftError}
                onChange={(e) => {
                  setDraft(e.target.value);
                  setDraftError(addDomains([], e.target.value).error);
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    addDraft();
                  }
                }}
              />
              <Button icon={<Plus className="h-3.5 w-3.5" />} disabled={!draft.trim()} onClick={() => addDraft()}>
                Add
              </Button>
              <Button variant="primary" icon={<Play className="h-3.5 w-3.5" />} loading={health.isFetching} onClick={run}>
                Run checks
              </Button>
            </div>
          </Field>
          {domains.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5">
              {domains.map((d) => (
                <DomainChip key={d} domain={d} onRemove={() => setDomains(domains.filter((x) => x !== d))} />
              ))}
            </div>
          )}
          {pending && !health.isFetching && <p className="text-xs text-zinc-500 dark:text-zinc-400">Run the checks to include your changes.</p>}
        </div>
      </Card>

      {health.isFetching && (
        <section className="nh-card space-y-2 px-4 py-3" aria-live="polite">
          <div className="flex items-center gap-2 text-[13px] text-zinc-600 dark:text-zinc-300">
            <Spinner />
            Checking… this takes up to half a minute.
          </div>
          <ProgressBar value={0} indeterminate />
        </section>
      )}

      {health.isError && !domainError && !health.isFetching && <ErrorBox>{errorMessage(health.error)}</ErrorBox>}

      {data && (
        <div className={cn('space-y-5 transition-opacity', health.isFetching && 'pointer-events-none opacity-50')}>
          <Report health={data} isAdmin={isAdmin} />
        </div>
      )}
    </div>
  );
}

function DomainChip({ domain, onRemove }: { domain: string; onRemove: () => void }) {
  return (
    <span className="inline-flex items-center gap-0.5 rounded-md border border-zinc-200 bg-zinc-50 py-0.5 pl-2 pr-0.5 font-mono text-xs text-zinc-800 dark:border-zinc-700 dark:bg-zinc-800/60 dark:text-zinc-200">
      {domain}
      <button
        type="button"
        title="Remove"
        aria-label={`Remove ${domain}`}
        onClick={onRemove}
        className="inline-flex h-5 w-5 items-center justify-center rounded text-zinc-400 transition-colors hover:bg-zinc-200 hover:text-zinc-700 dark:hover:bg-zinc-700 dark:hover:text-zinc-200"
      >
        <X className="h-3 w-3" />
      </button>
    </span>
  );
}

// ---------------------------------------------------------------- report

function Report({ health: h, isAdmin }: { health: MailHealth; isAdmin: boolean }) {
  const now = useNow(15_000);
  const counts = summarizeHealth(h);
  const worst = worstOfCounts(counts);
  // Never "in a few seconds": the server's clock may be ahead of the browser's.
  const refNow = Math.max(now, Date.now(), Date.parse(h.checkedAt) || 0);
  const meta = metaFor(worst);
  const Icon = meta.icon;
  const server = h.server ?? [];
  const domains = h.domains ?? [];

  return (
    <>
      <section className="nh-card flex flex-wrap items-center gap-4 px-4 py-3">
        <div
          className={cn(
            'flex h-10 w-10 shrink-0 items-center justify-center rounded-lg',
            worst === 'fail'
              ? 'bg-red-50 text-red-600 dark:bg-red-500/10 dark:text-red-400'
              : worst === 'warn'
                ? 'bg-amber-50 text-amber-600 dark:bg-amber-500/10 dark:text-amber-400'
                : worst === 'pass'
                  ? 'bg-emerald-50 text-emerald-600 dark:bg-emerald-500/10 dark:text-emerald-400'
                  : 'bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400',
          )}
        >
          <Icon className="h-5 w-5" />
        </div>
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-semibold">
              {worst === 'fail'
                ? 'Mail is likely to be rejected or marked as spam'
                : worst === 'warn'
                  ? 'Mail gets through, but some settings hurt deliverability'
                  : worst === 'pass'
                    ? 'Everything checked looks good'
                    : 'Nothing could be judged'}
          </h2>
          <p className="text-xs text-zinc-500 dark:text-zinc-400">
            {summaryText(counts)} · <span title={formatDateTime(h.checkedAt)}>checked {relativeTime(h.checkedAt, refNow)}</span>
          </p>
        </div>
      </section>

      <Card
        title={
          <span className="flex items-center gap-2">
            <Server className="h-4 w-4 text-zinc-400" />
            This server
          </span>
        }
        description={
          <span className="flex flex-wrap gap-x-3 gap-y-0.5">
            {h.publicIp && (
              <span>
                Address <Mono>{h.publicIp}</Mono>
              </span>
            )}
            {h.hostname && (
              <span>
                Host name <Mono>{h.hostname}</Mono>
              </span>
            )}
            <span>{deliveryLabel[h.delivery] ?? h.delivery}</span>
          </span>
        }
        actions={<CountBadges counts={countChecks(server)} />}
        tone={toneFor(server)}
        flush
      >
        <CheckList checks={server} />
      </Card>

      {domains.length === 0 ? (
        <Callout tone="info" icon={<Info />} title="No sending domains to check">
          {isAdmin ? (
            <>
              Add a DKIM key or an allowed sender domain in{' '}
              <Link to="/mail/settings" className="nh-link">
                Mail settings
              </Link>
              , or enter a domain above.
            </>
          ) : (
            'An administrator can add a DKIM key or an allowed sender domain in the mail settings, or enter a domain above.'
          )}
        </Callout>
      ) : (
        domains.map((d) => (
          <Card
            key={d.domain}
            title={
              <span className="flex items-center gap-2">
                <Globe className="h-4 w-4 text-zinc-400" />
                <span className="font-mono text-[13px]">{d.domain}</span>
              </span>
            }
            actions={<CountBadges counts={countChecks(d.checks)} />}
            tone={toneFor(d.checks)}
            flush
          >
            <CheckList checks={d.checks ?? []} />
          </Card>
        ))
      )}
    </>
  );
}

function toneFor(checks: Check[] | null | undefined): 'danger' | 'warning' | undefined {
  const w = worstStatus(checks);
  return w === 'fail' ? 'danger' : w === 'warn' ? 'warning' : undefined;
}

function CountBadges({ counts }: { counts: CheckCounts }) {
  const items: [MailCheckStatus, number, string][] = [
    ['fail', counts.fail, 'failed'],
    ['warn', counts.warn, counts.warn === 1 ? 'warning' : 'warnings'],
    ['pass', counts.pass, 'passed'],
  ];
  const shown = items.filter(([, n]) => n > 0);
  if (!shown.length) return null;
  return (
    <div className="flex flex-wrap items-center gap-1">
      {shown.map(([s, n, label]) => (
        <Badge key={s} tone={statusMeta[s].tone}>
          {n} {label}
        </Badge>
      ))}
    </div>
  );
}

function CheckList({ checks }: { checks: Check[] }) {
  if (!checks.length) return <p className="px-4 py-3 text-xs text-zinc-500">No checks.</p>;
  return (
    <ul className="divide-y divide-zinc-200 dark:divide-zinc-800">
      {checks.map((c, i) => (
        <CheckRow key={`${c.name}-${i}`} check={c} />
      ))}
    </ul>
  );
}

function CheckRow({ check: c }: { check: Check }) {
  const meta = metaFor(c.status);
  const Icon = meta.icon;
  const fix = fixView(c);
  const problem = c.status === 'fail' || c.status === 'warn';
  return (
    <li
      className={cn(
        'flex gap-3 px-4 py-3',
        c.status === 'fail' && 'bg-red-50/60 dark:bg-red-500/[0.06]',
        c.status === 'warn' && 'bg-amber-50/50 dark:bg-amber-500/[0.05]',
      )}
    >
      <Icon className={cn('mt-0.5 h-4 w-4 shrink-0', meta.iconClass)} />
      <div className="min-w-0 flex-1 space-y-2">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-[13px] font-medium text-zinc-900 dark:text-zinc-100">{c.name}</span>
            <Badge tone={meta.tone}>{meta.label}</Badge>
          </div>
          {c.detail && (
            <p className={cn('mt-0.5 break-words text-xs', problem ? 'text-zinc-700 dark:text-zinc-300' : 'text-zinc-500 dark:text-zinc-400')}>{c.detail}</p>
          )}
        </div>
        {c.record && (
          <div>
            <p className="mb-1 text-2xs font-semibold uppercase tracking-wide text-zinc-500">Found</p>
            <pre className="scrollbar-thin overflow-x-auto whitespace-pre-wrap break-all rounded-md border border-zinc-200 bg-zinc-50 px-2.5 py-1.5 font-mono text-[12px] leading-5 text-zinc-800 dark:border-zinc-800 dark:bg-zinc-950 dark:text-zinc-200">
              {c.record}
            </pre>
          </div>
        )}
        {fix.kind === 'dns' && (
          <div className="space-y-2 rounded-md border border-zinc-200 bg-white p-3 dark:border-zinc-800 dark:bg-zinc-900">
            <p className="flex items-center gap-1.5 text-xs font-medium text-zinc-700 dark:text-zinc-300">
              <Wrench className="h-3.5 w-3.5 shrink-0 text-zinc-400" />
              Publish this {fix.type} record
            </p>
            <Field label="Name">
              <CopyField value={fix.name} />
            </Field>
            <Field label="Value">
              <div className="flex items-start gap-1 rounded-md border border-zinc-200 bg-zinc-50 py-1 pl-2.5 pr-1 dark:border-zinc-700 dark:bg-zinc-800/60">
                <code className="min-w-0 flex-1 break-all py-0.5 font-mono text-[12px] text-zinc-800 dark:text-zinc-200">{fix.value}</code>
                <CopyButton text={fix.value} />
              </div>
            </Field>
          </div>
        )}
        {fix.kind === 'hint' && (
          <p className="flex items-start gap-1.5 text-xs text-zinc-700 dark:text-zinc-300">
            <Wrench className="mt-px h-3.5 w-3.5 shrink-0 text-zinc-400" />
            <span className="min-w-0 break-words">
              <span className="font-medium">How to fix: </span>
              {fix.text}
            </span>
          </p>
        )}
      </div>
    </li>
  );
}
