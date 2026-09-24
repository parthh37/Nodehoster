import { Ban } from 'lucide-react';
import type { SiteBanning } from '@/api/types';
import { FormSection } from '@/components/Layout';
import { Checkbox } from '@/components/Switch';
import type { SiteEditorProps } from './types';

/** The site's part in server-wide automatic IP banning (Settings › Security). */
export function BanningSection({ site, update }: SiteEditorProps) {
  const b = site.routing.banning ?? {};
  const set = (p: Partial<SiteBanning>) =>
    update((d) => {
      d.routing = { ...d.routing, banning: { ...d.routing.banning, ...p } };
    });
  return (
    <FormSection
      title={<span className="flex items-center gap-1.5"><Ban className="h-3.5 w-3.5" /> Automatic IP banning</span>}
      description="Server-wide banning of addresses that fail to sign in, scan or flood (Settings › Security)."
    >
      <Checkbox
        checked={!!b.allowTrapPaths}
        disabled={!!b.exempt}
        onChange={(v) => set({ allowTrapPaths: v })}
        label="This site serves the trap paths"
        description="For WordPress or PHP sites: requests for /wp-login.php, /xmlrpc.php… do not ban anyone here."
      />
      <Checkbox
        checked={!!b.exempt}
        onChange={(v) => set({ exempt: v })}
        label="Exempt from banning"
        description="Banned addresses still reach this site, and its answers never count towards a ban (e.g. an API for devices behind shared addresses)."
      />
    </FormSection>
  );
}
