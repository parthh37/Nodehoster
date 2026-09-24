import { useRef } from 'react';
import { FileUp, KeyRound } from 'lucide-react';
import type { ClientCertPolicy } from '@/api/types';
import { Button } from '@/components/Button';
import { Field } from '@/components/Field';
import { Select, Textarea } from '@/components/Input';
import { ListEditor } from '@/components/ListEditor';
import { Callout } from '@/components/Layout';
import { clientCertMode, fingerprintError, pathError, pemSummary, withClientCertMode, type ClientCertMode } from '@/lib/tls';

const MODES: { value: ClientCertMode; label: string }[] = [
  { value: 'ignore', label: 'Ignore' },
  { value: 'accept', label: 'Accept (optional)' },
  { value: 'require', label: 'Require' },
];

/**
 * A binding's client certificates (mutual TLS), like IIS "SSL Settings" ›
 * Client certificates: Ignore, Accept or Require, the trusted CAs and
 * optional allow lists.
 */
export function ClientCertEditor({
  policy,
  path,
  readOnly,
  onChange,
}: {
  policy: ClientCertPolicy | null | undefined;
  /** The binding's field path, e.g. bindings[0]. */
  path: string;
  readOnly?: boolean;
  onChange: (p: ClientCertPolicy | null) => void;
}) {
  const file = useRef<HTMLInputElement>(null);
  const mode = clientCertMode(policy);
  const p = `${path}.clientCert`;
  const set = (patch: Partial<ClientCertPolicy>) => onChange({ ...(policy ?? {}), mode, ...patch });
  const pem = pemSummary(policy?.caPem);

  const load = async (f: File | undefined) => {
    if (!f) return;
    const text = await f.text();
    const cur = (policy?.caPem ?? '').trim();
    set({ caPem: cur ? `${cur}\n${text.trim()}\n` : `${text.trim()}\n` });
  };

  return (
    <div className="mt-3 space-y-3 border-t border-zinc-200 pt-3 dark:border-zinc-800">
      <div className="grid gap-3 sm:grid-cols-[14rem_1fr]">
        <Field label="Client certificates" path={`${p}.mode`}>
          <Select value={mode} disabled={readOnly} onChange={(v) => onChange(withClientCertMode(policy, v as ClientCertMode))} options={MODES} />
        </Field>
        <p className="pt-6 text-xs text-zinc-500">
          {mode === 'ignore' && 'Clients are not asked for a certificate.'}
          {mode === 'accept' &&
            'Clients may present a certificate; the application learns whether it was valid. Paths below can require one.'}
          {mode === 'require' && 'The TLS handshake fails without a certificate issued by the CAs below.'}
        </p>
      </div>
      {mode !== 'ignore' && (
        <>
          <Field
            label="Trusted certificate authorities (PEM)"
            path={`${p}.caPem`}
            error={policy?.caPem ? pem.error : null}
            hint={pem.count ? `${pem.count} certificate${pem.count === 1 ? '' : 's'}. A self-signed client certificate may be listed to trust exactly it.` : 'The CA certificates (not keys) that issue your clients’ certificates.'}
            labelAction={
              !readOnly && (
                <>
                  <input ref={file} type="file" accept=".pem,.crt,.cer,.txt" className="hidden" onChange={(e) => void load(e.target.files?.[0]).then(() => (e.target.value = ''))} />
                  <Button size="xs" variant="ghost" icon={<FileUp className="h-3 w-3" />} onClick={() => file.current?.click()}>
                    Add from file
                  </Button>
                </>
              )
            }
          >
            <Textarea
              mono
              rows={4}
              readOnly={readOnly}
              value={policy?.caPem ?? ''}
              placeholder={'-----BEGIN CERTIFICATE-----\n…\n-----END CERTIFICATE-----'}
              onChange={(e) => set({ caPem: e.target.value })}
            />
          </Field>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Allowed subjects" path={`${p}.allowedSubjects`} prefix hint="Optional. Common name, subject DN or a DNS, email or URI name.">
              <ListEditor
                values={policy?.allowedSubjects}
                disabled={readOnly}
                path={`${p}.allowedSubjects`}
                onChange={(v) => set({ allowedSubjects: v })}
                placeholder="device-01.example.com"
                mono={false}
              />
            </Field>
            <Field label="Allowed fingerprints" path={`${p}.allowedFingerprints`} prefix hint="Optional. SHA-256 of the client certificate.">
              <ListEditor
                values={policy?.allowedFingerprints}
                disabled={readOnly}
                path={`${p}.allowedFingerprints`}
                validate={fingerprintError}
                onChange={(v) => set({ allowedFingerprints: v })}
                placeholder="3A:9F:…"
              />
            </Field>
          </div>
          {mode === 'accept' && (
            <Field
              label="Require a certificate for paths"
              path={`${p}.requirePaths`}
              prefix
              hint="Requests under these paths without a valid certificate get 403. TLS 1.3 cannot ask for a certificate later, so the handshake accepts and the path is checked here."
            >
              <ListEditor
                values={policy?.requirePaths}
                disabled={readOnly}
                path={`${p}.requirePaths`}
                validate={pathError}
                onChange={(v) => set({ requirePaths: v })}
                placeholder="/admin"
              />
            </Field>
          )}
          <Callout tone="info" icon={<KeyRound />} title="What the application receives">
            <span className="font-mono">X-Client-Verify</span> (SUCCESS, NONE or FAILED:reason) and, for a valid certificate,{' '}
            <span className="font-mono">X-Client-Cert</span> (URL-escaped PEM), <span className="font-mono">X-Client-Cert-Subject</span> and{' '}
            <span className="font-mono">X-Client-Cert-Fingerprint</span>. Copies sent by clients are always removed.
          </Callout>
        </>
      )}
    </div>
  );
}
