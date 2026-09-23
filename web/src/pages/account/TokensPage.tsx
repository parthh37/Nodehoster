import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { KeyRound, Plus, Trash2 } from 'lucide-react';
import { tokensApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { CreatedToken } from '@/api/types';
import { Button, IconButton } from '@/components/Button';
import { Card, Callout, EmptyState, Loading, Mono, PageHeader } from '@/components/Layout';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Dialog } from '@/components/Dialog';
import { ErrorBox, Field } from '@/components/Field';
import { Input, Select } from '@/components/Input';
import { CopyField } from '@/components/CopyButton';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { daysUntil, formatDate, relativeTime } from '@/lib/format';
import { Badge } from '@/components/Badge';

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

  return (
    <div>
      <PageHeader
        title="API tokens"
        icon={<KeyRound className="h-4 w-4" />}
        description={
          <>
            Personal tokens for automation and CI. Send them as <Mono>Authorization: Bearer nh_…</Mono>. Tokens act with your role.
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
  const m = useMutation({
    mutationFn: () => tokensApi.create(name.trim(), days === '0' ? undefined : Number(days)),
    onSuccess: (t) => {
      setCreated(t);
      void qc.invalidateQueries({ queryKey: qk.tokens });
    },
  });

  const close = () => {
    setName('');
    setDays('90');
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
      <div className="space-y-3">
        {m.isError && <ErrorBox>{errorMessage(m.error)}</ErrorBox>}
        <Field label="Name" hint="What will use it, e.g. “GitHub Actions deploy” or “Prometheus”.">
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
      </div>
    </Dialog>
  );
}
