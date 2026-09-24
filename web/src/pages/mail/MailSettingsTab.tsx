import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, Code2, FileSignature, Info, KeyRound, Lock, MailCheck, Send, Server, ShieldCheck } from 'lucide-react';
import { certsApi, serverApi, settingsApi } from '@/api/endpoints';
import { ApiError, errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import { SECRET, type DKIMKey, type MailSettings, type MailSmartHost, type MailUser } from '@/api/types';
import { useUnsavedChangesPrompt } from '@/hooks/useUnsaved';
import { Callout, Card, FormSection, Grid, Loading, Mono, Sections } from '@/components/Layout';
import { ErrorBox, Field, FormErrorBanner, FormErrors } from '@/components/Field';
import { Input, NumberInput, Select, Textarea } from '@/components/Input';
import { Checkbox, Radio, Switch } from '@/components/Switch';
import { ListEditor, RowsEditor } from '@/components/ListEditor';
import { SecretInput } from '@/components/SecretInput';
import { CopyButton, CopyField } from '@/components/CopyButton';
import { Button } from '@/components/Button';
import { SaveBar } from '@/components/SaveBar';
import { useToast } from '@/components/Toast';
import { clone, jsonEqual } from '@/lib/obj';
import { validateDomain, validatePublicIP } from '@/lib/mailHealth';
import { nodemailerSnippet, normalizeMail, pickupPath } from '@/lib/settingsDefaults';
import { validateIP } from '../sites/editors/RoutingEditor';

type MailUpdater = (fn: (m: MailSettings) => void) => void;

const SECURITY_PORTS: Record<string, number> = { starttls: 587, tls: 465, none: 25 };

export function MailSettingsTab() {
  const qc = useQueryClient();
  const toast = useToast();
  const navigate = useNavigate();
  const q = useQuery({ queryKey: qk.settings, queryFn: settingsApi.get });
  const [base, setBase] = useState<MailSettings | null>(null);
  const [draft, setDraft] = useState<MailSettings | null>(null);
  const [error, setError] = useState<Error | null>(null);

  const dirty = useMemo(() => !!base && !!draft && !jsonEqual(base, draft), [base, draft]);

  useEffect(() => {
    if (!q.data) return;
    const fresh = normalizeMail(q.data.mail);
    setBase(fresh);
    if (!dirty) setDraft(clone(fresh));
    // Only react to new server data.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q.data]);

  useUnsavedChangesPrompt(dirty, '/mail/settings');

  const update = useCallback<MailUpdater>((fn) => {
    setDraft((d) => {
      if (!d) return d;
      const c = clone(d);
      fn(c);
      return c;
    });
  }, []);

  const save = useMutation({
    // The whole settings document goes back; only the mail section changed.
    mutationFn: (m: MailSettings) => settingsApi.put({ ...q.data!, mail: m }),
    onSuccess: (saved) => {
      qc.setQueryData(qk.settings, saved);
      const fresh = normalizeMail(saved.mail);
      setBase(fresh);
      setDraft(clone(fresh));
      setError(null);
      void qc.invalidateQueries({ queryKey: qk.mail });
      // A deliverability report run with the old settings reruns on the next visit.
      void qc.invalidateQueries({ queryKey: qk.mailHealthAll });
      toast.success('Mail settings saved');
    },
    onError: (e) => {
      setError(e instanceof Error ? e : new Error(String(e)));
      toast.error('Could not save mail settings', e);
    },
  });

  if (q.isPending) return <Loading />;
  if (q.isError) return <ErrorBox>{errorMessage(q.error)}</ErrorBox>;
  if (!draft) return null;

  const props = { m: draft, update };

  return (
    <div className={dirty ? 'pb-20' : undefined}>
      <FormErrors error={error}>
        <FormErrorBanner className="mb-4" />
        {error instanceof ApiError && !!error.field && !error.field.startsWith('mail') && (
          <Callout
            tone="warning"
            icon={<AlertTriangle />}
            className="mb-4"
            actions={
              <button type="button" className="nh-link text-xs" onClick={() => navigate('/settings')}>
                Open settings
              </button>
            }
          >
            The problem is in another part of the server settings; fix it there, then save the mail settings again.
          </Callout>
        )}
        <div className="space-y-5">
          <ServerCard {...props} />
          <AccessCard {...props} />
          <DeliveryCard {...props} />
          <DkimCard {...props} />
          <SnippetCard m={draft} />
        </div>
      </FormErrors>
      {dirty && (
        <SaveBar
          label="You have unsaved mail settings"
          saving={save.isPending}
          onSave={() => draft && q.data && save.mutate(draft)}
          onDiscard={() => {
            if (base) setDraft(clone(base));
            setError(null);
          }}
        />
      )}
    </div>
  );
}

interface CardProps {
  m: MailSettings;
  update: MailUpdater;
}

function useSet(update: MailUpdater) {
  return (p: Partial<MailSettings>) =>
    update((d) => {
      Object.assign(d, p);
    });
}

// ---------------------------------------------------------------- server

function ServerCard({ m, update }: CardProps) {
  const set = useSet(update);
  const info = useQuery({ queryKey: qk.serverInfo, queryFn: serverApi.info, staleTime: 30_000 });
  return (
    <Card title={<span className="flex items-center gap-2"><Server className="h-4 w-4 text-zinc-400" />SMTP server</span>}>
      <Sections>
        <FormSection
          title="Service"
          description="Applications submit mail over SMTP; NodeHoster queues it on disk and delivers it, retrying until it expires. It never accepts mail for local mailboxes."
        >
          <Switch checked={m.enabled} onChange={(v) => set({ enabled: v })} label="Run the SMTP server" description="Listen for mail from the allowed clients." />
          <Grid>
            <Field label="Listen IP" path="mail.listenIp" hint="Blank = all addresses.">
              <Input mono value={m.listenIp} placeholder="All addresses" onChange={(e) => set({ listenIp: e.target.value.trim() })} />
            </Field>
            <Field label="Port" path="mail.port" hint="25 is the SMTP default; 587 or 2525 avoid clashing with another mail server.">
              <NumberInput mono className="w-32" min={1} max={65535} value={m.port} onChange={(v) => set({ port: v })} />
            </Field>
          </Grid>
          <Field
            label="Host name"
            path="mail.hostname"
            hint={
              <>
                Fully-qualified name used in the greeting and Received headers; should match the server's reverse DNS. Blank = the computer name
                {info.data?.hostname ? (
                  <>
                    {' '}(<Mono>{info.data.hostname}</Mono>)
                  </>
                ) : null}
                .
              </>
            }
          >
            <Input mono className="sm:w-80" value={m.hostname ?? ''} placeholder="mail.example.com" onChange={(e) => set({ hostname: e.target.value.trim().toLowerCase() })} />
          </Field>
          <Field
            label="Public address"
            path="mail.publicIp"
            error={validatePublicIP(m.publicIp ?? '')}
            hint="The address receivers see mail coming from. Leave blank to detect it; set it when this server is behind NAT."
          >
            <Input mono className="sm:w-80" value={m.publicIp ?? ''} placeholder="Detect automatically" onChange={(e) => set({ publicIp: e.target.value.trim() })} />
          </Field>
        </FormSection>
        <FormSection title="Pickup directory" description="Like the IIS SMTP Pickup folder: for applications that write messages to disk instead of speaking SMTP.">
          <Switch
            checked={m.pickupDirectory}
            onChange={(v) => set({ pickupDirectory: v })}
            label="Send .eml files dropped in the pickup folder"
            description={
              <>
                Files in <Mono>{pickupPath(info.data?.dataDir)}</Mono> are queued and deleted.
              </>
            }
          />
        </FormSection>
      </Sections>
    </Card>
  );
}

// ---------------------------------------------------------------- access

function AccessCard({ m, update }: CardProps) {
  const set = useSet(update);
  const certs = useQuery({ queryKey: qk.certs, queryFn: certsApi.list, staleTime: 30_000 });
  const valid = (certs.data ?? []).filter((c) => c.status === 'valid');
  const certOptions = [{ value: '', label: 'None (no STARTTLS)' }, ...valid.map((c) => ({ value: c.id, label: `${c.name} — ${(c.domains ?? []).join(', ')}` }))];
  if (m.certificateId && !valid.some((c) => c.id === m.certificateId)) {
    const c = (certs.data ?? []).find((x) => x.id === m.certificateId);
    certOptions.push({ value: m.certificateId, label: `${c?.name ?? m.certificateId} (not valid)` });
  }
  const openRelay = !m.requireAuth && (m.allowIps ?? []).some((ip) => ip === '0.0.0.0/0' || ip === '::/0');

  return (
    <Card title={<span className="flex items-center gap-2"><ShieldCheck className="h-4 w-4 text-zinc-400" />Access</span>}>
      <Sections>
        <FormSection
          title="Allowed clients"
          description="Only these addresses may connect and relay mail. Keep it to this computer unless other servers need to send through it."
        >
          <ListEditor
            values={m.allowIps}
            onChange={(v) => set({ allowIps: v })}
            path="mail.allowIps"
            placeholder="10.0.0.0/8"
            validate={validateIP}
            emptyText="No clients can connect."
          />
          {openRelay && (
            <Callout tone="danger" icon={<AlertTriangle />} title="Open relay">
              Anyone on the internet could send spam through this server. Require authentication or restrict the allowed addresses.
            </Callout>
          )}
        </FormSection>

        <FormSection
          title={<span className="flex items-center gap-1.5"><KeyRound className="h-3.5 w-3.5" /> Authentication</span>}
          description="Passwords are stored as bcrypt hashes and never shown. They are accepted only over TLS (STARTTLS) or from this computer."
        >
          <Switch
            checked={m.requireAuth}
            onChange={(v) => set({ requireAuth: v })}
            label="Require authentication"
            description="Without it, allowed clients relay without logging in (the IIS default for 127.0.0.1)."
          />
          <Field label="Users" path="mail.users">
            <RowsEditor<MailUser>
              items={m.users}
              onChange={(v) => set({ users: v })}
              addLabel="Add user"
              create={() => ({ username: '', password: '' })}
              empty={<p className="text-xs text-zinc-500">{m.requireAuth ? 'Add at least one user.' : 'No users.'}</p>}
              render={(u, up, i) => (
                <div className="grid grid-cols-2 gap-2">
                  <Field path={`mail.users[${i}].username`}>
                    <Input
                      mono
                      value={u.username}
                      placeholder="app"
                      autoComplete="off"
                      // The server keeps a stored password by user name, so a
                      // renamed user needs a new one.
                      onChange={(e) => up({ username: e.target.value.trim(), passwordHash: undefined })}
                    />
                  </Field>
                  <Field path={`mail.users[${i}].password`}>
                    <Input
                      type="password"
                      mono
                      autoComplete="new-password"
                      value={u.password ?? ''}
                      placeholder={u.passwordHash === SECRET ? '•••••••• (unchanged)' : 'password'}
                      onChange={(e) => up({ password: e.target.value })}
                    />
                  </Field>
                </div>
              )}
            />
          </Field>
          <Field label="STARTTLS certificate" path="mail.certificateId" hint="Lets clients encrypt the connection. The certificate should cover the host name above.">
            <Select className="sm:w-96" value={m.certificateId ?? ''} onChange={(v) => set({ certificateId: v })} options={certOptions} />
          </Field>
        </FormSection>

        <FormSection title="Senders & limits" description="Restrict the MAIL FROM domains so a compromised app cannot send as anyone.">
          <Field label="Allowed sender domains">
            <ListEditor
              path="mail.allowedSenderDomains"
              values={m.allowedSenderDomains}
              onChange={(v) => set({ allowedSenderDomains: v.map((d) => d.toLowerCase()) })}
              placeholder="example.com"
              validate={validateDomain}
              emptyText="Any sender domain."
            />
          </Field>
          <Grid>
            <Field label="Max message size" path="mail.maxMessageMB">
              <NumberInput min={1} max={150} value={m.maxMessageMB} onChange={(v) => set({ maxMessageMB: v })} suffix="MB" />
            </Field>
            <Field label="Max recipients" path="mail.maxRecipients" hint="Per message.">
              <NumberInput min={1} value={m.maxRecipients} onChange={(v) => set({ maxRecipients: v })} />
            </Field>
          </Grid>
        </FormSection>
      </Sections>
    </Card>
  );
}

// ---------------------------------------------------------------- delivery

function DeliveryCard({ m, update }: CardProps) {
  const set = useSet(update);
  const sh = m.smartHost;
  const setSH = (p: Partial<MailSmartHost>) =>
    update((d) => {
      d.smartHost = { ...d.smartHost, ...p };
    });
  return (
    <Card
      title={<span className="flex items-center gap-2"><Send className="h-4 w-4 text-zinc-400" />Delivery</span>}
      actions={
        <Link to="/mail/health">
          <Button size="sm" icon={<MailCheck className="h-3.5 w-3.5" />}>
            Check deliverability
          </Button>
        </Link>
      }
    >
      <Sections>
        <FormSection
          title="Route"
          description="Direct delivery is the default: the built-in SMTP server sends each message to the recipients' mail servers itself. A smart host is optional, for when you would rather relay through a mail service."
        >
          <Field path="mail.delivery">
            <Radio
              value={m.delivery as 'direct' | 'smarthost'}
              onChange={(v) => set({ delivery: v })}
              className="flex-col"
              options={[
                { value: 'direct', label: 'Direct (default)', description: "Look up each recipient domain's MX records and deliver to its mail server. No other service is needed." },
                { value: 'smarthost', label: 'Smart host (optional)', description: 'Relay everything through SendGrid, Amazon SES, Microsoft 365, your ISP…' },
              ]}
            />
          </Field>
          {m.delivery === 'direct' ? (
            <Callout tone="info" icon={<Info />} title="Direct delivery needs outbound port 25">
              Some cloud and residential providers block it. For mail to reach inboxes rather than spam, the server's public address needs a reverse DNS
              name matching the host name, and your domains need SPF, DKIM and DMARC records.{' '}
              <Link to="/mail/health" className="nh-link">
                Check deliverability
              </Link>{' '}
              tests all of this and shows the records to publish. If port 25 is blocked, use a smart host.
            </Callout>
          ) : (
            <div className="space-y-4">
              <Field label="Host" path="mail.smartHost.host">
                <Input mono value={sh.host} placeholder="smtp.sendgrid.net" onChange={(e) => setSH({ host: e.target.value.trim() })} />
              </Field>
              <Grid>
                <Field label="Port" path="mail.smartHost.port">
                  <NumberInput mono min={1} max={65535} value={sh.port} onChange={(v) => setSH({ port: v })} />
                </Field>
                <Field label="Security" path="mail.smartHost.security">
                  <Select
                    value={sh.security}
                    onChange={(v) => setSH({ security: v, port: Object.values(SECURITY_PORTS).includes(sh.port) ? SECURITY_PORTS[v] : sh.port })}
                    options={[
                      { value: 'starttls', label: 'STARTTLS' },
                      { value: 'tls', label: 'TLS (implicit)' },
                      { value: 'none', label: 'None' },
                    ]}
                  />
                </Field>
              </Grid>
              <Grid>
                <Field label="User name" path="mail.smartHost.username" hint="Blank = no authentication.">
                  <Input mono value={sh.username ?? ''} autoComplete="off" onChange={(e) => setSH({ username: e.target.value })} />
                </Field>
                <Field label="Password" path="mail.smartHost.password">
                  <SecretInput value={sh.password} onChange={(v) => setSH({ password: v })} />
                </Field>
              </Grid>
              <Switch
                checked={sh.insecureSkipVerify}
                onChange={(v) => setSH({ insecureSkipVerify: v })}
                label="Skip certificate verification"
                description="Only for a relay with a self-signed certificate on a trusted network."
              />
            </div>
          )}
        </FormSection>
        <FormSection title="Retries" description="Failed attempts are retried with increasing delays until the message expires. Undeliverable mail is kept for review, like IIS Badmail.">
          <Grid>
            <Field label="Give up after" path="mail.expireHours">
              <NumberInput min={1} max={720} value={m.expireHours} onChange={(v) => set({ expireHours: v })} suffix="hours" />
            </Field>
            <Field label="Keep undeliverable mail" path="mail.keepFailedDays">
              <NumberInput min={1} value={m.keepFailedDays} onChange={(v) => set({ keepFailedDays: v })} suffix="days" />
            </Field>
          </Grid>
        </FormSection>
      </Sections>
    </Card>
  );
}

// ---------------------------------------------------------------- DKIM

function DkimCard({ m, update }: CardProps) {
  return (
    <Card
      title={<span className="flex items-center gap-2"><FileSignature className="h-4 w-4 text-zinc-400" />DKIM signing</span>}
      description="Sign outgoing mail so receivers can verify it came from your domain. New keys start with signing off: save, publish the TXT record in the domain's DNS, then turn signing on. To rotate a key, add one with a new selector, publish it, switch signing over to it, then remove the old one."
    >
      <RowsEditor<DKIMKey>
        items={m.dkim}
        onChange={(v) =>
          update((d) => {
            d.dkim = v;
          })
        }
        path="mail.dkim"
        addLabel="Add key"
        create={() => ({ domain: '', selector: '', enabled: false, privateKey: '' })}
        empty={<p className="text-xs text-zinc-500">No DKIM keys. Mail is sent unsigned.</p>}
        rowClassName="rounded-lg border border-zinc-200 p-3 dark:border-zinc-800"
        render={(k, up, i) => <DkimKeyRow k={k} up={up} index={i} />}
      />
    </Card>
  );
}

function DkimKeyRow({ k, up, index }: { k: DKIMKey; up: (p: Partial<DKIMKey>) => void; index: number }) {
  const p = `mail.dkim[${index}]`;
  const stored = k.privateKey === SECRET;
  // Derived from the data as well as the checkbox: rows are keyed by
  // position, so this component may have belonged to another key before.
  const [wantImport, setWantImport] = useState(false);
  const importing = wantImport || (!!k.privateKey && !stored);
  const expected = `${k.selector}._domainkey.${k.domain}`;
  return (
    <div className="space-y-3">
      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,14rem)_auto]">
        <Field label="Domain" path={`${p}.domain`}>
          <Input mono value={k.domain} placeholder="example.com" readOnly={stored} title={stored ? 'A saved key keeps its domain; add a new key instead' : undefined} onChange={(e) => up({ domain: e.target.value.trim().toLowerCase() })} />
        </Field>
        <Field label="Selector" path={`${p}.selector`}>
          <Input mono value={k.selector} placeholder="nodehoster" readOnly={stored} title={stored ? 'A saved key keeps its selector; add a new key instead' : undefined} onChange={(e) => up({ selector: e.target.value.trim().toLowerCase() })} />
        </Field>
        <div className="sm:pt-7">
          <Switch size="sm" checked={k.enabled} onChange={(v) => up({ enabled: v })} label="Sign mail" />
        </div>
      </div>
      {stored ? (
        k.dnsRecord ? (
          <div className="space-y-2 rounded-md border border-zinc-200 bg-zinc-50/60 p-3 dark:border-zinc-800 dark:bg-zinc-900/40">
            <p className="flex items-center gap-1.5 text-xs text-zinc-600 dark:text-zinc-400">
              <Lock className="h-3.5 w-3.5 shrink-0" />
              <span>
                Publish this TXT record in the DNS of <span className="font-medium text-zinc-900 dark:text-zinc-100">{k.domain}</span>:
              </span>
            </p>
            <Field label="Name">
              <CopyField value={k.dnsName ?? expected} />
            </Field>
            <Field label="Value">
              <div className="flex items-start gap-1 rounded-md border border-zinc-200 bg-zinc-50 py-1 pl-2.5 pr-1 dark:border-zinc-700 dark:bg-zinc-800/60">
                <code className="min-w-0 flex-1 break-all py-0.5 font-mono text-[12px] text-zinc-800 dark:text-zinc-200">{k.dnsRecord}</code>
                <CopyButton text={k.dnsRecord} />
              </div>
            </Field>
          </div>
        ) : (
          <p className="text-xs text-zinc-500">Private key stored.</p>
        )
      ) : (
        <div className="space-y-2">
          <p className="text-xs text-zinc-500">
            {importing
              ? 'The imported key is stored encrypted when you save; its DNS record then appears here.'
              : 'A new 2048-bit RSA key is generated when you save; the DNS record to publish then appears here.'}
          </p>
          <Checkbox
            checked={importing}
            onChange={(v) => {
              setWantImport(v);
              if (!v) up({ privateKey: '' });
            }}
            label="Import an existing private key (PEM)"
          />
          {importing && (
            <Field path={`${p}.privateKey`}>
              <Textarea
                mono
                rows={6}
                spellCheck={false}
                value={k.privateKey ?? ''}
                placeholder={'-----BEGIN PRIVATE KEY-----\n…\n-----END PRIVATE KEY-----'}
                onChange={(e) => up({ privateKey: e.target.value })}
              />
            </Field>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- snippet

function SnippetCard({ m }: { m: MailSettings }) {
  const code = nodemailerSnippet(m);
  return (
    <Card
      title={<span className="flex items-center gap-2"><Code2 className="h-4 w-4 text-zinc-400" />Using it from Node.js</span>}
      description={
        <>
          Send with <Mono>nodemailer</Mono> or any SMTP client. Apps on other servers connect to this server's address instead and must be in the
          allowed clients.
        </>
      }
    >
      <div className="relative">
        <pre className="scrollbar-thin overflow-x-auto rounded-md border border-zinc-200 bg-zinc-50 p-3 pr-10 font-mono text-[12px] leading-5 text-zinc-800 dark:border-zinc-800 dark:bg-zinc-950 dark:text-zinc-200">
          {code}
        </pre>
        <CopyButton text={code} label="Copy code" className="absolute right-2 top-2" />
      </div>
    </Card>
  );
}
