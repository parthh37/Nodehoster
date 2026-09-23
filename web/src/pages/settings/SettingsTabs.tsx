import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery } from '@tanstack/react-query';
import { Bell, Cloud, MoreHorizontal, Pencil, Plus, Send, Trash2 } from 'lucide-react';
import { settingsApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { DNSProvider, WebhookTarget } from '@/api/types';
import { Card, EmptyState, FormSection, Grid, Mono, Sections } from '@/components/Layout';
import { ErrorBox, Field, PathError } from '@/components/Field';
import { Input, NumberInput, Select, Textarea } from '@/components/Input';
import { Checkbox, Switch } from '@/components/Switch';
import { SecretInput } from '@/components/SecretInput';
import { ListEditor } from '@/components/ListEditor';
import { Button, IconButton } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { Menu } from '@/components/Menu';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Badge } from '@/components/Badge';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { KEY_TYPES } from '../certificates/CertDialogs';
import type { SettingsTabProps } from './SettingsPage';

// ---------------------------------------------------------------- ACME

const DIRECTORIES = [
  { value: 'letsencrypt', label: "Let's Encrypt" },
  { value: 'letsencrypt-staging', label: "Let's Encrypt (staging — for testing)" },
  { value: 'zerossl', label: 'ZeroSSL' },
  { value: 'custom', label: 'Custom ACME directory URL…' },
];

export function AcmeTab({ s, update }: SettingsTabProps) {
  const a = s.acme;
  const set = (p: Partial<typeof a>) =>
    update((d) => {
      d.acme = { ...d.acme, ...p };
    });
  const isCustom = !!a.directory && !['letsencrypt', 'letsencrypt-staging', 'zerossl'].includes(a.directory);
  const needsEab = a.directory === 'zerossl' || isCustom;

  return (
    <Card title="ACME account" description="Used for Let's Encrypt (or compatible) certificates, including automatic certificates on HTTPS bindings.">
      <Sections>
        <FormSection title="Account">
          <Field label="Contact email" path="acme.email" hint="Expiry notices and account recovery from the certificate authority.">
            <Input type="email" value={a.email} onChange={(e) => set({ email: e.target.value.trim() })} placeholder="ops@example.com" />
          </Field>
          <Checkbox
            checked={a.agreeTos}
            onChange={(v) => set({ agreeTos: v })}
            label="I agree to the certificate authority's terms of service"
            description={
              a.directory.startsWith('letsencrypt') ? (
                <a href="https://letsencrypt.org/repository/" target="_blank" rel="noreferrer" className="nh-link">
                  Let's Encrypt Subscriber Agreement
                </a>
              ) : undefined
            }
          />
          <PathError path="acme.agreeTos" />
        </FormSection>
        <FormSection title="Directory" description="Staging issues untrusted certificates with much higher rate limits — use it to test your setup.">
          <Field label="Certificate authority" path="acme.directory">
            <Select
              value={isCustom ? 'custom' : a.directory || 'letsencrypt'}
              onChange={(v) => set({ directory: v === 'custom' ? 'https://' : v })}
              options={DIRECTORIES}
            />
          </Field>
          {isCustom && (
            <Field label="Directory URL" path="acme.directory">
              <Input mono value={a.directory} onChange={(e) => set({ directory: e.target.value.trim() })} placeholder="https://acme.example.com/directory" />
            </Field>
          )}
          {needsEab && (
            <Grid>
              <Field label="EAB key ID" path="acme.eabKeyId" hint="External account binding, from your CA dashboard.">
                <Input mono value={a.eabKeyId ?? ''} onChange={(e) => set({ eabKeyId: e.target.value.trim() })} />
              </Field>
              <Field label="EAB HMAC key" path="acme.eabHmac">
                <SecretInput value={a.eabHmac} onChange={(v) => set({ eabHmac: v })} />
              </Field>
            </Grid>
          )}
        </FormSection>
        <FormSection title="Certificates">
          <Grid>
            <Field label="Default key type" path="acme.keyType">
              <Select value={a.keyType || 'ec256'} onChange={(v) => set({ keyType: v })} options={KEY_TYPES} />
            </Field>
            <Field label="Renew before expiry" path="acme.renewBeforeDays" hint="Blank = at two thirds of the lifetime (about 30 days for 90-day certificates).">
              <NumberInput blankZero min={0} value={a.renewBeforeDays} onChange={(v) => set({ renewBeforeDays: v })} suffix="days" />
            </Field>
          </Grid>
        </FormSection>
      </Sections>
    </Card>
  );
}

