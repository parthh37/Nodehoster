import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { certsApi, settingsApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { CertificateView } from '@/api/types';
import { Button } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { Field, FormErrorBanner, FormErrors } from '@/components/Field';
import { Input, NumberInput, Select } from '@/components/Input';
import { Radio, Switch } from '@/components/Switch';
import { Callout } from '@/components/Layout';
import { ListEditor } from '@/components/ListEditor';
import { FileDrop } from '@/components/FileDrop';
import { Segmented } from '@/components/Tabs';
import { useToast } from '@/components/Toast';
import { HOST_RE } from '@/lib/siteDefaults';

const validateDomain = (v: string) => (HOST_RE.test(v.toLowerCase()) ? null : 'Not a valid domain name');

export const KEY_TYPES = [
  { value: 'ec256', label: 'ECDSA P-256 (recommended)' },
  { value: 'ec384', label: 'ECDSA P-384' },
  { value: 'rsa2048', label: 'RSA 2048' },
  { value: 'rsa4096', label: 'RSA 4096' },
];

function useInvalidateCerts() {
  const qc = useQueryClient();
  return () => void qc.invalidateQueries({ queryKey: qk.certs });
}

export function AcmeDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const toast = useToast();
  const invalidate = useInvalidateCerts();
  const settings = useQuery({ queryKey: qk.settings, queryFn: settingsApi.get, enabled: open });
  const [name, setName] = useState('');
  const [domains, setDomains] = useState<string[]>([]);
  const [challenge, setChallenge] = useState<'http-01' | 'dns-01'>('http-01');
  const [dnsProviderId, setDns] = useState('');
  const [keyType, setKeyType] = useState('');
  const [autoRenew, setAutoRenew] = useState(true);

  useEffect(() => {
    if (!open) return;
    setName('');
    setDomains([]);
    setChallenge('http-01');
    setDns('');
    setKeyType('');
    setAutoRenew(true);
    m.reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const providers = settings.data?.dnsProviders ?? [];
  const hasWildcard = domains.some((d) => d.startsWith('*.'));
  const acmeReady = !!settings.data?.acme.email && !!settings.data?.acme.agreeTos;

  const m = useMutation({
    mutationFn: () =>
      certsApi.requestAcme({
        name: name.trim() || domains[0],
        domains,
        autoRenew,
        acme: { challenge, dnsProviderId: challenge === 'dns-01' ? dnsProviderId : undefined, keyType: keyType || undefined },
      }),
    onSuccess: (c) => {
      toast.info(`Requesting ${c.name}`, 'Issuance runs in the background; the list updates when it completes.');
      invalidate();
      onClose();
    },
  });

  const blocked = domains.length === 0 || (challenge === 'dns-01' && !dnsProviderId) || (hasWildcard && challenge !== 'dns-01');

  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="lg"
      title="Request a Let's Encrypt certificate"
      description="Issued by the ACME directory configured in Settings."
      onSubmit={() => !blocked && m.mutate()}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={blocked} loading={m.isPending}>
            Request certificate
          </Button>
        </>
      }
    >
      <FormErrors error={m.error}>
        <div className="space-y-4">
          <FormErrorBanner />
          {settings.data && !acmeReady && (
            <Callout tone="warning" title="ACME account not configured">
              Set a contact email and accept the terms of service in{' '}
              <Link to="/settings/acme" className="nh-link" onClick={onClose}>
                Settings → ACME
              </Link>{' '}
              first.
            </Callout>
          )}
          <Field label="Domains" path="domains" prefix hint="The first domain is the certificate's common name. Use *.example.com for a wildcard (DNS-01 only).">
            <ListEditor values={domains} onChange={(v) => setDomains(v.map((d) => d.toLowerCase()))} placeholder="www.example.com" validate={validateDomain} />
          </Field>
          <Field label="Friendly name" path="name" hint="Defaults to the first domain.">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={domains[0] ?? 'example.com'} />
          </Field>
          <Field label="Challenge" path="acme.challenge">
            <Radio
              value={challenge}
              onChange={setChallenge}
              options={[
                { value: 'http-01', label: 'HTTP-01', description: 'DNS points here; port 80 reachable from the internet.' },
                { value: 'dns-01', label: 'DNS-01', description: 'A TXT record via your DNS provider. Required for wildcards.' },
              ]}
            />
          </Field>
          {hasWildcard && challenge !== 'dns-01' && <Callout tone="warning">Wildcard domains require the DNS-01 challenge.</Callout>}
          {challenge === 'dns-01' && (
            <Field label="DNS provider" path="acme.dnsProviderId">
              {providers.length ? (
                <Select value={dnsProviderId} onChange={setDns} placeholder="Choose a provider…" options={providers.map((p) => ({ value: p.id, label: `${p.name} (${p.provider})` }))} />
              ) : (
                <Callout tone="info">
                  No DNS providers configured.{' '}
                  <Link to="/settings/dns" className="nh-link" onClick={onClose}>
                    Add one in Settings → DNS providers
                  </Link>
                  .
                </Callout>
              )}
            </Field>
          )}
          <Field label="Key type" path="acme.keyType">
            <Select
              value={keyType}
              onChange={setKeyType}
              options={[{ value: '', label: `Default (${settings.data?.acme.keyType || 'ec256'})` }, ...KEY_TYPES]}
            />
          </Field>
          <Switch checked={autoRenew} onChange={setAutoRenew} label="Renew automatically" description="Renewed ahead of expiry and hot-swapped without restarting sites." />
        </div>
      </FormErrors>
    </Dialog>
  );
}

