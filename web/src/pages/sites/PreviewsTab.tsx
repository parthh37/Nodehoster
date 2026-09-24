import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ExternalLink, Eye, GitBranch, GitPullRequest, KeyRound, RefreshCw, ShieldAlert, ShieldCheck, Trash2, Webhook } from 'lucide-react';
import { certsApi, previewsApi, settingsApi, sitesApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { BasicAuthUser, PreviewConfig, PreviewView, Site, SiteView } from '@/api/types';
import { Badge, type Tone } from '@/components/Badge';
import { Button, IconButton } from '@/components/Button';
import { Card, Callout, EmptyState, FormSection, Grid, Loading, Mono, Sections } from '@/components/Layout';
import { ErrorBox, Field } from '@/components/Field';
import { Input, NumberInput, Select } from '@/components/Input';
import { ListEditor, RowsEditor } from '@/components/ListEditor';
import { SecretInput } from '@/components/SecretInput';
import { Switch } from '@/components/Switch';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Dialog } from '@/components/Dialog';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { usePermissions, useSitePermissions } from '@/hooks/useAuth';
import { useNow } from '@/hooks/useNow';
import { formatDateTime, relativeTime } from '@/lib/format';
import { safeHref } from '@/lib/safeHref';
import {
  AUTO_PREVIEW_CERTS_PER_WEEK,
  branchPatternError,
  clientCertPreviewError,
  describePreview,
  exampleHost,
  failureText,
  hostPatternError,
  previewConfigOf,
  previewStateLabel,
  pullRequestWord,
  requiresClientCert,
  splitHostPattern,
  WEBHOOK_EVENTS,
} from '@/lib/previews';
import { runsNode } from '@/lib/siteDefaults';
import { EnvVarsEditor } from './editors/EnvEditor';
import { validateIP } from './editors/RoutingEditor';
import type { SiteEditorProps } from './editors/types';

/**
 * Preview deployments of a git-deployed site: the previews it has (their
 * address, pull request or branch, state; redeploy and delete for
 * operators) and their settings, part of the site draft (administrators).
 */
export function PreviewsTab({ site, update, readOnly, savedSite, dirty }: SiteEditorProps & { savedSite: Site; dirty: boolean }) {
  return (
    <div className="space-y-5">
      <PreviewList site={savedSite} dirty={dirty} />
      <fieldset disabled={readOnly} className="min-w-0">
        <PreviewSettingsCard site={site} update={update} readOnly={readOnly} />
      </fieldset>
    </div>
  );
}

const stateTones: Record<string, Tone> = {
  ready: 'green',
  deploying: 'amber',
  pending: 'amber',
  'awaiting-approval': 'amber',
  failed: 'red',
  deleting: 'gray',
};

export function PreviewStateBadge({ state }: { state: string }) {
  return (
    <Badge tone={stateTones[state] ?? 'gray'} dot pulse={state === 'deploying' || state === 'deleting'} className="capitalize">
      {previewStateLabel(state)}
    </Badge>
  );
}

