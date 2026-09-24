import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { MoreHorizontal, Pencil, Plus, Power, RotateCcw, Trash2 } from 'lucide-react';
import { alertsApi, type AlertRule } from '@/api/alerts';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import { describeRule, effectiveRules, newRule, ruleApplies, siteMetrics, withSiteRule, withoutSiteRule, type EffectiveRule } from '@/lib/alerts';
import { Badge } from '@/components/Badge';
import { Button, IconButton } from '@/components/Button';
import { Callout, Card, Loading } from '@/components/Layout';
import { ErrorBox, PathError } from '@/components/Field';
import { Menu, type MenuItem } from '@/components/Menu';
import { Switch } from '@/components/Switch';
import { Table, TableMessage, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { ActiveAlerts, AlertHistory } from '../alerts/AlertViews';
import { RuleDialog } from '../alerts/RuleEditor';
import type { SiteEditorProps } from './editors/types';

/**
 * A site's alerts: what fires now, the rules it is watched by (the
 * server-wide ones, which this site may change or turn off, and its own)
 * and what fired before. Rules are saved with the site.
 */
export function AlertsTab({ site, update, readOnly }: SiteEditorProps) {
  const list = useQuery({ queryKey: qk.alertList(site.id), queryFn: () => alertsApi.list(site.id), refetchInterval: 15_000 });
  const history = useQuery({ queryKey: qk.alertHistory(site.id), queryFn: () => alertsApi.history({ siteId: site.id, limit: 100 }), refetchInterval: 60_000 });
  const server = useQuery({ queryKey: qk.siteAlertRules(site.id), queryFn: () => alertsApi.siteRules(site.id) });
  const active = [...(list.data?.firing ?? []), ...(list.data?.pending ?? [])];

  return (
    <div className="space-y-5">
      {server.data && !server.data.enabled && (
        <Callout tone="info" title="Alerts are off on this server">
          The rules below are not evaluated until an administrator turns alerts on in Settings › Alerts.
        </Callout>
      )}
      <Card title="Now" description="Alerts of this site firing or pending." flush>
        {list.isPending ? <Loading className="p-4" /> : list.isError ? <ErrorBox className="m-4">{errorMessage(list.error)}</ErrorBox> : <ActiveAlerts alerts={active} showSite={false} />}
      </Card>
      <RulesCard site={site} update={update} readOnly={readOnly} defaults={server.data?.defaults} />
      <Card title="History" flush>
        {history.isPending ? <Loading className="p-4" /> : <AlertHistory alerts={history.data ?? []} showSite={false} />}
      </Card>
    </div>
  );
}

const SOURCE: Record<EffectiveRule['source'], { label: string; tone: 'gray' | 'blue' | 'violet' }> = {
  server: { label: 'server-wide', tone: 'gray' },
  override: { label: 'changed here', tone: 'blue' },
  site: { label: 'this site', tone: 'violet' },
};

function RulesCard({ site, update, readOnly, defaults }: SiteEditorProps & { defaults: AlertRule[] | undefined }) {
  const [editing, setEditing] = useState<{ rule: AlertRule; title: string; lock: boolean } | null>(null);
  const alerts = site.alerts ?? {};
  const optedOut = !!alerts.disabled;
  const rules = defaults ? effectiveRules(defaults, site) : [];
  const setRule = (r: AlertRule) =>
    update((d) => {
      d.alerts = withSiteRule(d.alerts, r);
    });
  const dropRule = (id: string) =>
    update((d) => {
      d.alerts = withoutSiteRule(d.alerts, id);
    });
  const taken = [...(defaults ?? []).map((r) => r.id), ...(alerts.rules ?? []).map((r) => r.id)];
  const addable = siteMetrics().filter((m) => ruleApplies({ metric: m.key }, site));

  const actions = (e: EffectiveRule): MenuItem[] => {
    const r = e.rule;
    const toggle = { label: r.disabled ? 'Turn on' : 'Turn off for this site', icon: <Power />, onSelect: () => setRule({ ...r, disabled: !r.disabled || undefined }) };
    switch (e.source) {
      case 'server':
        return [{ label: 'Change for this site…', icon: <Pencil />, onSelect: () => setEditing({ rule: r, title: 'Change the rule for this site', lock: true }) }, toggle];
      case 'override':
        return [
          { label: 'Edit…', icon: <Pencil />, onSelect: () => setEditing({ rule: r, title: 'Change the rule for this site', lock: true }) },
          toggle,
          { label: 'Back to the server-wide rule', icon: <RotateCcw />, onSelect: () => dropRule(r.id) },
        ];
      default:
        return [
          { label: 'Edit…', icon: <Pencil />, onSelect: () => setEditing({ rule: r, title: 'Edit rule', lock: false }) },
          toggle,
          { label: 'Remove', icon: <Trash2 />, danger: true, onSelect: () => dropRule(r.id) },
        ];
    }
  };

  return (
    <Card
      title="Rules"
      description="The server-wide rules that can be measured here, changed or turned off for this site if needed, and rules of its own."
      actions={
        !readOnly && (
          <Switch
            checked={!optedOut}
            onChange={(v) =>
              update((d) => {
                d.alerts = { ...d.alerts, disabled: !v || undefined };
              })
            }
            label="Alerts for this site"
          />
        )
      }
      flush
    >
      <PathError path="alerts" prefix className="mx-4 mt-3" />
      {optedOut && (
        <Callout tone="warning" className="m-4">
          This site raises no alerts: it opted out.
        </Callout>
      )}
      {!defaults ? (
        <Loading className="p-4" />
      ) : (
        <Table dense>
          <THead>
            <Tr>
              <Th>Rule</Th>
              <Th className="w-32">From</Th>
              <Th className="w-28">Severity</Th>
              <Th className="w-10" />
            </Tr>
          </THead>
          <TBody>
            {rules.length === 0 && <TableMessage colSpan={4}>No rules.</TableMessage>}
            {rules.map((e) => (
              <Tr key={e.rule.id} className={e.off || e.skipped || optedOut ? 'opacity-60' : undefined}>
                <Td>
                  <div className="text-[13px]">{describeRule(e.rule)}</div>
                  {e.base && <div className="text-xs text-zinc-500">Server-wide: {describeRule(e.base)}</div>}
                  {e.skipped && <div className="text-xs text-zinc-500">Not evaluated here: {e.skipped}</div>}
                </Td>
                <Td>
                  <Badge tone={SOURCE[e.source].tone}>{SOURCE[e.source].label}</Badge>
                </Td>
                <Td>{e.off ? <Badge>off</Badge> : <Badge tone={e.rule.severity === 'critical' ? 'red' : 'amber'}>{e.rule.severity}</Badge>}</Td>
                <Td className="text-right">
                  {!readOnly && !(e.skipped && e.source === 'server') && (
                    <Menu trigger={(t) => <IconButton label="Actions" icon={<MoreHorizontal className="h-4 w-4" />} {...t} />} items={actions(e)} />
                  )}
                </Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      )}
      {!readOnly && defaults && (
        <div className="border-t border-zinc-200 px-4 py-3 dark:border-zinc-800">
          <Menu
            align="left"
            trigger={(t) => (
              <Button size="sm" icon={<Plus className="h-3.5 w-3.5" />} {...t}>
                Add a rule for this site
              </Button>
            )}
            items={addable.map((m) => ({
              label: m.label,
              onSelect: () => setEditing({ rule: newRule(m.key, taken, 'site-'), title: 'Add a rule for this site', lock: false }),
            }))}
          />
          <p className="mt-2 text-xs text-zinc-500">
            Server-wide rules are edited in <Link to="/settings/alerts" className="nh-link">Settings › Alerts</Link>. Changes here are saved with the site.
          </p>
        </div>
      )}
      <RuleDialog
        open={!!editing}
        initial={editing?.rule ?? null}
        scope="site"
        lockMetric={editing?.lock}
        title={editing?.title ?? ''}
        onClose={() => setEditing(null)}
        onSave={(r) => {
          setRule(r);
          setEditing(null);
        }}
      />
    </Card>
  );
}
