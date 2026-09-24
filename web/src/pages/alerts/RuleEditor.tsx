// Editing alert rules: a dialog for one rule, and the rules table of the
// Alerts settings tab.

import { useEffect, useState } from 'react';
import { MoreHorizontal, Pencil, Plus, Power, Trash2 } from 'lucide-react';
import type { AlertRule } from '@/api/alerts';
import { Badge } from '@/components/Badge';
import { Button, IconButton } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { Field } from '@/components/Field';
import { NumberInput, Select } from '@/components/Input';
import { Grid } from '@/components/Layout';
import { Menu } from '@/components/Menu';
import { Table, TableMessage, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { describeRule, metricInfo, newRule, normalizeRule, ruleError, serverMetrics, siteMetrics } from '@/lib/alerts';

export type RuleScope = 'site' | 'server';

const unitSuffix = (unit: string | undefined) => (unit === 'instances' ? 'down' : unit);

/**
 * One rule. For an override of a server-wide rule the metric is fixed
 * (lockMetric): it replaces that rule for one site.
 */
export function RuleDialog({
  open,
  initial,
  scope,
  lockMetric,
  title,
  onSave,
  onClose,
}: {
  open: boolean;
  initial: AlertRule | null;
  scope: RuleScope;
  lockMetric?: boolean;
  title: string;
  onSave: (r: AlertRule) => void;
  onClose: () => void;
}) {
  const [r, setR] = useState<AlertRule | null>(initial);
  useEffect(() => setR(initial ? normalizeRule(initial) : null), [initial, open]);
  if (!r) return null;
  const info = metricInfo(r.metric);
  const metrics = scope === 'server' ? serverMetrics() : siteMetrics();
  const err = ruleError(r);
  const set = (p: Partial<AlertRule>) => setR((cur) => (cur ? normalizeRule({ ...cur, ...p }) : cur));

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={title}
      size="lg"
      description="The alert fires when the condition has held for the whole period, and resolves once it has been clear for the recovery period (Settings › Alerts)."
      onSubmit={() => !err && onSave(r)}
      footer={
        <>
          {err && <span className="mr-auto text-xs text-red-600 dark:text-red-400">{err}</span>}
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!!err}>
            Done
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field label="Metric" hint={info?.hint}>
          <Select
            value={r.metric}
            disabled={lockMetric}
            onChange={(v) => set({ metric: v, threshold: metricInfo(v)?.threshold ?? r.threshold })}
            options={metrics.map((m) => ({ value: m.key, label: m.label }))}
          />
        </Field>
        <Grid cols={3}>
          <Field label={info?.below ? 'Fires below' : r.metric === 'instancesDown' ? 'Fires with more than' : 'Fires above'}>
            <NumberInput float min={0} max={info?.max} value={r.threshold} onChange={(v) => set({ threshold: v })} suffix={unitSuffix(info?.unit)} />
          </Field>
          <Field label="For" hint="0 = at once.">
            <NumberInput min={0} max={1440} value={r.forMinutes} onChange={(v) => set({ forMinutes: v })} suffix="min" />
          </Field>
          <Field label="Severity" hint="Critical alerts turn the status icon amber.">
            <Select
              value={r.severity}
              onChange={(v) => set({ severity: v })}
              options={[
                { value: 'warning', label: 'Warning' },
                { value: 'critical', label: 'Critical' },
              ]}
            />
          </Field>
        </Grid>
        {info?.windowed && (
          <Grid>
            <Field label="Window" hint="The rate is taken over this much traffic.">
              <NumberInput min={1} max={30} value={r.windowMinutes} onChange={(v) => set({ windowMinutes: v })} suffix="min" />
            </Field>
            <Field label="At least" hint="Fewer requests in the window count as fine: one failure out of one request is not a 100% error rate.">
              <NumberInput min={1} value={r.minRequests} onChange={(v) => set({ minRequests: v })} suffix="requests" />
            </Field>
          </Grid>
        )}
        <Field label="Remind every" hint="While it fires. Blank = notify once.">
          <NumberInput className="w-48" blankZero min={0} max={168} value={r.repeatHours ?? 0} onChange={(v) => set({ repeatHours: v || undefined })} suffix="hours" />
        </Field>
        <p className="text-xs text-zinc-500">{describeRule(r)}.</p>
      </div>
    </Dialog>
  );
}

