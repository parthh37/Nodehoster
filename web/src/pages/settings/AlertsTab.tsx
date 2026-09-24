import { Link } from 'react-router-dom';
import { BellRing } from 'lucide-react';
import type { AlertSettings } from '@/api/alerts';
import { Callout, Card, FormSection, Sections } from '@/components/Layout';
import { Field } from '@/components/Field';
import { NumberInput } from '@/components/Input';
import { Switch } from '@/components/Switch';
import { ListEditor } from '@/components/ListEditor';
import { RulesTable } from '../alerts/RuleEditor';
import type { SettingsTabProps } from './SettingsPage';

const EMAIL_RE = /^[^\s@,;<>"]+@[^\s@,;<>"]+$/;

/** Resource alerts: the rules every site gets, the server's own, and delivery. */
export function AlertsTab({ s, update }: SettingsTabProps) {
  const a = s.alerts;
  const set = (p: Partial<AlertSettings>) =>
    update((d) => {
      d.alerts = { ...d.alerts, ...p };
    });
  const siteRules = a.siteRules ?? [];
  const serverRules = a.serverRules ?? [];
  const webhooks = (s.webhooks ?? []).filter((w) => w.enabled && (!w.events?.length || w.events.some((e) => e.startsWith('alert.'))));

  return (
    <div className="space-y-5">
      <Card title={<span className="flex items-center gap-2"><BellRing className="h-4 w-4 text-zinc-400" />Resource alerts</span>}>
        <Sections>
          <FormSection
            title="Alerts"
            description="Like Azure Monitor metric alerts: a rule fires when a metric stays past its limit for a while, and resolves once it has been back for the recovery period."
          >
            <Switch checked={a.enabled} onChange={(v) => set({ enabled: v })} label="Evaluate alert rules" description="Every 15 seconds, from the metrics NodeHoster already collects." />
            <Field label="Recovery period" path="alerts.recoveryMinutes" hint="How long a firing alert's condition must stay clear before it resolves, so a value hovering at the limit does not fire over and over.">
              <NumberInput className="w-40" min={1} max={60} value={a.recoveryMinutes} onChange={(v) => set({ recoveryMinutes: v })} suffix="min" />
            </Field>
          </FormSection>
          <FormSection
            title="Notifications"
            description="Alerts are events (alert.firing, alert.resolved): they go to the event log, the status icon and the webhooks that take them. Stopped or starting sites do not raise alerts."
          >
            {webhooks.length === 0 ? (
              <Callout tone="info">
                No enabled webhook takes alert events.{' '}
                <Link to="/settings/notifications" className="nh-link">
                  Add a Slack, Teams or Discord webhook
                </Link>{' '}
                to be told in chat.
              </Callout>
            ) : (
              <p className="text-[13px] text-zinc-600 dark:text-zinc-400">Sent to {webhooks.map((w) => w.name).join(', ')}.</p>
            )}
            <Field label="E-mail to" path="alerts.emailTo" prefix hint="Through the built-in SMTP server's queue (delivered directly or through its smart host), one message per evaluation.">
              <ListEditor
                values={a.emailTo ?? []}
                onChange={(v) => set({ emailTo: v })}
                placeholder="ops@example.com"
                validate={(v) => (EMAIL_RE.test(v.trim()) ? null : 'Not an e-mail address')}
              />
            </Field>
          </FormSection>
        </Sections>
      </Card>

      <Card
        title="Site rules"
        description="Evaluated for every site they can be measured on (Node.js metrics for Node.js sites and workers, request metrics for sites that answer HTTP). A site can change or turn off a rule, or opt out, on its Alerts tab."
      >
        <RulesTable scope="site" rules={siteRules} onChange={(r) => set({ siteRules: r })} otherIds={serverRules.map((r) => r.id)} />
      </Card>

      <Card title="Server rules" description="The machine itself. Certificate expiry has its own warning (Process & logs › Certificate alerts).">
        <RulesTable scope="server" rules={serverRules} onChange={(r) => set({ serverRules: r })} otherIds={siteRules.map((r) => r.id)} />
      </Card>
    </div>
  );
}