// ---------------------------------------------------------------- DNS providers

export function DnsTab({ s, update }: SettingsTabProps) {
  const catalog = useQuery({ queryKey: qk.dnsCatalog, queryFn: settingsApi.dnsCatalog, staleTime: Infinity });
  const confirm = useConfirm();
  const [editing, setEditing] = useState<{ index: number; p: DNSProvider } | null>(null);
  const list = s.dnsProviders ?? [];
  const nameOf = (code: string) => catalog.data?.find((c) => c.code === code)?.name ?? code;

  const save = (index: number, p: DNSProvider) =>
    update((d) => {
      const l = d.dnsProviders ?? [];
      if (index < 0) l.push(p);
      else l[index] = p;
      d.dnsProviders = l;
    });

  return (
    <Card
      title="DNS providers"
      description="API credentials for DNS-01 challenges, required for wildcard certificates or servers not reachable on port 80. Credentials are encrypted at rest."
      actions={
        <Button size="sm" variant="primary" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setEditing({ index: -1, p: { id: '', name: '', provider: '', credentials: {} } })}>
          Add provider
        </Button>
      }
      flush
    >
      {list.length === 0 ? (
        <EmptyState
          compact
          icon={<Cloud />}
          title="No DNS providers"
          description="Add Cloudflare, Route 53, Azure DNS and others so certificates can be validated with a TXT record."
        />
      ) : (
        <Table>
          <THead>
            <tr>
              <Th>Name</Th>
              <Th>Provider</Th>
              <Th>Credentials</Th>
              <Th className="w-10" />
            </tr>
          </THead>
          <TBody>
            {list.map((p, i) => (
              <Tr key={p.id || i}>
                <Td className="font-medium">
                  {p.name}
                  <PathError path={`dnsProviders[${i}]`} prefix />
                </Td>
                <Td>
                  {nameOf(p.provider)} <Mono className="text-zinc-500">({p.provider})</Mono>
                </Td>
                <Td className="text-xs text-zinc-500">
                  {Object.keys(p.credentials ?? {}).map((k) => (
                    <Mono key={k} className="mr-2">
                      {k}
                    </Mono>
                  ))}
                </Td>
                <Td>
                  <Menu
                    trigger={(t) => <IconButton label="Actions" icon={<MoreHorizontal className="h-4 w-4" />} {...t} />}
                    items={[
                      { label: 'Edit', icon: <Pencil />, onSelect: () => setEditing({ index: i, p }) },
                      {
                        label: 'Remove',
                        icon: <Trash2 />,
                        danger: true,
                        onSelect: async () => {
                          const r = await confirm({
                            title: `Remove ${p.name}?`,
                            message: 'Certificates using this provider will fail to renew until they are switched to another provider.',
                            confirmLabel: 'Remove',
                            danger: true,
                          });
                          if (r.ok)
                            update((d) => {
                              d.dnsProviders = (d.dnsProviders ?? []).filter((_, j) => j !== i);
                            });
                        },
                      },
                    ]}
                  />
                </Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      )}
      <DnsProviderDialog
        value={editing?.p ?? null}
        catalog={catalog.data ?? []}
        catalogError={catalog.isError ? errorMessage(catalog.error) : null}
        onClose={() => setEditing(null)}
        onApply={(p) => {
          if (editing) save(editing.index, p);
          setEditing(null);
        }}
      />
    </Card>
  );
}

function DnsProviderDialog({
  value,
  catalog,
  catalogError,
  onClose,
  onApply,
}: {
  value: DNSProvider | null;
  catalog: { code: string; name: string; fields: { key: string; label: string; secret: boolean; optional: boolean }[] }[];
  catalogError: string | null;
  onClose: () => void;
  onApply: (p: DNSProvider) => void;
}) {
  const [p, setP] = useState<DNSProvider>({ id: '', name: '', provider: '', credentials: {} });
  const [touched, setTouched] = useState(false);
  useEffect(() => {
    if (value) setP({ ...value, credentials: { ...value.credentials } });
    setTouched(false);
  }, [value]);
  const entry = catalog.find((c) => c.code === p.provider);
  const missing = entry?.fields.filter((f) => !f.optional && !p.credentials[f.key]) ?? [];
  const valid = !!p.name.trim() && !!entry && missing.length === 0;
  const sorted = useMemo(() => [...catalog].sort((a, b) => a.name.localeCompare(b.name)), [catalog]);

  return (
    <Dialog
      open={!!value}
      onClose={onClose}
      title={value?.id ? `Edit ${value.name}` : 'Add DNS provider'}
      description="Changes are applied to the form; click Save to store them."
      onSubmit={() => {
        setTouched(true);
        if (!valid) return;
        // Keep only the selected provider's fields.
        const creds: Record<string, string> = {};
        for (const f of entry!.fields) if (p.credentials[f.key] !== undefined && p.credentials[f.key] !== '') creds[f.key] = p.credentials[f.key];
        onApply({ ...p, name: p.name.trim(), credentials: creds });
      }}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary">
            Apply
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {catalogError && <ErrorBox>{catalogError}</ErrorBox>}
        <Field label="Name" error={touched && !p.name.trim() ? 'Required' : null} hint="e.g. “Cloudflare – example.com”">
          <Input value={p.name} onChange={(e) => setP({ ...p, name: e.target.value })} />
        </Field>
        <Field label="Provider" error={touched && !entry ? 'Choose a provider' : null}>
          <Select
            value={p.provider}
            onChange={(v) => setP({ ...p, provider: v, credentials: v === value?.provider ? { ...value.credentials } : {} })}
            placeholder="Choose…"
            options={sorted.map((c) => ({ value: c.code, label: c.name }))}
          />
        </Field>
        {entry && (
          <div className="space-y-3 rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
            {entry.fields.length === 0 && <p className="text-xs text-zinc-500">This provider needs no credentials.</p>}
            {entry.fields.map((f) => (
              <Field
                key={f.key}
                label={
                  <>
                    {f.label} {f.optional && <span className="font-normal text-zinc-400">(optional)</span>}
                  </>
                }
                hint={<Mono>{f.key}</Mono>}
                error={touched && !f.optional && !p.credentials[f.key] ? 'Required' : null}
              >
                {f.secret ? (
                  <SecretInput
                    value={p.credentials[f.key] ?? ''}
                    onChange={(v) => setP({ ...p, credentials: { ...p.credentials, [f.key]: v } })}
                    allowClear={f.optional}
                  />
                ) : (
                  <Input mono value={p.credentials[f.key] ?? ''} onChange={(e) => setP({ ...p, credentials: { ...p.credentials, [f.key]: e.target.value } })} />
                )}
              </Field>
            ))}
          </div>
        )}
      </div>
    </Dialog>
  );
}

// ---------------------------------------------------------------- notifications

export const EVENT_TYPES = [
  'site.started',
  'site.stopped',
  'site.crashed',
  'site.failed',
  'site.unhealthy',
  'site.recycled',
  'deploy.succeeded',
  'deploy.failed',
  'cert.issued',
  'cert.renewed',
  'cert.failed',
  'cert.expiring',
  'server.started',
];

const FORMATS = [
  { value: 'generic', label: 'Generic JSON' },
  { value: 'slack', label: 'Slack' },
  { value: 'teams', label: 'Microsoft Teams' },
  { value: 'discord', label: 'Discord' },
];

export function NotificationsTab({ s, update }: SettingsTabProps) {
  const confirm = useConfirm();
  const toast = useToast();
  const [editing, setEditing] = useState<{ index: number; w: WebhookTarget } | null>(null);
  const list = s.webhooks ?? [];
  const test = useMutation({
    mutationFn: (w: WebhookTarget) => settingsApi.testWebhook(w),
    onSuccess: (_r, w) => toast.success(`Test sent to ${w.name || w.url}`),
    onError: (e) => toast.error('Test delivery failed', e),
  });

  return (
    <Card
      title="Webhooks"
      description="Send events to chat or incident tools. Leave the event list empty to receive everything."
      actions={
        <Button
          size="sm"
          variant="primary"
          icon={<Plus className="h-3.5 w-3.5" />}
          onClick={() => setEditing({ index: -1, w: { id: '', name: '', url: '', format: 'slack', events: [], enabled: true } })}
        >
          Add webhook
        </Button>
      }
      flush
    >
      {list.length === 0 ? (
        <EmptyState compact icon={<Bell />} title="No webhooks" description="Get notified in Slack, Teams or Discord when a site crashes, a deployment fails or a certificate is about to expire." />
      ) : (
        <Table>
          <THead>
            <tr>
              <Th>Name</Th>
              <Th>URL</Th>
              <Th>Format</Th>
              <Th>Events</Th>
              <Th>Enabled</Th>
              <Th className="w-40" />
            </tr>
          </THead>
          <TBody>
            {list.map((w, i) => (
              <Tr key={w.id || i}>
                <Td className="font-medium">
                  {w.name || <span className="text-zinc-400">Unnamed</span>}
                  <PathError path={`webhooks[${i}]`} prefix />
                </Td>
                <Td className="max-w-xs truncate">
                  <Mono className="text-zinc-500" title={w.url}>
                    {w.url}
                  </Mono>
                </Td>
                <Td>{FORMATS.find((f) => f.value === w.format)?.label ?? w.format}</Td>
                <Td className="max-w-xs">
                  {(w.events ?? []).length === 0 ? (
                    <span className="text-xs text-zinc-500">All events</span>
                  ) : (
                    <div className="flex flex-wrap gap-1">
                      {(w.events ?? []).map((e) => (
                        <Badge key={e} mono>
                          {e}
                        </Badge>
                      ))}
                    </div>
                  )}
                </Td>
                <Td>
                  <Switch
                    size="sm"
                    checked={w.enabled}
                    onChange={(v) =>
                      update((d) => {
                        d.webhooks![i].enabled = v;
                      })
                    }
                  />
                </Td>
                <Td>
                  <div className="flex justify-end gap-1">
                    <Button size="sm" variant="ghost" icon={<Send className="h-3.5 w-3.5" />} loading={test.isPending && test.variables === w} onClick={() => test.mutate(w)}>
                      Test
                    </Button>
                    <IconButton label="Edit" icon={<Pencil className="h-3.5 w-3.5" />} onClick={() => setEditing({ index: i, w })} />
                    <IconButton
                      label="Remove"
                      variant="danger-ghost"
                      icon={<Trash2 className="h-3.5 w-3.5" />}
                      onClick={async () => {
                        const r = await confirm({ title: `Remove webhook ${w.name || w.url}?`, confirmLabel: 'Remove', danger: true });
                        if (r.ok)
                          update((d) => {
                            d.webhooks = (d.webhooks ?? []).filter((_, j) => j !== i);
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
      <WebhookDialog
        value={editing?.w ?? null}
        onClose={() => setEditing(null)}
        onApply={(w) => {
          if (editing)
            update((d) => {
              const l = d.webhooks ?? [];
              if (editing.index < 0) l.push(w);
              else l[editing.index] = w;
              d.webhooks = l;
            });
          setEditing(null);
        }}
      />
    </Card>
  );
}

function WebhookDialog({ value, onClose, onApply }: { value: WebhookTarget | null; onClose: () => void; onApply: (w: WebhookTarget) => void }) {
  const toast = useToast();
  const [w, setW] = useState<WebhookTarget>({ id: '', name: '', url: '', format: 'generic', events: [], enabled: true });
  const [touched, setTouched] = useState(false);
  useEffect(() => {
    if (value) setW({ ...value, events: [...(value.events ?? [])] });
    setTouched(false);
  }, [value]);
  const urlErr = !/^https?:\/\/.+/.test(w.url) ? 'Enter an http:// or https:// URL' : null;
  const test = useMutation({
    mutationFn: () => settingsApi.testWebhook(w),
    onSuccess: () => toast.success('Test notification delivered'),
    onError: (e) => toast.error('Test delivery failed', e),
  });
  const events = w.events ?? [];
  const custom = events.filter((e) => !EVENT_TYPES.includes(e));
  const toggle = (e: string, on: boolean) => setW({ ...w, events: on ? [...events, e] : events.filter((x) => x !== e) });

  return (
    <Dialog
      open={!!value}
      onClose={onClose}
      size="lg"
      title={value?.id ? 'Edit webhook' : 'Add webhook'}
      onSubmit={() => {
        setTouched(true);
        if (urlErr || !w.name.trim()) return;
        onApply({ ...w, name: w.name.trim() });
      }}
      footer={
        <>
          <Button icon={<Send className="h-3.5 w-3.5" />} className="mr-auto" disabled={!!urlErr} loading={test.isPending} onClick={() => test.mutate()}>
            Send test
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
          <Field label="Name" error={touched && !w.name.trim() ? 'Required' : null}>
            <Input value={w.name} onChange={(e) => setW({ ...w, name: e.target.value })} placeholder="#ops channel" />
          </Field>
          <Field label="Format">
            <Select value={w.format} onChange={(v) => setW({ ...w, format: v })} options={FORMATS} />
          </Field>
        </Grid>
        <Field label="URL" error={touched ? urlErr : null}>
          <Input mono value={w.url} onChange={(e) => setW({ ...w, url: e.target.value.trim() })} placeholder="https://hooks.slack.com/services/…" />
        </Field>
        <Field label="Events" hint="None selected = all events.">
          <div className="grid grid-cols-2 gap-x-4 gap-y-1.5 rounded-lg border border-zinc-200 p-3 dark:border-zinc-800 sm:grid-cols-3">
            {EVENT_TYPES.map((e) => (
              <Checkbox key={e} checked={events.includes(e)} onChange={(on) => toggle(e, on)} label={<span className="font-mono text-xs">{e}</span>} />
            ))}
          </div>
        </Field>
        <Field label="Other event types" hint="Exact type names, e.g. from the Events page.">
          <ListEditor values={custom} onChange={(v) => setW({ ...w, events: [...events.filter((e) => EVENT_TYPES.includes(e)), ...v] })} placeholder="site.custom" />
        </Field>
        <Switch checked={w.enabled} onChange={(v) => setW({ ...w, enabled: v })} label="Enabled" />
      </div>
    </Dialog>
  );
}

// ---------------------------------------------------------------- TLS & proxy

export function TlsTab({ s, update }: SettingsTabProps) {
  return (
    <div className="space-y-5">
      <Card title="TLS">
        <Sections>
          <FormSection title="Protocol" description="Applies to every HTTPS binding.">
            <Field label="Minimum TLS version" path="tls.minVersion">
              <Select
                className="w-48"
                value={s.tls.minVersion}
                onChange={(v) =>
                  update((d) => {
                    d.tls.minVersion = v;
                  })
                }
                options={[
                  { value: '1.2', label: 'TLS 1.2' },
                  { value: '1.3', label: 'TLS 1.3' },
                ]}
              />
            </Field>
            <Switch
              checked={s.tls.http2}
              onChange={(v) =>
                update((d) => {
                  d.tls.http2 = v;
                })
              }
              label="HTTP/2"
              description="Negotiated with ALPN on HTTPS bindings."
            />
          </FormSection>
        </Sections>
      </Card>
      <Card title="Reverse proxy">
        <Sections>
          <FormSection title="Headers & clients">
            <Field label="Server header" path="proxy.serverHeader" hint="Value of the Server response header. Blank removes it.">
              <Input
                className="w-72"
                value={s.proxy.serverHeader}
                onChange={(e) =>
                  update((d) => {
                    d.proxy.serverHeader = e.target.value;
                  })
                }
              />
            </Field>
            <Field
              label="Trusted proxies"
              path="proxy.trustedProxies"
              prefix
              hint="Load balancers or CDNs in front of this server whose X-Forwarded-For is trusted for the client IP."
            >
              <ListEditor
                values={s.proxy.trustedProxies}
                onChange={(v) =>
                  update((d) => {
                    d.proxy.trustedProxies = v;
                  })
                }
                placeholder="10.0.0.0/8"
              />
            </Field>
          </FormSection>
          <FormSection title="Timeouts">
            <Grid>
              <Field label="Read header timeout" path="proxy.readHeaderTimeoutSec">
                <NumberInput
                  min={1}
                  value={s.proxy.readHeaderTimeoutSec}
                  onChange={(v) =>
                    update((d) => {
                      d.proxy.readHeaderTimeoutSec = v;
                    })
                  }
                  suffix="sec"
                />
              </Field>
              <Field label="Idle (keep-alive) timeout" path="proxy.idleTimeoutSec">
                <NumberInput
                  min={1}
                  value={s.proxy.idleTimeoutSec}
                  onChange={(v) =>
                    update((d) => {
                      d.proxy.idleTimeoutSec = v;
                    })
                  }
                  suffix="sec"
                />
              </Field>
            </Grid>
          </FormSection>
          <FormSection title="Default page" description="Shown for requests that match no site binding. Leave blank for the built-in page.">
            <Field path="proxy.defaultPageHtml">
              <Textarea
                mono
                rows={8}
                value={s.proxy.defaultPageHtml ?? ''}
                placeholder="<h1>Nothing here</h1>"
                onChange={(e) =>
                  update((d) => {
                    d.proxy.defaultPageHtml = e.target.value;
                  })
                }
              />
            </Field>
          </FormSection>
        </Sections>
      </Card>
    </div>
  );
}

// ---------------------------------------------------------------- process & logs

export function ProcessTab({ s, update }: SettingsTabProps) {
  const size = s.portRangeEnd - s.portRangeStart + 1;
  return (
    <Card title="Processes, logs and alerts">
      <Sections>
        <FormSection title="Port range" description="Local ports assigned to Node.js instances in automatic port mode. Keep them free of other services and firewalled from outside.">
          <Grid>
            <Field label="From" path="portRangeStart">
              <NumberInput
                mono
                min={1024}
                max={65535}
                value={s.portRangeStart}
                onChange={(v) =>
                  update((d) => {
                    d.portRangeStart = v;
                  })
                }
              />
            </Field>
            <Field label="To" path="portRangeEnd">
              <NumberInput
                mono
                min={1024}
                max={65535}
                value={s.portRangeEnd}
                onChange={(v) =>
                  update((d) => {
                    d.portRangeEnd = v;
                  })
                }
              />
            </Field>
          </Grid>
          <p className={size < 16 ? 'text-xs text-red-600' : 'text-xs text-zinc-500'}>{size > 0 ? `${size.toLocaleString()} ports` : 'Invalid range'} (at least 16, between 1024 and 65535)</p>
        </FormSection>
        <FormSection title="Application logs" description="Per-site stdout/stderr and access logs are rotated by size.">
          <Grid cols={3}>
            <Field label="Max file size" path="logMaxSizeMB">
              <NumberInput
                min={1}
                value={s.logMaxSizeMB}
                onChange={(v) =>
                  update((d) => {
                    d.logMaxSizeMB = v;
                  })
                }
                suffix="MB"
              />
            </Field>
            <Field label="Files kept" path="logMaxFiles">
              <NumberInput
                min={1}
                value={s.logMaxFiles}
                onChange={(v) =>
                  update((d) => {
                    d.logMaxFiles = v;
                  })
                }
              />
            </Field>
            <Field label="Retention" path="logRetentionDays" hint="Blank = keep until rotated out.">
              <NumberInput
                blankZero
                min={0}
                value={s.logRetentionDays}
                onChange={(v) =>
                  update((d) => {
                    d.logRetentionDays = v;
                  })
                }
                suffix="days"
              />
            </Field>
          </Grid>
        </FormSection>
        <FormSection title="Certificate alerts" description="Raise a warning event (and notify webhooks) when a certificate is about to expire.">
          <Field label="Warn before expiry" path="certExpiryWarnDays">
            <NumberInput
              className="w-40"
              min={1}
              value={s.certExpiryWarnDays}
              onChange={(v) =>
                update((d) => {
                  d.certExpiryWarnDays = v;
                })
              }
              suffix="days"
            />
          </Field>
        </FormSection>
        <FormSection title="Node.js" description="Default runtime for sites that do not pin a version.">
          <p className="text-[13px]">
            {s.defaultNodeVersion ? <Mono>v{s.defaultNodeVersion.replace(/^v/, '')}</Mono> : 'Node.js on the system PATH'} —{' '}
            <Link to="/node" className="nh-link">
              manage on the Node.js page
            </Link>
          </p>
        </FormSection>
      </Sections>
    </Card>
  );
}
