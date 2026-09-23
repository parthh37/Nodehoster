import { useEffect, useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  AlertTriangle,
  Archive,
  CheckCircle2,
  CloudUpload,
  DatabaseBackup,
  Download,
  FileArchive,
  History,
  KeyRound,
  Pencil,
  Play,
  Plus,
  RotateCcw,
  Send,
  Trash2,
  XCircle,
} from 'lucide-react';
import { backupsApi, serverApi, sitesApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { BackupDestination, BackupDestinationType, BackupObject, BackupRun, RestoreResult } from '@/api/types';
import { Button, IconButton } from '@/components/Button';
import { Card, Callout, EmptyState, FormSection, Grid, KV, Mono, Sections } from '@/components/Layout';
import { ErrorBox, Field, PathError } from '@/components/Field';
import { Input, NumberInput, Select, Textarea } from '@/components/Input';
import { Checkbox, Switch } from '@/components/Switch';
import { SecretInput } from '@/components/SecretInput';
import { Dialog } from '@/components/Dialog';
import { FileDrop } from '@/components/FileDrop';
import { Table, TableMessage, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Badge } from '@/components/Badge';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import {
  DESTINATION_TYPES,
  SHARED_WARN_BYTES,
  WEEKDAYS,
  destinationProblem,
  destinationSummary,
  isValidTime,
  newDestination,
  retentionSummary,
  runTone,
  scheduleSummary,
} from '@/lib/backup';
import { durationBetween, formatBytes, formatDateTime, relativeTime } from '@/lib/format';
import { SECRET } from '@/api/types';
import type { SettingsTabProps } from './SettingsPage';

/** Settings → Backups: scheduled, off-machine backups and restore. */
export function BackupsTab({ s, update }: SettingsTabProps) {
  const b = s.backup;
  const set = (p: Partial<typeof b>) =>
    update((d) => {
      d.backup = { ...d.backup, ...p };
    });
  return (
    <div className="space-y-5">
      <StatusCard />
      <Card title="Schedule" description="Scheduled backups use the saved settings; save your changes before running one.">
        <Sections>
          <FormSection title="When" description="Server local time. A backup missed while the service was stopped is not caught up.">
            <Switch checked={b.enabled} onChange={(v) => set({ enabled: v })} label="Back up automatically" description={b.enabled ? scheduleSummary(b.time, b.weekdays) : 'Only when started with Run now.'} />
            <Grid>
              <Field label="Time" path="backup.time" error={isValidTime(b.time) ? null : 'Use HH:MM, 24-hour'}>
                <Input className="w-28" mono value={b.time} onChange={(e) => set({ time: e.target.value.trim() })} placeholder="02:30" />
              </Field>
            </Grid>
            <Field label="Days" path="backup.weekdays" hint="None selected = every day.">
              <div className="flex flex-wrap gap-3">
                {WEEKDAYS.map((name, i) => (
                  <Checkbox
                    key={name}
                    checked={b.weekdays.includes(i)}
                    onChange={(on) => set({ weekdays: on ? [...b.weekdays, i].sort((x, y) => x - y) : b.weekdays.filter((x) => x !== i) })}
                    label={name}
                  />
                ))}
              </div>
            </Field>
          </FormSection>
          <FormSection title="Retention" description="Applied at each destination after an upload, to this server's archives only. Other files are never deleted.">
            <Grid>
              <Field label="Keep the last" path="backup.keepLast" hint="Archives; 0 = no count limit.">
                <NumberInput className="w-28" value={b.keepLast} onChange={(v) => set({ keepLast: Math.max(0, v) })} />
              </Field>
              <Field label="Keep for" path="backup.keepDays" hint="Days; 0 = no age limit.">
                <NumberInput className="w-28" value={b.keepDays} onChange={(v) => set({ keepDays: Math.max(0, v) })} />
              </Field>
            </Grid>
            <p className="text-xs text-zinc-500 dark:text-zinc-400">{retentionSummary(b.keepLast, b.keepDays)}. The newest archive is always kept.</p>
          </FormSection>
        </Sections>
      </Card>
      <ContentsCard {...{ s, update }} />
      <DestinationsCard {...{ s, update }} />
      <HistoryCard />
      <RestoreCard destinations={b.destinations.filter((d) => d.id)} />
    </div>
  );
}

// ---------------------------------------------------------------- status

function StatusCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: qk.backups, queryFn: backupsApi.status, refetchInterval: (query) => (query.state.data?.running ? 2000 : 30_000) });
  const run = useMutation({
    mutationFn: backupsApi.run,
    onSuccess: (st) => {
      qc.setQueryData(qk.backups, st);
      toast.success('Backup started');
    },
    onError: (e) => toast.error('Could not start the backup', e),
  });
  const st = q.data;
  const last = st?.history[0];
  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <DatabaseBackup className="h-4 w-4 text-zinc-400" />
          Backups
        </span>
      }
      actions={
        <>
          <a href={serverApi.backupArchiveUrl} download>
            <Button size="sm" icon={<Download className="h-3.5 w-3.5" />}>
              Download archive
            </Button>
          </a>
          <Button size="sm" variant="primary" icon={<Play className="h-3.5 w-3.5" />} loading={run.isPending} disabled={st?.running} onClick={() => run.mutate()}>
            Run now
          </Button>
        </>
      }
    >
      {q.isError ? (
        <ErrorBox>{errorMessage(q.error)}</ErrorBox>
      ) : (
        <KV
          items={[
            [
              'State',
              st?.running ? (
                <Badge tone="blue" dot pulse>
                  {st.runningWhat === 'restore' ? 'Restoring' : 'Backing up'} since {relativeTime(st.runningSince)}
                </Badge>
              ) : (
                <Badge tone={st?.enabled ? 'green' : 'gray'}>{st?.enabled ? 'Scheduled' : 'Not scheduled'}</Badge>
              ),
            ],
            ['Next backup', st?.nextRun ? `${formatDateTime(st.nextRun)} (${relativeTime(st.nextRun)})` : '—'],
            ['Last backup', last ? <RunSummary run={last} /> : 'Never'],
            [
              'Encryption',
              st?.encrypted ? (
                <span className="flex items-center gap-1.5">
                  <KeyRound className="h-3.5 w-3.5 text-emerald-600" /> Passphrase: archives restore on any server
                </span>
              ) : (
                <span className="text-amber-700 dark:text-amber-300">None: archives only restore on this server</span>
              ),
            ],
          ]}
        />
      )}
    </Card>
  );
}

