import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, RotateCcw, Save } from 'lucide-react';
import { certsApi, settingsApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { AdminSettings } from '@/api/types';
import { Button } from '@/components/Button';
import { Card, Callout, FormSection, Loading, Sections } from '@/components/Layout';
import { ErrorBox, Field, FormErrorBanner, FormErrors } from '@/components/Field';
import { Input, Select } from '@/components/Input';
import { Radio } from '@/components/Switch';
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
