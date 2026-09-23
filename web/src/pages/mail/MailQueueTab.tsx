import { Fragment, useEffect, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertCircle, ChevronRight, Download, Inbox, Mail, RefreshCw, Send, Settings, Trash2 } from 'lucide-react';
import { mailApi, type MailQueueFilter } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { MailMessage, MailRecipient, MailStatus } from '@/api/types';
import { usePermissions } from '@/hooks/useAuth';
import { useNow } from '@/hooks/useNow';
import { Callout, Card, EmptyState, KV, Loading, Mono, Stat } from '@/components/Layout';
import { ErrorBox, Field, FormErrorBanner, FormErrors } from '@/components/Field';
import { Input } from '@/components/Input';
import { Button, IconButton } from '@/components/Button';
import { Badge, type Tone } from '@/components/Badge';
import { Dialog } from '@/components/Dialog';
import { Segmented } from '@/components/Tabs';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { cn } from '@/lib/cn';
import { formatBytes, formatDateTime, formatNumber, pluralize, relativeTime } from '@/lib/format';

const POLL = 5000;

const EMAIL_RE = /^[^\s@]+@[^\s@]+$/;

const messageState: Record<string, { tone: Tone; label: string }> = {
  queued: { tone: 'blue', label: 'Queued' },
  sending: { tone: 'accent', label: 'Sending' },
  failed: { tone: 'red', label: 'Failed' },
};

const recipientState: Record<string, { tone: Tone; label: string }> = {
  pending: { tone: 'amber', label: 'pending' },
  delivered: { tone: 'green', label: 'delivered' },
  failed: { tone: 'red', label: 'failed' },
};

const sourceLabel: Record<string, string> = { smtp: 'SMTP', pickup: 'Pickup folder', test: 'Test message' };

export function MailQueueTab() {
  const { canOperate, isAdmin } = usePermissions();
  const qc = useQueryClient();
  const toast = useToast();
  const [filter, setFilter] = useState<MailQueueFilter>('');
  const [testing, setTesting] = useState(false);
  const status = useQuery({ queryKey: qk.mailStatus, queryFn: mailApi.status, refetchInterval: POLL });
  const queue = useQuery({
    queryKey: qk.mailQueue(filter),
    queryFn: () => mailApi.queue(filter),
    refetchInterval: POLL,
    placeholderData: (prev) => prev,
  });

  const retryAll = useMutation({
    mutationFn: mailApi.retryAll,
    onSuccess: () => {
      toast.success('Retrying queued mail now');
      void qc.invalidateQueries({ queryKey: qk.mail });
    },
    onError: (e) => toast.error('Could not retry', e),
  });

  const queued = status.data?.queued ?? 0;

  return (
    <div className="space-y-5">
      <StatusPanel status={status.data} error={status.isError ? errorMessage(status.error) : null} loading={status.isPending} isAdmin={isAdmin} />

      <Card
        title="Queue"
        description="Messages waiting for delivery, and undeliverable mail kept for review. Refreshes every 5 seconds."
        actions={
          <>
            <Segmented
              value={filter || 'all'}
              onChange={(v) => setFilter(v === 'all' ? '' : (v as MailQueueFilter))}
              options={[
                { value: 'all', label: 'All' },
                { value: 'queued', label: 'Queued' },
                { value: 'failed', label: 'Failed' },
              ]}
            />
            {canOperate && (
              <Button size="sm" icon={<RefreshCw className="h-3.5 w-3.5" />} disabled={!queued} loading={retryAll.isPending} onClick={() => retryAll.mutate()}>
                Retry all
              </Button>
            )}
            {isAdmin && (
              <Button size="sm" variant="primary" icon={<Send className="h-3.5 w-3.5" />} onClick={() => setTesting(true)}>
                Send test email
              </Button>
            )}
          </>
        }
        flush
      >
        {queue.isPending ? (
          <Loading />
        ) : queue.isError ? (
          <ErrorBox className="m-4">{errorMessage(queue.error)}</ErrorBox>
        ) : (queue.data ?? []).length === 0 ? (
          <EmptyState
            compact
            icon={<Inbox />}
            title={filter === 'queued' ? 'No messages waiting' : filter === 'failed' ? 'No undeliverable mail' : 'The queue is empty'}
            description={filter ? undefined : 'Messages appear here while they wait for delivery, and stay here if they cannot be delivered.'}
          />
        ) : (
          <QueueTable messages={queue.data ?? []} canOperate={canOperate} isAdmin={isAdmin} />
        )}
      </Card>

      <TestMailDialog open={testing} onClose={() => setTesting(false)} />
    </div>
  );
}

// ---------------------------------------------------------------- status

