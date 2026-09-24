import { useQuery } from '@tanstack/react-query';
import { Globe, Lock, Plus, Trash2 } from 'lucide-react';
import { certsApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { Binding, CertificateView } from '@/api/types';
import { Button, IconButton } from '@/components/Button';
import { Field, PathError } from '@/components/Field';
import { Input, NumberInput, Select } from '@/components/Input';
import { Badge } from '@/components/Badge';
import { Callout } from '@/components/Layout';
import { DaysLeft, stateTone } from '@/components/StatusBadges';
import { defaultBinding, HOST_RE } from '@/lib/siteDefaults';
import { bindingHref, defaultPort } from '@/lib/bindings';
import { cn } from '@/lib/cn';
import { usePermissions } from '@/hooks/useAuth';
import { siteSlots } from '@/lib/slots';
import type { SiteEditorProps } from './types';

export function BindingsEditor({ site, update, readOnly, compact }: SiteEditorProps & { compact?: boolean }) {
  // The certificate store is server-wide: not readable with access to selected sites only.
  const { siteScoped } = usePermissions();
  const certs = useQuery({ queryKey: qk.certs, queryFn: certsApi.list, staleTime: 30_000, enabled: !siteScoped });
  const bindings = site.bindings ?? [];
  const hasAuto = bindings.some((b) => b.protocol === 'https' && b.certMode === 'auto');
  // Deployment slots: each binding routes to production or to one slot.
  const slotNames = siteSlots(site).map((s) => s.name);

  const set = (i: number, patch: Partial<Binding>) =>
    update((d) => {
      d.bindings[i] = { ...d.bindings[i], ...patch };
    });

  const setProtocol = (i: number, protocol: string) =>
    update((d) => {
      const b = d.bindings[i];
      const wasDefault = b.port === defaultPort(b.protocol);
      b.protocol = protocol;
      if (wasDefault) b.port = defaultPort(protocol);
      if (protocol === 'https') {
        b.certMode = b.certificateId ? 'certificate' : 'auto';
      } else {
        b.certMode = '';
        b.certificateId = '';
      }
    });

  const add = (protocol: 'http' | 'https') =>
    update((d) => {
      const host = d.bindings.find((b) => b.host)?.host ?? '';
      d.bindings.push(defaultBinding(protocol, protocol === 'https' ? host : ''));
    });

  return (
    <div className="space-y-3">
      {bindings.length === 0 && (
        <Callout tone="warning" icon={<Globe />} title="No bindings">
          Without a binding the site cannot receive requests. Add at least one, e.g. <span className="font-mono">http *:80</span> with a host
          name.
        </Callout>
      )}
      {bindings.map((b, i) => (
        <BindingRow
          key={i}
          index={i}
          b={b}
          certs={certs.data ?? []}
          readOnly={readOnly}
          compact={compact}
          slots={slotNames.length > 0 ? slotNames : undefined}
          onChange={(p) => set(i, p)}
          onProtocol={(p) => setProtocol(i, p)}
          onRemove={() =>
            update((d) => {
              d.bindings.splice(i, 1);
            })
          }
        />
      ))}
      <PathError path="bindings" />
      {!readOnly && (
        <div className="flex gap-2">
          <Button size="sm" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => add('http')}>
            Add HTTP binding
          </Button>
          <Button size="sm" icon={<Lock className="h-3.5 w-3.5" />} onClick={() => add('https')}>
            Add HTTPS binding
          </Button>
        </div>
      )}
      {hasAuto && (
        <Callout tone="info" icon={<Lock />} title="Automatic certificates (Let's Encrypt)">
          NodeHoster requests and renews a certificate for each HTTPS binding set to <em>Auto</em>. The host name's DNS must point to this
          server and port 80 must be reachable from the internet for the HTTP-01 challenge. For wildcard names, request a DNS-01 certificate on
          the Certificates page and pick it here.
        </Callout>
      )}
    </div>
  );
}