export function ImportDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const toast = useToast();
  const invalidate = useInvalidateCerts();
  const [format, setFormat] = useState<'pfx' | 'pem'>('pfx');
  const [file, setFile] = useState<File | null>(null);
  const [keyFile, setKeyFile] = useState<File | null>(null);
  const [password, setPassword] = useState('');
  const [name, setName] = useState('');

  useEffect(() => {
    if (!open) return;
    setFormat('pfx');
    setFile(null);
    setKeyFile(null);
    setPassword('');
    setName('');
    m.reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const m = useMutation({
    mutationFn: () => certsApi.import({ file: file!, keyFile: format === 'pem' ? keyFile : null, password: format === 'pfx' ? password : undefined, name: name.trim() || undefined }),
    onSuccess: (c) => {
      toast.success(`Imported ${c.name}`);
      invalidate();
      onClose();
    },
  });

  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="lg"
      title="Import a certificate"
      description="From a PKCS#12 file (.pfx / .p12, e.g. exported from IIS) or PEM files."
      onSubmit={() => file && m.mutate()}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!file} loading={m.isPending}>
            Import
          </Button>
        </>
      }
    >
      <FormErrors error={m.error}>
        <div className="space-y-4">
          <FormErrorBanner />
          <Segmented
            value={format}
            onChange={(f) => {
              setFormat(f);
              setFile(null);
              setKeyFile(null);
            }}
            options={[
              { value: 'pfx', label: 'PFX / P12' },
              { value: 'pem', label: 'PEM' },
            ]}
          />
          {format === 'pfx' ? (
            <>
              <Field label="Certificate file" path="file">
                <FileDrop file={file} onFile={setFile} accept=".pfx,.p12" label="Drop a .pfx or .p12 file, or click to browse" compact />
              </Field>
              <Field label="Password" path="password">
                <Input type="password" autoComplete="off" value={password} onChange={(e) => setPassword(e.target.value)} />
              </Field>
            </>
          ) : (
            <>
              <Field label="Certificate (with chain)" path="file">
                <FileDrop file={file} onFile={setFile} accept=".pem,.crt,.cer" label="Drop fullchain.pem / .crt" hint="May also contain the private key." compact />
              </Field>
              <Field label="Private key" path="keyFile" hint="Optional if the certificate file already includes the key.">
                <FileDrop file={keyFile} onFile={setKeyFile} accept=".pem,.key" label="Drop privkey.pem / .key" compact />
              </Field>
            </>
          )}
          <Field label="Friendly name" path="name" hint="Defaults to the certificate's subject.">
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
        </div>
      </FormErrors>
    </Dialog>
  );
}

