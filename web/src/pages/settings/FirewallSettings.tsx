import { Link } from 'react-router-dom';
import { ShieldAlert } from 'lucide-react';
import type { WAFMode, WAFSettings } from '@/api/wafTypes';
import { Card, FormSection, Grid, Sections } from '@/components/Layout';
import { Field } from '@/components/Field';
import { NumberInput, Select } from '@/components/Input';
import { PARANOIA_LEVELS, WAF_MODES } from '@/lib/waf';
import type { SettingsTabProps } from './SettingsPage';

/** Server-wide web application firewall settings: what new sites start with, and how long events are kept. */
export function FirewallSettings({ s, update }: SettingsTabProps) {
  const w = s.waf;
  const set = (p: Partial<WAFSettings>) =>
    update((d) => {
      d.waf = { ...d.waf, ...p };
    });
  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <ShieldAlert className="h-4 w-4 text-zinc-400" />
          Web application firewall
        </span>
      }
      description={
        <>
          Each site sets its own mode, paranoia level and exclusions on its Firewall tab; blocked requests are listed on the{' '}
          <Link to="/firewall" className="nh-link">
            Firewall
          </Link>{' '}
          page.
        </>
      }
    >
      <Sections>
        <FormSection
          title="New sites"
          description="Sites created from now on start with these (redirects and background workers start with the firewall off). Existing sites are not changed."
        >
          <Grid cols={3}>
            <Field label="Mode" path="waf.defaultMode">
              <Select value={w.defaultMode} onChange={(v) => set({ defaultMode: v as WAFMode })} options={WAF_MODES.map((m) => ({ value: m.value, label: m.label }))} />
            </Field>
            <Field label="Paranoia level" path="waf.defaultParanoiaLevel">
              <Select
                value={w.defaultParanoiaLevel}
                onChange={(v) => set({ defaultParanoiaLevel: Number(v) })}
                options={PARANOIA_LEVELS.map((p) => ({ value: p.value, label: p.label }))}
              />
            </Field>
            <Field label="Anomaly threshold" path="waf.defaultAnomalyThreshold">
              <NumberInput min={1} max={1000} value={w.defaultAnomalyThreshold} onChange={(v) => set({ defaultAnomalyThreshold: v })} />
            </Field>
          </Grid>
          <p className="text-xs text-zinc-500">{WAF_MODES.find((m) => m.value === w.defaultMode)?.description}</p>
        </FormSection>
        <FormSection title="Events" description="Blocked and detected requests are kept this long, and at most 100,000 of them.">
          <Field label="Keep events for" path="waf.eventRetentionDays" className="max-w-xs">
            <NumberInput min={1} max={3650} value={w.eventRetentionDays} onChange={(v) => set({ eventRetentionDays: v })} suffix="days" />
          </Field>
        </FormSection>
      </Sections>
    </Card>
  );
}
