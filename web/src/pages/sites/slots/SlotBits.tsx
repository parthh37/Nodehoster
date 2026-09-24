// Small pieces shared by the Slots, Deployments, Bindings and Logs tabs.
import { Layers } from 'lucide-react';
import type { Site } from '@/api/types';
import { Badge } from '@/components/Badge';
import { Field } from '@/components/Field';
import { Select } from '@/components/Input';
import { PRODUCTION, slotLabel, slotOptions } from '@/lib/slots';
import { cn } from '@/lib/cn';

/** A slot's name as a badge; production is plain, other slots stand out. */
export function SlotBadge({ name, className, title }: { name: string | undefined | null; className?: string; title?: string }) {
  const label = slotLabel(name);
  return (
    <Badge tone={label === PRODUCTION ? 'gray' : 'violet'} className={className} title={title}>
      <Layers className="h-3 w-3" />
      {label}
    </Badge>
  );
}

/** Which slots run a release, for the deployment history. */
export function ReleaseSlotBadges({ slots }: { slots: string[] }) {
  if (slots.length === 0) return null;
  return (
    <>
      {slots.map((s) => (
        <Badge key={s} tone="accent" title={`Runs in ${s}`}>
          <Layers className="h-3 w-3" />
          {s}
        </Badge>
      ))}
    </>
  );
}

/** "Deploy to" / "Activate in" select: production ("") or a slot. */
export function SlotTargetField({
  site,
  value,
  onChange,
  label = 'Deploy to',
  hint,
  className,
}: {
  site: Pick<Site, 'type' | 'slots'>;
  value: string;
  onChange: (slot: string) => void;
  label?: string;
  hint?: string;
  className?: string;
}) {
  return (
    <Field label={label} hint={hint} className={cn(className)}>
      <Select value={value} onChange={onChange} options={slotOptions(site)} />
    </Field>
  );
}