function PreviewList({ site, dirty }: { site: Site; dirty: boolean }) {
  const { canOperate } = useSitePermissions(site.id);
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const now = useNow(30_000);
  const [branchOpen, setBranchOpen] = useState(false);
  const cfg = previewConfigOf(site);

  const q = useQuery({
    queryKey: qk.sitePreviews(site.id),
    queryFn: () => previewsApi.list(site.id),
    refetchInterval: (query) => ((query.state.data ?? []).some((p) => p.state === 'deploying' || p.state === 'deleting') ? 3000 : 15_000),
  });
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: qk.sitePreviews(site.id) });
    void qc.invalidateQueries({ queryKey: qk.sites, exact: true });
  };

  const redeploy = useMutation({
    mutationFn: (p: PreviewView) => previewsApi.redeploy(site.id, p.id),
    onSuccess: (_, p) => {
      toast.success(`Deploying ${p.preview.host} again`);
      refresh();
    },
    onError: (e) => toast.error('Could not redeploy the preview', e),
  });
  const approve = useMutation({
    mutationFn: (p: PreviewView) => previewsApi.approve(site.id, p.id, p.preview.commit),
    onSuccess: (_, p) => {
      toast.success(`Approved ${(p.preview.commit ?? '').slice(0, 7) || 'the preview'}: deploying ${p.preview.host}`);
      refresh();
    },
    onError: (e) => {
      toast.error('Could not approve the preview', e);
      refresh();
    },
  });
  const remove = useMutation({
    mutationFn: (p: PreviewView) => previewsApi.remove(site.id, p.id),
    onSuccess: (_, p) => {
      toast.success(`Deleting ${p.preview.host}`);
      refresh();
    },
    onError: (e) => toast.error('Could not delete the preview', e),
  });

  const list = q.data ?? [];
  return (
    <Card
      title="Preview deployments"
      description={
        cfg.enabled ? (
          `A temporary site per pull request or previewed branch, deleted when the pull request is closed or merged or the branch is deleted${
            cfg.expireDays > 0 ? `, or after ${cfg.expireDays} days without a push` : ''
          }.`
        ) : (
          'Previews are turned off for this site.'
        )
      }
      actions={
        canOperate && (
          <Button
            icon={<GitBranch className="h-3.5 w-3.5" />}
            disabled={!cfg.enabled || dirty}
            title={!cfg.enabled ? 'Turn previews on and save first' : dirty ? 'Save your changes first' : undefined}
            onClick={() => setBranchOpen(true)}
          >
            Preview a branch
          </Button>
        )
      }
      flush
    >
      {q.isPending ? (
        <Loading />
      ) : q.isError ? (
        <ErrorBox className="m-4">{errorMessage(q.error)}</ErrorBox>
      ) : list.length === 0 ? (
        <EmptyState
          icon={<GitPullRequest />}
          title="No previews"
          description={
            cfg.enabled
              ? 'Open a pull request (or push to a previewed branch): the push webhook creates its preview.'
              : 'Turn previews on below to get a temporary site for every pull request.'
          }
        />
      ) : (
        <Table>
          <THead>
            <tr>
              <Th>Preview</Th>
              <Th>Address</Th>
              <Th>State</Th>
              <Th>Commit</Th>
              <Th>Last push</Th>
              <Th className="w-28" />
            </tr>
          </THead>
          <TBody>
            {list.map((p) => (
              <Tr key={p.id}>
                <Td className="max-w-[18rem]">
                  <div className="flex items-center gap-1.5">
                    {p.preview.kind === 'pr' ? <GitPullRequest className="h-3.5 w-3.5 shrink-0 text-zinc-400" /> : <GitBranch className="h-3.5 w-3.5 shrink-0 text-zinc-400" />}
                    <Link to={`/sites/${p.id}`} className="truncate font-medium hover:underline">
                      {describePreview(p.preview)}
                    </Link>
                    {p.preview.fork && (
                      <Badge tone="amber" title="From a fork: its code comes from outside the repository">
                        fork
                      </Badge>
                    )}
                    {p.preview.prUrl && (
                      <a href={safeHref(p.preview.prUrl)} target="_blank" rel="noreferrer" className="text-zinc-400 hover:text-zinc-700" title={`Open the ${pullRequestWord(p.preview.provider)}`}>
                        <ExternalLink className="h-3 w-3" />
                      </a>
                    )}
                  </div>
                  {(p.preview.title || p.preview.author) && (
                    <p className="truncate text-xs text-zinc-500">
                      {p.preview.title}
                      {p.preview.author && <span className="text-zinc-400"> · {p.preview.author}</span>}
                    </p>
                  )}
                </Td>
                <Td>
                  {p.preview.awaitingApproval && !p.preview.approvedCommit ? (
                    <span className="font-mono text-[12.5px] text-zinc-400" title="Not served before it is approved">
                      {p.preview.host}
                    </span>
                  ) : (
                    <a href={safeHref(p.preview.url)} target="_blank" rel="noreferrer" className="font-mono text-[12.5px] text-accent-700 hover:underline dark:text-accent-400">
                      {p.preview.host}
                    </a>
                  )}
                </Td>
                <Td>
                  <PreviewStateBadge state={p.state} />
                  {p.state === 'failed' && p.lastDeployment?.message && (
                    <p className="max-w-[16rem] truncate text-xs text-red-600 dark:text-red-400" title={p.lastDeployment.message}>
                      {failureText(p.lastDeployment.message)}
                    </p>
                  )}
                </Td>
                <Td>
                  {p.preview.commit ? <Mono title={p.preview.commit}>{p.preview.commit.slice(0, 7)}</Mono> : <span className="text-zinc-400">—</span>}
                  {p.preview.awaitingApproval && p.preview.approvedCommit && (
                    <p className="text-xs text-zinc-500" title={`Approved by ${p.preview.approvedBy ?? '?'}; still served`}>
                      serving <Mono>{p.preview.approvedCommit.slice(0, 7)}</Mono>
                    </p>
                  )}
                </Td>
                <Td className="whitespace-nowrap text-xs text-zinc-500" title={formatDateTime(p.preview.lastPush)}>
                  {relativeTime(p.preview.lastPush, now)}
                </Td>
                <Td>
                  <div className="flex justify-end gap-0.5">
                    <Link to={`/sites/${p.id}`}>
                      <IconButton label="Open the preview's site" icon={<Eye className="h-3.5 w-3.5" />} />
                    </Link>
                    {canOperate && (
                      <>
                        {p.state === 'awaiting-approval' ? (
                          <IconButton
                            label="Approve and deploy this commit"
                            icon={<ShieldCheck className="h-3.5 w-3.5" />}
                            disabled={approve.isPending}
                            onClick={async () => {
                              const commit = (p.preview.commit ?? '').slice(0, 7) || 'its head commit';
                              const r = await confirm({
                                title: `Approve ${describePreview(p.preview)} at ${commit}?`,
                                message: p.preview.fork
                                  ? `This ${pullRequestWord(p.preview.provider)} comes from a fork. Its install and build commands, then its code, will run on this server (${site.type === 'static' ? "with the service's privileges: static sites cannot run as a separate account" : "with the service's privileges unless the site runs as a separate account"}). Review the changes of commit ${commit} first: only that commit is deployed, and each new push needs approval again.`
                                  : `Commit ${commit} will be built and deployed on this server. Only that commit is deployed; each new push needs approval again.`,
                                danger: !!p.preview.fork,
                                confirmLabel: 'Approve and deploy',
                              });
                              if (r.ok) approve.mutate(p);
                            }}
                          />
                        ) : (
                          <IconButton
                            label="Deploy again"
                            icon={<RefreshCw className="h-3.5 w-3.5" />}
                            disabled={p.state === 'deploying' || p.state === 'deleting'}
                            onClick={() => redeploy.mutate(p)}
                          />
                        )}
                        <IconButton
                          label="Delete preview"
                          variant="danger-ghost"
                          icon={<Trash2 className="h-3.5 w-3.5" />}
                          disabled={p.state === 'deleting'}
                          onClick={async () => {
                            const r = await confirm({
                              title: `Delete ${p.preview.host}?`,
                              message: 'Its site, releases, logs and automatic certificate are removed. A new push to it creates it again.',
                              danger: true,
                              confirmLabel: 'Delete preview',
                            });
                            if (r.ok) remove.mutate(p);
                          }}
                        />
                      </>
                    )}
                  </div>
                </Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      )}
      <BranchDialog open={branchOpen} onClose={() => setBranchOpen(false)} site={site} onDone={refresh} />
    </Card>
  );
}

function BranchDialog({ open, onClose, site, onDone }: { open: boolean; onClose: () => void; site: Site; onDone: () => void }) {
  const [branch, setBranch] = useState('');
  const toast = useToast();
  const production = site.deploy.git.branch ?? '';
  const deploy = useMutation({
    mutationFn: () => previewsApi.deployBranch(site.id, branch.trim()),
    onSuccess: () => {
      toast.success(`Deploying branch ${branch.trim()}`);
      onDone();
      setBranch('');
      onClose();
    },
  });
  const err = branch.trim() && branch.trim() === production ? 'The production branch is the site itself' : null;
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Preview a branch"
      description="Deploys the branch's latest commit as a preview (or deploys its preview again), whatever the branch patterns say."
      onSubmit={() => !err && branch.trim() && deploy.mutate()}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" loading={deploy.isPending} disabled={!branch.trim() || !!err}>
            Deploy preview
          </Button>
        </>
      }
    >
      <Field label="Branch" error={err}>
        <Input mono autoFocus value={branch} placeholder="feature/checkout" onChange={(e) => setBranch(e.target.value)} />
      </Field>
      {deploy.isError && <ErrorBox className="mt-3">{errorMessage(deploy.error)}</ErrorBox>}
    </Dialog>
  );
}