function RunSummary({ run }: { run: BackupRun }) {
  return (
    <span className="flex flex-wrap items-center gap-2">
      <Badge tone={runTone(run.status)}>{run.status}</Badge>
      <span>{formatDateTime(run.startedAt)}</span>
      {run.file && <span className="text-zinc-500">{formatBytes(run.size)}</span>}
      {run.error && <span className="text-red-600 dark:text-red-400">{run.error}</span>}
    </span>
  );
}

// ---------------------------------------------------------------- contents

function ContentsCard({ s, update }: SettingsTabProps) {
  const b = s.backup;
  const set = (p: Partial<typeof b>) =>
    update((d) => {
      d.backup = { ...d.backup, ...p };
    });
  const sites = useQuery({ queryKey: qk.sites, queryFn: sitesApi.list, staleTime: 30_000 });
  const sizes = useQuery({ queryKey: qk.backupSharedSizes, queryFn: backupsApi.sharedSizes, enabled: b.includeShared, staleTime: 60_000 });
  const chosen = (sizes.data ?? []).filter((x) => b.sharedSiteIds.length === 0 || b.sharedSiteIds.includes(x.siteId));
  const total = chosen.reduce((n, x) => n + x.bytes, 0);
  const partial = chosen.some((x) => x.partial);
  const noPassphrase = !b.passphrase;

  return (
    <Card title="Contents and encryption">
      <Sections>
        <FormSection title="What is backed up" description="Binaries, releases and Node.js runtimes are left out: a new server gets them from the installer and a redeploy.">
          <Checkbox checked disabled onChange={() => {}} label="Configuration" description="Server settings, sites and certificate records. Always included." />
          <Checkbox
            checked={b.includeCertificates}
            onChange={(v) => set({ includeCertificates: v })}
            label="Certificates and private keys"
            description="Without them, a restored server re-issues ACME certificates and needs imported ones imported again."
          />
          <Checkbox
            checked={b.includeShared}
            onChange={(v) => set({ includeShared: v })}
            label="Sites' shared folders"
            description="Files kept across deployments (.env, uploads). Can be large."
          />
          {b.includeShared && (
            <div className="space-y-2 pl-6">
              <Field label="Sites" hint="None selected = every site.">
                <div className="grid grid-cols-2 gap-x-4 gap-y-1.5 rounded-lg border border-zinc-200 p-3 dark:border-zinc-800 sm:grid-cols-3">
                  {(sites.data ?? []).map((site) => {
                    const size = sizes.data?.find((x) => x.siteId === site.id);
                    return (
                      <Checkbox
                        key={site.id}
                        checked={b.sharedSiteIds.includes(site.id)}
                        onChange={(on) => set({ sharedSiteIds: on ? [...b.sharedSiteIds, site.id] : b.sharedSiteIds.filter((x) => x !== site.id) })}
                        label={site.name}
                        description={size ? `${formatBytes(size.bytes)}${size.partial ? '+' : ''}` : 'no shared folder'}
                      />
                    );
                  })}
                </div>
              </Field>
              {total >= SHARED_WARN_BYTES && (
                <Callout tone="warning" icon={<AlertTriangle />}>
                  The selected shared folders hold {formatBytes(total)}
                  {partial ? ' or more' : ''}. Every backup copies all of it (there are no incremental backups); S3 destinations accept at most 5 GiB per archive.
                </Callout>
              )}
            </div>
          )}
        </FormSection>
        <FormSection
          title="Passphrase"
          description="Encrypts archives (AES-256-GCM, key from scrypt) and lets another server restore the secrets. Keep it somewhere safe: it cannot be recovered."
        >
          <Field label="Backup passphrase" path="backup.passphrase">
            <SecretInput value={b.passphrase} onChange={(v) => set({ passphrase: v })} placeholder="No passphrase" mono={false} />
          </Field>
          {noPassphrase ? (
            <Callout tone="warning" icon={<AlertTriangle />} title="Archives only restore on this server">
              Without a passphrase, secrets and private keys in an archive stay encrypted with this machine's key, which cannot be moved to another server. If this server is
              lost, a replacement restores the configuration but every secret must be entered again. Shared folders are stored unencrypted.
            </Callout>
          ) : (
            <Callout tone="success" icon={<KeyRound />}>
              Archives are encrypted and restore on any NodeHoster server with this passphrase.
            </Callout>
          )}
        </FormSection>
      </Sections>
    </Card>
  );
}

