import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { KeyRound, Plus, Trash2 } from 'lucide-react';
import { sitesApi, tokensApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { CreatedToken, Role } from '@/api/types';
import { Button, IconButton } from '@/components/Button';
import { Card, Callout, EmptyState, Loading, Mono, PageHeader } from '@/components/Layout';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Dialog } from '@/components/Dialog';
import { ErrorBox, Field, FormErrorBanner, FormErrors } from '@/components/Field';
import { Checkbox, Switch } from '@/components/Switch';
import { Input, Select } from '@/components/Input';
import { CopyField } from '@/components/CopyButton';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { daysUntil, formatDate, relativeTime } from '@/lib/format';
import { Badge } from '@/components/Badge';
import { usePermissions } from '@/hooks/useAuth';
import { describeTokenRestriction, tokenRoleChoices } from '@/lib/access';

export function TokensPage() {
  const q = useQuery({ queryKey: qk.tokens, queryFn: tokensApi.list });
  const qc = useQueryClient();
  const confirm = useConfirm();
  const toast = useToast();
  const [creating, setCreating] = useState(false);

  const revoke = useMutation({
    mutationFn: (id: string) => tokensApi.revoke(id),
    onSuccess: () => {
      toast.success('Token revoked');
      void qc.invalidateQueries({ queryKey: qk.tokens });
    },
    onError: (e) => toast.error('Could not revoke token', e),
  });

  const tokens = q.data ?? [];
  const sites = useQuery({ queryKey: qk.sites, queryFn: sitesApi.list, staleTime: 30_000 });
  const siteName = (id: string) => sites.data?.find((s) => s.id === id)?.name;

  return (
    <div>
      <PageHeader
        title="API tokens"
        icon={<KeyRound className="h-4 w-4" />}
        description={
          <>
            Personal tokens for automation and CI. Send them as <Mono>Authorization: Bearer nh_…</Mono>. Tokens act with your access, or less if you restrict them.
          </>
        }
        actions={
          <Button variant="primary" icon={<Plus className="h-4 w-4" />} onClick={() => setCreating(true)}>
            New token
          </Button>
        }
      />
      <Card flush>
        {q.isPending ? (
          <Loading />
        ) : q.isError ? (
          <div className="p-4">
            <ErrorBox>{errorMessage(q.error)}</ErrorBox>
          </div>
        ) : tokens.length === 0 ? (
          <EmptyState
            icon={<KeyRound />}
            title="No API tokens"
            description="Create a token to call the NodeHoster API from scripts, CI pipelines or monitoring (e.g. Prometheus scraping /metrics)."
            action={
              <Button variant="primary" icon={<Plus className="h-4 w-4" />} onClick={() => setCreating(true)}>
                New token
              </Button>
            }
          />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th>Name</Th>
                <Th>Token</Th>
                <Th>Access</Th>
                <Th>Created</Th>
                <Th>Last used</Th>
                <Th>Expires</Th>
                <Th className="w-10" />
              </tr>
            </THead>
            <TBody>
              {tokens.map((t) => {
                const d = daysUntil(t.expiresAt);
                return (
                  <Tr key={t.id}>
                    <Td className="font-medium">{t.name}</Td>
                    <Td>
                      <Mono className="text-zinc-500">{t.prefix}…</Mono>
                    </Td>
                    <Td className="text-zinc-500">{describeTokenRestriction(t, siteName)}</Td>
                    <Td className="text-zinc-500">{formatDate(t.createdAt)}</Td>
                    <Td className="text-zinc-500">{t.lastUsed ? relativeTime(t.lastUsed) : 'Never'}</Td>
                    <Td>
                      {!t.expiresAt ? (
                        <span className="text-zinc-500">Never</span>
                      ) : d !== null && d < 0 ? (
                        <Badge tone="red">Expired</Badge>
                      ) : (
                        <span className={d !== null && d < 14 ? 'text-amber-600' : 'text-zinc-500'}>{formatDate(t.expiresAt)}</span>
                      )}
                    </Td>
                    <Td>
                      <IconButton
                        label="Revoke"
                        variant="danger-ghost"
                        icon={<Trash2 className="h-3.5 w-3.5" />}
                        onClick={async () => {
                          const r = await confirm({
                            title: `Revoke "${t.name}"?`,
                            message: 'Anything using this token will immediately lose access.',
                            confirmLabel: 'Revoke token',
                            danger: true,
                          });
                          if (r.ok) revoke.mutate(t.id);
                        }}
                      />
                    </Td>
                  </Tr>
                );
              })}
            </TBody>
          </Table>
        )}
      </Card>
      <CreateTokenDialog open={creating} onClose={() => setCreating(false)} />
    </div>
  );
}

function CreateTokenDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState('');
  const [days, setDays] = useState('90');
  const [created, setCreated] = useState<CreatedToken | null>(null);
  const { access } = usePermissions();
  const [restrict, setRestrict] = useState(false);
  const [role, setRole] = useState<Role | ''>('');
  const [siteIds, setSiteIds] = useState<string[]>([]);
  // The sites list holds exactly the sites you can access.
  const sites = useQuery({ queryKey: qk.sites, queryFn: sitesApi.list, staleTime: 30_000, enabled: open && restrict });
  const roleChoices = tokenRoleChoices(access, siteIds.length > 0);
  const m = useMutation({
    mutationFn: () =>
      tokensApi.create(name.trim(), days === '0' ? undefined : Number(days), restrict ? { role: roleChoices.includes(role as Role) ? role : '', siteIds } : undefined),
    onSuccess: (t) => {
      setCreated(t);
      void qc.invalidateQueries({ queryKey: qk.tokens });
    },
  });

  const close = () => {
    setName('');
    setDays('90');
    setRestrict(false);
    setRole('');
    setSiteIds([]);
    setCreated(null);
    m.reset();
    onClose();
  };

  if (created) {
    return (
      <Dialog
        open={open}
        onClose={close}
        title="Token created"
        description={`"${created.info.name}"`}
        footer={
          <Button variant="primary" onClick={close}>
            Done
          </Button>
        }
      >
        <div className="space-y-3">
          <Callout tone="warning" title="Copy this token now">
            It will not be shown again. Store it in a secret manager or your CI variables.
          </Callout>
          <CopyField value={created.token} />
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog
      open={open}
      onClose={close}
      title="New API token"
      size="sm"
      onSubmit={() => name.trim() && m.mutate()}
      footer={
        <>
          <Button onClick={close}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!name.trim()} loading={m.isPending}>
            Create token
          </Button>
        </>
      }
    >
      <FormErrors error={m.error}>
        <div className="space-y-3">
          <FormErrorBanner />
          <Field label="Name" path="name" hint="What will use it, e.g. “GitHub Actions deploy” or “Prometheus”.">
            <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={64} />
          </Field>
          <Field label="Expires">
            <Select
              value={days}
              onChange={setDays}
              options={[
                { value: '7', label: 'In 7 days' },
                { value: '30', label: 'In 30 days' },
                { value: '90', label: 'In 90 days' },
                { value: '365', label: 'In 1 year' },
                { value: '0', label: 'Never' },
              ]}
            />
          </Field>
          <Switch
            checked={restrict}
            onChange={setRestrict}
            label="Restrict this token"
            description="Give it less than your own access, e.g. a CI token that can only deploy one site. It never has more than you have."
          />
          {restrict && (
            <>
              <Field label="Maximum role" path="role">
                <Select
                  value={roleChoices.includes(role as Role) ? role : ''}
                  onChange={(v) => setRole(v as Role | '')}
                  options={[{ value: '', label: 'Same as mine' }, ...roleChoices.map((r) => ({ value: r, label: r[0].toUpperCase() + r.slice(1) }))]}
                />
              </Field>
              <Field label="Sites" path="siteIds" prefix hint="None selected: every site you can access. With sites, the token can do nothing server-wide.">
                {sites.isError ? (
                  <ErrorBox>{errorMessage(sites.error)}</ErrorBox>
                ) : (
                  <div className="max-h-48 space-y-1.5 overflow-y-auto rounded-md border border-zinc-200 p-2.5 dark:border-zinc-800">
                    {(sites.data ?? []).length === 0 && <p className="text-[13px] text-zinc-500">{sites.isPending ? 'Loading sites…' : 'There are no sites.'}</p>}
                    {[...(sites.data ?? [])]
                      .sort((a, b) => a.name.localeCompare(b.name))
                      .map((s) => (
                        <Checkbox
                          key={s.id}
                          label={s.name}
                          checked={siteIds.includes(s.id)}
                          onChange={(on) => setSiteIds((ids) => (on ? [...ids, s.id] : ids.filter((x) => x !== s.id)))}
                        />
                      ))}
                  </div>
                )}
              </Field>
            </>
          )}
        </div>
      </FormErrors>
    </Dialog>
  );
}
