import { useEffect, useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Plug, ShieldAlert, ShieldCheck } from 'lucide-react';
import { serversApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { ServerConnection, ServerTestResult, ServerView } from '@/api/types';
import { Button } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { Field, FormErrorBanner, FormErrors } from '@/components/Field';
import { Input, Select } from '@/components/Input';
import { Callout, KV, Mono } from '@/components/Layout';
import { SecretInput } from '@/components/SecretInput';
import { useToast } from '@/components/Toast';
import { formatDate } from '@/lib/format';
import { formatFingerprint, parseFingerprint, serverUrlError } from '@/lib/servers';

const ROLE_OPTIONS = [
  { value: 'admin', label: 'Administrators' },
  { value: 'operator', label: 'Operators and administrators' },
  { value: 'viewer', label: 'Every user with a server role (viewers read only)' },
];

type Draft = Omit<ServerConnection, 'id'>;

const empty: Draft = { name: '', url: '', token: '', fingerprint: '', minRole: 'admin' };

/**
 * Adds or edits a connection. "Test connection" shows the certificate the
 * server presents; an untrusted one (self-signed) can be pinned after
 * comparing its fingerprint with the server's (trust on first use). The
 * token is only sent once the connection is trusted.
 */
export function ServerDialog({ open, server, onClose }: { open: boolean; server: ServerView | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [d, setD] = useState<Draft>(empty);
  const [result, setResult] = useState<ServerTestResult | null>(null);
  useEffect(() => {
    if (!open) return;
    setD(server ? { name: server.name, url: server.url, token: server.token, fingerprint: server.fingerprint ?? '', minRole: server.minRole } : empty);
    setResult(null);
    save.reset();
    test.reset();
  }, [open, server]); // eslint-disable-line react-hooks/exhaustive-deps
  const set = (p: Partial<Draft>) => {
    setD((x) => ({ ...x, ...p }));
    if (p.url !== undefined || p.token !== undefined || p.fingerprint !== undefined) setResult(null);
  };

  const fp = parseFingerprint(d.fingerprint ?? '');
  const urlErr = d.url ? serverUrlError(d.url) : null;
  const complete = !!d.url.trim() && !urlErr && !!d.token && fp !== null;

  const test = useMutation({
    mutationFn: (fingerprint: string) => serversApi.test({ id: server?.id, url: d.url.trim(), token: d.token, fingerprint }),
    onSuccess: setResult,
  });
  const save = useMutation({
    mutationFn: () => {
      const body = { ...d, name: d.name.trim(), url: d.url.trim(), fingerprint: fp ?? '' };
      return server ? serversApi.update(server.id, { ...body, id: server.id }) : serversApi.create(body);
    },
    onSuccess: (v) => {
      toast.success(server ? `${v.name} saved` : `${v.name} added`, v.health.checkedAt ? undefined : 'Its health shows in a few seconds.');
      void qc.invalidateQueries({ queryKey: qk.servers });
      // The server checks the connection in the background: show its health.
      window.setTimeout(() => void qc.invalidateQueries({ queryKey: qk.servers }), 3000);
      onClose();
    },
  });
  const trust = (fingerprint: string) => {
    set({ fingerprint });
    test.mutate(fingerprint);
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="lg"
      title={server ? `Edit ${server.name}` : 'Connect to a server'}
      description="Another NodeHoster server's web console, managed from this one with an API token created there."
      onSubmit={() => complete && d.name.trim() && save.mutate()}
      footer={
        <>
          <Button className="mr-auto" icon={<Plug className="h-3.5 w-3.5" />} disabled={!complete} loading={test.isPending} onClick={() => test.mutate(fp ?? '')}>
            Test connection
          </Button>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!complete || !d.name.trim()} loading={save.isPending}>
            {server ? 'Save' : 'Add server'}
          </Button>
        </>
      }
    >
      <FormErrors error={save.error ?? test.error}>
        <div className="space-y-4">
          <FormErrorBanner />
          <Field label="Name" path="name" hint="How the server appears in the switcher, e.g. web02 or Production EU.">
            <Input value={d.name} onChange={(e) => set({ name: e.target.value })} autoComplete="off" spellCheck={false} />
          </Field>
          <Field label="Web console URL" path="url" error={urlErr} hint="Where that server's web console listens, e.g. https://web02:8484.">
            <Input mono value={d.url} placeholder="https://web02:8484" onChange={(e) => set({ url: e.target.value })} autoComplete="off" spellCheck={false} />
          </Field>
          <Field
            label="API token"
            path="token"
            hint="Create one on that server (Account → API tokens), for this purpose only, limited to the role this server's users need there. It is encrypted here and never shown again."
          >
            <SecretInput value={d.token} onChange={(v) => set({ token: v })} placeholder="nh_…" allowClear={false} />
          </Field>
          <Field
            label="Who may use it"
            path="minRole"
            hint="Users of this server with at least this role. On that server they never get more than their role here, nor more than the token allows."
          >
            <Select value={d.minRole} onChange={(v) => set({ minRole: v as Draft['minRole'] })} options={ROLE_OPTIONS} />
          </Field>
          <Field
            label="Pinned certificate (SHA-256)"
            path="fingerprint"
            error={fp === null ? 'A SHA-256 fingerprint is 64 hexadecimal digits' : null}
            hint="For a self-signed certificate: the connection then accepts that certificate only. Leave empty when the certificate is issued by a trusted authority."
          >
            <Input mono value={d.fingerprint ?? ''} placeholder="Test the connection to see it" onChange={(e) => set({ fingerprint: e.target.value })} spellCheck={false} />
          </Field>
          {result && <TestResult result={result} pinned={fp ?? ''} onTrust={trust} trusting={test.isPending} />}
        </div>
      </FormErrors>
    </Dialog>
  );
}

function TestResult({ result, pinned, onTrust, trusting }: { result: ServerTestResult; pinned: string; onTrust: (fp: string) => void; trusting: boolean }) {
  const c = result.certificate;
  const h = result.health;
  return (
    <div className="space-y-3">
      {c && (
        <div className="rounded-md border border-zinc-200 p-3 dark:border-zinc-800">
          <p className="mb-2 flex items-center gap-1.5 text-[13px] font-semibold">
            {result.trusted ? <ShieldCheck className="h-4 w-4 text-emerald-600" /> : <ShieldAlert className="h-4 w-4 text-amber-600" />}
            Certificate {c.verified ? 'issued by a trusted authority' : pinned && c.fingerprint === pinned ? 'pinned' : 'not trusted'}
          </p>
          <KV
            items={[
              ['Subject', c.subject || '—'],
              ['Issuer', c.issuer || '—'],
              ['Names', c.dnsNames.length ? c.dnsNames.join(', ') : '—'],
              ['Valid', `${formatDate(c.notBefore)} – ${formatDate(c.notAfter)}`],
              ['SHA-256', <Mono key="fp" className="break-all">{formatFingerprint(c.fingerprint)}</Mono>],
            ]}
          />
        </div>
      )}
      {c && !result.trusted && (
        <Callout
          tone={pinned ? 'danger' : 'warning'}
          title={pinned ? 'The server presented another certificate than the pinned one' : 'Trust this certificate?'}
          actions={
            <Button size="sm" onClick={() => onTrust(c.fingerprint)} loading={trusting}>
              {pinned ? 'Pin the new one' : 'Trust and pin'}
            </Button>
          }
        >
          {c.verifyError && <p className="mb-1">{c.verifyError}</p>}
          Compare the fingerprint above with the one of that server's web console certificate (its Certificates page shows SHA-256 fingerprints). Pin it only if they are
          the same: otherwise something between the servers may be listening in.
        </Callout>
      )}
      {result.trusted &&
        (h.reachable ? (
          <Callout tone="success" title={`Connected to ${h.hostname || 'the server'}`}>
            NodeHoster {h.version || '(version unknown)'} · as {h.user || 'the token user'} ({h.role === 'sites' ? 'limited to some sites' : h.role || 'role unknown'}) · {h.sites}{' '}
            {h.sites === 1 ? 'site' : 'sites'}
          </Callout>
        ) : (
          <Callout tone="danger" title="The server cannot be used">
            {h.error}
          </Callout>
        ))}
      {!c && !result.trusted && h.error && (
        <Callout tone="danger" title="The server cannot be reached">
          {h.error}
        </Callout>
      )}
    </div>
  );
}