export function SelfSignedDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const toast = useToast();
  const invalidate = useInvalidateCerts();
  const [name, setName] = useState('');
  const [domains, setDomains] = useState<string[]>([]);
  const [days, setDays] = useState(365);

  useEffect(() => {
    if (!open) return;
    setName('');
    setDomains([]);
    setDays(365);
    m.reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const m = useMutation({
    mutationFn: () => certsApi.selfSigned({ name: name.trim() || domains[0], domains, validDays: days }),
    onSuccess: (c) => {
      toast.success(`Created ${c.name}`);
      invalidate();
      onClose();
    },
  });

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Create a self-signed certificate"
      description="For testing and internal use. Browsers will show a warning."
      onSubmit={() => domains.length && m.mutate()}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!domains.length || days < 1} loading={m.isPending}>
            Create
          </Button>
        </>
      }
    >
      <FormErrors error={m.error}>
        <div className="space-y-4">
          <FormErrorBanner />
          <Field label="Domains" path="domains" prefix>
            <ListEditor values={domains} onChange={(v) => setDomains(v.map((d) => d.toLowerCase()))} placeholder="localhost" validate={validateDomain} />
          </Field>
          <Field label="Friendly name" path="name">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={domains[0] ?? ''} />
          </Field>
          <Field label="Valid for" path="validDays">
            <NumberInput className="w-40" min={1} max={3650} value={days} onChange={setDays} suffix="days" />
          </Field>
        </div>
      </FormErrors>
    </Dialog>
  );
}

export function ExportDialog({ cert, onClose }: { cert: CertificateView | null; onClose: () => void }) {
  const toast = useToast();
  const [format, setFormat] = useState<'pfx' | 'pem'>('pfx');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    setFormat('pfx');
    setPassword('');
    setError(null);
  }, [cert]);

  const run = async () => {
    if (!cert) return;
    setBusy(true);
    setError(null);
    try {
      const safe = cert.name.replace(/[^\w.-]+/g, '_');
      await certsApi.export(cert.id, format, format === 'pfx' ? password : undefined, format === 'pfx' ? `${safe}.pfx` : `${safe}-pem.zip`);
      toast.success('Certificate exported');
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={!!cert}
      onClose={onClose}
      size="sm"
      title={`Export ${cert?.name ?? ''}`}
      description="Includes the private key. Keep the file safe."
      onSubmit={run}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" loading={busy} disabled={format === 'pfx' && !password}>
            Download
          </Button>
        </>
      }
    >
      <FormErrors error={error instanceof Error ? error : null}>
        <div className="space-y-4">
          <FormErrorBanner />
          <Radio
            value={format}
            onChange={setFormat}
            options={[
              { value: 'pfx', label: 'PFX', description: 'Password-protected PKCS#12 for IIS / Windows.' },
              { value: 'pem', label: 'PEM', description: 'ZIP with certificate, chain and key.' },
            ]}
          />
          {format === 'pfx' && (
            <Field label="Password" path="password" hint="Required to protect the private key.">
              <Input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
          )}
        </div>
      </FormErrors>
    </Dialog>
  );
}

export function EditCertDialog({ cert, onClose }: { cert: CertificateView | null; onClose: () => void }) {
  const toast = useToast();
  const invalidate = useInvalidateCerts();
  const [name, setName] = useState('');
  const [autoRenew, setAutoRenew] = useState(false);
  useEffect(() => {
    if (cert) {
      setName(cert.name);
      setAutoRenew(cert.autoRenew);
    }
    m.reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cert]);
  const m = useMutation({
    mutationFn: () => certsApi.update(cert!.id, { name: name.trim(), autoRenew }),
    onSuccess: () => {
      toast.success('Certificate updated');
      invalidate();
      onClose();
    },
  });
  return (
    <Dialog
      open={!!cert}
      onClose={onClose}
      size="sm"
      title="Edit certificate"
      onSubmit={() => name.trim() && m.mutate()}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!name.trim()} loading={m.isPending}>
            Save
          </Button>
        </>
      }
    >
      <FormErrors error={m.error}>
        <div className="space-y-4">
          <FormErrorBanner />
          <Field label="Friendly name" path="name">
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          {cert?.source === 'acme' && (
            <Switch checked={autoRenew} onChange={setAutoRenew} label="Renew automatically" />
          )}
        </div>
      </FormErrors>
    </Dialog>
  );
}
