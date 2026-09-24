import { useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { CheckCircle2, FileArchive, GitBranch, History, Loader2, RotateCcw, ScrollText, Upload, Webhook, XCircle } from 'lucide-react';
import { sitesApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { openDeploymentLogStream } from '@/api/sse';
import { qk } from '@/api/queryKeys';
import type { Deployment, Site } from '@/api/types';
import { Button } from '@/components/Button';
import { Card, Callout, EmptyState, FormSection, Grid, Loading, Mono, Sections } from '@/components/Layout';
import { Field, ErrorBox } from '@/components/Field';
import { Input, NumberInput } from '@/components/Input';
import { SecretInput } from '@/components/SecretInput';
import { ListEditor } from '@/components/ListEditor';
import { CopyField } from '@/components/CopyButton';
import { Dialog } from '@/components/Dialog';
import { FileDrop } from '@/components/FileDrop';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Badge } from '@/components/Badge';
import { StateBadge } from '@/components/StatusBadges';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { useSitePermissions } from '@/hooks/useAuth';
import { useNow } from '@/hooks/useNow';
import { durationBetween, formatDateTime, relativeTime } from '@/lib/format';
import { runsNode } from '@/lib/siteDefaults';
import { cn } from '@/lib/cn';
import type { SiteEditorProps } from './editors/types';
import { SecretRefInput, useSecretStores } from './editors/SecretRefInput';
import { Checkbox } from '@/components/Switch';

export function DeployConfigCard({ site, update, readOnly }: SiteEditorProps) {
  const d = site.deploy;
  const set = (p: Partial<typeof d>) =>
    update((s) => {
      s.deploy = { ...s.deploy, ...p };
    });
  const setGit = (p: Partial<typeof d.git>) => set({ git: { ...d.git, ...p } });
  const hookUrl = `${window.location.origin}/hooks/deploy/${site.id}`;
  const stores = useSecretStores(!readOnly);

  return (
    <Card title="Deployment settings" description="Each deployment is extracted into a new release folder, built, and activated with a zero-downtime switch.">
      <Sections>
        <FormSection title="Git source" description="Clone from a repository over HTTPS. For private repositories use a personal access token.">
          <Field label="Repository URL" path="deploy.git.repo">
            <Input mono value={d.git.repo ?? ''} placeholder="https://github.com/acme/api.git" onChange={(e) => setGit({ repo: e.target.value.trim() })} />
          </Field>
          <Grid>
            <Field label="Branch" path="deploy.git.branch">
              <Input mono value={d.git.branch ?? ''} placeholder="main" onChange={(e) => setGit({ branch: e.target.value.trim() })} />
            </Field>
            {d.git.tokenFrom ? (
              <Field label="Access token" hint="Read from the secret store at each deployment.">
                <SecretRefInput value={d.git.tokenFrom} onChange={(r) => setGit({ tokenFrom: r })} path="deploy.git.tokenFrom" stores={stores} readOnly={readOnly} />
              </Field>
            ) : (
              <Field label="Access token" path="deploy.git.token" hint="Stored encrypted.">
                <SecretInput value={d.git.token} onChange={(v) => setGit({ token: v })} placeholder="ghp_…" />
              </Field>
            )}
          </Grid>
          <Checkbox
            checked={!!d.git.tokenFrom}
            onChange={(on) => setGit(on ? { token: '', tokenFrom: { store: stores[0]?.name ?? '', ref: '' } } : { tokenFrom: undefined })}
            label="Read the access token from a secret store"
            description="Vault / OpenBao, Infisical or Bitwarden Secrets Manager (Settings → Secret stores). A stored token is removed."
          />
        </FormSection>
        <FormSection title="Build" description="Commands run in the release folder before it is activated.">
          <Field label="Install command" path="deploy.installCommand">
            <Input mono value={d.installCommand ?? ''} placeholder={runsNode(site.type) ? 'npm ci --omit=dev' : ''} onChange={(e) => set({ installCommand: e.target.value })} />
          </Field>
          <Field label="Build command" path="deploy.buildCommand">
            <Input mono value={d.buildCommand ?? ''} placeholder="npm run build" onChange={(e) => set({ buildCommand: e.target.value })} />
          </Field>
        </FormSection>
        <FormSection title="Releases" description="Old releases are kept for instant rollback.">
          <Field label="Keep releases" path="deploy.keepReleases">
            <NumberInput className="w-32" min={1} value={d.keepReleases} onChange={(v) => set({ keepReleases: v })} />
          </Field>
          <Field
            label="Shared paths"
            path="deploy.sharedPaths"
            prefix
            hint="Files and folders persisted across releases (linked into each release), e.g. .env, uploads, data."
          >
            <ListEditor values={d.sharedPaths} onChange={(v) => set({ sharedPaths: v })} placeholder="uploads" />
          </Field>
        </FormSection>
        <FormSection
          title={<span className="flex items-center gap-1.5"><Webhook className="h-3.5 w-3.5" /> Webhook</span>}
          description="Trigger a Git deployment on push. GitHub/Gitea: set the secret and content type application/json; others can append ?secret=…"
        >
          <Field label="Payload URL">
            <CopyField value={hookUrl} />
          </Field>
          <Field label="Webhook secret" path="deploy.webhookSecret" hint="HMAC-SHA256 signature in X-Hub-Signature-256. Leave empty to disable the webhook.">
            <SecretInput value={d.webhookSecret} onChange={(v) => set({ webhookSecret: v })} />
          </Field>
        </FormSection>
      </Sections>
    </Card>
  );
}

function statusIcon(s: string) {
  if (s === 'succeeded') return <CheckCircle2 className="h-4 w-4 text-emerald-600 dark:text-emerald-400" />;
  if (s === 'failed') return <XCircle className="h-4 w-4 text-red-600 dark:text-red-400" />;
  return <Loader2 className="h-4 w-4 animate-spin text-amber-500" />;
}

function DeployStatusBadge({ status }: { status: string }) {
  if (status === 'running') return <Badge tone="amber" dot pulse>In progress</Badge>;
  return <StateBadge state={status} />;
}

const sourceLabel: Record<string, string> = { zip: 'Upload', git: 'Git', webhook: 'Webhook', rollback: 'Rollback' };

export function DeploymentsPanel({ site, savedSite, dirty }: { site: Site; savedSite: Site; dirty: boolean }) {
  const { canOperate } = useSitePermissions(site.id);
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const now = useNow(1000);
  const [gitOpen, setGitOpen] = useState(false);
  const [zipOpen, setZipOpen] = useState(false);
  const [logFor, setLogFor] = useState<Deployment | null>(null);

  const q = useQuery({
    queryKey: qk.deployments(site.id),
    queryFn: () => sitesApi.deployments(site.id),
    refetchInterval: (query) => ((query.state.data ?? []).some((d) => d.status === 'running') ? 2000 : 30_000),
  });

  const activate = useMutation({
    mutationFn: (dep: Deployment) => sitesApi.activate(site.id, dep.id),
    onSuccess: () => {
      toast.success('Release activated');
      void qc.invalidateQueries({ queryKey: qk.deployments(site.id) });
      void qc.invalidateQueries({ queryKey: qk.site(site.id) });
    },
    onError: (e) => toast.error('Rollback failed', e),
  });

  const deps = q.data ?? [];
  const hasGit = !!savedSite.deploy.git.repo;
  const running = deps.some((d) => d.status === 'running');

  const onStarted = (dep: Deployment) => {
    void qc.invalidateQueries({ queryKey: qk.deployments(site.id) });
    setLogFor(dep);
  };

  return (
    <Card
      title="Deployments"
      description={site.activeRelease ? <>Active release <Mono>{site.activeRelease}</Mono></> : 'Running from the configured application path.'}
      actions={
        canOperate && (
          <>
            <Button
              icon={<GitBranch className="h-3.5 w-3.5" />}
              disabled={!hasGit || dirty || running}
              title={!hasGit ? 'Configure a Git repository and save first' : dirty ? 'Save your changes first' : undefined}
              onClick={() => setGitOpen(true)}
            >
              Deploy from Git
            </Button>
            <Button variant="primary" icon={<Upload className="h-3.5 w-3.5" />} disabled={dirty || running} title={dirty ? 'Save your changes first' : undefined} onClick={() => setZipOpen(true)}>
              Upload .zip
            </Button>
          </>
        )
      }
      flush
    >
      {dirty && canOperate && (
        <div className="px-4 pt-3">
          <Callout tone="warning">Save your configuration changes before deploying.</Callout>
        </div>
      )}
      {q.isPending ? (
        <Loading />
      ) : q.isError ? (
        <div className="p-4">
          <ErrorBox>{errorMessage(q.error)}</ErrorBox>
        </div>
      ) : deps.length === 0 ? (
        <EmptyState
          icon={<History />}
          title="No deployments yet"
          description={
            hasGit
              ? 'Deploy the configured branch, upload a .zip of your build, or push to the repository with the webhook configured.'
              : 'Upload a .zip of your application, or configure a Git repository above to deploy from source.'
          }
        />
      ) : (
        <Table>
          <THead>
            <tr>
              <Th>Status</Th>
              <Th>Source</Th>
              <Th>Commit / message</Th>
              <Th>Started</Th>
              <Th className="text-right">Duration</Th>
              <Th>By</Th>
              <Th className="w-44" />
            </tr>
          </THead>
          <TBody>
            {deps.map((d) => {
              const active = !!site.activeRelease && (site.activeRelease === d.id || (!!d.releaseDir && d.releaseDir.endsWith(site.activeRelease)));
              return (
                <Tr key={d.id} className={cn(active && 'bg-accent-50/60 dark:bg-accent-500/[0.06]')}>
                  <Td>
                    <div className="flex items-center gap-2">
                      {statusIcon(d.status)}
                      <span
                        className={cn(
                          'text-[13px] font-medium capitalize',
                          d.status === 'failed' ? 'text-red-600 dark:text-red-400' : d.status === 'running' ? 'text-amber-600 dark:text-amber-400' : '',
                        )}
                      >
                        {d.status === 'running' ? 'In progress' : d.status}
                      </span>
                      {active && <Badge tone="accent">active</Badge>}
                    </div>
                  </Td>
                  <Td className="text-zinc-600 dark:text-zinc-300">{sourceLabel[d.source] ?? d.source}</Td>
                  <Td className="max-w-xs">
                    {d.commit && <Mono className="mr-2 text-zinc-500">{d.commit.slice(0, 8)}</Mono>}
                    <span className="truncate text-[13px]" title={d.message}>
                      {d.message || (!d.commit && <span className="text-zinc-400">—</span>)}
                    </span>
                  </Td>
                  <Td className="whitespace-nowrap text-zinc-500" title={formatDateTime(d.startedAt)}>
                    {relativeTime(d.startedAt, now)}
                  </Td>
                  <Td className="text-right tabular text-zinc-500">{durationBetween(d.startedAt, d.finishedAt, now)}</Td>
                  <Td className="text-zinc-500">{d.user || '—'}</Td>
                  <Td>
                    <div className="flex justify-end gap-1">
                      <Button size="sm" variant="ghost" icon={<ScrollText className="h-3.5 w-3.5" />} onClick={() => setLogFor(d)}>
                        Log
                      </Button>
                      {canOperate && d.status === 'succeeded' && !active && (
                        <Button
                          size="sm"
                          variant="ghost"
                          icon={<RotateCcw className="h-3.5 w-3.5" />}
                          loading={activate.isPending && activate.variables?.id === d.id}
                          onClick={async () => {
                            const r = await confirm({
                              title: 'Activate this release?',
                              message: (
                                <>
                                  The site switches to the release from <b>{formatDateTime(d.startedAt)}</b>
                                  {d.commit && (
                                    <>
                                      {' '}
                                      (<span className="font-mono">{d.commit.slice(0, 8)}</span>)
                                    </>
                                  )}
                                  . Node instances are recycled without downtime.
                                </>
                              ),
                              confirmLabel: 'Activate',
                              danger: true,
                            });
                            if (r.ok) activate.mutate(d);
                          }}
                        >
                          Activate
                        </Button>
                      )}
                    </div>
                  </Td>
                </Tr>
              );
            })}
          </TBody>
        </Table>
      )}
      <GitDeployDialog open={gitOpen} onClose={() => setGitOpen(false)} site={savedSite} onStarted={onStarted} />
      <ZipDeployDialog open={zipOpen} onClose={() => setZipOpen(false)} site={savedSite} onStarted={onStarted} />
      <DeploymentLogDialog siteId={site.id} dep={logFor} onClose={() => setLogFor(null)} />
    </Card>
  );
}

function GitDeployDialog({ open, onClose, site, onStarted }: { open: boolean; onClose: () => void; site: Site; onStarted: (d: Deployment) => void }) {
  const [branch, setBranch] = useState('');
  useEffect(() => {
    if (open) setBranch(site.deploy.git.branch || '');
  }, [open, site.deploy.git.branch]);
  const m = useMutation({
    mutationFn: () => sitesApi.deployGit(site.id, branch.trim() && branch.trim() !== site.deploy.git.branch ? branch.trim() : undefined),
    onSuccess: (d) => {
      onClose();
      onStarted(d);
    },
  });
  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="sm"
      title="Deploy from Git"
      description={<span className="font-mono">{site.deploy.git.repo}</span>}
      onSubmit={() => m.mutate()}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" loading={m.isPending} icon={<GitBranch className="h-3.5 w-3.5" />}>
            Deploy
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {m.isError && <ErrorBox>{errorMessage(m.error)}</ErrorBox>}
        <Field label="Branch" hint="Defaults to the configured branch.">
          <Input mono value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" />
        </Field>
      </div>
    </Dialog>
  );
}

