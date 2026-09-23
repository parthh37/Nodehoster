import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Eraser } from 'lucide-react';
import { sitesApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { SiteStatus, SiteView } from '@/api/types';
import { Button } from '@/components/Button';
import { Card, KV } from '@/components/Layout';
import { Field } from '@/components/Field';
import { Input } from '@/components/Input';
import { useToast } from '@/components/Toast';
import { useSitePermissions } from '@/hooks/useAuth';
import { hitRatioLabel, purgePathError } from '@/lib/cache';
import { formatBytes, formatNumber } from '@/lib/format';

/** Response cache statistics and purge, on the site's overview. */
export function CachePanel({ site, status }: { site: SiteView; status: SiteStatus | undefined }) {
  const { canOperate } = useSitePermissions(site.id);
  const toast = useToast();
  const qc = useQueryClient();
  const [path, setPath] = useState('');
  const st = status?.cache;
  const cfg = site.routing.cache;
  const purge = useMutation({
    mutationFn: () => sitesApi.purgeCache(site.id, path.trim()),
    onSuccess: (r) => {
      toast.success(`Purged ${formatNumber(r.purged)} cached ${r.purged === 1 ? 'response' : 'responses'}`);
      setPath('');
      void qc.invalidateQueries({ queryKey: qk.site(site.id) });
    },
    onError: (e) => toast.error('Could not purge the cache', e),
  });
  const pathErr = purgePathError(path.trim());
  return (
    <Card title="Response cache" description={`${formatBytes(cfg.maxMemoryMB * 1024 * 1024)} budget · entries up to ${formatBytes(cfg.maxObjectKB * 1024)}`}>
      <div className="grid gap-5 md:grid-cols-[1fr_minmax(0,22rem)]">
        <KV
          items={[
            ['Cached responses', formatNumber(st?.entries ?? 0)],
            ['Memory used', formatBytes(st?.bytes ?? 0)],
            ['Hit ratio', hitRatioLabel(st)],
            ['Hits / misses', `${formatNumber(st?.hits ?? 0)} / ${formatNumber(st?.misses ?? 0)}`],
          ]}
        />
        {canOperate && (
          <form
            className="space-y-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (!pathErr) purge.mutate();
            }}
          >
            <Field label="Purge" error={pathErr} hint="Blank purges everything; a path purges what starts with it. The cache is also emptied on recycles and configuration changes.">
              <div className="flex gap-2">
                <Input mono className="flex-1" value={path} placeholder="/blog" onChange={(e) => setPath(e.target.value)} />
                <Button type="submit" icon={<Eraser className="h-3.5 w-3.5" />} loading={purge.isPending} disabled={!!pathErr}>
                  Purge
                </Button>
              </div>
            </Field>
          </form>
        )}
      </div>
    </Card>
  );
}
