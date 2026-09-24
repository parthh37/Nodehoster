import type { AffinityConfig } from '@/api/types';
import { FormSection, Grid } from '@/components/Layout';
import { Field } from '@/components/Field';
import { Input, NumberInput } from '@/components/Input';
import { Switch } from '@/components/Switch';
import { COOKIE_NAME_RE, MAX_COOKIE_LIFETIME_SEC, affinitySupported, affinityTargets } from '@/lib/affinity';
import type { SiteEditorProps } from './types';

/**
 * Cookie-based session affinity (ARR "client affinity"): one setting for
 * whatever the site balances — its instances, its servers or its upstreams.
 */
export function AffinitySection({ site, update }: SiteEditorProps) {
  if (!affinitySupported(site)) return null;
  const a = site.routing.affinity;
  const set = (p: Partial<AffinityConfig>) =>
    update((d) => {
      d.routing = { ...d.routing, affinity: { ...d.routing.affinity, ...p } };
    });
  const targets = affinityTargets(site);
  const nameErr = a.cookieName && !COOKIE_NAME_RE.test(a.cookieName) ? "Letters, digits and !#$%&'*+-.^_`|~ only" : null;
  return (
    <FormSection
      title="Session affinity"
      description="Keep each client on the backend that answered it first, with a cookie. Unlike client IP hash it works behind CDNs and NAT, e.g. for Socket.IO long-polling or in-memory sessions."
    >
      <Switch
        checked={a.enabled}
        onChange={(v) => set({ enabled: v, cookieName: a.cookieName || 'NHAffinity' })}
        label="Client affinity"
        description={
          targets
            ? `Pins each client to one ${targets}, whatever the load balancing strategy; a client whose backend is down is moved and gets a new cookie.`
            : 'This site has a single backend now, so no cookie is set until it has more.'
        }
      />
      {a.enabled && (
        <Grid>
          <Field label="Cookie name" path="routing.affinity.cookieName" error={nameErr} hint="HttpOnly, SameSite=Lax, Secure over HTTPS.">
            <Input mono value={a.cookieName} placeholder="NHAffinity" onChange={(e) => set({ cookieName: e.target.value.trim() })} />
          </Field>
          <Field label="Lifetime" path="routing.affinity.lifetimeSec" hint="Blank = until the browser closes. Renewed while the client stays active.">
            <NumberInput blankZero min={0} max={MAX_COOKIE_LIFETIME_SEC} value={a.lifetimeSec} onChange={(v) => set({ lifetimeSec: v })} suffix="sec" />
          </Field>
        </Grid>
      )}
    </FormSection>
  );
}
