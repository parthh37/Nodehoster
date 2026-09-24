import { ExternalLink, Play, RefreshCw, RotateCw, Square } from 'lucide-react';
import type { Binding, SiteState, SiteView } from '@/api/types';
import { IconButton, Button } from '@/components/Button';
import { bindingHref, bindingLabel } from '@/lib/bindings';
import { runsNode } from '@/lib/siteDefaults';
import { cn } from '@/lib/cn';
import { useSiteActions } from '@/hooks/useSiteActions';
import { useSitePermissions } from '@/hooks/useAuth';
import { SlotBadge } from './slots/SlotBits';

/** `hideSlot`: leave out the deployment slot label of a slot's binding. */
export function BindingLink({ b, className, hideSlot }: { b: Binding; className?: string; hideSlot?: boolean }) {
  const href = bindingHref(b);
  const label = bindingLabel(b);
  const cls = cn('font-mono text-[12.5px]', className);
  const link = !href ? (
    <span className={cls}>{label}</span>
  ) : (
    <a href={href} target="_blank" rel="noreferrer" className={cn(cls, 'nh-link')} onClick={(e) => e.stopPropagation()}>
      {label}
    </a>
  );
  if (!b.slot || hideSlot) return link;
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      {link}
      <SlotBadge name={b.slot} title={`Routes to the ${b.slot} deployment slot`} />
    </span>
  );
}

export function BindingList({ bindings, max = 2 }: { bindings: Binding[] | null | undefined; max?: number }) {
  const list = bindings ?? [];
  if (list.length === 0) return <span className="text-xs text-zinc-400">No bindings</span>;
  return (
    <div className="flex flex-col gap-0.5">
      {list.slice(0, max).map((b, i) => (
        <BindingLink key={b.id || i} b={b} />
      ))}
      {list.length > max && <span className="text-xs text-zinc-500">+{list.length - max} more</span>}
    </div>
  );
}

const canStart = (s: SiteState | undefined) => !s || s === 'stopped' || s === 'failed';
const canStop = (s: SiteState | undefined) => !!s && s !== 'stopped';

/** Row-level icon actions for the sites table. */
export function SiteRowActions({ site, state }: { site: SiteView; state: SiteState | undefined }) {
  const { run, pending } = useSiteActions();
  const { canOperate } = useSitePermissions(site.id);
  if (!canOperate) return null;
  const busy = pending?.id === site.id;
  return (
    <div className="flex items-center justify-end gap-0.5" onClick={(e) => e.stopPropagation()}>
      {canStart(state) ? (
        <IconButton label="Start" icon={<Play className="h-3.5 w-3.5" />} disabled={busy} onClick={() => run(site.id, site.name, 'start')} />
      ) : (
        <IconButton label="Stop" icon={<Square className="h-3.5 w-3.5" />} disabled={busy} onClick={() => run(site.id, site.name, 'stop')} />
      )}
      {runsNode(site.type) && (
        <IconButton
          label="Recycle (zero-downtime)"
          icon={<RefreshCw className="h-3.5 w-3.5" />}
          disabled={busy || canStart(state)}
          onClick={() => run(site.id, site.name, 'recycle')}
        />
      )}
      <IconButton label="Restart" icon={<RotateCw className="h-3.5 w-3.5" />} disabled={busy} onClick={() => run(site.id, site.name, 'restart')} />
    </div>
  );
}

/** Header action buttons for the site detail page. */
export function SiteHeaderActions({ site, state }: { site: SiteView; state: SiteState | undefined }) {
  const { run, pending } = useSiteActions();
  const { canOperate } = useSitePermissions(site.id);
  const busy = pending?.id === site.id ? pending.action : undefined;
  const browse = (site.bindings ?? []).map((b) => bindingHref(b)).find(Boolean);
  return (
    <>
      {canOperate && (
        <>
          {canStart(state) && (
            <Button variant="primary" icon={<Play className="h-3.5 w-3.5" />} loading={busy === 'start'} onClick={() => run(site.id, site.name, 'start')}>
              Start
            </Button>
          )}
          {canStop(state) && (
            <Button icon={<Square className="h-3.5 w-3.5" />} loading={busy === 'stop'} onClick={() => run(site.id, site.name, 'stop')}>
              Stop
            </Button>
          )}
          {runsNode(site.type) && (
            <Button
              icon={<RefreshCw className="h-3.5 w-3.5" />}
              loading={busy === 'recycle'}
              disabled={canStart(state)}
              title="Zero-downtime rolling restart"
              onClick={() => run(site.id, site.name, 'recycle')}
            >
              Recycle
            </Button>
          )}
          <Button icon={<RotateCw className="h-3.5 w-3.5" />} loading={busy === 'restart'} onClick={() => run(site.id, site.name, 'restart')}>
            Restart
          </Button>
        </>
      )}
      {/* A background worker serves no HTTP: nothing to browse. */}
      {site.type !== 'worker' && (
        <Button
          icon={<ExternalLink className="h-3.5 w-3.5" />}
          disabled={!browse}
          onClick={() => browse && window.open(browse, '_blank', 'noopener')}
          title={browse ?? 'No browsable binding'}
        >
          Browse
        </Button>
      )}
    </>
  );
}