/** The preview settings, edited in the site draft. */
function PreviewSettingsCard({ site, update, readOnly }: SiteEditorProps) {
  const { isAdmin, siteScoped } = usePermissions();
  const p = previewConfigOf(site);
  const set = (patch: Partial<PreviewConfig>) =>
    update((d) => {
      d.deploy.previews = { ...previewConfigOf(d), ...patch };
    });
  const certs = useQuery({ queryKey: qk.certs, queryFn: certsApi.list, staleTime: 30_000, enabled: !siteScoped && p.protocol === 'https' });
  const settings = useQuery({ queryKey: qk.settings, queryFn: settingsApi.get, enabled: isAdmin && p.certMode === 'wildcard' });
  const [, suffix] = splitHostPattern(p.hostPattern.trim().toLowerCase());
  const patternErr = p.enabled && p.hostPattern ? hostPatternError(p.hostPattern) : null;

  const missing: string[] = [];
  if (!site.deploy.git.repo) missing.push('a Git repository');
  if (!site.deploy.git.branch) missing.push('the production branch');
  if (!site.deploy.webhookSecret) missing.push('a webhook secret');

  const wildcardCerts = (certs.data ?? []).filter((c) => (c.domains ?? []).includes(`*.${suffix}`));
  const enable = (on: boolean) => {
    const patch: Partial<PreviewConfig> = { enabled: on };
    if (on && !p.hostPattern) patch.hostPattern = 'pr-{number}.preview.example.com';
    set(patch);
  };

  return (
    <Card
      title="Preview settings"
      description="Each preview is a site of its own, made from this one's configuration at every deployment: one instance, its own shared folder, one release kept, no scheduled tasks or deployment slots, none of the site's secrets. A few previews deploy at a time on the server; the others wait their turn."
    >
      <Sections>
        <FormSection title="Previews">
          <Switch
            checked={p.enabled}
            onChange={enable}
            label="Create preview deployments"
            description="The push webhook creates, redeploys and deletes them. Turning this off keeps existing previews until they are closed or expire."
          />
          {p.enabled && missing.length > 0 && (
            <Callout tone="warning">
              Previews need {missing.join(', ')}: set {missing.length > 1 ? 'them' : 'it'} on the{' '}
              <Link className="underline" to={`/sites/${site.id}/deployments`}>
                Deployments
              </Link>{' '}
              tab.
            </Callout>
          )}
        </FormSection>
        {p.enabled && (
          <>
            <FormSection title="What gets a preview" description={`Never the production branch (${site.deploy.git.branch || 'not set'}).`}>
              <Switch
                checked={p.pullRequests}
                onChange={(v) => set({ pullRequests: v })}
                label="Pull requests (GitLab: merge requests)"
                description="Opened, pushed to or reopened: deployed. Closed or merged: deleted."
              />
              <Field label="Branches" path="deploy.previews.branches" prefix hint="Pushes to matching branches get a preview; deleting the branch deletes it. * stays within a path segment, ** does not.">
                <ListEditor values={p.branches} onChange={(v) => set({ branches: v })} placeholder="feature/*" validate={branchPatternError} emptyText="No branch previews." />
              </Field>
              <Switch
                checked={p.allowForks}
                onChange={(v) => set({ allowForks: v })}
                label="Build pull requests from forks"
                description="Anyone can open one. Each push waits for an operator to approve its commit; once approved, its install and build commands, then its code, run on this server."
              />
              {p.allowForks && (
                <Callout tone="danger" icon={<ShieldAlert />}>
                  Fork code runs on this server, with the NodeHoster service's privileges{' '}
                  {site.type === 'static' ? '(static sites have no Run as account: their builds always run as the service)' : 'unless the site runs as a separate account (Run as)'}. Approve only
                  commits you have reviewed: a malicious pull request could read this server's files and anything the site's account can reach. Fork previews
                  never get the site's secrets. Turning this off deletes the fork previews.
                </Callout>
              )}
              {p.pullRequests && (
                <Field
                  label="Approval before building"
                  path="deploy.previews.requireApproval"
                  hint="A held pull request is listed here, awaiting approval; nothing of it is fetched until an operator approves its commit. Branch previews are never held."
                >
                  <Select
                    value={p.requireApproval || 'forks'}
                    onChange={(v) => set({ requireApproval: v as PreviewConfig['requireApproval'] })}
                    options={[
                      { value: 'forks', label: 'Pull requests from forks' },
                      { value: 'all', label: 'Every pull request' },
                    ]}
                  />
                </Field>
              )}
            </FormSection>

            <FormSection
              title="Address"
              description={`One binding per preview, at a host name made from the pattern. Point *.${suffix || 'preview.example.com'} (a wildcard DNS record) at this server.`}
            >
              <Field
                label="Host pattern"
                path="deploy.previews.hostPattern"
                error={patternErr}
                hint={
                  !patternErr && p.hostPattern ? (
                    <>
                      e.g. <Mono>{exampleHost(p.hostPattern, 'pr', 42, 'fix/login')}</Mono> for pull request #42,{' '}
                      <Mono>{exampleHost(p.hostPattern, 'branch', 0, 'feature/checkout')}</Mono> for branch feature/checkout
                    </>
                  ) : (
                    'Placeholders {number} (pull request) and {branch} in the first label'
                  )
                }
              >
                <Input mono value={p.hostPattern} placeholder="pr-{number}.preview.example.com" onChange={(e) => set({ hostPattern: e.target.value })} />
              </Field>
              <Grid cols={3}>
                <Field label="Protocol" path="deploy.previews.protocol">
                  <Select
                    value={p.protocol}
                    onChange={(v) => set({ protocol: v, port: v === 'https' ? 443 : 80, certMode: v === 'https' ? p.certMode || 'auto' : '' })}
                    options={[
                      { value: 'http', label: 'HTTP' },
                      { value: 'https', label: 'HTTPS' },
                    ]}
                  />
                </Field>
                <Field label="IP address" path="deploy.previews.ip" hint="Blank = all">
                  <Input mono value={p.ip ?? ''} placeholder="*" onChange={(e) => set({ ip: e.target.value.trim() })} />
                </Field>
                <Field label="Port" path="deploy.previews.port">
                  <NumberInput min={1} max={65535} value={p.port} onChange={(v) => set({ port: v })} />
                </Field>
              </Grid>
              {p.protocol === 'https' && (
                <>
                  <Field label="Certificate" path="deploy.previews.certMode">
                    <Select
                      value={p.certMode || 'auto'}
                      onChange={(v) => set({ certMode: v as PreviewConfig['certMode'] })}
                      options={[
                        { value: 'auto', label: "One Let's Encrypt certificate per preview (HTTP-01)" },
                        { value: 'wildcard', label: `A wildcard certificate for *.${suffix || '…'}, obtained through DNS-01` },
                        { value: 'certificate', label: `A certificate for *.${suffix || '…'} from the store` },
                      ]}
                    />
                  </Field>
                  {p.certMode === 'certificate' && (
                    <Field label="Wildcard certificate" path="deploy.previews.certificateId">
                      <Select
                        value={p.certificateId ?? ''}
                        onChange={(v) => set({ certificateId: v })}
                        placeholder={wildcardCerts.length ? 'Choose…' : `No certificate for *.${suffix} in the store`}
                        options={wildcardCerts.map((c) => ({ value: c.id, label: `${c.name} (${c.status})` }))}
                      />
                    </Field>
                  )}
                  {p.certMode === 'wildcard' && (
                    <Field label="DNS provider" path="deploy.previews.dnsProviderId" hint="Found in the certificate store when it exists already; otherwise requested with the first preview and renewed automatically.">
                      <Select
                        value={p.dnsProviderId ?? ''}
                        onChange={(v) => set({ dnsProviderId: v })}
                        placeholder={(settings.data?.dnsProviders ?? []).length ? 'Choose…' : 'Add a DNS provider in Settings first'}
                        options={(settings.data?.dnsProviders ?? []).map((d) => ({ value: d.id, label: `${d.name} (${d.provider})` }))}
                      />
                    </Field>
                  )}
                  {(p.certMode || 'auto') === 'auto' && (
                    <Callout tone="warning">
                      Each preview host needs port 80 reachable for the challenge, and every new preview asks Let's Encrypt for a certificate, counted against the
                      limit of 50 a week per registered domain that your production sites share. NodeHoster asks for at most {AUTO_PREVIEW_CERTS_PER_WEEK} a week for
                      previews under *.{suffix || '…'} and refuses new previews beyond: use a wildcard certificate when previews are frequent.
                    </Callout>
                  )}
                  {requiresClientCert(site) && (
                    <p className="text-xs text-zinc-500">Previews ask for client certificates as the site's HTTPS binding does (mutual TLS).</p>
                  )}
                </>
              )}
              {clientCertPreviewError(site, p) && <Callout tone="danger">{clientCertPreviewError(site, p)}</Callout>}
            </FormSection>

            <FormSection title="Lifecycle" description="Operations on one preview never overlap; pushes arriving during a deployment collapse into one more.">
              <Grid>
                <Field
                  label="Previews at most"
                  path="deploy.previews.maxPreviews"
                  hint="A new one beyond it evicts a preview that never deployed first, else the one pushed to least recently. A pull request awaiting approval only replaces another one awaiting approval."
                >
                  <NumberInput min={1} max={100} value={p.maxPreviews} onChange={(v) => set({ maxPreviews: v })} />
                </Field>
                <Field label="Delete after days without a push" path="deploy.previews.expireDays" hint="0 = never">
                  <NumberInput min={0} max={365} value={p.expireDays} onChange={(v) => set({ expireDays: v })} suffix="days" />
                </Field>
              </Grid>
            </FormSection>

            {runsNode(site.type) && (
              <FormSection
                title="Environment"
                description={
                  <>
                    Previews get the site's plain variables, not its secrets: secret variables, those read from a secret store and slot settings stay in
                    production. Give previews their own here, e.g. a separate <Mono>DATABASE_URL</Mono>. <Mono>PREVIEW=1</Mono>, <Mono>PREVIEW_BRANCH</Mono>,{' '}
                    <Mono>PREVIEW_PR</Mono> and <Mono>PREVIEW_URL</Mono> are always set.
                  </>
                }
              >
                <EnvVarsEditor
                  env={p.env ?? []}
                  onChange={(env) => set({ env })}
                  path="deploy.previews.env"
                  readOnly={readOnly}
                  emptyDescription="Previews use the site's plain variables."
                />
                <Switch
                  checked={!!p.inheritSecrets}
                  onChange={(v) => set({ inheritSecrets: v })}
                  label="Give same-repository previews the site's secrets"
                  description="Everyone who can push a branch or open a pull request in the repository could read them from a preview. Previews of forks never get them."
                />
              </FormSection>
            )}

            <FormSection
              title={<span className="flex items-center gap-1.5"><KeyRound className="h-3.5 w-3.5" /> Access</span>}
              description="Keep previews from being public. Each replaces the site's own setting in previews."
            >
              <Switch
                checked={p.basicAuth.enabled}
                onChange={(v) => set({ basicAuth: { ...p.basicAuth, enabled: v } })}
                label="Require a user name and password"
              />
              {p.basicAuth.enabled && (
                <Field label="Users" path="deploy.previews.basicAuth.users" prefix>
                  <RowsEditor<BasicAuthUser>
                    items={p.basicAuth.users}
                    onChange={(users) => set({ basicAuth: { ...p.basicAuth, users } })}
                    addLabel="Add user"
                    create={() => ({ username: '', password: '' })}
                    empty={<p className="text-xs text-zinc-500">Add at least one user.</p>}
                    render={(u, up, i) => (
                      <div className="grid grid-cols-2 gap-2">
                        <Field path={`deploy.previews.basicAuth.users[${i}].username`}>
                          <Input mono value={u.username} placeholder="user" autoComplete="off" onChange={(e) => up({ username: e.target.value })} />
                        </Field>
                        <Field path={`deploy.previews.basicAuth.users[${i}].password`}>
                          <Input
                            type="password"
                            mono
                            autoComplete="new-password"
                            value={u.password ?? ''}
                            placeholder="password (blank keeps it)"
                            onChange={(e) => up({ password: e.target.value })}
                          />
                        </Field>
                      </div>
                    )}
                  />
                </Field>
              )}
              <Field label="Allow only" path="deploy.previews.allowIps" prefix>
                <ListEditor values={p.allowIps} onChange={(v) => set({ allowIps: v })} placeholder="10.0.0.0/8" validate={validateIP} emptyText="The site's IP restrictions apply." />
              </Field>
            </FormSection>

            <FormSection
              title={<span className="flex items-center gap-1.5"><Webhook className="h-3.5 w-3.5" /> Git host</span>}
              description="Previews come from the site's push webhook. Subscribe it to these events:"
            >
              <ul className="list-disc space-y-0.5 pl-5 text-xs text-zinc-600 dark:text-zinc-400">
                {WEBHOOK_EVENTS.map((w) => (
                  <li key={w.host}>
                    <span className="font-medium">{w.host}</span>: {w.events}
                  </li>
                ))}
              </ul>
              <Switch
                checked={p.reportStatus}
                onChange={(v) => set({ reportStatus: v })}
                label="Report the preview on the pull request"
                description="Sets a commit status (nodehoster/preview) on GitHub, GitLab or Gitea whose link opens the preview."
              />
              {p.reportStatus && (
                <Field label="Token" path="deploy.previews.statusToken" hint="Needs permission to set commit statuses. Stored encrypted. Empty = the Git access token.">
                  <SecretInput value={p.statusToken} onChange={(v) => set({ statusToken: v })} placeholder="The Git access token" />
                </Field>
              )}
            </FormSection>
          </>
        )}
      </Sections>
    </Card>
  );
}

/** Shown on a preview's own page: what it previews, and where its settings live. */
export function PreviewBanner({ site }: { site: SiteView }) {
  const parentId = site.previewOf ?? '';
  const parent = useQuery({ queryKey: qk.site(parentId), queryFn: () => sitesApi.get(parentId), enabled: !!parentId, retry: false });
  const info = site.preview;
  if (!info) return null;
  return (
    <Callout tone="info" icon={info.kind === 'pr' ? <GitPullRequest /> : <GitBranch />} className="mb-4">
      Preview of{' '}
      {parent.data ? (
        <Link className="font-medium underline" to={`/sites/${parentId}/previews`}>
          {parent.data.name}
        </Link>
      ) : (
        'another site'
      )}{' '}
      for {info.kind === 'pr' ? `${pullRequestWord(info.provider)} ` : ''}
      {describePreview(info)}
      {info.title ? ` — ${info.title}` : ''}. Its configuration is made from its site's at every deployment: change that site's settings, not these.
    </Callout>
  );
}