function ZipDeployDialog({ open, onClose, site, onStarted }: { open: boolean; onClose: () => void; site: Site; onStarted: (d: Deployment) => void }) {
  const [file, setFile] = useState<File | null>(null);
  useEffect(() => {
    if (open) setFile(null);
  }, [open]);
  const m = useMutation({
    mutationFn: () => sitesApi.deployZip(site.id, file!),
    onSuccess: (d) => {
      onClose();
      onStarted(d);
    },
  });
  const badType = file && !/\.zip$/i.test(file.name) ? 'Select a .zip file' : null;
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Upload a release"
      description="A .zip of your application. The archive root (or its single top-level folder) becomes the release folder."
      onSubmit={() => file && !badType && m.mutate()}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!file || !!badType} loading={m.isPending} icon={<Upload className="h-3.5 w-3.5" />}>
            Upload & deploy
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {m.isError && <ErrorBox>{errorMessage(m.error)}</ErrorBox>}
        {badType && <ErrorBox>{badType}</ErrorBox>}
        <FileDrop file={file} onFile={setFile} accept=".zip,application/zip" icon={<FileArchive />} label="Drop a .zip here, or click to browse" hint={site.deploy.installCommand ? `Then runs: ${site.deploy.installCommand}${site.deploy.buildCommand ? ` && ${site.deploy.buildCommand}` : ''}` : undefined} />
      </div>
    </Dialog>
  );
}