/** A list of rules (the server-wide site rules, or the server rules), editable. */
export function RulesTable({
  rules,
  onChange,
  scope,
  readOnly,
  otherIds = [],
}: {
  rules: AlertRule[];
  onChange: (r: AlertRule[]) => void;
  scope: RuleScope;
  readOnly?: boolean;
  /** IDs taken by the other list: site and server rule IDs share one namespace. */
  otherIds?: string[];
}) {
  const [editing, setEditing] = useState<{ index: number; rule: AlertRule } | null>(null);
  const metrics = scope === 'server' ? serverMetrics() : siteMetrics();
  const replace = (i: number, r: AlertRule | null) => onChange(r ? rules.map((x, j) => (j === i ? r : x)) : rules.filter((_, j) => j !== i));

  return (
    <div className="space-y-2">
      <Table dense>
        <THead>
          <Tr>
            <Th>Rule</Th>
            <Th className="w-24">Severity</Th>
            <Th className="w-28">Reminders</Th>
            <Th className="w-10" />
          </Tr>
        </THead>
        <TBody>
          {rules.length === 0 && <TableMessage colSpan={4}>No rules.</TableMessage>}
          {rules.map((r, i) => (
            <Tr key={r.id} className={r.disabled ? 'opacity-60' : undefined}>
              <Td>
                <div className="text-[13px]">{describeRule(r)}</div>
                <div className="font-mono text-2xs text-zinc-400">{r.id}</div>
              </Td>
              <Td>
                {r.disabled ? (
                  <Badge>off</Badge>
                ) : (
                  <Badge tone={r.severity === 'critical' ? 'red' : 'amber'}>{r.severity}</Badge>
                )}
              </Td>
              <Td className="text-xs text-zinc-500">{r.repeatHours ? `every ${r.repeatHours} h` : 'once'}</Td>
              <Td className="text-right">
                {!readOnly && (
                  <Menu
                    trigger={(t) => <IconButton label="Actions" icon={<MoreHorizontal className="h-4 w-4" />} {...t} />}
                    items={[
                      { label: 'Edit', icon: <Pencil />, onSelect: () => setEditing({ index: i, rule: r }) },
                      { label: r.disabled ? 'Turn on' : 'Turn off', icon: <Power />, onSelect: () => replace(i, { ...r, disabled: !r.disabled || undefined }) },
                      'separator',
                      { label: 'Remove', icon: <Trash2 />, danger: true, onSelect: () => replace(i, null) },
                    ]}
                  />
                )}
              </Td>
            </Tr>
          ))}
        </TBody>
      </Table>
      {!readOnly && (
        <Menu
          align="left"
          trigger={(t) => (
            <Button size="sm" icon={<Plus className="h-3.5 w-3.5" />} {...t}>
              Add rule
            </Button>
          )}
          items={metrics.map((m) => ({
            label: m.label,
            onSelect: () => setEditing({ index: -1, rule: newRule(m.key, [...rules.map((x) => x.id), ...otherIds]) }),
          }))}
        />
      )}
      <RuleDialog
        open={!!editing}
        initial={editing?.rule ?? null}
        scope={scope}
        title={editing?.index === -1 ? 'Add rule' : 'Edit rule'}
        onClose={() => setEditing(null)}
        onSave={(r) => {
          if (editing && editing.index >= 0) replace(editing.index, r);
          else onChange([...rules, r]);
          setEditing(null);
        }}
      />
    </div>
  );
}