function StatusPanel({ status: s, error, loading, isAdmin }: { status?: MailStatus; error: string | null; loading: boolean; isAdmin: boolean }) {
  const since = s?.since ? relativeTime(s.since) : undefined;
  let badge: { tone: Tone; label: string } = { tone: 'gray', label: 'Off' };
  if (s?.enabled) badge = s.listening ? { tone: 'green', label: 'Listening' } : { tone: 'red', label: 'Not listening' };

  return (
    <div className="space-y-3">
      <section className="nh-card flex flex-wrap items-center gap-4 px-4 py-3">
        <div
          className={cn(
            'flex h-10 w-10 shrink-0 items-center justify-center rounded-lg',
            s?.enabled && s.listening
              ? 'bg-emerald-50 text-emerald-600 dark:bg-emerald-500/10 dark:text-emerald-400'
              : s?.enabled
                ? 'bg-red-50 text-red-600 dark:bg-red-500/10 dark:text-red-400'
                : 'bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400',
          )}
        >
          <Mail className="h-5 w-5" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold">SMTP server</h2>
            {s && (
              <Badge tone={badge.tone} dot pulse={s.enabled && s.listening}>
                {badge.label}
              </Badge>
            )}
          </div>
          <p className="text-xs text-zinc-500 dark:text-zinc-400">
            {loading ? (
              'Loading…'
            ) : error ? (
              <span className="text-red-600 dark:text-red-400">{error}</span>
            ) : !s ? null : !s.enabled ? (
              <>
                Turned off: applications cannot submit mail.{' '}
                {isAdmin ? (
                  <Link to="/mail/settings" className="nh-link">
                    Turn it on in Mail settings
                  </Link>
                ) : (
                  'An administrator can turn it on.'
                )}
              </>
            ) : s.listening ? (
              <>
                Accepting mail on <Mono>{s.addr}</Mono>
              </>
            ) : (
              'The server is turned on but could not start listening.'
            )}
          </p>
        </div>
        {isAdmin && (
          <Link to="/mail/settings">
            <Button size="sm" icon={<Settings className="h-3.5 w-3.5" />}>
              Settings
            </Button>
          </Link>
        )}
      </section>
      {s?.enabled && !s.listening && (
        <Callout tone="danger" icon={<AlertCircle />} title={s.addr ? `Cannot listen on ${s.addr}` : 'Cannot listen'}>
          <span className="break-words font-mono text-xs">{s.error || 'Unknown error.'}</span>
          {s.error && /address already in use|only one usage/i.test(s.error) && (
            <p className="mt-1">Another program is using the port, often the Windows (IIS) SMTP service, which NodeHoster does not need, or another mail server. Stop it or choose another port.</p>
          )}
        </Callout>
      )}
      {s && (
        <div>
          <div className="grid gap-3 sm:grid-cols-3 lg:grid-cols-5">
            <Stat label="Queued" value={formatNumber(s.queued)} tone={s.queued ? 'amber' : 'default'} sub="waiting" />
            <Stat label="Failed" value={formatNumber(s.failed)} tone={s.failed ? 'red' : 'default'} sub="undeliverable" />
            <Stat label="Accepted" value={formatNumber(s.accepted)} sub="messages" />
            <Stat label="Delivered" value={formatNumber(s.delivered)} tone={s.delivered ? 'green' : 'default'} sub="recipients" />
            <Stat label="Bounced" value={formatNumber(s.bounced)} tone={s.bounced ? 'red' : 'default'} sub="recipients" />
          </div>
          {since && (
            <p className="mt-1.5 text-right text-2xs text-zinc-500" title={formatDateTime(s.since)}>
              Accepted, delivered and bounced since the service started {since}.
            </p>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- queue

function QueueTable({ messages, canOperate, isAdmin }: { messages: MailMessage[]; canOperate: boolean; isAdmin: boolean }) {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const now = useNow(15_000);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const toggle = (id: string) =>
    setExpanded((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  const refresh = () => void qc.invalidateQueries({ queryKey: qk.mail });
  const retry = useMutation({
    mutationFn: (id: string) => mailApi.retry(id),
    onSuccess: () => {
      toast.success('Retrying now');
      refresh();
    },
    onError: (e) => toast.error('Could not retry', e),
  });
  const remove = useMutation({
    mutationFn: (id: string) => mailApi.remove(id),
    onSuccess: () => {
      toast.success('Message deleted');
      refresh();
    },
    onError: (e) => toast.error('Could not delete message', e),
  });

  const onDelete = async (m: MailMessage) => {
    const r = await confirm({
      title: 'Delete this message?',
      message: (
        <>
          {m.subject ? <>“{m.subject}” </> : 'The message '}from <span className="font-mono">{m.from || '<>'}</span> is removed from the queue
          {m.state === 'failed' ? '.' : ' and will not be delivered.'}
        </>
      ),
      confirmLabel: 'Delete',
      danger: true,
    });
    if (r.ok) remove.mutate(m.id);
  };

  const cols = 8;
  return (
    <Table>
      <THead>
        <tr>
          <Th className="w-8" />
          <Th>Received</Th>
          <Th>From</Th>
          <Th>To</Th>
          <Th>Subject</Th>
          <Th className="text-right">Size</Th>
          <Th>Status</Th>
          <Th className="w-24" />
        </tr>
      </THead>
      <TBody>
        {messages.map((m) => {
          const open = expanded.has(m.id);
          const rcpts = m.recipients ?? [];
          const st = messageState[m.state] ?? { tone: 'gray' as Tone, label: m.state };
          return (
            <Fragment key={m.id}>
              <Tr onClick={() => toggle(m.id)} className={cn(open && 'bg-zinc-50 dark:bg-zinc-800/30')}>
                <Td className="pr-0">
                  <ChevronRight className={cn('h-3.5 w-3.5 text-zinc-400 transition-transform', open && 'rotate-90')} />
                </Td>
                <Td className="whitespace-nowrap">
                  <div className="text-xs">{formatDateTime(m.receivedAt)}</div>
                  <div className="text-2xs text-zinc-500">{relativeTime(m.receivedAt, now)}</div>
                </Td>
                <Td className="max-w-[14rem]">
                  <Mono className="block truncate" title={m.from}>
                    {m.from || '<>'}
                  </Mono>
                  <div className="text-2xs text-zinc-500">{sourceLabel[m.source] ?? m.source}</div>
                </Td>
                <Td className="max-w-[16rem]">
                  <RecipientsSummary recipients={rcpts} />
                </Td>
                <Td className="max-w-[16rem] truncate" title={m.subject}>
                  {m.subject || <span className="text-zinc-400">(no subject)</span>}
                </Td>
                <Td className="whitespace-nowrap text-right tabular text-xs">{formatBytes(m.size)}</Td>
                <Td className="max-w-[18rem]">
                  <div className="flex items-center gap-1.5">
                    <Badge tone={st.tone} dot pulse={m.state === 'sending'}>
                      {st.label}
                    </Badge>
                    {m.attempts > 0 && <span className="text-2xs text-zinc-500">{pluralize(m.attempts, 'attempt')}</span>}
                  </div>
                  {m.state === 'queued' && m.nextAttempt && <div className="text-2xs text-zinc-500">next {relativeTime(m.nextAttempt, now)}</div>}
                  {m.lastError && (
                    <div className="truncate font-mono text-2xs text-red-600 dark:text-red-400" title={m.lastError}>
                      {m.lastError}
                    </div>
                  )}
                </Td>
                <Td onClick={(e) => e.stopPropagation()} className="cursor-default">
                  <div className="flex justify-end gap-0.5">
                    {canOperate && m.state !== 'sending' && (
                      <IconButton
                        label={m.state === 'failed' ? 'Send again to the failed recipients' : 'Retry now'}
                        icon={<RefreshCw className="h-3.5 w-3.5" />}
                        loading={retry.isPending && retry.variables === m.id}
                        onClick={() => retry.mutate(m.id)}
                      />
                    )}
                    {isAdmin && (
                      <a href={mailApi.emlUrl(m.id)} download>
                        <IconButton label="Download .eml" icon={<Download className="h-3.5 w-3.5" />} />
                      </a>
                    )}
                    {isAdmin && (
                      <IconButton
                        label="Delete"
                        variant="danger-ghost"
                        icon={<Trash2 className="h-3.5 w-3.5" />}
                        loading={remove.isPending && remove.variables === m.id}
                        onClick={() => void onDelete(m)}
                      />
                    )}
                  </div>
                </Td>
              </Tr>
              {open && (
                <tr className="bg-zinc-50/60 dark:bg-zinc-900/40">
                  <td colSpan={cols} className="px-4 py-3">
                    <MessageDetails m={m} now={now} />
                  </td>
                </tr>
              )}
            </Fragment>
          );
        })}
      </TBody>
    </Table>
  );
}

function RecipientsSummary({ recipients }: { recipients: MailRecipient[] }) {
  if (recipients.length === 0) return <span className="text-zinc-400">—</span>;
  const counts = recipients.reduce<Record<string, number>>((acc, r) => ({ ...acc, [r.state]: (acc[r.state] ?? 0) + 1 }), {});
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 items-center gap-1">
        <Mono className="truncate" title={recipients.map((r) => r.address).join(', ')}>
          {recipients[0].address}
        </Mono>
        {recipients.length > 1 && <span className="shrink-0 text-xs text-zinc-500">+{recipients.length - 1}</span>}
      </div>
      {recipients.length > 1 && (
        <div className="mt-0.5 flex flex-wrap gap-1">
          {Object.entries(counts).map(([state, n]) => (
            <Badge key={state} tone={recipientState[state]?.tone ?? 'gray'} className="px-1 text-2xs">
              {n} {recipientState[state]?.label ?? state}
            </Badge>
          ))}
        </div>
      )}
    </div>
  );
}

function MessageDetails({ m, now }: { m: MailMessage; now: number }) {
  const rcpts = m.recipients ?? [];
  const items: [ReactNode, ReactNode][] = [
    ['Message ID', <Mono className="break-all">{m.id}</Mono>],
    ['Source', sourceLabel[m.source] ?? m.source],
  ];
  if (m.clientIp) items.push(['Client', <Mono>{m.clientIp}</Mono>]);
  if (m.user) items.push(['Signed in as', <Mono>{m.user}</Mono>]);
  items.push(['Received', formatDateTime(m.receivedAt)], ['Size', formatBytes(m.size)], ['Attempts', formatNumber(m.attempts)]);
  if (m.lastAttempt) items.push(['Last attempt', formatDateTime(m.lastAttempt)]);
  if (m.state === 'queued' && m.nextAttempt) items.push(['Next attempt', `${formatDateTime(m.nextAttempt)} (${relativeTime(m.nextAttempt, now)})`]);
  return (
    <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_22rem]">
      <div className="min-w-0">
        <p className="mb-1.5 text-2xs font-semibold uppercase tracking-wide text-zinc-500">Recipients</p>
        <ul className="divide-y divide-zinc-200 rounded-md border border-zinc-200 bg-white dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-900">
          {rcpts.map((r, i) => {
            const st = recipientState[r.state] ?? { tone: 'gray' as Tone, label: r.state };
            return (
              <li key={i} className="px-3 py-1.5">
                <div className="flex items-center gap-2">
                  <Mono className="min-w-0 flex-1 truncate">{r.address}</Mono>
                  {r.deliveredAt && <span className="text-2xs text-zinc-500">{formatDateTime(r.deliveredAt)}</span>}
                  <Badge tone={st.tone}>{st.label}</Badge>
                </div>
                {r.error && <p className="mt-0.5 break-words font-mono text-2xs text-red-600 dark:text-red-400">{r.error}</p>}
              </li>
            );
          })}
        </ul>
        {m.lastError && (
          <div className="mt-3 flex items-start gap-2 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-xs text-red-800 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300">
            <AlertCircle className="mt-px h-3.5 w-3.5 shrink-0" />
            <div className="min-w-0">
              <span className="font-semibold">Last attempt{m.lastAttempt ? ` ${relativeTime(m.lastAttempt, now)}` : ''}: </span>
              <span className="break-words font-mono">{m.lastError}</span>
            </div>
          </div>
        )}
      </div>
      <KV items={items} />
    </div>
  );
}

// ---------------------------------------------------------------- test

function TestMailDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [to, setTo] = useState('');
  const [from, setFrom] = useState('');
  const [touched, setTouched] = useState(false);
  const send = useMutation({
    mutationFn: () => mailApi.test({ to: to.trim(), from: from.trim() || undefined }),
    onSuccess: () => {
      toast.success('Test message queued', `To ${to.trim()}. Follow its delivery in the queue.`);
      void qc.invalidateQueries({ queryKey: qk.mail });
      onClose();
    },
  });

  useEffect(() => {
    if (!open) return;
    setTo('');
    setFrom('');
    setTouched(false);
    send.reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const toErr = !to.trim() ? 'Required' : !EMAIL_RE.test(to.trim()) ? 'Enter an email address' : null;
  const fromErr = from.trim() && !EMAIL_RE.test(from.trim()) ? 'Enter an email address' : null;

  return (
    <Dialog
      open={open}
      onClose={onClose}
      icon={<Send className="h-5 w-5 text-zinc-400" />}
      title="Send test email"
      description="Queues a short message and delivers it with the current delivery settings."
      onSubmit={() => {
        setTouched(true);
        if (!toErr && !fromErr) send.mutate();
      }}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" icon={<Send className="h-3.5 w-3.5" />} loading={send.isPending}>
            Send
          </Button>
        </>
      }
    >
      <FormErrors error={send.error}>
        <div className="space-y-4">
          <FormErrorBanner />
          <Field label="To" path="to" required error={touched && toErr}>
            <Input type="email" value={to} onChange={(e) => setTo(e.target.value)} placeholder="you@example.com" autoComplete="off" />
          </Field>
          <Field label="From" path="from" error={touched && fromErr} hint="Blank = the server's default sender. Use an address in a domain you sign with DKIM to test deliverability.">
            <Input type="email" value={from} onChange={(e) => setFrom(e.target.value)} placeholder="app@example.com" autoComplete="off" />
          </Field>
        </div>
      </FormErrors>
    </Dialog>
  );
}