export function DeploymentLogDialog({ siteId, dep, onClose }: { siteId: string; dep: Deployment | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [lines, setLines] = useState<string[]>([]);
  const [current, setCurrent] = useState<Deployment | null>(dep);
  const [error, setError] = useState<string | null>(null);
  const [live, setLive] = useState(false);
  const boxRef = useRef<HTMLPreElement>(null);
  const stick = useRef(true);

  useEffect(() => {
    setCurrent(dep);
    setLines([]);
    setError(null);
    if (!dep) return;
    if (dep.status === 'running') {
      setLive(true);
      const h = openDeploymentLogStream(siteId, dep.id, {
        onLine: (l) => setLines((prev) => [...prev, ...l.split('\n')]),
        onDone: (d) => {
          setLive(false);
          if (d) setCurrent(d);
          void qc.invalidateQueries({ queryKey: qk.deployments(siteId) });
          void qc.invalidateQueries({ queryKey: qk.site(siteId) });
        },
      });
      return () => h.close();
    }
    setLive(false);
    let cancelled = false;
    sitesApi
      .deploymentLog(siteId, dep.id)
      .then((t) => !cancelled && setLines(t.replace(/\n$/, '').split('\n')))
      .catch((e) => !cancelled && setError(errorMessage(e)));
    return () => {
      cancelled = true;
    };
  }, [dep, siteId, qc]);

  useEffect(() => {
    const el = boxRef.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [lines]);

  return (
    <Dialog
      open={!!dep}
      onClose={onClose}
      size="xl"
      title={
        <span className="flex items-center gap-2">
          Deployment log
          {current && <DeployStatusBadge status={current.status} />}
          {live && (
            <Badge tone="accent" dot pulse>
              live
            </Badge>
          )}
        </span>
      }
      description={current ? `${sourceLabel[current.source] ?? current.source} · started ${formatDateTime(current.startedAt)}${current.commit ? ` · ${current.commit.slice(0, 8)}` : ''}` : undefined}
      footer={<Button onClick={onClose}>Close</Button>}
    >
      {error ? (
        <ErrorBox>{error}</ErrorBox>
      ) : (
        <pre
          ref={boxRef}
          onScroll={(e) => {
            const el = e.currentTarget;
            stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
          }}
          className="scrollbar-thin h-[55vh] overflow-auto rounded-md bg-zinc-950 p-3 font-mono text-xs leading-5 text-zinc-200"
        >
          {lines.length === 0 ? <span className="text-zinc-500">{live ? 'Waiting for output…' : 'No output.'}</span> : lines.map((l, i) => <div key={i} className={cn(/error|fail|err!/i.test(l) && 'text-red-400')}>{l || ' '}</div>)}
        </pre>
      )}
      {current?.status === 'failed' && current.message && <ErrorBox className="mt-3">{current.message}</ErrorBox>}
    </Dialog>
  );
}
