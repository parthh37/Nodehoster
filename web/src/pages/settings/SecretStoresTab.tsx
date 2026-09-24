import { useEffect, useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { CheckCircle2, KeyRound, Pencil, Plus, Send, ShieldCheck, Trash2, Vault, XCircle } from 'lucide-react';
import { secretStoresApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { SecretStore, SecretTestResult } from '@/api/types';
import { SECRET } from '@/api/types';
import { Button, IconButton } from '@/components/Button';
import { Callout, Card, EmptyState, Grid, Mono } from '@/components/Layout';
import { Field, PathError } from '@/components/Field';
import { Input, NumberInput, Select, Textarea } from '@/components/Input';
import { SecretInput } from '@/components/SecretInput';
import { Radio } from '@/components/Switch';
import { Dialog } from '@/components/Dialog';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Badge } from '@/components/Badge';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { relativeTime } from '@/lib/format';
import {
  DEFAULT_INFISICAL_URL,
  STORE_TYPES,
  newStore,
  refHint,
  refPlaceholder,
  refProblem,
  storeHealth,
  storeProblem,
  storeSummary,
  storeTypeLabel,
} from '@/lib/secretStores';
import type { SettingsTabProps } from './SettingsPage';

/**
 * Settings → Secret stores: Vault / OpenBao, Infisical and Bitwarden
 * Secrets Manager, which environment variables and deploy tokens can take
 * their values from.
 */
export function SecretStoresTab({ s, update }: SettingsTabProps) {
  const confirm = useConfirm();
  const toast = useToast();
  const [editing, setEditing] = useState<{ index: number; s: SecretStore } | null>(null);
  const [adding, setAdding] = useState(false);
  const list = s.secretStores;
  const status = useQuery({ queryKey: qk.secretStores, queryFn: secretStoresApi.status, refetchInterval: 30_000 });
  const test = useMutation({
    mutationFn: (st: SecretStore) => secretStoresApi.test(st),
    onSuccess: (r, st) => (r.ok ? toast.success(`${st.name} works`, r.detail) : toast.error(`${st.name} failed`, r.error)),
    onError: (e) => toast.error('Test failed', e),
  });

  return (
    <div className="space-y-4">
      <Card
        title="Secret stores"
        description="Environment variables and git tokens can take their values from these stores instead of NodeHoster's configuration, like Key Vault references in Azure App Service."
        actions={
          <Button size="sm" variant="primary" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setAdding(true)}>
            Add store
          </Button>
        }
        flush
      >
        <PathError path="secretStores" className="px-4 pt-3" />
        {list.length === 0 ? (
          <EmptyState
            compact
            icon={<Vault />}
            title="No secret stores"
            description="Add HashiCorp Vault or OpenBao, Infisical, or Bitwarden Secrets Manager, then pick “From secret store” for a variable."
          />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th>Name</Th>
                <Th>Type</Th>
                <Th>Location</Th>
                <Th>State</Th>
                <Th className="w-40" />
              </tr>
            </THead>
            <TBody>
              {list.map((st, i) => {
                const live = status.data?.find((x) => x.name === st.name);
                const health = storeHealth(live);
                return (
                  <Tr key={st.id || i}>
                    <Td className="font-medium">
                      <Mono>{st.name}</Mono>
                      <PathError path={`secretStores[${i}]`} prefix />
                    </Td>
                    <Td>{storeTypeLabel(st.type)}</Td>
                    <Td className="max-w-xs truncate">
                      <Mono className="text-zinc-500" title={storeSummary(st)}>
                        {storeSummary(st)}
                      </Mono>
                    </Td>
                    <Td>
                      <div className="flex flex-col gap-0.5">
                        <Badge tone={health.tone === 'zinc' ? 'gray' : health.tone} dot title={live?.lastError}>
                          {health.label}
                        </Badge>
                        {live && (
                          <span className="text-2xs text-zinc-500">
                            {live.references} reference{live.references === 1 ? '' : 's'} · {live.cached} in memory
                            {live.lastSuccess && <> · read {relativeTime(live.lastSuccess)}</>}
                          </span>
                        )}
                        {health.tone === 'red' && live?.lastError && <span className="max-w-xs truncate text-2xs text-red-600 dark:text-red-400">{live.lastError}</span>}
                      </div>
                    </Td>
                    <Td>
                      <div className="flex justify-end gap-1">
                        <Button size="sm" variant="ghost" icon={<Send className="h-3.5 w-3.5" />} loading={test.isPending && test.variables === st} onClick={() => test.mutate(st)}>
                          Test
                        </Button>
                        <IconButton label="Edit" icon={<Pencil className="h-3.5 w-3.5" />} onClick={() => setEditing({ index: i, s: st })} />
                        <IconButton
                          label="Remove"
                          variant="danger-ghost"
                          icon={<Trash2 className="h-3.5 w-3.5" />}
                          onClick={async () => {
                            const r = await confirm({
                              title: `Remove secret store ${st.name}?`,
                              message: 'Sites that use it must be changed first: saving is refused while a variable or token references it.',
                              confirmLabel: 'Remove',
                              danger: true,
                            });
                            if (r.ok)
                              update((x) => {
                                x.secretStores = x.secretStores.filter((_, j) => j !== i);
                              });
                          }}
                        />
                      </div>
                    </Td>
                  </Tr>
                );
              })}
            </TBody>
          </Table>
        )}
      </Card>

      <Callout tone="info" icon={<ShieldCheck />} title="How values are used">
        Values are read when an instance starts or recycles, a task runs or a deployment builds, and kept in memory for the store's cache time so a
        recycle does not ask the store once per instance. They are never written to disk, logs or backups, and no page shows them — a test only says
        whether a reference resolves. When a store cannot be reached, the last value read is used (a <Mono>secret.stale</Mono> event says so); without
        one, the start fails with a <Mono>secret.failed</Mono> event. Vaultwarden does not implement Secrets Manager: use the official Bitwarden server.
      </Callout>

      <Dialog open={adding} onClose={() => setAdding(false)} title="Add secret store" size="md">
        <div className="space-y-2">
          {STORE_TYPES.map((t) => (
            <button
              key={t.value}
              type="button"
              onClick={() => {
                setAdding(false);
                setEditing({ index: -1, s: newStore(t.value) });
              }}
              className="block w-full rounded-md border border-zinc-200 px-3 py-2.5 text-left hover:border-accent-500 hover:bg-accent-50/50 dark:border-zinc-800 dark:hover:bg-accent-500/5"
            >
              <span className="text-[13px] font-medium">{t.label}</span>
              <span className="block text-xs text-zinc-500 dark:text-zinc-400">{t.description}</span>
            </button>
          ))}
        </div>
      </Dialog>
      <StoreDialog
        value={editing?.s ?? null}
        others={list.filter((_, j) => j !== editing?.index).map((x) => x.name)}
        onClose={() => setEditing(null)}
        onApply={(st) => {
          if (editing)
            update((x) => {
              if (editing.index < 0) x.secretStores.push(st);
              else x.secretStores[editing.index] = st;
            });
          setEditing(null);
        }}
      />
    </div>
  );
}

