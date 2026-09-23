import { useEffect, useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { CheckCircle2, Pencil, Plus, Radio as RadioIcon, Send, Trash2, XCircle } from 'lucide-react';
import { logShippingApi, sitesApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { LogLevel, LogTarget, LogTargetStatus } from '@/api/types';
import { Button, IconButton } from '@/components/Button';
import { Card, Callout, EmptyState, Grid, Mono } from '@/components/Layout';
import { Field, PathError } from '@/components/Field';
import { Input, Select, Textarea } from '@/components/Input';
import { Checkbox, Switch } from '@/components/Switch';
import { SecretInput } from '@/components/SecretInput';
import { Dialog } from '@/components/Dialog';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Badge } from '@/components/Badge';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { FACILITIES, LOG_SOURCES, TARGET_TYPES, newTarget, statusTone, targetProblem, targetSummary } from '@/lib/logShipping';
import { formatNumber, relativeTime } from '@/lib/format';
import type { SettingsTabProps } from './SettingsPage';

const LEVELS: { value: LogLevel; label: string }[] = [
  { value: 'debug', label: 'Debug' },
  { value: 'info', label: 'Information' },
  { value: 'warning', label: 'Warning' },
  { value: 'error', label: 'Error' },
];

/** Settings → Log shipping: targets and their delivery status. */
export function LogShippingTab({ s, update }: SettingsTabProps) {
  const confirm = useConfirm();
  const toast = useToast();
  const [editing, setEditing] = useState<{ index: number; t: LogTarget } | null>(null);
  const [adding, setAdding] = useState(false);
  const list = s.logShipping.targets;
  const status = useQuery({ queryKey: qk.logShippingStatus, queryFn: logShippingApi.status, refetchInterval: 5000 });
  const test = useMutation({
    mutationFn: (t: LogTarget) => logShippingApi.test(t),
    onSuccess: (_r, t) => toast.success(`Test message sent to ${t.name}`),
    onError: (e) => toast.error('Test message failed', e),
  });
  const statusOf = (id: string) => status.data?.find((x) => x.id === id);

  return (
    <div className="space-y-5">
      <Callout tone="info">
        Logging never waits for a collector: each target has a queue of 10,000 records, and while a collector is slow or down the oldest records are dropped (and
        counted below) and delivery is retried with back-off.
      </Callout>
      <Card
        title="Targets"
        description="Where the server log, the sites' output and access logs, events and the audit log are sent."
        actions={
          <Button size="sm" variant="primary" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setAdding(true)}>
            Add target
          </Button>
        }
        flush
      >
        {list.length === 0 ? (
          <EmptyState compact icon={<RadioIcon />} title="No log targets" description="Send logs to syslog, Seq or any HTTP collector." />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th>Name</Th>
                <Th>Destination</Th>
                <Th>Sources</Th>
                <Th>Delivery</Th>
                <Th>Enabled</Th>
                <Th className="w-40" />
              </tr>
            </THead>
            <TBody>
              {list.map((t, i) => (
                <Tr key={t.id || i}>
                  <Td className="font-medium">
                    {t.name}
                    <PathError path={`logShipping.targets[${i}]`} prefix />
                  </Td>
                  <Td className="max-w-xs truncate">
                    <Mono className="text-zinc-500" title={targetSummary(t)}>
                      {targetSummary(t)}
                    </Mono>
                  </Td>
                  <Td className="max-w-xs">
                    <div className="flex flex-wrap gap-1">
                      {t.sources.map((src) => (
                        <Badge key={src}>{src}</Badge>
                      ))}
                      {t.siteIds.length > 0 && <Badge tone="blue">{t.siteIds.length} site(s)</Badge>}
                    </div>
                  </Td>
                  <Td>
                    <DeliveryStatus st={statusOf(t.id)} saved={!!t.id} />
                  </Td>
                  <Td>
                    <Switch
                      size="sm"
                      checked={t.enabled}
                      onChange={(v) =>
                        update((d) => {
                          d.logShipping.targets[i].enabled = v;
                        })
                      }
                    />
                  </Td>
                  <Td>
                    <div className="flex justify-end gap-1">
                      <Button size="sm" variant="ghost" icon={<Send className="h-3.5 w-3.5" />} loading={test.isPending && test.variables === t} onClick={() => test.mutate(t)}>
                        Test
                      </Button>
                      <IconButton label="Edit" icon={<Pencil className="h-3.5 w-3.5" />} onClick={() => setEditing({ index: i, t })} />
                      <IconButton
                        label="Remove"
                        variant="danger-ghost"
                        icon={<Trash2 className="h-3.5 w-3.5" />}
                        onClick={async () => {
                          const r = await confirm({ title: `Remove log target ${t.name}?`, confirmLabel: 'Remove', danger: true });
                          if (r.ok)
                            update((d) => {
                              d.logShipping.targets = d.logShipping.targets.filter((_, j) => j !== i);
                            });
                        }}
                      />
                    </div>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        )}
      </Card>
      <Dialog open={adding} onClose={() => setAdding(false)} title="Add log target">
        <div className="space-y-2">
          {TARGET_TYPES.map((t) => (
            <button
              key={t.value}
              type="button"
              onClick={() => {
                setAdding(false);
                setEditing({ index: -1, t: newTarget(t.value) });
              }}
              className="block w-full rounded-md border border-zinc-200 px-3 py-2.5 text-left hover:border-accent-500 hover:bg-accent-50/50 dark:border-zinc-800 dark:hover:bg-accent-500/5"
            >
              <span className="text-[13px] font-medium">{t.label}</span>
              <span className="block text-xs text-zinc-500 dark:text-zinc-400">{t.description}</span>
            </button>
          ))}
        </div>
      </Dialog>
      <TargetDialog
        value={editing?.t ?? null}
        onClose={() => setEditing(null)}
        onApply={(t) => {
          if (editing)
            update((d) => {
              if (editing.index < 0) d.logShipping.targets.push(t);
              else d.logShipping.targets[editing.index] = t;
            });
          setEditing(null);
        }}
      />
    </div>
  );
}

function DeliveryStatus({ st, saved }: { st: LogTargetStatus | undefined; saved: boolean }) {
  if (!saved) return <span className="text-xs text-zinc-500">Not saved yet</span>;
  if (!st) return <span className="text-xs text-zinc-500">—</span>;
  if (!st.enabled) return <Badge>off</Badge>;
  const tone = statusTone(st);
  return (
    <div className="space-y-0.5 text-xs">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge tone={tone} dot>
          {tone === 'red' ? 'failing' : tone === 'amber' ? 'losing records' : 'ok'}
        </Badge>
        <span className="text-zinc-500">
          sent {formatNumber(st.sent)}
          {st.queued > 0 && ` · queued ${formatNumber(st.queued)}`}
          {st.dropped > 0 && ` · dropped ${formatNumber(st.dropped)}`}
          {st.failed > 0 && ` · failed ${formatNumber(st.failed)}`}
        </span>
      </div>
      {st.lastSuccess && <div className="text-zinc-500">last delivery {relativeTime(st.lastSuccess)}</div>}
      {st.lastError && (
        <div className="max-w-xs truncate text-red-600 dark:text-red-400" title={st.lastError}>
          {st.lastErrorAt ? `${relativeTime(st.lastErrorAt)}: ` : ''}
          {st.lastError}
        </div>
      )}
    </div>
  );
}

function TargetDialog({ value, onClose, onApply }: { value: LogTarget | null; onClose: () => void; onApply: (t: LogTarget) => void }) {
  const [t, setT] = useState<LogTarget>(newTarget('syslog'));
  const [touched, setTouched] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; error?: string } | null>(null);
  const sites = useQuery({ queryKey: qk.sites, queryFn: sitesApi.list, staleTime: 30_000, enabled: !!value });
  useEffect(() => {
    if (value) setT(JSON.parse(JSON.stringify(value)) as LogTarget);
    setTouched(false);
    setResult(null);
  }, [value]);
  const problem = targetProblem(t);
  const err = (f: string) => (touched && problem?.field === f ? problem.message : null);
  const test = useMutation({
    mutationFn: () => logShippingApi.test(t),
    onSuccess: () => setResult({ ok: true }),
    onError: (e) => setResult({ ok: false, error: errorMessage(e) }),
  });
  const sy = t.syslog;
  const setSy = (p: Partial<NonNullable<LogTarget['syslog']>>) => setT({ ...t, syslog: { ...sy!, ...p } });
  const h = t.http;
  const setH = (p: Partial<NonNullable<LogTarget['http']>>) => setT({ ...t, http: { ...h!, ...p } });

  return (
    <Dialog
      open={!!value}
      onClose={onClose}
      size="lg"
      title={`${value?.id ? 'Edit' : 'Add'} ${TARGET_TYPES.find((x) => x.value === t.type)?.label ?? ''} target`}
      onSubmit={() => {
        setTouched(true);
        if (problem) return;
        onApply({ ...t, name: t.name.trim() });
      }}
      footer={
        <>
          <Button icon={<Send className="h-3.5 w-3.5" />} className="mr-auto" loading={test.isPending} disabled={!!problem && problem.field !== 'sources'} onClick={() => test.mutate()}>
            Send test message
          </Button>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary">
            Apply
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Grid>
          <Field label="Name" error={err('name')}>
            <Input value={t.name} onChange={(e) => setT({ ...t, name: e.target.value })} placeholder={{ syslog: 'Graylog', seq: 'Seq', http: 'Vector' }[t.type]} />
          </Field>
          <div className="flex items-end pb-1.5">
            <Switch checked={t.enabled} onChange={(v) => setT({ ...t, enabled: v })} label="Enabled" />
          </div>
        </Grid>

        {t.type === 'syslog' && sy && (
          <>
            <Grid cols={3}>
              <Field label="Address" error={err('syslog.address')} className="sm:col-span-2">
                <Input mono value={sy.address} onChange={(e) => setSy({ address: e.target.value.trim() })} placeholder="logs.example.com:514" />
              </Field>
              <Field label="Transport">
                <Select
                  value={sy.transport}
                  onChange={(v) => setSy({ transport: v as 'udp' | 'tcp' | 'tls' })}
                  options={[
                    { value: 'udp', label: 'UDP' },
                    { value: 'tcp', label: 'TCP' },
                    { value: 'tls', label: 'TLS' },
                  ]}
                />
              </Field>
              <Field label="Facility">
                <Select value={sy.facility} onChange={(v) => setSy({ facility: v })} options={FACILITIES.map((f) => ({ value: f, label: f }))} />
              </Field>
              <Field label="App name" hint="Default nodehoster">
                <Input mono value={sy.appName} onChange={(e) => setSy({ appName: e.target.value.trim() })} placeholder="nodehoster" />
              </Field>
              <Field label="Host name" hint="Default: this computer">
                <Input mono value={sy.hostname} onChange={(e) => setSy({ hostname: e.target.value.trim() })} />
              </Field>
            </Grid>
            {sy.transport === 'tls' && (
              <>
                <Field label="CA certificate" hint="PEM, when the collector's certificate is not publicly trusted.">
                  <Textarea mono rows={3} value={sy.caCert ?? ''} onChange={(e) => setSy({ caCert: e.target.value })} placeholder="-----BEGIN CERTIFICATE-----" />
                </Field>
                <Checkbox checked={sy.insecureSkipVerify} onChange={(v) => setSy({ insecureSkipVerify: v })} label="Do not verify the collector's certificate" description="Encrypts without authenticating the collector. For testing only." />
              </>
            )}
            <p className="text-xs text-zinc-500 dark:text-zinc-400">
              RFC 5424 messages; TCP and TLS use octet counting, so multi-line messages stay whole. UDP messages are cut at 8 KiB.
            </p>
          </>
        )}

        {t.type === 'seq' && t.seq && (
          <Grid>
            <Field label="Server URL" error={err('seq.url')}>
              <Input mono value={t.seq.url} onChange={(e) => setT({ ...t, seq: { ...t.seq!, url: e.target.value.trim() } })} placeholder="https://seq.example.com" />
            </Field>
            <Field label="API key" hint="Sent as X-Seq-ApiKey.">
              <SecretInput value={t.seq.apiKey} onChange={(v) => setT({ ...t, seq: { ...t.seq!, apiKey: v } })} />
            </Field>
          </Grid>
        )}

        {t.type === 'http' && h && (
          <>
            <Grid>
              <Field label="URL" error={err('http.url')}>
                <Input mono value={h.url} onChange={(e) => setH({ url: e.target.value.trim() })} placeholder="https://logs.example.com/ingest" />
              </Field>
              <Field label="Body">
                <Select
                  value={h.format}
                  onChange={(v) => setH({ format: v as 'json' | 'ndjson' })}
                  options={[
                    { value: 'json', label: 'JSON array per batch' },
                    { value: 'ndjson', label: 'NDJSON (one record per line)' },
                  ]}
                />
              </Field>
            </Grid>
            <Field label="Headers" error={err('http.headers')} hint="For authentication, e.g. Authorization. Secret values are stored encrypted and never shown again.">
              <div className="space-y-2">
                {h.headers.map((hd, i) => (
                  <div key={i} className="flex items-center gap-2">
                    <Input mono className="w-48" value={hd.name} placeholder="Authorization" onChange={(e) => setH({ headers: h.headers.map((x, j) => (j === i ? { ...x, name: e.target.value.trim() } : x)) })} />
                    {hd.secret ? (
                      <SecretInput className="flex-1" value={hd.value} onChange={(v) => setH({ headers: h.headers.map((x, j) => (j === i ? { ...x, value: v } : x)) })} />
                    ) : (
                      <Input mono className="flex-1" value={hd.value} onChange={(e) => setH({ headers: h.headers.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)) })} />
                    )}
                    <Checkbox checked={hd.secret} onChange={(v) => setH({ headers: h.headers.map((x, j) => (j === i ? { ...x, secret: v, value: '' } : x)) })} label="Secret" />
                    <IconButton label="Remove" variant="danger-ghost" icon={<Trash2 className="h-3.5 w-3.5" />} onClick={() => setH({ headers: h.headers.filter((_, j) => j !== i) })} />
                  </div>
                ))}
                <Button size="sm" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setH({ headers: [...h.headers, { name: '', value: '', secret: true }] })}>
                  Add header
                </Button>
              </div>
            </Field>
          </>
        )}

        <Field label="Sources" error={err('sources')}>
          <div className="grid gap-2 rounded-lg border border-zinc-200 p-3 dark:border-zinc-800 sm:grid-cols-2">
            {LOG_SOURCES.map((src) => (
              <Checkbox
                key={src.value}
                checked={t.sources.includes(src.value)}
                onChange={(on) => setT({ ...t, sources: on ? [...t.sources, src.value] : t.sources.filter((x) => x !== src.value) })}
                label={src.label}
                description={src.description}
              />
            ))}
          </div>
        </Field>
        <Grid>
          <Field label="Server log from" hint="The lowest level of the server log that is sent.">
            <Select value={t.minLevel} onChange={(v) => setT({ ...t, minLevel: v as LogLevel })} options={LEVELS} disabled={!t.sources.includes('server')} />
          </Field>
        </Grid>
        <Field label="Sites" hint="Application output, access logs and events of these sites only. None selected = every site.">
          <div className="grid grid-cols-2 gap-x-4 gap-y-1.5 rounded-lg border border-zinc-200 p-3 dark:border-zinc-800 sm:grid-cols-3">
            {(sites.data ?? []).map((site) => (
              <Checkbox
                key={site.id}
                checked={t.siteIds.includes(site.id)}
                onChange={(on) => setT({ ...t, siteIds: on ? [...t.siteIds, site.id] : t.siteIds.filter((x) => x !== site.id) })}
                label={site.name}
              />
            ))}
            {sites.data?.length === 0 && <span className="text-xs text-zinc-500">No sites yet.</span>}
          </div>
        </Field>

        {result && (
          <Callout tone={result.ok ? 'success' : 'danger'} icon={result.ok ? <CheckCircle2 /> : <XCircle />} title={result.ok ? 'The collector accepted a test message' : 'The test message failed'}>
            {result.error}
          </Callout>
        )}
      </div>
    </Dialog>
  );
}
