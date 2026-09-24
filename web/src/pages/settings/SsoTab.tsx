import { useMutation, useQuery } from '@tanstack/react-query';
import { AlertTriangle, ArrowUp, CheckCircle2, Info, Plus, Stethoscope, Trash2 } from 'lucide-react';
import { settingsApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { Role, SSORoleRule, SSOSettings } from '@/api/types';
import { Callout, Card, FormSection, Grid, Sections } from '@/components/Layout';
import { ErrorBox, Field, PathError } from '@/components/Field';
import { Input, Select } from '@/components/Input';
import { Switch } from '@/components/Switch';
import { SecretInput } from '@/components/SecretInput';
import { ListEditor } from '@/components/ListEditor';
import { Button, IconButton } from '@/components/Button';
import { CopyField } from '@/components/CopyButton';
import { defaultSsoLabel, isEntraIssuer } from '@/lib/sso';
import type { SettingsTabProps } from './SettingsPage';

const SERVER_ROLES: { value: Role; label: string }[] = [
  { value: 'admin', label: 'Administrator' },
  { value: 'operator', label: 'Operator' },
  { value: 'viewer', label: 'Viewer' },
];

/** Single sign-on to this console with Microsoft Entra ID or another OpenID Connect provider. */
export function SsoTab({ s, update }: SettingsTabProps) {
  const sso = s.sso;
  const set = (p: Partial<SSOSettings>) =>
    update((d) => {
      d.sso = { ...d.sso, ...p };
    });
  const callback = useQuery({ queryKey: qk.ssoCallbackUrl, queryFn: settingsApi.ssoCallbackUrl, staleTime: Infinity });
  const test = useMutation({ mutationFn: () => settingsApi.ssoTest(sso) });
  const rules = sso.roleMap ?? [];
  const setRules = (r: SSORoleRule[]) => set({ roleMap: r });
  const entra = isEntraIssuer(sso.issuer);

  return (
    <div className="space-y-5">
      <Card
        title="Single sign-on"
        description="Let users sign in to this console with Microsoft Entra ID or any OpenID Connect provider (Okta, Google Workspace, Keycloak, AD FS…). Multi-factor authentication is the provider's job: SSO sign-ins skip NodeHoster's own two-factor code."
      >
        <Sections>
          <FormSection title="Status">
            <Switch
              checked={sso.enabled}
              onChange={(v) => set({ enabled: v })}
              label="Enable single sign-on"
              description="Adds a sign-in button to the login page. Configure and test the provider first."
            />
          </FormSection>
          <FormSection
            title="Identity provider"
            description="Register NodeHoster as a web application at your provider, with the callback URL below as its redirect URI."
          >
            <Field
              label="Callback (redirect) URL"
              hint="The address of this console as you opened it. Open the console by the name users will use before copying it."
            >
              {callback.isError ? <ErrorBox>{errorMessage(callback.error)}</ErrorBox> : <CopyField value={callback.data?.redirectUrl ?? '…'} />}
            </Field>
            <Callout tone="info" icon={<Info />} title="Microsoft Entra ID quick setup">
              <ol className="ml-4 list-decimal space-y-0.5">
                <li>
                  In the Entra admin center, open <b>App registrations → New registration</b>. Choose <i>Accounts in this organizational directory only</i> and
                  add a <b>Web</b> redirect URI: the callback URL above.
                </li>
                <li>
                  Copy the <b>Directory (tenant) ID</b> into the issuer as{' '}
                  <code className="font-mono">https://login.microsoftonline.com/&lt;tenant ID&gt;/v2.0</code> and the <b>Application (client) ID</b> below.
                </li>
                <li>
                  Under <b>Certificates &amp; secrets</b>, create a client secret and paste its <i>value</i> below. Note its expiry date.
                </li>
                <li>
                  To map roles, add app roles (the <code className="font-mono">roles</code> claim) or a groups claim under <b>Token configuration</b>. App roles
                  also avoid the group limit of large directories.
                </li>
              </ol>
            </Callout>
            <Field label="Issuer URL" path="sso.issuer" hint="Its discovery document must be at <issuer>/.well-known/openid-configuration.">
              <Input
                mono
                value={sso.issuer}
                onChange={(e) => set({ issuer: e.target.value.trim() })}
                placeholder="https://login.microsoftonline.com/<tenant ID>/v2.0"
              />
            </Field>
            <Grid>
              <Field label="Application (client) ID" path="sso.clientId">
                <Input mono value={sso.clientId} onChange={(e) => set({ clientId: e.target.value.trim() })} />
              </Field>
              <Field label="Client secret" path="sso.clientSecret" hint="Encrypted at rest.">
                <SecretInput value={sso.clientSecret} onChange={(v) => set({ clientSecret: v })} />
              </Field>
            </Grid>
            <Grid>
              <Field label="Button label" path="sso.label">
                <Input value={sso.label ?? ''} onChange={(e) => set({ label: e.target.value })} placeholder={defaultSsoLabel(sso.issuer)} />
              </Field>
              <Field
                label="User name claim"
                path="sso.usernameClaim"
                hint="Matched, ignoring case, against NodeHoster user names. When missing, preferred_username, upn and email are tried."
              >
                <Input mono value={sso.usernameClaim} onChange={(e) => set({ usernameClaim: e.target.value.trim() })} placeholder="preferred_username" />
              </Field>
            </Grid>
            <Field label="Extra scopes" path="sso.scopes" prefix hint="openid, profile and email are always requested.">
              <ListEditor values={sso.scopes} onChange={(v) => set({ scopes: v })} placeholder="e.g. groups" addLabel="Add scope" emptyText="None." />
            </Field>
            <div className="flex flex-wrap items-center gap-3">
              <Button icon={<Stethoscope className="h-3.5 w-3.5" />} loading={test.isPending} disabled={!sso.issuer} onClick={() => test.mutate()}>
                Test configuration
              </Button>
              <span className="text-xs text-zinc-500">Fetches the discovery document and signing keys. Only a real sign-in proves the client secret.</span>
            </div>
            {test.isError && <ErrorBox>{errorMessage(test.error)}</ErrorBox>}
            {test.data &&
              (test.data.ok ? (
                <Callout tone="success" icon={<CheckCircle2 />} title="The provider answers correctly">
                  <div className="space-y-0.5 font-mono text-xs">
                    <div>issuer: {test.data.issuer}</div>
                    <div>authorize: {test.data.authorizationEndpoint}</div>
                    <div>token: {test.data.tokenEndpoint}</div>
                    <div>
                      signing keys: {test.data.keys} ({test.data.jwksUri})
                    </div>
                  </div>
                </Callout>
              ) : (
                <Callout tone="danger" icon={<AlertTriangle />} title="Problems found">
                  <ul className="ml-4 list-disc space-y-0.5">
                    {test.data.problems.map((p) => (
                      <li key={p}>{p}</li>
                    ))}
                  </ul>
                </Callout>
              ))}
          </FormSection>
          <FormSection title="Password sign-in" description="Password sign-in can stay available next to single sign-on, or be turned off.">
            <Switch
              checked={sso.disablePassword}
              onChange={(v) => set({ disablePassword: v })}
              label="Turn off password sign-in"
              description="Only while single sign-on is enabled. API tokens keep working."
            />
            {sso.disablePassword && (
              <Callout tone="warning" icon={<AlertTriangle />} title="Keep a way in">
                If the identity provider fails, a Windows administrator can still manage the server with NodeHoster Manager, and{' '}
                <code className="font-mono">nodehoster reset-password &lt;user&gt;</code> sets a new password and turns password sign-in back on.
              </Callout>
            )}
          </FormSection>
          <FormSection
            title="Users and roles"
            description="By default only existing NodeHoster users can sign in, matched by name. Site access is always granted by hand on the Users page."
          >
            <Switch
              checked={sso.autoCreate}
              onChange={(v) => set({ autoCreate: v })}
              label="Create unknown users"
              description="Signing in creates the user, without a password, with the mapped or default role."
            />
            <Field
              label="Default role"
              path="sso.defaultRole"
              hint={rules.length ? 'For users no mapping rule matches. None: they are refused.' : 'The role of users created by single sign-on.'}
            >
              <Select
                className="w-56"
                value={sso.defaultRole ?? ''}
                onChange={(v) => set({ defaultRole: v as Role | '' })}
                options={[{ value: '', label: 'None' }, ...SERVER_ROLES]}
              />
            </Field>
            <Field
              label="Role claim"
              path="sso.roleClaim"
              hint={entra ? 'roles for Entra ID app roles, groups for group object IDs.' : 'The claim listing the user’s groups or roles, e.g. groups.'}
            >
              <Input mono className="w-56" value={sso.roleClaim ?? ''} onChange={(e) => set({ roleClaim: e.target.value.trim() })} placeholder="roles" />
            </Field>
            <Field
              label="Role mapping"
              path="sso.roleMap"
              prefix
              hint="With a role claim and rules, the role is worked out at every sign-in: the first matching rule wins. A site-scoped user no rule matches keeps their site access."
            >
              <div className="space-y-1.5">
                {rules.length === 0 && <p className="text-xs text-zinc-500">No rules: roles are managed on the Users page.</p>}
                {rules.map((r, i) => (
                  <div key={i}>
                    <div className="flex items-center gap-2">
                      <Input
                        mono
                        className="flex-1"
                        value={r.value}
                        placeholder={entra ? 'NodeHoster.Admin or a group object ID' : 'group or role'}
                        onChange={(e) => setRules(rules.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))}
                        aria-label={`Rule ${i + 1} value`}
                      />
                      <span className="text-xs text-zinc-500">→</span>
                      <Select
                        className="w-40"
                        value={r.role}
                        options={SERVER_ROLES}
                        onChange={(v) => setRules(rules.map((x, j) => (j === i ? { ...x, role: v as Role } : x)))}
                        aria-label={`Rule ${i + 1} role`}
                      />
                      <IconButton
                        label="Move up"
                        icon={<ArrowUp className="h-3.5 w-3.5" />}
                        disabled={i === 0}
                        onClick={() => setRules([...rules.slice(0, i - 1), r, rules[i - 1], ...rules.slice(i + 1)])}
                      />
                      <IconButton label="Remove" icon={<Trash2 className="h-3.5 w-3.5" />} onClick={() => setRules(rules.filter((_, j) => j !== i))} />
                    </div>
                    <PathError path={`sso.roleMap[${i}]`} prefix />
                  </div>
                ))}
                <Button size="sm" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setRules([...rules, { value: '', role: 'viewer' }])}>
                  Add rule
                </Button>
              </div>
            </Field>
          </FormSection>
        </Sections>
      </Card>
    </div>
  );
}