// ---------------------------------------------------------------- destinations

const TYPE_LABEL: Record<BackupDestinationType, string> = { folder: 'Folder', s3: 'S3', azure: 'Azure Blob', sftp: 'SFTP' };

function DestinationsCard({ s, update }: SettingsTabProps) {
  const confirm = useConfirm();
  const toast = useToast();
  const [editing, setEditing] = useState<{ index: number; d: BackupDestination } | null>(null);
  const [adding, setAdding] = useState(false);
  const list = s.backup.destinations;
  const test = useMutation({
    mutationFn: (d: BackupDestination) => backupsApi.test(d),
    onSuccess: (r, d) => (r.ok ? toast.success(`${d.name} works`, 'Listed, wrote and deleted a test file.') : toast.error(`${d.name} failed`, r.error)),
    onError: (e) => toast.error('Test failed', e),
  });

  return (
    <Card
      title="Destinations"
      description="Every enabled destination receives each archive."
      actions={
        <Button size="sm" variant="primary" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setAdding(true)}>
          Add destination
        </Button>
      }
      flush
    >
      <PathError path="backup.destinations" className="px-4 pt-3" />
      {list.length === 0 ? (
        <EmptyState compact icon={<CloudUpload />} title="No destinations" description="Add a network share, an S3 bucket, an Azure container or an SFTP server." />
      ) : (
        <Table>
          <THead>
            <tr>
              <Th>Name</Th>
              <Th>Type</Th>
              <Th>Location</Th>
              <Th>Enabled</Th>
              <Th className="w-40" />
            </tr>
          </THead>
          <TBody>
            {list.map((d, i) => (
              <Tr key={d.id || i}>
                <Td className="font-medium">
                  {d.name}
                  <PathError path={`backup.destinations[${i}]`} prefix />
                </Td>
                <Td>{TYPE_LABEL[d.type] ?? d.type}</Td>
                <Td className="max-w-xs truncate">
                  <Mono className="text-zinc-500" title={destinationSummary(d)}>
                    {destinationSummary(d)}
                  </Mono>
                </Td>
                <Td>
                  <Switch
                    size="sm"
                    checked={d.enabled}
                    onChange={(v) =>
                      update((x) => {
                        x.backup.destinations[i].enabled = v;
                      })
                    }
                  />
                </Td>
                <Td>
                  <div className="flex justify-end gap-1">
                    <Button size="sm" variant="ghost" icon={<Send className="h-3.5 w-3.5" />} loading={test.isPending && test.variables === d} onClick={() => test.mutate(d)}>
                      Test
                    </Button>
                    <IconButton label="Edit" icon={<Pencil className="h-3.5 w-3.5" />} onClick={() => setEditing({ index: i, d })} />
                    <IconButton
                      label="Remove"
                      variant="danger-ghost"
                      icon={<Trash2 className="h-3.5 w-3.5" />}
                      onClick={async () => {
                        const r = await confirm({ title: `Remove destination ${d.name}?`, message: 'Archives already there are not deleted.', confirmLabel: 'Remove', danger: true });
                        if (r.ok)
                          update((x) => {
                            x.backup.destinations = x.backup.destinations.filter((_, j) => j !== i);
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
      <Dialog open={adding} onClose={() => setAdding(false)} title="Add destination" size="md">
        <div className="space-y-2">
          {DESTINATION_TYPES.map((t) => (
            <button
              key={t.value}
              type="button"
              onClick={() => {
                setAdding(false);
                setEditing({ index: -1, d: newDestination(t.value) });
              }}
              className="block w-full rounded-md border border-zinc-200 px-3 py-2.5 text-left hover:border-accent-500 hover:bg-accent-50/50 dark:border-zinc-800 dark:hover:bg-accent-500/5"
            >
              <span className="text-[13px] font-medium">{t.label}</span>
              <span className="block text-xs text-zinc-500 dark:text-zinc-400">{t.description}</span>
            </button>
          ))}
        </div>
      </Dialog>
      <DestinationDialog
        value={editing?.d ?? null}
        onClose={() => setEditing(null)}
        onApply={(d) => {
          if (editing)
            update((x) => {
              const l = x.backup.destinations;
              if (editing.index < 0) l.push(d);
              else l[editing.index] = d;
            });
          setEditing(null);
        }}
      />
    </Card>
  );
}

function DestinationDialog({ value, onClose, onApply }: { value: BackupDestination | null; onClose: () => void; onApply: (d: BackupDestination) => void }) {
  const [d, setD] = useState<BackupDestination>(newDestination('folder'));
  const [touched, setTouched] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; error?: string; hostKey?: string } | null>(null);
  useEffect(() => {
    if (value) setD(JSON.parse(JSON.stringify(value)) as BackupDestination);
    setTouched(false);
    setResult(null);
  }, [value]);
  const problem = destinationProblem(d);
  const err = (field: string) => (touched && problem?.field === field ? problem.message : null);
  const test = useMutation({
    mutationFn: () => backupsApi.test(d),
    onSuccess: setResult,
    onError: (e) => setResult({ ok: false, error: errorMessage(e) }),
  });
  const s3 = d.s3;
  const az = d.azure;
  const sftp = d.sftp;
  const setS3 = (p: Partial<NonNullable<BackupDestination['s3']>>) => setD({ ...d, s3: { ...s3!, ...p } });
  const setAz = (p: Partial<NonNullable<BackupDestination['azure']>>) => setD({ ...d, azure: { ...az!, ...p } });
  const setSftp = (p: Partial<NonNullable<BackupDestination['sftp']>>) => setD({ ...d, sftp: { ...sftp!, ...p } });

  return (
    <Dialog
      open={!!value}
      onClose={onClose}
      size="lg"
      title={`${value?.id ? 'Edit' : 'Add'} ${DESTINATION_TYPES.find((t) => t.value === d.type)?.label ?? 'destination'}`}
      onSubmit={() => {
        setTouched(true);
        if (problem) return;
        onApply({ ...d, name: d.name.trim() });
      }}
      footer={
        <>
          <Button
            icon={<Send className="h-3.5 w-3.5" />}
            className="mr-auto"
            loading={test.isPending}
            disabled={!!problem && !(d.type === 'sftp' && problem.field === 'sftp.hostKey')}
            onClick={() => test.mutate()}
          >
            Test
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
            <Input value={d.name} onChange={(e) => setD({ ...d, name: e.target.value })} placeholder={{ folder: 'NAS', s3: 'R2 bucket', azure: 'Azure', sftp: 'Offsite box' }[d.type]} />
          </Field>
          <div className="flex items-end pb-1.5">
            <Switch checked={d.enabled} onChange={(v) => setD({ ...d, enabled: v })} label="Enabled" />
          </div>
        </Grid>

        {d.type === 'folder' && d.folder && (
          <>
            <Field label="Folder" error={err('folder.path')} hint="A local folder, or a UNC path to a share.">
              <Input mono value={d.folder.path} onChange={(e) => setD({ ...d, folder: { path: e.target.value } })} placeholder="\\nas\backups\web01" />
            </Field>
            <Callout tone="info">
              The service runs as LocalSystem, which reaches network shares as this computer's account (<Mono>DOMAIN\COMPUTER$</Mono>): give that account Modify permission
              on the share and the folder. Mapped drive letters are per user and not visible to the service.
            </Callout>
          </>
        )}

        {d.type === 's3' && s3 && (
          <>
            <Grid>
              <Field label="Endpoint" error={err('s3.endpoint')} hint="Empty for AWS. R2: https://<account>.r2.cloudflarestorage.com">
                <Input mono value={s3.endpoint ?? ''} onChange={(e) => setS3({ endpoint: e.target.value.trim() })} placeholder="https://s3.us-west-004.backblazeb2.com" />
              </Field>
              <Field label="Region" error={err('s3.region')} hint="e.g. eu-west-1; R2: auto">
                <Input mono value={s3.region} onChange={(e) => setS3({ region: e.target.value.trim() })} placeholder="us-east-1" />
              </Field>
              <Field label="Bucket" error={err('s3.bucket')}>
                <Input mono value={s3.bucket} onChange={(e) => setS3({ bucket: e.target.value.trim() })} />
              </Field>
              <Field label="Prefix" hint="Folder inside the bucket, e.g. web01/">
                <Input mono value={s3.prefix ?? ''} onChange={(e) => setS3({ prefix: e.target.value.trim() })} />
              </Field>
              <Field label="Access key ID" error={err('s3.accessKeyId')}>
                <Input mono value={s3.accessKeyId} onChange={(e) => setS3({ accessKeyId: e.target.value.trim() })} autoComplete="off" />
              </Field>
              <Field label="Secret access key" error={err('s3.secretAccessKey')}>
                <SecretInput value={s3.secretAccessKey} onChange={(v) => setS3({ secretAccessKey: v })} allowClear={false} />
              </Field>
            </Grid>
            <Checkbox
              checked={s3.pathStyle}
              onChange={(v) => setS3({ pathStyle: v })}
              label="Path-style addressing"
              description="endpoint/bucket/key instead of bucket.endpoint/key. Needed for MinIO and most self-hosted servers."
            />
            <p className="text-xs text-zinc-500 dark:text-zinc-400">Archives are uploaded in one request, so an S3 destination takes archives up to 5 GiB.</p>
          </>
        )}

        {d.type === 'azure' && az && (
          <>
            <Grid>
              <Field label="Storage account" error={err('azure.account')}>
                <Input mono value={az.account} onChange={(e) => setAz({ account: e.target.value.trim().toLowerCase() })} />
              </Field>
              <Field label="Container" error={err('azure.container')}>
                <Input mono value={az.container} onChange={(e) => setAz({ container: e.target.value.trim().toLowerCase() })} />
              </Field>
              <Field label="Prefix" hint="Virtual folder, e.g. web01/">
                <Input mono value={az.prefix ?? ''} onChange={(e) => setAz({ prefix: e.target.value.trim() })} />
              </Field>
              <Field label="Endpoint" hint="Empty for Azure public cloud.">
                <Input mono value={az.endpoint ?? ''} onChange={(e) => setAz({ endpoint: e.target.value.trim() })} placeholder="https://<account>.blob.core.windows.net" />
              </Field>
            </Grid>
            <Field label="SAS token" error={err('azure.sasToken')} hint="Needs read, write, delete and list permission on the container. Preferred over the account key.">
              <SecretInput value={az.sasToken} onChange={(v) => setAz({ sasToken: v })} placeholder="sv=…&sig=…" />
            </Field>
            <Field label="Account key" hint="Used only without a SAS token.">
              <SecretInput value={az.accountKey} onChange={(v) => setAz({ accountKey: v })} />
            </Field>
          </>
        )}

        {d.type === 'sftp' && sftp && (
          <>
            <Grid cols={3}>
              <Field label="Host" error={err('sftp.host')} className="sm:col-span-2">
                <Input mono value={sftp.host} onChange={(e) => setSftp({ host: e.target.value.trim(), hostKey: '' })} placeholder="backup.example.com" />
              </Field>
              <Field label="Port">
                <NumberInput value={sftp.port} onChange={(v) => setSftp({ port: v })} />
              </Field>
            </Grid>
            <Grid>
              <Field label="User name" error={err('sftp.username')}>
                <Input mono value={sftp.username} onChange={(e) => setSftp({ username: e.target.value.trim() })} autoComplete="off" />
              </Field>
              <Field label="Directory" hint="Relative to the login directory, or absolute.">
                <Input mono value={sftp.directory} onChange={(e) => setSftp({ directory: e.target.value.trim() })} placeholder="backups/web01" />
              </Field>
              <Field label="Password" error={err('sftp.password')}>
                <SecretInput value={sftp.password} onChange={(v) => setSftp({ password: v })} />
              </Field>
              <Field label="Private key passphrase">
                <SecretInput value={sftp.passphrase} onChange={(v) => setSftp({ passphrase: v })} />
              </Field>
            </Grid>
            <Field label="Private key" hint="OpenSSH or PEM. Used before the password.">
              {sftp.privateKey === SECRET ? (
                <div className="flex items-center gap-2 text-[13px] text-zinc-500">
                  <KeyRound className="h-3.5 w-3.5" /> A key is stored.
                  <Button size="xs" variant="ghost" onClick={() => setSftp({ privateKey: '' })}>
                    Replace
                  </Button>
                </div>
              ) : (
                <Textarea mono rows={4} value={sftp.privateKey ?? ''} onChange={(e) => setSftp({ privateKey: e.target.value })} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" />
              )}
            </Field>
            <Field
              label="Host key fingerprint"
              error={err('sftp.hostKey')}
              hint="Required: the server's key is checked on every connection. Test reads it from the server; compare it with ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub on the server before accepting it."
            >
              <Input mono value={sftp.hostKey} onChange={(e) => setSftp({ hostKey: e.target.value.trim() })} placeholder="SHA256:…" />
            </Field>
          </>
        )}

        {result && (
          <Callout
            tone={result.ok ? 'success' : 'danger'}
            icon={result.ok ? <CheckCircle2 /> : <XCircle />}
            title={result.ok ? 'The destination works: a test file was written and deleted' : 'The test failed'}
            actions={
              d.type === 'sftp' && result.hostKey && result.hostKey !== sftp?.hostKey ? (
                <Button size="sm" onClick={() => setSftp({ hostKey: result.hostKey! })}>
                  Accept this key
                </Button>
              ) : undefined
            }
          >
            {result.error}
            {d.type === 'sftp' && result.hostKey && result.hostKey !== sftp?.hostKey && (
              <div className="mt-1">
                The server presented <Mono>{result.hostKey}</Mono>. Accept it only if it matches the server's own fingerprint.
              </div>
            )}
          </Callout>
        )}
      </div>
    </Dialog>
  );
}

// ---------------------------------------------------------------- history

function HistoryCard() {
  const q = useQuery({ queryKey: qk.backups, queryFn: backupsApi.status });
  const history = q.data?.history ?? [];
  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <History className="h-4 w-4 text-zinc-400" />
          History
        </span>
      }
      flush
    >
      <Table>
        <THead>
          <tr>
            <Th>Started</Th>
            <Th>Result</Th>
            <Th>Archive</Th>
            <Th>Destinations</Th>
            <Th>Took</Th>
          </tr>
        </THead>
        <TBody>
          {history.length === 0 && <TableMessage colSpan={5}>No backups yet.</TableMessage>}
          {history.map((r) => (
            <Tr key={r.id}>
              <Td className="whitespace-nowrap">
                {formatDateTime(r.startedAt)}
                <div className="text-xs text-zinc-500">{r.trigger === 'schedule' ? 'Scheduled' : 'Manual'}</div>
              </Td>
              <Td>
                <Badge tone={runTone(r.status)}>{r.status}</Badge>
                {r.error && <div className="mt-1 max-w-xs text-xs text-red-600 dark:text-red-400">{r.error}</div>}
              </Td>
              <Td className="max-w-xs">
                {r.file ? (
                  <>
                    <Mono className="block truncate" title={r.file}>
                      {r.file}
                    </Mono>
                    <div className="text-xs text-zinc-500">
                      {formatBytes(r.size)}
                      {r.encrypted ? ' · encrypted' : ''} · {r.contents.join(', ')}
                    </div>
                  </>
                ) : (
                  '—'
                )}
              </Td>
              <Td className="max-w-sm">
                <ul className="space-y-0.5 text-xs">
                  {r.destinations.map((d) => (
                    <li key={d.id} className="flex items-start gap-1.5">
                      {d.ok ? <CheckCircle2 className="mt-px h-3.5 w-3.5 shrink-0 text-emerald-600" /> : <XCircle className="mt-px h-3.5 w-3.5 shrink-0 text-red-600" />}
                      <span>
                        <span className="font-medium">{d.name}</span>
                        {d.pruned > 0 && <span className="text-zinc-500"> · {d.pruned} old deleted</span>}
                        {d.error && <span className="block text-zinc-500">{d.error}</span>}
                      </span>
                    </li>
                  ))}
                </ul>
              </Td>
              <Td className="whitespace-nowrap">{durationBetween(r.startedAt, r.finishedAt)}</Td>
            </Tr>
          ))}
        </TBody>
      </Table>
    </Card>
  );
}

// ---------------------------------------------------------------- restore

function RestoreCard({ destinations }: { destinations: BackupDestination[] }) {
  const toast = useToast();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const [file, setFile] = useState<File | null>(null);
  const [passphrase, setPassphrase] = useState('');
  const [fromDest, setFromDest] = useState(false);
  const [result, setResult] = useState<RestoreResult | null>(null);
  const done = (r: RestoreResult) => {
    setResult(r);
    setFile(null);
    setPassphrase('');
    toast.success('Backup restored', 'Sites, certificates and settings were reloaded.');
    void qc.invalidateQueries();
  };
  const restore = useMutation({ mutationFn: () => serverApi.restore(file!, passphrase), onSuccess: done });
  const isZip = !!file && /\.zip$/i.test(file.name);

  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <RotateCcw className="h-4 w-4 text-zinc-400" />
          Restore
        </span>
      }
      tone="danger"
      actions={
        <Button size="sm" icon={<Archive className="h-3.5 w-3.5" />} disabled={destinations.length === 0} onClick={() => setFromDest(true)}>
          Restore from…
        </Button>
      }
    >
      <div className="space-y-4">
        <Callout tone="warning" icon={<AlertTriangle />}>
          Restoring replaces the server settings and overwrites sites that have the same ID as in the backup; other sites are kept. Certificates this server already has are
          kept. A site's shared folder is replaced (the site is stopped meanwhile; the current folder is kept as <Mono>shared.pre-restore</Mono>).
        </Callout>
        {restore.isError && <ErrorBox>{errorMessage(restore.error)}</ErrorBox>}
        {result && <RestoreSummary r={result} />}
        <FileDrop
          file={file}
          onFile={(f) => {
            setFile(f);
            setResult(null);
          }}
          accept=".zip,.json,application/zip,application/json"
          icon={<FileArchive />}
          label="Drop a nodehoster-backup-….zip archive (or an older .json export), or click to browse"
        />
        {isZip && (
          <Field label="Passphrase" hint="Only for an encrypted archive.">
            <Input type="password" className="w-80" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} autoComplete="off" />
          </Field>
        )}
        <div className="flex justify-end">
          <Button
            variant="danger"
            icon={<RotateCcw className="h-4 w-4" />}
            disabled={!file}
            loading={restore.isPending}
            onClick={async () => {
              if (!file) return;
              const r = await confirm({
                title: 'Restore from backup?',
                message: (
                  <>
                    Settings and matching sites will be replaced by the contents of <span className="font-mono">{file.name}</span>.
                  </>
                ),
                confirmLabel: 'Restore',
                danger: true,
              });
              if (r.ok) restore.mutate();
            }}
          >
            Restore
          </Button>
        </div>
      </div>
      <RestoreFromDialog open={fromDest} destinations={destinations} onClose={() => setFromDest(false)} onDone={done} />
    </Card>
  );
}

function RestoreSummary({ r }: { r: RestoreResult }) {
  return (
    <Callout tone={r.warnings.length ? 'warning' : 'success'} icon={r.warnings.length ? <AlertTriangle /> : <CheckCircle2 />} title="Restored">
      <div>
        {r.sites} site{r.sites === 1 ? '' : 's'}
        {r.hostname ? ` from ${r.hostname}` : ''}
        {r.created ? `, backed up ${formatDateTime(r.created)}` : ''}
        {r.certificates > 0 && `; ${r.certificates} certificate${r.certificates === 1 ? '' : 's'} with keys`}
        {r.sharedSites.length > 0 && `; shared folders of ${r.sharedSites.join(', ')}`}.
      </div>
      {r.warnings.length > 0 && (
        <ul className="mt-1 list-disc pl-5">
          {r.warnings.map((w) => (
            <li key={w}>{w}</li>
          ))}
        </ul>
      )}
    </Callout>
  );
}

function RestoreFromDialog({
  open,
  destinations,
  onClose,
  onDone,
}: {
  open: boolean;
  destinations: BackupDestination[];
  onClose: () => void;
  onDone: (r: RestoreResult) => void;
}) {
  const confirm = useConfirm();
  const [dest, setDest] = useState('');
  const [pick, setPick] = useState<BackupObject | null>(null);
  const [passphrase, setPassphrase] = useState('');
  useEffect(() => {
    if (open) {
      setDest((d) => d || destinations[0]?.id || '');
      setPick(null);
      setPassphrase('');
    }
  }, [open, destinations]);
  const files = useQuery({ queryKey: qk.backupFiles(dest), queryFn: () => backupsApi.files(dest), enabled: open && !!dest, staleTime: 0 });
  const restore = useMutation({
    mutationFn: () => backupsApi.restoreFrom(dest, pick!.name, passphrase),
    onSuccess: (r) => {
      onClose();
      onDone(r);
    },
  });
  const status = useQuery({ queryKey: qk.backups, queryFn: backupsApi.status, enabled: open });
  const host = status.data?.hostname;
  const sorted = useMemo(() => files.data ?? [], [files.data]);

  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="xl"
      title="Restore from a destination"
      description="Archives from every server are listed, so a replacement server can restore the one it replaces."
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="danger"
            icon={<RotateCcw className="h-4 w-4" />}
            disabled={!pick}
            loading={restore.isPending}
            onClick={async () => {
              if (!pick) return;
              const r = await confirm({ title: 'Restore this backup?', message: <span className="font-mono">{pick.name}</span>, confirmLabel: 'Restore', danger: true });
              if (r.ok) restore.mutate();
            }}
          >
            Restore
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Grid>
          <Field label="Destination">
            <Select
              value={dest}
              onChange={(v) => {
                setDest(v);
                setPick(null);
              }}
              options={destinations.map((d) => ({ value: d.id, label: d.name }))}
            />
          </Field>
          <Field label="Passphrase" hint="For encrypted archives.">
            <Input type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} autoComplete="off" />
          </Field>
        </Grid>
        {files.isError && <ErrorBox>{errorMessage(files.error)}</ErrorBox>}
        {restore.isError && <ErrorBox>{errorMessage(restore.error)}</ErrorBox>}
        <div className="max-h-80 overflow-auto rounded-md border border-zinc-200 dark:border-zinc-800">
          <Table dense>
            <THead>
              <tr>
                <Th />
                <Th>Archive</Th>
                <Th>Server</Th>
                <Th>Created</Th>
                <Th>Size</Th>
              </tr>
            </THead>
            <TBody>
              {files.isPending && <TableMessage colSpan={5}>Listing…</TableMessage>}
              {files.data?.length === 0 && <TableMessage colSpan={5}>No archives at this destination.</TableMessage>}
              {sorted.map((o) => (
                <Tr key={o.name} onClick={() => setPick(o)} className="cursor-pointer">
                  <Td className="w-8">
                    <input type="radio" readOnly checked={pick?.name === o.name} className="accent-accent-600" />
                  </Td>
                  <Td>
                    <Mono>{o.name}</Mono>
                  </Td>
                  <Td>
                    {o.host}
                    {host && o.host.toLowerCase() === host.toLowerCase() && (
                      <Badge tone="accent" className="ml-1.5">
                        this server
                      </Badge>
                    )}
                  </Td>
                  <Td className="whitespace-nowrap">{formatDateTime(o.created)}</Td>
                  <Td className="whitespace-nowrap">{formatBytes(o.size)}</Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        </div>
      </div>
    </Dialog>
  );
}

