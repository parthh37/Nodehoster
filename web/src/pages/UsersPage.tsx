import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { KeyRound, MoreHorizontal, Pencil, Plus, ShieldCheck, Trash2, UserPlus, Users } from 'lucide-react';
import { sitesApi, usersApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { Role, SiteGrant, SiteRole, User } from '@/api/types';
import { useMe } from '@/hooks/useAuth';
import { Button, IconButton } from '@/components/Button';
import { Card, EmptyState, Loading, PageHeader } from '@/components/Layout';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Badge } from '@/components/Badge';
import { RoleBadge } from '@/components/StatusBadges';
import { Dialog } from '@/components/Dialog';
import { ErrorBox, Field, FormErrorBanner, FormErrors } from '@/components/Field';
import { Input, Select } from '@/components/Input';
import { Radio, Switch } from '@/components/Switch';
import { Menu } from '@/components/Menu';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { formatDate, relativeTime } from '@/lib/format';
import { describeSiteAccess, setGrant } from '@/lib/access';

type ServerRole = Exclude<Role, 'sites'>;

const ROLES: { value: ServerRole; label: string; description: string }[] = [
  { value: 'admin', label: 'Administrator', description: 'Everything, including settings, users and secrets.' },
  { value: 'operator', label: 'Operator', description: 'Start, stop, restart and deploy sites.' },
  { value: 'viewer', label: 'Viewer', description: 'Read-only access.' },
];

type Scope = 'server' | 'sites';

const SCOPES: { value: Scope; label: string; description: string }[] = [
  { value: 'server', label: 'All sites', description: 'A role on the whole server.' },
  { value: 'sites', label: 'Selected sites', description: 'Chosen sites only; nothing server-wide.' },
];

const GRANT_OPTIONS = [
  { value: '', label: 'No access' },
  { value: 'viewer', label: 'Viewer' },
  { value: 'operator', label: 'Operator' },
];

/** The role, scope and grants a user dialog edits. */
interface AccessDraft {
  scope: Scope;
  role: ServerRole;
  grants: SiteGrant[];
}

const draftOf = (u?: User): AccessDraft =>
  u?.role === 'sites'
    ? { scope: 'sites', role: 'viewer', grants: u.sites ?? [] }
    : { scope: 'server', role: (u?.role as ServerRole | undefined) ?? 'operator', grants: [] };

const accessBody = (d: AccessDraft): { role: Role; sites?: SiteGrant[] } =>
  d.scope === 'sites' ? { role: 'sites', sites: d.grants } : { role: d.role };

/**
 * Site access, as IIS Manager permissions: a role on the whole server, or
 * viewer/operator on selected sites. Changing a site's configuration always
 * needs a server administrator.
 */
function AccessEditor({ value, onChange, disabled }: { value: AccessDraft; onChange: (d: AccessDraft) => void; disabled?: boolean }) {
  const sites = useQuery({ queryKey: qk.sites, queryFn: sitesApi.list, staleTime: 30_000 });
  const list = [...(sites.data ?? [])].sort((a, b) => a.name.localeCompare(b.name));
  const roleOn = (id: string) => value.grants.find((g) => g.siteId === id)?.role ?? '';
  return (
    <fieldset disabled={disabled} className="space-y-4">
      <Field label="Site access" path="role">
        <Radio value={value.scope} onChange={(scope) => onChange({ ...value, scope })} options={SCOPES} className="flex-col" />
      </Field>
      {value.scope === 'server' ? (
        <Field label="Role">
          <Radio value={value.role} onChange={(role) => onChange({ ...value, role })} options={ROLES} className="flex-col" />
        </Field>
      ) : (
        <Field label="Sites" path="sites" prefix hint="Operators can start, stop, restart and deploy; viewers can only look. Site settings stay with administrators.">
          {sites.isError ? (
            <ErrorBox>{errorMessage(sites.error)}</ErrorBox>
          ) : list.length === 0 ? (
            <p className="text-[13px] text-zinc-500">{sites.isPending ? 'Loading sites…' : 'There are no sites yet.'}</p>
          ) : (
            <div className="max-h-64 divide-y divide-zinc-200 overflow-y-auto rounded-md border border-zinc-200 dark:divide-zinc-800 dark:border-zinc-800">
              {list.map((s) => (
                <div key={s.id} className="flex items-center justify-between gap-3 px-3 py-1.5">
                  <span className="min-w-0 truncate text-[13px] font-medium">{s.name}</span>
                  <Select
                    className="w-36 shrink-0"
                    value={roleOn(s.id)}
                    options={GRANT_OPTIONS}
                    onChange={(r) => onChange({ ...value, grants: setGrant(value.grants, s.id, (r || null) as SiteRole | null) })}
                    aria-label={`Access to ${s.name}`}
                  />
                </div>
              ))}
            </div>
          )}
        </Field>
      )}
    </fieldset>
  );
}

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
  const sites = useQuery({ queryKey: qk.sites, queryFn: sitesApi.list, staleTime: 30_000 });
  const siteName = (id: string) => sites.data?.find((s) => s.id === id)?.name;

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
                <Th>Site access</Th>
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
                  <Td className="text-zinc-500">{describeSiteAccess(u, siteName)}</Td>
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
                        { label: 'Edit access & status', icon: <Pencil />, onSelect: () => setEditing(u) },
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
  const [access, setAccess] = useState<AccessDraft>(draftOf());
  useEffect(() => {
    if (open) {
      setUsername('');
      setPassword('');
      setAccess(draftOf());
      m.reset();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  const m = useMutation({
    mutationFn: () => usersApi.create({ username: username.trim(), password, ...accessBody(access) }),
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
          <AccessEditor value={access} onChange={setAccess} />
        </div>
      </FormErrors>
    </Dialog>
  );
}

function EditUserDialog({ user, isSelf, onClose }: { user: User | null; isSelf: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [access, setAccess] = useState<AccessDraft>(draftOf());
  const [disabled, setDisabled] = useState(false);
  useEffect(() => {
    if (user) {
      setAccess(draftOf(user));
      setDisabled(user.disabled);
    }
    m.reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [user]);
  const m = useMutation({
    // Your own access is not sent: an administrator cannot lock themselves out.
    mutationFn: () => usersApi.update(user!.id, isSelf ? { disabled } : { ...accessBody(access), disabled }),
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
          {isSelf && <p className="text-xs text-zinc-500">You cannot change your own access.</p>}
          <AccessEditor value={access} onChange={setAccess} disabled={isSelf} />
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
