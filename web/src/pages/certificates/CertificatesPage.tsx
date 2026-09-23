import { Fragment, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertCircle, BadgeCheck, Download, FileKey, MoreHorizontal, Pencil, RefreshCw, ShieldPlus, Trash2, Upload } from 'lucide-react';
import { certsApi, settingsApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { CertificateView } from '@/api/types';
import { usePermissions } from '@/hooks/useAuth';
import { Button, IconButton } from '@/components/Button';
import { Card, EmptyState, Loading, Mono, PageHeader } from '@/components/Layout';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Badge, type Tone } from '@/components/Badge';
import { DaysLeft, StateBadge } from '@/components/StatusBadges';
import { Menu } from '@/components/Menu';
import { ErrorBox } from '@/components/Field';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { formatDate, relativeTime } from '@/lib/format';
import { AcmeDialog, EditCertDialog, ExportDialog, ImportDialog, SelfSignedDialog } from './CertDialogs';

const sourceTone: Record<string, Tone> = { acme: 'accent', imported: 'blue', selfsigned: 'gray' };
const sourceLabel: Record<string, string> = { acme: 'ACME', imported: 'Imported', selfsigned: 'Self-signed' };

type DialogKind = 'acme' | 'import' | 'selfsigned' | null;

export function CertificatesPage() {
  const { isAdmin, canOperate } = usePermissions();
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const q = useQuery({
    queryKey: qk.certs,
    queryFn: certsApi.list,
    refetchInterval: (query) => ((query.state.data ?? []).some((c) => c.status === 'pending') ? 3000 : 60_000),
  });
  const settings = useQuery({ queryKey: qk.settings, queryFn: settingsApi.get, enabled: isAdmin, staleTime: 60_000 });
  const warnDays = settings.data?.certExpiryWarnDays || 30;

  const [dialog, setDialog] = useState<DialogKind>(null);
  const [exporting, setExporting] = useState<CertificateView | null>(null);
  const [editing, setEditing] = useState<CertificateView | null>(null);

  const renew = useMutation({
    mutationFn: (c: CertificateView) => certsApi.renew(c.id),
    onSuccess: (_r, c) => {
      toast.info(`Renewal of ${c.name} started`, 'This usually takes under a minute.');
      void qc.invalidateQueries({ queryKey: qk.certs });
    },
    onError: (e) => toast.error('Could not renew certificate', e),
  });

  const del = useMutation({
    mutationFn: (c: CertificateView) => certsApi.remove(c.id),
    onSuccess: (_r, c) => {
      toast.success(`${c.name} deleted`);
      void qc.invalidateQueries({ queryKey: qk.certs });
    },
    onError: (e) => toast.error('Could not delete certificate', e),
  });

  const certs = useMemo(() => [...(q.data ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [q.data]);

  const onDelete = async (c: CertificateView) => {
    const inUse = (c.usedBy ?? []).length > 0;
    const r = await confirm({
      title: `Delete ${c.name}?`,
      message: inUse ? (
        <>
          This certificate is used by{' '}
          {(c.usedBy ?? []).map((u) => u.siteName).join(', ')}. Choose another certificate there first — the server will refuse to delete a
          certificate in use.
        </>
      ) : (
        'The certificate and its private key are removed from the store. This cannot be undone.'
      ),
      confirmLabel: 'Delete certificate',
      danger: true,
    });
    if (r.ok) del.mutate(c);
  };

  return (
    <div>
      <PageHeader
        title="Certificates"
        description="Server certificates used by HTTPS bindings. Let's Encrypt certificates renew automatically."
        actions={
          isAdmin && (
            <>
              <Button icon={<Upload className="h-4 w-4" />} onClick={() => setDialog('import')}>
                Import
              </Button>
              <Button icon={<FileKey className="h-4 w-4" />} onClick={() => setDialog('selfsigned')}>
                Self-signed
              </Button>
              <Button variant="primary" icon={<ShieldPlus className="h-4 w-4" />} onClick={() => setDialog('acme')}>
                Request certificate
              </Button>
            </>
          )
        }
      />
      {q.isError && <ErrorBox className="mb-4">{errorMessage(q.error)}</ErrorBox>}
      <Card flush>
        {q.isPending ? (
          <Loading />
        ) : certs.length === 0 ? (
          <EmptyState
            icon={<BadgeCheck />}
            title="No certificates yet"
            description={
              <>
                The easiest way to get one is to add an HTTPS binding with <em>Auto (Let's Encrypt)</em> on a site. You can also request a
                certificate here (DNS-01 for wildcards), import a .pfx / .pem, or create a self-signed certificate for testing.
              </>
            }
            action={
              isAdmin && (
                <>
                  <Button icon={<Upload className="h-4 w-4" />} onClick={() => setDialog('import')}>
                    Import
                  </Button>
                  <Button variant="primary" icon={<ShieldPlus className="h-4 w-4" />} onClick={() => setDialog('acme')}>
                    Request certificate
                  </Button>
                </>
              )
            }
          />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th>Name</Th>
                <Th>Domains</Th>
                <Th>Source</Th>
                <Th>Issuer</Th>
                <Th>Expires</Th>
                <Th>Status</Th>
                <Th>Auto-renew</Th>
                <Th>Used by</Th>
                <Th className="w-10" />
              </tr>
            </THead>
            <TBody>
              {certs.map((c) => (
                <Fragment key={c.id}>
                  <Tr className={c.lastError ? '[&>td]:border-b-0' : undefined}>
                    <Td className="max-w-[14rem]">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate font-medium">{c.name}</span>
                        {c.managed && <Badge tone="gray" title="Created automatically for a binding with certMode=auto">managed</Badge>}
                      </div>
                      {c.fingerprint && <Mono className="block truncate text-2xs text-zinc-400" title={`SHA-256 ${c.fingerprint}`}>{c.fingerprint.slice(0, 23)}…</Mono>}
                    </Td>
                    <Td className="max-w-[16rem]">
                      <div className="flex flex-col gap-0.5">
                        {(c.domains ?? []).slice(0, 3).map((d) => (
                          <Mono key={d} className="truncate">
                            {d}
                          </Mono>
                        ))}
                        {(c.domains ?? []).length > 3 && <span className="text-xs text-zinc-500">+{(c.domains ?? []).length - 3} more</span>}
                      </div>
                    </Td>
                    <Td>
                      <Badge tone={sourceTone[c.source] ?? 'gray'}>{sourceLabel[c.source] ?? c.source}</Badge>
                      {c.acme?.challenge && <span className="ml-1 font-mono text-2xs text-zinc-500">{c.acme.challenge}</span>}
                    </Td>
                    <Td className="max-w-[12rem] truncate text-xs text-zinc-600 dark:text-zinc-400" title={c.issuer}>
                      {c.issuer || '—'}
                    </Td>
                    <Td className="whitespace-nowrap">
                      <div className="text-xs">
                        <DaysLeft notAfter={c.notAfter} warnDays={warnDays} />
                      </div>
                      <div className="text-2xs text-zinc-500">{formatDate(c.notAfter)}</div>
                    </Td>
                    <Td>
                      <StateBadge state={c.status} />
                    </Td>
                    <Td>{c.autoRenew ? <Badge tone="green">on</Badge> : <span className="text-xs text-zinc-500">off</span>}</Td>
                    <Td className="max-w-[12rem]">
                      {(c.usedBy ?? []).length === 0 ? (
                        <span className="text-xs text-zinc-400">Not used</span>
                      ) : (
                        <div className="flex flex-col gap-0.5">
                          {(c.usedBy ?? []).slice(0, 3).map((u, i) => (
                            <Link key={i} to={u.siteId ? `/sites/${u.siteId}/bindings` : '/mail/settings'} className="nh-link truncate text-xs" title={u.binding}>
                              {u.siteName}
                            </Link>
                          ))}
                          {(c.usedBy ?? []).length > 3 && <span className="text-xs text-zinc-500">+{(c.usedBy ?? []).length - 3} more</span>}
                        </div>
                      )}
                    </Td>
                    <Td>
                      {canOperate && (
                        <Menu
                          trigger={(p) => <IconButton label="Actions" icon={<MoreHorizontal className="h-4 w-4" />} {...p} />}
                          items={[
                            {
                              label: 'Renew now',
                              icon: <RefreshCw />,
                              hidden: c.source !== 'acme',
                              disabled: c.status === 'pending',
                              onSelect: () => renew.mutate(c),
                            },
                            { label: 'Edit', icon: <Pencil />, hidden: !isAdmin, onSelect: () => setEditing(c) },
                            { label: 'Export', icon: <Download />, hidden: !isAdmin, disabled: c.status === 'pending', onSelect: () => setExporting(c) },
                            'separator',
                            { label: 'Delete', icon: <Trash2 />, danger: true, hidden: !isAdmin, onSelect: () => void onDelete(c) },
                          ]}
                        />
                      )}
                    </Td>
                  </Tr>
                  {c.lastError && (
                    <tr>
                      <td colSpan={9} className="px-4 pb-3">
                        <div className="flex items-start gap-2 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-xs text-red-800 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300">
                          <AlertCircle className="mt-px h-3.5 w-3.5 shrink-0" />
                          <div className="min-w-0">
                            <span className="font-semibold">Last attempt failed{c.lastAttempt ? ` ${relativeTime(c.lastAttempt)}` : ''}: </span>
                            <span className="break-words font-mono">{c.lastError}</span>
                          </div>
                        </div>
                      </td>
                    </tr>
                  )}
                </Fragment>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      <AcmeDialog open={dialog === 'acme'} onClose={() => setDialog(null)} />
      <ImportDialog open={dialog === 'import'} onClose={() => setDialog(null)} />
      <SelfSignedDialog open={dialog === 'selfsigned'} onClose={() => setDialog(null)} />
      <ExportDialog cert={exporting} onClose={() => setExporting(null)} />
      <EditCertDialog cert={editing} onClose={() => setEditing(null)} />
    </div>
  );
}
