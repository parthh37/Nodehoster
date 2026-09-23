import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, DatabaseBackup, Download, FileJson, RotateCcw, Save } from 'lucide-react';
import { certsApi, serverApi, settingsApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { AdminSettings } from '@/api/types';
import { Button } from '@/components/Button';
import { Card, Callout, FormSection, Loading, Sections } from '@/components/Layout';
import { ErrorBox, Field, FormErrorBanner, FormErrors } from '@/components/Field';
import { Input, Select } from '@/components/Input';
import { Radio } from '@/components/Switch';
import { FileDrop } from '@/components/FileDrop';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { jsonEqual } from '@/lib/obj';

export function AdminConsoleTab() {
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: qk.adminSettings, queryFn: settingsApi.getAdmin });
  const certs = useQuery({ queryKey: qk.certs, queryFn: certsApi.list, staleTime: 30_000 });
  const [form, setForm] = useState<AdminSettings | null>(null);

  useEffect(() => {
    if (q.data) setForm(q.data);
  }, [q.data]);

  const save = useMutation({
    mutationFn: (a: AdminSettings) => settingsApi.putAdmin(a),
    onSuccess: (a) => {
      qc.setQueryData(qk.adminSettings, a);
      setForm(a);
      toast.success('Admin console settings saved', a.restartRequired ? 'Restart the NodeHoster service to apply them.' : undefined);
    },
  });

  if (q.isPending || !form) return q.isError ? <ErrorBox>{errorMessage(q.error)}</ErrorBox> : <Loading />;
  const dirty = !!q.data && !jsonEqual(form, q.data);
  const validCerts = (certs.data ?? []).filter((c) => c.status === 'valid');

  return (
    <FormErrors error={save.error}>
      <div className="space-y-4">
        {form.restartRequired && (
          <Callout tone="warning" icon={<AlertTriangle />} title="Restart required">
            The saved settings differ from the ones the console is running with. Restart the NodeHoster Windows service to apply them.
          </Callout>
        )}
        <FormErrorBanner />
        <Card
          title="Admin console"
          description="Where this console listens. Changes take effect after the NodeHoster service restarts."
          footer={
            <div className="flex items-center justify-end gap-2">
              <Button icon={<RotateCcw className="h-3.5 w-3.5" />} disabled={!dirty || save.isPending} onClick={() => setForm(q.data!)}>
                Reset
              </Button>
              <Button variant="primary" icon={<Save className="h-3.5 w-3.5" />} disabled={!dirty} loading={save.isPending} onClick={() => save.mutate(form)}>
                Save
              </Button>
            </div>
          }
        >
          <Sections>
            <FormSection title="Listener" description="Address and port, e.g. 0.0.0.0:8484 for all interfaces or 127.0.0.1:8484 for local access only.">
              <Field label="Listen address" path="listen">
                <Input mono className="w-72" value={form.listen} onChange={(e) => setForm({ ...form, listen: e.target.value.trim() })} placeholder="0.0.0.0:8484" />
              </Field>
            </FormSection>
            <FormSection title="HTTPS" description="The console always sets a Strict session cookie; use HTTPS whenever it is reachable over a network.">
              <Field label="TLS" path="tls">
                <Radio
                  value={form.tls as 'selfsigned' | 'certificate' | 'none'}
                  onChange={(v) => setForm({ ...form, tls: v, certificateId: v === 'certificate' ? form.certificateId : '' })}
                  className="flex-col"
                  options={[
                    { value: 'selfsigned', label: 'Self-signed certificate', description: 'Generated automatically. Browsers show a warning.' },
                    { value: 'certificate', label: 'Certificate from the store', description: 'Use a trusted certificate for the console host name.' },
                    { value: 'none', label: 'None (plain HTTP)', description: 'Only behind another TLS-terminating proxy or on localhost.' },
                  ]}
                />
              </Field>
              {form.tls === 'certificate' && (
                <Field label="Certificate" path="certificateId">
                  <Select
                    value={form.certificateId}
                    onChange={(v) => setForm({ ...form, certificateId: v })}
                    placeholder={validCerts.length ? 'Choose…' : 'No valid certificates'}
                    options={validCerts.map((c) => ({ value: c.id, label: `${c.name} — ${(c.domains ?? []).join(', ')}` }))}
                  />
                </Field>
              )}
              {form.tls === 'none' && (
                <Callout tone="danger" icon={<AlertTriangle />}>
                  Passwords and session cookies will travel unencrypted. Only use this on 127.0.0.1 or behind a TLS proxy.
                </Callout>
              )}
            </FormSection>
          </Sections>
        </Card>
      </div>
    </FormErrors>
  );
}

export function BackupTab() {
  const toast = useToast();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const [file, setFile] = useState<File | null>(null);
  const restore = useMutation({
    mutationFn: (f: File) => serverApi.restore(f),
    onSuccess: () => {
      toast.success('Configuration restored', 'Sites, certificates and settings were reloaded.');
      setFile(null);
      void qc.invalidateQueries();
    },
  });

  return (
    <div className="space-y-5">
      <Card title={<span className="flex items-center gap-2"><DatabaseBackup className="h-4 w-4 text-zinc-400" />Backup</span>}>
        <div className="flex flex-wrap items-center justify-between gap-4">
          <p className="max-w-2xl text-[13px] text-zinc-600 dark:text-zinc-300">
            Download a JSON file with every site, certificate metadata (not the key files) and the server settings. Secrets stay encrypted with this server's key, so keep
            the file safe and restore it on the same machine or after restoring the NodeHoster data folder.
          </p>
          <a href={serverApi.backupUrl} download>
            <Button variant="primary" icon={<Download className="h-4 w-4" />}>
              Download backup
            </Button>
          </a>
        </div>
      </Card>
      <Card title={<span className="flex items-center gap-2"><RotateCcw className="h-4 w-4 text-zinc-400" />Restore</span>} tone="danger">
        <div className="space-y-4">
          <Callout tone="warning" icon={<AlertTriangle />}>
            Restoring replaces the server settings and overwrites sites that have the same ID as in the backup. Other sites are kept. Certificate files are not part of the backup.
          </Callout>
          {restore.isError && <ErrorBox>{errorMessage(restore.error)}</ErrorBox>}
          <FileDrop file={file} onFile={setFile} accept=".json,application/json" icon={<FileJson />} label="Drop a nodehoster-backup-….json file, or click to browse" />
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
                if (r.ok) restore.mutate(file);
              }}
            >
              Restore
            </Button>
          </div>
        </div>
      </Card>
    </div>
  );
}
