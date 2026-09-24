import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Ban, Eye, Pencil, Plus, ShieldAlert, ShieldCheck, ShieldOff, Trash2 } from 'lucide-react';
import { wafApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { WAFConfig, WAFExclusion, WAFMode } from '@/api/wafTypes';
import { Card, Callout, EmptyState, FormSection, Grid, Sections, Stat } from '@/components/Layout';
import { Field } from '@/components/Field';
import { NumberInput, Select } from '@/components/Input';
import { Segmented } from '@/components/Tabs';
import { Button, IconButton } from '@/components/Button';
import { Table, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { formatNumber } from '@/lib/format';
import { PARANOIA_LEVELS, WAF_MODES, describeExclusion, effectiveWAF } from '@/lib/waf';
import { ExclusionDialog } from '../waf/ExclusionDialog';
import { WafEvents } from '../waf/WafEvents';
import type { SiteEditorProps } from './editors/types';

/**
 * The site's web application firewall: mode, sensitivity and exclusions
 * (saved with the site), its counters and its events.
 */
export function FirewallTab({ site, update, readOnly }: SiteEditorProps) {
  const w: WAFConfig = site.routing.waf ?? {};
  const eff = effectiveWAF(w);
  const set = (p: Partial<WAFConfig>) =>
    update((d) => {
      d.routing = { ...d.routing, waf: { ...d.routing.waf, ...p } };
    });
  const stats = useQuery({ queryKey: qk.siteWaf(site.id), queryFn: () => wafApi.site(site.id), refetchInterval: 15_000 });
  const exclusions = w.exclusions ?? [];
  const [editing, setEditing] = useState<{ index: number; x: WAFExclusion } | null>(null);
  const s = stats.data?.stats;

  return (
    <div className="space-y-5">
      <fieldset disabled={readOnly} className="min-w-0 space-y-5">
        <Card
          title={
            <span className="flex items-center gap-2">
              <ShieldAlert className="h-4 w-4 text-zinc-400" />
              Web application firewall
            </span>
          }
          description="Inspects requests for SQL injection, cross-site scripting, path traversal, command injection and similar attacks before they reach the site, scoring them like the OWASP Core Rule Set."
        >
          <Sections>
            <FormSection title="Mode" description={WAF_MODES.find((m) => m.value === eff.mode)?.description}>
              <Segmented<WAFMode> options={WAF_MODES.map((m) => ({ value: m.value, label: m.label }))} value={eff.mode} onChange={(v) => set({ mode: v })} />
              {eff.mode === 'detect' && (
                <Callout tone="info" icon={<Eye />}>
                  Nothing is blocked. Check the events below for false positives, add exclusions for them, then switch to Block.
                </Callout>
              )}
              {eff.mode === 'block' && (
                <Callout tone="success" icon={<ShieldCheck />}>
                  Blocked requests get a 403 page with a request ID the visitor can quote; each block counts towards{' '}
                  <Link to="/settings/security" className="nh-link">
                    automatic IP banning
                  </Link>{' '}
                  unless the site is exempt (Routing › Access).
                </Callout>
              )}
            </FormSection>
            <FormSection title="Sensitivity" description="Each matching rule adds its severity (critical 5, error 4, warning 3, notice 2); a request whose score reaches the threshold is blocked.">
              <Grid cols={3}>
                <Field label="Paranoia level" path="routing.waf.paranoiaLevel">
                  <Select value={eff.paranoia} onChange={(v) => set({ paranoiaLevel: Number(v) })} options={PARANOIA_LEVELS.map((p) => ({ value: p.value, label: p.label }))} />
                </Field>
                <Field label="Anomaly threshold" path="routing.waf.anomalyThreshold" hint="5 = one critical match blocks.">
                  <NumberInput min={1} max={1000} value={eff.threshold} onChange={(v) => set({ anomalyThreshold: v })} />
                </Field>
                <Field label="Inspect bodies up to" path="routing.waf.inspectBodyKB" hint="The rest streams to the site uninspected.">
                  <NumberInput min={1} max={4096} value={eff.bodyKB} onChange={(v) => set({ inspectBodyKB: v })} suffix="KB" />
                </Field>
              </Grid>
              <p className="text-xs text-zinc-500">{PARANOIA_LEVELS.find((p) => p.value === eff.paranoia)?.description}</p>
            </FormSection>
          </Sections>
        </Card>

        <Card
          title="Exclusions"
          description="Rules that are wrong for this site: turned off under a path, or not applied to named arguments, cookies or headers (a CMS editor posting HTML)."
          actions={
            !readOnly && (
              <Button size="sm" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setEditing({ index: -1, x: {} })}>
                Add exclusion
              </Button>
            )
          }
          flush
        >
          {exclusions.length === 0 ? (
            <EmptyState icon={<ShieldOff />} title="No exclusions" description="Use Exclude on an event below to create one from a false positive." />
          ) : (
            <Table>
              <THead>
                <tr>
                  <Th>Exclusion</Th>
                  <Th className="w-72">Comment</Th>
                  {!readOnly && <Th className="w-20" />}
                </tr>
              </THead>
              <TBody>
                {exclusions.map((x, i) => (
                  <Tr key={i}>
                    <Td className="text-[13px]">{describeExclusion(x)}</Td>
                    <Td className="text-xs text-zinc-500">{x.comment}</Td>
                    {!readOnly && (
                      <Td className="whitespace-nowrap text-right">
                        <IconButton label="Edit" icon={<Pencil className="h-3.5 w-3.5" />} onClick={() => setEditing({ index: i, x })} />
                        <IconButton
                          label="Remove"
                          variant="danger-ghost"
                          icon={<Trash2 className="h-3.5 w-3.5" />}
                          onClick={() => set({ exclusions: exclusions.filter((_, j) => j !== i) })}
                        />
                      </Td>
                    )}
                  </Tr>
                ))}
              </TBody>
            </Table>
          )}
        </Card>
      </fieldset>

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Inspected" value={formatNumber(s?.inspected ?? 0)} sub="since the service started" />
        <Stat label="Blocked" value={formatNumber(s?.blocked ?? 0)} tone={s?.blocked ? 'red' : 'default'} icon={<Ban />} />
        <Stat label="Detected only" value={formatNumber(s?.detected ?? 0)} tone={s?.detected ? 'amber' : 'default'} icon={<Eye />} />
        <Stat
          label="Top category"
          value={(() => {
            const top = Object.entries(s?.matches ?? {}).sort((a, b) => b[1] - a[1])[0];
            return top && top[1] > 0 ? top[0] : '—';
          })()}
        />
      </div>

      <WafEvents siteId={site.id} />

      <ExclusionDialog
        open={!!editing}
        initial={editing?.x ?? {}}
        title={editing && editing.index >= 0 ? 'Edit exclusion' : 'Add exclusion'}
        saveLabel="Done"
        onClose={() => setEditing(null)}
        onSave={(x) => {
          if (!editing) return;
          set({ exclusions: editing.index >= 0 ? exclusions.map((o, j) => (j === editing.index ? x : o)) : [...exclusions, x] });
          setEditing(null);
        }}
      />
    </div>
  );
}
