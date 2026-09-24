// Deployment slot filter and line tag for the Logs tab.
import type { Site } from '@/api/types';
import { Select } from '@/components/Input';
import { ALL_SLOTS, siteSlots } from '@/lib/slots';

/**
 * Which slot's logs to show. Application lines can show every slot's; an
 * access log is one slot's, so "All slots" reads as production there.
 */
export function LogSlotSelect({
  site,
  value,
  onChange,
  access,
}: {
  site: Pick<Site, 'type' | 'slots'>;
  value: string;
  onChange: (v: string) => void;
  access: boolean;
}) {
  const options = [
    ...(access ? [] : [{ value: ALL_SLOTS, label: 'All slots' }]),
    { value: '', label: 'production' },
    ...siteSlots(site).map((s) => ({ value: s.name, label: s.name })),
  ];
  return (
    <Select
      className="w-32"
      aria-label="Deployment slot"
      title="Deployment slot"
      value={access && value === ALL_SLOTS ? '' : value}
      onChange={onChange}
      options={options}
    />
  );
}

/** The slot that wrote a log line, on the dark log background. */
export function LineSlot({ slot }: { slot: string | undefined }) {
  if (!slot) return null;
  return (
    <span className="shrink-0 select-none self-start rounded bg-violet-500/15 px-1 text-[11px] text-violet-300" title={`Written by the ${slot} slot`}>
      {slot}
    </span>
  );
}
