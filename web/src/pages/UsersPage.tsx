import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { KeyRound, MoreHorizontal, Pencil, Plus, ShieldCheck, Trash2, UserPlus, Users } from 'lucide-react';
import { usersApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { Role, User } from '@/api/types';
import { useMe } from '@/hooks/useAuth';
import { Button, IconButton } from '@/components/Button';
import { Card, EmptyState, Loading, PageHeader } from '@/components/Layout';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Badge } from '@/components/Badge';
import { RoleBadge } from '@/components/StatusBadges';
import { Dialog } from '@/components/Dialog';
import { ErrorBox, Field, FormErrorBanner, FormErrors } from '@/components/Field';
import { Input } from '@/components/Input';
import { Radio, Switch } from '@/components/Switch';
import { Menu } from '@/components/Menu';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { formatDate, relativeTime } from '@/lib/format';

const ROLES: { value: Role; label: string; description: string }[] = [
  { value: 'admin', label: 'Administrator', description: 'Everything, including settings, users and secrets.' },
  { value: 'operator', label: 'Operator', description: 'Start, stop, restart and deploy sites.' },
  { value: 'viewer', label: 'Viewer', description: 'Read-only access.' },
];

export function UsersPage() {
  const q = useQuery({ queryKey: qk.users, queryFn: usersApi.list });
  const me = useMe();
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<User | null>(null);
  const [resetting, setResetting] = useState<User | null>(null);

  const del = useMutation({
    mutationFn: (u: User) => usersApi.remove(u.id),
    onSuccess: (_r, u) => {
      toast.success(`${u.username} deleted`);
      void qc.invalidateQueries({ queryKey: qk.users });
    },
    onError: (e) => toast.error('Could not delete user', e),
  });

  const users = [...(q.data ?? [])].sort((a, b) => a.username.localeCompare(b.username));
  const myId = me.data?.user.id;

  return (
    <div>
      <PageHeader
        title="Users"
        description="Console and API accounts. Roles control what each user can change."
        actions={
          <Button variant="primary" icon={<UserPlus className="h-4 w-4" />} onClick={() => setCreating(true)}>
            New user
          </Button>
        }
      />
      {q.isError && <ErrorBox className="mb-4">{errorMessage(q.error)}</ErrorBox>}
      <Card flush>
        {q.isPending ? (
          <Loading />
        ) : users.length === 0 ? (
          <EmptyState icon={<Users />} title="No users" action={<Button onClick={() => setCreating(true)}>New user</Button>} />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th>User name</Th>
                <Th>Role</Th>
                <Th>2FA</Th>
                <Th>Status</Th>
                <Th>Last sign-in</Th>
                <Th>Created</Th>
                <Th className="w-10" />
              </tr>
            </THead>
            <TBody>
              {users.map((u) => (
                <Tr key={u.id}>
                  <Td className="font-medium">
                    {u.username}
                    {u.id === myId && <span className="ml-2 text-xs font-normal text-zinc-500">(you)</span>}
                  </Td>
                  <Td>
                    <RoleBadge role={u.role} />
                  </Td>
                  <Td>{u.totpEnabled ? <Badge tone="green"><ShieldCheck className="h-3 w-3" /> on</Badge> : <span className="text-xs text-zinc-400">off</span>}</Td>
                  <Td>{u.disabled ? <Badge tone="red">disabled</Badge> : <Badge tone="green">active</Badge>}</Td>
                  <Td className="text-zinc-500" title={u.lastLogin ? new Date(u.lastLogin).toLocaleString() : undefined}>
                    {u.lastLogin ? relativeTime(u.lastLogin) : 'Never'}
                  </Td>
                  <Td className="text-zinc-500">{formatDate(u.createdAt)}</Td>
                  <Td>
                    <Menu
                      trigger={(p) => <IconButton label="Actions" icon={<MoreHorizontal className="h-4 w-4" />} {...p} />}
                      items={[
                        { label: 'Edit role & status', icon: <Pencil />, onSelect: () => setEditing(u) },
                        { label: 'Reset password', icon: <KeyRound />, onSelect: () => setResetting(u) },
                        'separator',
                        {
                          label: 'Delete',
                          icon: <Trash2 />,
                          danger: true,
                          disabled: u.id === myId,
                          onSelect: async () => {
                            const r = await confirm({
                              title: `Delete ${u.username}?`,
                              message: 'The user is signed out everywhere and their API tokens stop working.',
                              confirmLabel: 'Delete user',
                              danger: true,
                            });
                            if (r.ok) del.mutate(u);
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
      </Card>
      <CreateUserDialog open={creating} onClose={() => setCreating(false)} />
      <EditUserDialog user={editing} isSelf={editing?.id === myId} onClose={() => setEditing(null)} />
      <ResetPasswordDialog user={resetting} onClose={() => setResetting(null)} />
    </div>
  );
}

function CreateUserDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [role, setRole] = useState<Role>('operator');
  useEffect(() => {
    if (open) {
      setUsername('');
      setPassword('');
      setRole('operator');
      m.reset();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  const m = useMutation({
    mutationFn: () => usersApi.create({ username: username.trim(), password, role }),
    onSuccess: (u) => {
      toast.success(`User ${u.username} created`, 'They will be asked to choose a new password at first sign-in.');
      void qc.invalidateQueries({ queryKey: qk.users });
      onClose();
    },
  });
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="New user"
      onSubmit={() => username.trim() && password && m.mutate()}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" icon={<Plus className="h-3.5 w-3.5" />} disabled={!username.trim() || !password} loading={m.isPending}>
            Create user
          </Button>
        </>
      }
    >
      <FormErrors error={m.error}>
        <div className="space-y-4">
          <FormErrorBanner />
          <Field label="User name" path="username">
            <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" spellCheck={false} />
          </Field>
          <Field label="Initial password" path="password" hint="Share it securely.">
            <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" />
          </Field>
          <Field label="Role" path="role">
            <Radio value={role} onChange={setRole} options={ROLES} className="flex-col" />
          </Field>
        </div>
      </FormErrors>
    </Dialog>
  );
}

function EditUserDialog({ user, isSelf, onClose }: { user: User | null; isSelf: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [role, setRole] = useState<Role>('viewer');
  const [disabled, setDisabled] = useState(false);
  useEffect(() => {
    if (user) {
      setRole(user.role);
      setDisabled(user.disabled);
    }
    m.reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [user]);
  const m = useMutation({
    mutationFn: () => usersApi.update(user!.id, { role, disabled }),
    onSuccess: () => {
      toast.success('User updated');
      void qc.invalidateQueries({ queryKey: qk.users });
      onClose();
    },
  });
  return (
    <Dialog
      open={!!user}
      onClose={onClose}
      title={`Edit ${user?.username ?? ''}`}
      onSubmit={() => m.mutate()}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" loading={m.isPending}>
            Save
          </Button>
        </>
      }
    >
      <FormErrors error={m.error}>
        <div className="space-y-4">
          <FormErrorBanner />
          <Field label="Role" path="role" hint={isSelf ? 'You cannot change your own role.' : undefined}>
            <fieldset disabled={isSelf}>
              <Radio value={role} onChange={setRole} options={ROLES} className="flex-col" />
            </fieldset>
          </Field>
          <Switch
            checked={disabled}
            onChange={setDisabled}
            disabled={isSelf}
            label="Disable account"
            description="Blocks sign-in and API tokens without deleting the user."
          />
        </div>
      </FormErrors>
    </Dialog>
  );
}

function ResetPasswordDialog({ user, onClose }: { user: User | null; onClose: () => void }) {
  const toast = useToast();
  const [password, setPassword] = useState('');
  const [confirmPw, setConfirmPw] = useState('');
  useEffect(() => {
    setPassword('');
    setConfirmPw('');
    m.reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [user]);
  const m = useMutation({
    mutationFn: () => usersApi.update(user!.id, { password }),
    onSuccess: () => {
      toast.success(`Password reset for ${user?.username}`);
      onClose();
    },
  });
  const mismatch = confirmPw && password !== confirmPw ? 'Passwords do not match' : null;
  return (
    <Dialog
      open={!!user}
      onClose={onClose}
      size="sm"
      title={`Reset password for ${user?.username ?? ''}`}
      description="The user will have to choose a new password at next sign-in."
      onSubmit={() => password && !mismatch && m.mutate()}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!password || password !== confirmPw} loading={m.isPending}>
            Reset password
          </Button>
        </>
      }
    >
      <FormErrors error={m.error}>
        <div className="space-y-4">
          <FormErrorBanner />
          <Field label="New password" path="password">
            <Input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
          </Field>
          <Field label="Confirm" error={mismatch}>
            <Input type="password" autoComplete="new-password" value={confirmPw} onChange={(e) => setConfirmPw(e.target.value)} />
          </Field>
        </div>
      </FormErrors>
    </Dialog>
  );
}
