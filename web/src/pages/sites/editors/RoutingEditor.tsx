import { useQuery } from '@tanstack/react-query';
import { Construction, Gauge, Globe2, KeyRound, Route, Shield, ShieldBan, FileWarning, Heading } from 'lucide-react';
import { sitesApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { BasicAuthUser, HeaderRule, Location, RoutingConfig } from '@/api/types';
import { Card, Callout, FormSection, Grid, Sections } from '@/components/Layout';
import { Field, PathError } from '@/components/Field';
import { Input, NumberInput, Select, Textarea } from '@/components/Input';
import { Checkbox, Switch } from '@/components/Switch';
import { ListEditor, RowsEditor } from '@/components/ListEditor';
import { KeyValueEditor } from '@/components/KeyValueEditor';
import { Badge } from '@/components/Badge';
import { cn } from '@/lib/cn';
import type { SiteEditorProps } from './types';
import { AffinitySection } from './AffinityEditor';

const IP_RE = /^([0-9.]+|[0-9a-fA-F:]+)(\/\d{1,3})?$/;
export const validateIP = (v: string) => (IP_RE.test(v) ? null : 'Enter an IP address or CIDR, e.g. 10.0.0.0/8');

function useRouting({ site, update }: SiteEditorProps) {
  const r = site.routing;
  const set = (patch: Partial<RoutingConfig>) =>
    update((d) => {
      d.routing = { ...d.routing, ...patch };
    });
  return { r, set };
}

export function MaintenanceCard(props: SiteEditorProps) {
  const { r, set } = useRouting(props);
  const m = r.maintenance;
  const setM = (p: Partial<typeof m>) => set({ maintenance: { ...m, ...p } });
  return (
    <section
      className={cn(
        'overflow-hidden rounded-lg border transition-colors',
        m.enabled
          ? 'border-amber-300 bg-amber-50/70 dark:border-amber-500/40 dark:bg-amber-500/[0.07]'
          : 'border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-900',
      )}
    >
      <div className="flex items-center gap-4 px-4 py-4">
        <div
          className={cn(
            'flex h-10 w-10 shrink-0 items-center justify-center rounded-lg',
            m.enabled ? 'bg-amber-500 text-white' : 'bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400',
          )}
        >
          <Construction className="h-5 w-5" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold">Maintenance mode</h2>
            {m.enabled && <Badge tone="amber">On</Badge>}
          </div>
          <p className="text-xs text-zinc-600 dark:text-zinc-400">
            {m.enabled
              ? 'Visitors get a 503 maintenance page. Allowed IPs still reach the application.'
              : 'Temporarily answer every request with a 503 maintenance page, e.g. during migrations.'}
          </p>
        </div>
        <Switch checked={m.enabled} onChange={(v) => setM({ enabled: v })} />
      </div>
      {m.enabled && (
        <div className="space-y-4 border-t border-amber-200 px-4 py-4 dark:border-amber-500/20">
          <Grid>
            <Field label="Allowed IPs" path="routing.maintenance.allowIps" prefix hint="These clients bypass the maintenance page.">
              <ListEditor values={m.allowIps} onChange={(v) => setM({ allowIps: v })} placeholder="203.0.113.10" validate={validateIP} />
            </Field>
            <Field label="Retry-After" path="routing.maintenance.retryAfterSec" hint="Seconds; tells clients and crawlers when to come back.">
              <NumberInput blankZero min={0} value={m.retryAfterSec} onChange={(v) => setM({ retryAfterSec: v })} suffix="sec" />
            </Field>
          </Grid>
          <Field label="Custom page HTML" path="routing.maintenance.html" hint="Leave blank for the built-in page.">
            <Textarea mono rows={6} value={m.html ?? ''} onChange={(e) => setM({ html: e.target.value })} placeholder="<h1>Back soon</h1>" />
          </Field>
        </div>
      )}
    </section>
  );
}

export function RoutingGeneral(props: SiteEditorProps) {
  const { r, set } = useRouting(props);
  const hasHttps = (props.site.bindings ?? []).some((b) => b.protocol === 'https');
  return (
    <Card title={<span className="flex items-center gap-2"><Globe2 className="h-4 w-4 text-zinc-400" />Request handling</span>}>
      <Sections>
        <FormSection title="HTTPS" description="Redirect plain HTTP to HTTPS and tell browsers to always use HTTPS.">
          <Switch
            checked={r.httpsRedirect}
            onChange={(v) => set({ httpsRedirect: v })}
            label="Redirect HTTP to HTTPS"
            description={hasHttps ? 'Permanent redirect to the same host over HTTPS.' : 'Add an HTTPS binding for this to take effect.'}
          />
          <Switch checked={r.hsts.enabled} onChange={(v) => set({ hsts: { ...r.hsts, enabled: v, maxAgeSec: r.hsts.maxAgeSec || 31536000 } })} label="HTTP Strict Transport Security (HSTS)" />
          {r.hsts.enabled && (
            <div className="space-y-3 border-l-2 border-zinc-200 pl-4 dark:border-zinc-800">
              <Field label="max-age" path="routing.hsts.maxAgeSec" hint="31536000 = 1 year">
                <NumberInput className="w-48" min={0} value={r.hsts.maxAgeSec} onChange={(v) => set({ hsts: { ...r.hsts, maxAgeSec: v } })} suffix="sec" />
              </Field>
              <Checkbox checked={r.hsts.includeSubdomains} onChange={(v) => set({ hsts: { ...r.hsts, includeSubdomains: v } })} label="includeSubDomains" />
              <Checkbox
                checked={r.hsts.preload}
                onChange={(v) => set({ hsts: { ...r.hsts, preload: v } })}
                label="preload"
                description="Only enable if you intend to submit the domain to the browser preload list."
              />
            </div>
          )}
        </FormSection>
        <FormSection title="Proxying" description="Applies to responses from the application or upstreams.">
          <Switch
            checked={r.compression}
            onChange={(v) => set({ compression: v })}
            label="Compression"
            description="Brotli or gzip, as the client prefers, for text responses over 1 KB. Static files with a .br or .gz copy next to them are sent pre-compressed."
          />
          <Switch checked={r.webSockets} onChange={(v) => set({ webSockets: v })} label="WebSockets" description="Allow connection upgrades (Socket.IO, GraphQL subscriptions…)." />
          <Switch checked={r.accessLog} onChange={(v) => set({ accessLog: v })} label="Access log" description="Record every request in the site's access log." />
          <Grid>
            <Field label="Max request body" path="routing.maxBodyMB" hint="Blank = unlimited">
              <NumberInput blankZero min={0} value={r.maxBodyMB} onChange={(v) => set({ maxBodyMB: v })} suffix="MB" />
            </Field>
            <Field label="Upstream timeout" path="routing.timeoutSec" hint="Time to wait for response headers. Blank = default.">
              <NumberInput blankZero min={0} value={r.timeoutSec} onChange={(v) => set({ timeoutSec: v })} suffix="sec" />
            </Field>
          </Grid>
        </FormSection>
        <AffinitySection {...props} />
      </Sections>
    </Card>
  );
}

function HeaderRules({ value, onChange, path }: { value: HeaderRule[] | undefined; onChange: (v: HeaderRule[]) => void; path: string }) {
  return (
    <RowsEditor<HeaderRule>
      items={value}
      onChange={onChange}
      path={path}
      addLabel="Add header rule"
      create={() => ({ action: 'set', name: '', value: '' })}
      empty={<p className="text-xs text-zinc-500">No rules.</p>}
      render={(h, up, i) => (
        <div className="grid grid-cols-[7rem_minmax(8rem,14rem)_1fr] gap-2">
          <Select
            value={h.action}
            onChange={(v) => up({ action: v })}
            options={[
              { value: 'set', label: 'Set' },
              { value: 'add', label: 'Add' },
              { value: 'remove', label: 'Remove' },
            ]}
          />
          <Field path={`${path}[${i}].name`}>
            <Input mono value={h.name} placeholder="X-Frame-Options" onChange={(e) => up({ name: e.target.value })} />
          </Field>
          {h.action !== 'remove' ? (
            <Field path={`${path}[${i}].value`}>
              <Input mono value={h.value ?? ''} placeholder="SAMEORIGIN" onChange={(e) => up({ value: e.target.value })} />
            </Field>
          ) : (
            <span />
          )}
        </div>
      )}
    />
  );
}

export function HeadersCard(props: SiteEditorProps) {
  const { r, set } = useRouting(props);
  return (
    <Card title={<span className="flex items-center gap-2"><Heading className="h-4 w-4 text-zinc-400" />Headers</span>}>
      <Sections>
        <FormSection title="Request headers" description="Modify headers sent to the application or upstream.">
          <HeaderRules value={r.requestHeaders} onChange={(v) => set({ requestHeaders: v })} path="routing.requestHeaders" />
        </FormSection>
        <FormSection title="Response headers" description="Modify headers sent to clients, e.g. security headers.">
          <HeaderRules value={r.responseHeaders} onChange={(v) => set({ responseHeaders: v })} path="routing.responseHeaders" />
        </FormSection>
      </Sections>
    </Card>
  );
}

export function LocationsCard(props: SiteEditorProps) {
  const { r, set } = useRouting(props);
  const sites = useQuery({ queryKey: qk.sites, queryFn: sitesApi.list, staleTime: 30_000 });
  const others = (sites.data ?? []).filter((s) => s.id !== props.site.id);
  return (
    <Card
      title={<span className="flex items-center gap-2"><Route className="h-4 w-4 text-zinc-400" />Locations</span>}
      description="Mount another backend under a path — like an IIS application or virtual directory beneath this site."
    >
      <RowsEditor<Location>
        items={r.locations}
        onChange={(v) => set({ locations: v })}
        path="routing.locations"
        addLabel="Add location"
        create={() => ({ path: '/api', kind: 'url', url: '', stripPrefix: false })}
        empty={<p className="text-xs text-zinc-500">No locations. Every path is handled by this site.</p>}
        rowClassName="rounded-lg border border-zinc-200 p-3 dark:border-zinc-800"
        render={(l, up, i) => {
          const p = `routing.locations[${i}]`;
          return (
            <div className="space-y-2">
              <div className="grid gap-3 sm:grid-cols-[10rem_9rem_1fr]">
                <Field label="Path" path={`${p}.path`}>
                  <Input mono value={l.path} placeholder="/api" onChange={(e) => up({ path: e.target.value })} />
                </Field>
                <Field label="Served by" path={`${p}.kind`}>
                  <Select
                    value={l.kind}
                    onChange={(v) => up({ kind: v, siteId: '', url: '', root: '' })}
                    options={[
                      { value: 'site', label: 'Another site' },
                      { value: 'url', label: 'URL (proxy)' },
                      { value: 'static', label: 'Static folder' },
                    ]}
                  />
                </Field>
                {l.kind === 'site' && (
                  <Field label="Site" path={`${p}.siteId`}>
                    <Select
                      value={l.siteId ?? ''}
                      onChange={(v) => up({ siteId: v })}
                      placeholder="Choose a site…"
                      options={others.map((s) => ({ value: s.id, label: s.name }))}
                    />
                  </Field>
                )}
                {l.kind === 'url' && (
                  <Field label="Upstream URL" path={`${p}.url`}>
                    <Input mono value={l.url ?? ''} placeholder="http://127.0.0.1:9000" onChange={(e) => up({ url: e.target.value.trim() })} />
                  </Field>
                )}
                {l.kind === 'static' && (
                  <Field label="Folder" path={`${p}.root`}>
                    <Input mono value={l.root ?? ''} placeholder="D:\shared\assets" onChange={(e) => up({ root: e.target.value })} />
                  </Field>
                )}
              </div>
              <Checkbox
                checked={l.stripPrefix}
                onChange={(v) => up({ stripPrefix: v })}
                label="Strip prefix"
                description={
                  <>
                    <span className="font-mono">{l.path || '/api'}/users</span> is forwarded as <span className="font-mono">{l.stripPrefix ? '/users' : `${l.path || '/api'}/users`}</span>
                  </>
                }
              />
            </div>
          );
        }}
      />
    </Card>
  );
}

export function AccessCard(props: SiteEditorProps) {
  const { r, set } = useRouting(props);
  const ba = r.basicAuth;
  const setBA = (p: Partial<typeof ba>) => set({ basicAuth: { ...ba, ...p } });
  const rl = r.rateLimit;
  const setRL = (p: Partial<typeof rl>) => set({ rateLimit: { ...rl, ...p } });

  return (
    <Card title={<span className="flex items-center gap-2"><Shield className="h-4 w-4 text-zinc-400" />Access control</span>}>
      <Sections>
        <FormSection
          title={<span className="flex items-center gap-1.5"><ShieldBan className="h-3.5 w-3.5" /> IP restrictions</span>}
          description="Single addresses or CIDR ranges. When the allow list is not empty, everyone else is denied."
        >
          <Grid>
            <Field label="Allow" path="routing.ip.allow" prefix>
              <ListEditor values={r.ip.allow} onChange={(v) => set({ ip: { ...r.ip, allow: v } })} placeholder="10.0.0.0/8" validate={validateIP} emptyText="All clients allowed." />
            </Field>
            <Field label="Deny" path="routing.ip.deny" prefix>
              <ListEditor values={r.ip.deny} onChange={(v) => set({ ip: { ...r.ip, deny: v } })} placeholder="198.51.100.7" validate={validateIP} />
            </Field>
          </Grid>
          <PathError path="routing.ip" prefix />
        </FormSection>

        <FormSection
          title={<span className="flex items-center gap-1.5"><KeyRound className="h-3.5 w-3.5" /> Basic authentication</span>}
          description="Password-protect the site, e.g. a staging environment. Passwords are stored as bcrypt hashes and never shown."
        >
          <Switch checked={ba.enabled} onChange={(v) => setBA({ enabled: v })} label="Require a user name and password" />
          {ba.enabled && (
            <>
              <Field label="Realm" path="routing.basicAuth.realm">
                <Input className="w-72" value={ba.realm} onChange={(e) => setBA({ realm: e.target.value })} />
              </Field>
              <Field label="Users" path="routing.basicAuth.users" prefix>
                <RowsEditor<BasicAuthUser>
                  items={ba.users}
                  onChange={(v) => setBA({ users: v })}
                  addLabel="Add user"
                  create={() => ({ username: '', password: '' })}
                  empty={<p className="text-xs text-zinc-500">Add at least one user.</p>}
                  render={(u, up, i) => (
                    <div className="grid grid-cols-2 gap-2">
                      <Field path={`routing.basicAuth.users[${i}].username`}>
                        <Input mono value={u.username} placeholder="user" autoComplete="off" onChange={(e) => up({ username: e.target.value })} />
                      </Field>
                      <Field path={`routing.basicAuth.users[${i}].password`}>
                        <Input
                          type="password"
                          mono
                          autoComplete="new-password"
                          value={u.password ?? ''}
                          placeholder={u.passwordHash ? '•••••••• (unchanged)' : 'password'}
                          onChange={(e) => up({ password: e.target.value })}
                        />
                      </Field>
                    </div>
                  )}
                />
              </Field>
              <Field label="Exclude paths" path="routing.basicAuth.excludePaths" prefix hint="Path prefixes that do not require a password, e.g. /healthz or /.well-known">
                <ListEditor values={ba.excludePaths} onChange={(v) => setBA({ excludePaths: v })} placeholder="/healthz" />
              </Field>
            </>
          )}
        </FormSection>

        <FormSection
          title={<span className="flex items-center gap-1.5"><Gauge className="h-3.5 w-3.5" /> Rate limiting</span>}
          description="Per client IP token bucket. Clients over the limit get 429 Too Many Requests."
        >
          <Switch checked={rl.enabled} onChange={(v) => setRL({ enabled: v, requestsPerSecond: rl.requestsPerSecond || 10 })} label="Limit request rate" />
          {rl.enabled && (
            <Grid>
              <Field label="Requests per second" path="routing.rateLimit.requestsPerSecond">
                <NumberInput float min={0} step="0.1" value={rl.requestsPerSecond} onChange={(v) => setRL({ requestsPerSecond: v })} suffix="req/s" />
              </Field>
              <Field label="Burst" path="routing.rateLimit.burst" hint="Short bursts above the rate. Blank = 2× rate.">
                <NumberInput blankZero min={0} value={rl.burst} onChange={(v) => setRL({ burst: v })} />
              </Field>
            </Grid>
          )}
        </FormSection>
      </Sections>
    </Card>
  );
}

export function ErrorPagesCard(props: SiteEditorProps) {
  const { r, set } = useRouting(props);
  return (
    <Card
      title={<span className="flex items-center gap-2"><FileWarning className="h-4 w-4 text-zinc-400" />Custom error pages</span>}
      description="Replace NodeHoster's error pages (e.g. 502 when the app is down, 503, 404) with your own HTML."
    >
      <div className="space-y-2">
        <KeyValueEditor
          value={r.errorPages}
          onChange={(v) => set({ errorPages: v })}
          keyLabel="Status"
          valueLabel="HTML"
          keyPlaceholder="502"
          valuePlaceholder="<h1>We'll be right back</h1>"
          keyWidth="w-24"
          multiline
          addLabel="Add error page"
          empty={<p className="text-xs text-zinc-500">Using the built-in error pages.</p>}
        />
        <PathError path="routing.errorPages" prefix />
        {Object.keys(r.errorPages ?? {}).some((k) => !/^[45]\d\d$/.test(k)) && (
          <Callout tone="warning">Status codes must be between 400 and 599.</Callout>
        )}
      </div>
    </Card>
  );
}
