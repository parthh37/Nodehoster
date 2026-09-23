import { ExternalLink, Play, RefreshCw, RotateCw, Square } from 'lucide-react';
import type { Binding, SiteState, SiteView } from '@/api/types';
import { IconButton, Button } from '@/components/Button';
import { bindingHref, bindingLabel } from '@/lib/bindings';
import { cn } from '@/lib/cn';
import { useSiteActions } from '@/hooks/useSiteActions';
import { usePermissions } from '@/hooks/useAuth';

export function BindingLink({ b, className }: { b: Binding; className?: string }) {
  const href = bindingHref(b);
  const label = bindingLabel(b);
  const cls = cn('font-mono text-[12.5px]', className);
  if (!href) return <span className={cls}>{label}</span>;
  return (
    <a href={href} target="_blank" rel="noreferrer" className={cn(cls, 'nh-link')} onClick={(e) => e.stopPropagation()}>
      {label}
    </a>
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
  const { canOperate } = usePermissions();
  if (!canOperate) return null;
  const busy = pending?.id === site.id;
  return (
    <div className="flex items-center justify-end gap-0.5" onClick={(e) => e.stopPropagation()}>
      {canStart(state) ? (
        <IconButton label="Start" icon={<Play className="h-3.5 w-3.5" />} disabled={busy} onClick={() => run(site.id, site.name, 'start')} />
      ) : (
        <IconButton label="Stop" icon={<Square className="h-3.5 w-3.5" />} disabled={busy} onClick={() => run(site.id, site.name, 'stop')} />
      )}
      {site.type === 'node' && (
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
  const { canOperate } = usePermissions();
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
          {site.type === 'node' && (
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
      <Button
        icon={<ExternalLink className="h-3.5 w-3.5" />}
        disabled={!browse}
        onClick={() => browse && window.open(browse, '_blank', 'noopener')}
        title={browse ?? 'No browsable binding'}
      >
        Browse
      </Button>
    </>
  );
}