function StoreDialog({
  value,
  others,
  onClose,
  onApply,
}: {
  value: SecretStore | null;
  others: string[];
  onClose: () => void;
  onApply: (s: SecretStore) => void;
}) {
  const [d, setD] = useState<SecretStore>(newStore('vault'));
  const [touched, setTouched] = useState(false);
  const [ref, setRef] = useState('');
  const [result, setResult] = useState<SecretTestResult | null>(null);
  useEffect(() => {
    if (value) setD(JSON.parse(JSON.stringify(value)) as SecretStore);
    setTouched(false);
    setRef('');
    setResult(null);
  }, [value]);
  const problem = storeProblem(d, others, value);
  const err = (field: string) => (touched && problem?.field === field ? problem.message : null);
  const refErr = ref.trim() ? refProblem(d.type, ref) : null;
  const test = useMutation({
    mutationFn: () => secretStoresApi.test({ ...d, name: d.name.trim() || 'test' }, ref.trim()),
    onSuccess: setResult,
    onError: (e) => setResult({ ok: false, error: errorMessage(e) }),
  });
  const v = d.vault;
  const inf = d.infisical;
  const bw = d.bitwarden;
  const setVault = (p: Partial<NonNullable<SecretStore['vault']>>) => setD({ ...d, vault: { ...v!, ...p } });
  const setInf = (p: Partial<NonNullable<SecretStore['infisical']>>) => setD({ ...d, infisical: { ...inf!, ...p } });
  const setBw = (p: Partial<NonNullable<SecretStore['bitwarden']>>) => setD({ ...d, bitwarden: { ...bw!, ...p } });
  const bwMode = d.url ? 'selfhosted' : bw?.region === 'eu' ? 'eu' : 'us';

  return (
    <Dialog
      open={!!value}
      onClose={onClose}
      size="lg"
      icon={<KeyRound />}
      title={`${value?.id ? 'Edit' : 'Add'} ${storeTypeLabel(d.type)}`}
      onSubmit={() => {
        setTouched(true);
        if (problem) return;
        onApply({ ...d, name: d.name.trim() });
      }}
      footer={
        <>
          <Button icon={<Send className="h-3.5 w-3.5" />} className="mr-auto" loading={test.isPending} disabled={!!problem && problem.field !== 'name'} onClick={() => test.mutate()}>
            Test{ref.trim() ? ' and read' : ''}
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
          <Field label="Name" error={err('name')} hint="What references use: secretref:<name>/<secret>.">
            <Input mono value={d.name} onChange={(e) => setD({ ...d, name: e.target.value.replace(/\s/g, '') })} placeholder={{ vault: 'vault', infisical: 'infisical', bitwarden: 'bitwarden' }[d.type]} />
          </Field>
          {d.type !== 'bitwarden' && (
            <Field
              label={d.type === 'vault' ? 'Address' : 'Server URL'}
              error={err('url')}
              hint={d.type === 'vault' ? 'Vault or OpenBao over https (http:// only on localhost), e.g. https://vault.example.com:8200' : `Empty for Infisical Cloud (${DEFAULT_INFISICAL_URL}); EU: https://eu.infisical.com. Self-hosted: https (http:// only on localhost).`}
            >
              <Input mono value={d.url} onChange={(e) => setD({ ...d, url: e.target.value.trim() })} placeholder={d.type === 'vault' ? 'https://vault.example.com:8200' : DEFAULT_INFISICAL_URL} />
            </Field>
          )}
        </Grid>

        {d.type === 'vault' && v && (
          <>
            <Field label="Authentication">
              <Radio
                value={v.auth}
                onChange={(a) => setVault({ auth: a })}
                options={[
                  { value: 'token', label: 'Token', description: 'A periodic token is renewed automatically.' },
                  { value: 'approle', label: 'AppRole', description: 'NodeHoster signs in, renews its token and signs in again at its max TTL.' },
                ]}
              />
            </Field>
            {v.auth === 'token' ? (
              <Field label="Token" error={err('vault.token')}>
                <SecretInput value={v.token} onChange={(t) => setVault({ token: t })} allowClear={false} placeholder="hvs.…" />
              </Field>
            ) : (
              <Grid cols={3}>
                <Field label="Role ID" error={err('vault.roleId')}>
                  <Input mono value={v.roleId ?? ''} onChange={(e) => setVault({ roleId: e.target.value.trim() })} autoComplete="off" />
                </Field>
                <Field label="Secret ID" error={err('vault.secretId')}>
                  <SecretInput value={v.secretId} onChange={(t) => setVault({ secretId: t })} allowClear={false} />
                </Field>
                <Field label="AppRole mount" error={err('vault.authMount')}>
                  <Input mono value={v.authMount ?? ''} onChange={(e) => setVault({ authMount: e.target.value.trim() })} placeholder="approle" />
                </Field>
              </Grid>
            )}
            <Grid cols={3}>
              <Field label="KV mount" error={err('vault.mount')} hint="The secrets engine's path.">
                <Input mono value={v.mount} onChange={(e) => setVault({ mount: e.target.value.trim() })} placeholder="secret" />
              </Field>
              <Field label="KV version">
                <Select value={v.kvVersion} onChange={(x) => setVault({ kvVersion: Number(x) })} options={[{ value: 2, label: 'Version 2 (versioned)' }, { value: 1, label: 'Version 1' }]} />
              </Field>
              <Field label="Namespace" error={err('vault.namespace')} hint="Vault Enterprise / OpenBao; empty for none.">
                <Input mono value={v.namespace ?? ''} onChange={(e) => setVault({ namespace: e.target.value.trim() })} />
              </Field>
            </Grid>
          </>
        )}

        {d.type === 'infisical' && inf && (
          <Grid>
            <Field label="Client ID" error={err('infisical.clientId')} hint="Of a machine identity with Universal Auth.">
              <Input mono value={inf.clientId} onChange={(e) => setInf({ clientId: e.target.value.trim() })} autoComplete="off" />
            </Field>
            <Field label="Client secret" error={err('infisical.clientSecret')}>
              <SecretInput value={inf.clientSecret} onChange={(t) => setInf({ clientSecret: t })} allowClear={false} />
            </Field>
            <Field label="Project ID" error={err('infisical.projectId')} hint="Project settings → Project ID.">
              <Input mono value={inf.projectId} onChange={(e) => setInf({ projectId: e.target.value.trim() })} />
            </Field>
            <Field label="Environment" error={err('infisical.environment')} hint="The environment's slug.">
              <Input mono value={inf.environment} onChange={(e) => setInf({ environment: e.target.value.trim() })} placeholder="prod" />
            </Field>
          </Grid>
        )}

        {d.type === 'bitwarden' && bw && (
          <>
            <Field label="Access token" error={err('bitwarden.accessToken')} hint="Machine accounts → Access tokens. Shown once by Bitwarden; stored encrypted here.">
              <SecretInput value={bw.accessToken} onChange={(t) => setBw({ accessToken: t })} allowClear={false} placeholder="0.xxxxxxxx-….xxxx:xxxx==" />
            </Field>
            <Field label="Server" error={err('bitwarden.region')}>
              <Radio
                value={bwMode}
                onChange={(m) => {
                  if (m === 'selfhosted') setD({ ...d, url: d.url || 'https://', bitwarden: { ...bw, region: '' } });
                  else setD({ ...d, url: '', bitwarden: { ...bw, region: m as 'us' | 'eu' } });
                }}
                options={[
                  { value: 'us', label: 'Bitwarden cloud (US)' },
                  { value: 'eu', label: 'Bitwarden cloud (EU)' },
                  { value: 'selfhosted', label: 'Self-hosted' },
                ]}
              />
            </Field>
            {bwMode === 'selfhosted' && (
              <Field label="Server URL" error={err('url')} hint="The base URL of the Bitwarden server, https (http:// only on localhost); its API and identity server are at /api and /identity.">
                <Input mono value={d.url} onChange={(e) => setD({ ...d, url: e.target.value.trim() })} placeholder="https://bitwarden.example.com" />
              </Field>
            )}
            <Callout tone="info">Vaultwarden does not implement Secrets Manager; a self-hosted store must be the official Bitwarden server with Secrets Manager enabled.</Callout>
          </>
        )}

        <Grid>
          <Field label="Cache values for" error={err('cacheTtlSec')} hint="Seconds a value is reused before the store is asked again.">
            <NumberInput value={d.cacheTtlSec} onChange={(n) => setD({ ...d, cacheTtlSec: n })} suffix="s" />
          </Field>
          <Field label="Recycle sites when a secret changes" error={err('watchIntervalSec')} hint="Check every N seconds; 0 = off. Sites recycle without downtime.">
            <NumberInput value={d.watchIntervalSec} onChange={(n) => setD({ ...d, watchIntervalSec: n })} suffix="s" />
          </Field>
        </Grid>
        {d.type !== 'bitwarden' || bwMode === 'selfhosted' ? (
          <Field label="CA certificate" error={err('caCert')} hint="PEM, for a self-hosted server with a private CA. Trusted in addition to Windows' certificate store; verification cannot be turned off.">
            <Textarea mono rows={3} value={d.caCert ?? ''} onChange={(e) => setD({ ...d, caCert: e.target.value })} placeholder="-----BEGIN CERTIFICATE-----" />
          </Field>
        ) : null}

        <Field label="Test with a reference (optional)" error={refErr} hint={`${refHint(d.type)} The value is never shown.`}>
          <Input mono value={ref} onChange={(e) => setRef(e.target.value)} placeholder={refPlaceholder(d.type)} />
        </Field>

        {result && (
          <Callout tone={result.ok ? 'success' : 'danger'} icon={result.ok ? <CheckCircle2 /> : <XCircle />} title={result.ok ? 'The store works' : 'The test failed'}>
            {result.detail && <div>{result.detail}</div>}
            {result.error && <div>{result.error}</div>}
          </Callout>
        )}
        {(d.vault?.token === SECRET || d.vault?.secretId === SECRET || d.infisical?.clientSecret === SECRET || d.bitwarden?.accessToken === SECRET) && (
          <p className="text-xs text-zinc-500 dark:text-zinc-400">
            Stored credentials are used for the test until you replace them, and only with the server they were entered for.
          </p>
        )}
        {touched && problem && (problem.field === 'bitwarden.apiUrl' || problem.field === 'bitwarden.identityUrl') && (
          <p className="text-xs text-red-600 dark:text-red-400">
            {problem.field === 'bitwarden.apiUrl' ? 'API URL' : 'Identity URL'}: {problem.message}
          </p>
        )}
      </div>
    </Dialog>
  );
}