function BindingRow({
  index,
  b,
  certs,
  readOnly,
  compact,
  slots,
  onChange,
  onProtocol,
  onRemove,
}: {
  index: number;
  b: Binding;
  certs: CertificateView[];
  readOnly?: boolean;
  compact?: boolean;
  /** The site's deployment slots, when it has any. */
  slots?: string[];
  onChange: (p: Partial<Binding>) => void;
  onProtocol: (p: string) => void;
  onRemove: () => void;
}) {
  const p = `bindings[${index}]`;
  const hostErr = b.host && !HOST_RE.test(b.host.trim().toLowerCase()) ? 'Not a valid host name' : null;
  const https = b.protocol === 'https';
  const autoWildcardErr =
    https && b.certMode === 'auto' && (!b.host || b.host.startsWith('*.'))
      ? 'Automatic certificates need a specific host name. Pick a certificate for wildcards or blank hosts.'
      : null;
  const managed = https && b.certMode === 'auto' && b.host ? certs.find((c) => c.managed && (c.domains ?? []).includes(b.host.toLowerCase())) : undefined;
  const href = bindingHref(b);

  return (
    <div className={cn('rounded-lg border border-zinc-200 bg-zinc-50/50 p-3 dark:border-zinc-800 dark:bg-zinc-900/40')}>
      <div className={cn('grid items-start gap-3', compact ? 'grid-cols-[6.5rem_1fr_6rem]' : 'grid-cols-[6.5rem_10rem_6rem_1fr_auto]')}>
        <Field label="Protocol" path={`${p}.protocol`}>
          <Select
            value={b.protocol}
            onChange={onProtocol}
            options={[
              { value: 'http', label: 'http' },
              { value: 'https', label: 'https' },
            ]}
            mono
          />
        </Field>
        <Field label="IP address" path={`${p}.ip`} className={compact ? 'col-span-1' : undefined}>
          <Input mono value={b.ip} placeholder="All Unassigned" onChange={(e) => onChange({ ip: e.target.value.trim() })} />
        </Field>
        <Field label="Port" path={`${p}.port`}>
          <NumberInput mono min={1} max={65535} value={b.port} onChange={(v) => onChange({ port: v })} />
        </Field>
        <Field
          label="Host name"
          path={`${p}.host`}
          error={hostErr}
          className={compact ? 'col-span-3' : undefined}
          hint={!b.host ? 'Blank answers any host name on this IP and port.' : undefined}
        >
          <Input mono value={b.host} placeholder="www.example.com" onChange={(e) => onChange({ host: e.target.value.trim().toLowerCase() })} />
        </Field>
        {!readOnly && !compact && (
          <div className="pt-6">
            <IconButton label="Remove binding" variant="danger-ghost" size="md" icon={<Trash2 className="h-4 w-4" />} onClick={onRemove} />
          </div>
        )}
      </div>
      {https && (
        <div className="mt-3 grid gap-3 border-t border-zinc-200 pt-3 dark:border-zinc-800 sm:grid-cols-[14rem_1fr]">
          <Field label="SSL certificate" path={`${p}.certMode`} error={autoWildcardErr}>
            <Select
              value={b.certMode || 'auto'}
              onChange={(v) => onChange({ certMode: v as Binding['certMode'], certificateId: v === 'auto' ? '' : b.certificateId })}
              options={[
                { value: 'auto', label: "Auto (Let's Encrypt)" },
                { value: 'certificate', label: 'Select a certificate' },
              ]}
            />
          </Field>
          {b.certMode === 'certificate' ? (
            <Field label="Certificate" path={`${p}.certificateId`}>
              <Select
                value={b.certificateId ?? ''}
                onChange={(v) => onChange({ certificateId: v })}
                placeholder={certs.length ? 'Choose…' : 'No certificates in the store'}
                options={certs.map((c) => ({
                  value: c.id,
                  label: `${c.name} — ${(c.domains ?? []).join(', ')}${c.status !== 'valid' ? ` (${c.status})` : ''}`,
                }))}
              />
            </Field>
          ) : (
            <div className="flex items-center gap-2 pt-6 text-xs text-zinc-500">
              {managed ? (
                <>
                  <Badge tone={stateTone(managed.status)} dot>
                    {managed.status}
                  </Badge>
                  {managed.status === 'valid' && <DaysLeft notAfter={managed.notAfter} />}
                  {managed.lastError && <span className="truncate text-red-600" title={managed.lastError}>{managed.lastError}</span>}
                </>
              ) : (
                <span>A certificate will be requested after saving.</span>
              )}
            </div>
          )}
        </div>
      )}
      {(slots || b.slot) && (
        <div className="mt-3 grid gap-3 border-t border-zinc-200 pt-3 dark:border-zinc-800 sm:grid-cols-[14rem_1fr]">
          <Field label="Deployment slot" path={`${p}.slot`}>
            <Select
              value={b.slot ?? ''}
              onChange={(v) => onChange({ slot: v })}
              options={[
                { value: '', label: 'Production' },
                ...(slots ?? []).map((s) => ({ value: s, label: s })),
                ...(b.slot && !(slots ?? []).includes(b.slot) ? [{ value: b.slot, label: `${b.slot} (no such slot)` }] : []),
              ]}
            />
          </Field>
          <p className="self-end pb-1.5 text-xs text-zinc-500">
            {b.slot
              ? `Requests on this binding go to the ${b.slot} slot. The binding stays with ${b.slot} when it is swapped.`
              : 'Requests on this binding go to production. The binding stays with production on a swap.'}
          </p>
        </div>
      )}
      <div className="mt-2 flex items-center justify-between gap-2">
        <PathError path={p} />
        {href && !compact && (
          <a href={href} target="_blank" rel="noreferrer" className="nh-link ml-auto font-mono text-xs">
            {href}
          </a>
        )}
        {!readOnly && compact && (
          <Button size="xs" variant="danger-ghost" icon={<Trash2 className="h-3 w-3" />} onClick={onRemove} className="ml-auto">
            Remove
          </Button>
        )}
      </div>
    </div>
  );
}
