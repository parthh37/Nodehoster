import { DatabaseZap } from 'lucide-react';
import type { CacheConfig } from '@/api/types';
import { Card, FormSection, Grid, Sections } from '@/components/Layout';
import { Field } from '@/components/Field';
import { NumberInput } from '@/components/Input';
import { Radio, Switch } from '@/components/Switch';
import { ListEditor } from '@/components/ListEditor';
import { VARY_BY_QUERY, validateCachePath, validateHeaderName } from '@/lib/cache';
import type { SiteEditorProps } from './types';

/** Response cache settings (IIS output caching / ARR cache), on the Routing tab. */
export function CacheCard({ site, update }: SiteEditorProps) {
  if (site.type !== 'node' && site.type !== 'proxy') return null;
  const c = site.routing.cache;
  const set = (p: Partial<CacheConfig>) =>
    update((d) => {
      d.routing = { ...d.routing, cache: { ...d.routing.cache, ...p } };
    });
  return (
    <Card
      title={<span className="flex items-center gap-2"><DatabaseZap className="h-4 w-4 text-zinc-400" />Response cache</span>}
      description="Answer repeated GET requests from memory, following the application's Cache-Control, Expires and Vary headers. Responses show X-Cache: HIT, MISS or BYPASS."
    >
      <Sections>
        <FormSection
          title="Caching"
          description="Never cached: responses marked no-store, private or no-cache, with Set-Cookie, or to requests with cookies or Authorization (unless the response is public)."
        >
          <Switch checked={c.enabled} onChange={(v) => set({ enabled: v })} label="Cache responses" />
          {c.enabled && (
            <>
              <Grid cols={3}>
                <Field label="Memory" path="routing.cache.maxMemoryMB" hint="This site's budget; least recently used entries are evicted.">
                  <NumberInput min={1} max={16384} value={c.maxMemoryMB} onChange={(v) => set({ maxMemoryMB: v })} suffix="MB" />
                </Field>
                <Field label="Largest response" path="routing.cache.maxObjectKB" hint="Bigger responses are passed through.">
                  <NumberInput min={1} value={c.maxObjectKB} onChange={(v) => set({ maxObjectKB: v })} suffix="KB" />
                </Field>
                <Field label="Default lifetime" path="routing.cache.defaultTtlSec" hint="For responses without max-age or Expires. Blank = don't cache them.">
                  <NumberInput blankZero min={0} value={c.defaultTtlSec} onChange={(v) => set({ defaultTtlSec: v })} suffix="sec" />
                </Field>
              </Grid>
            </>
          )}
        </FormSection>
        {c.enabled && (
          <FormSection title="Cache key" description="What makes two requests for the same path different responses.">
            <Field label="Query string" path="routing.cache.varyByQuery">
              <Radio value={c.varyByQuery} onChange={(v) => set({ varyByQuery: v })} options={VARY_BY_QUERY} />
            </Field>
            {c.varyByQuery === 'listed' && (
              <Field label="Parameters" path="routing.cache.queryParams" prefix>
                <ListEditor values={c.queryParams} onChange={(v) => set({ queryParams: v })} placeholder="page" />
              </Field>
            )}
            <Field label="Vary by request headers" path="routing.cache.varyHeaders" prefix hint="On top of the headers the application lists in Vary.">
              <ListEditor values={c.varyHeaders} onChange={(v) => set({ varyHeaders: v })} placeholder="Accept-Language" validate={validateHeaderName} />
            </Field>
            <Field label="Never cache" path="routing.cache.bypassPaths" prefix hint="Path prefixes always sent to the application.">
              <ListEditor values={c.bypassPaths} onChange={(v) => set({ bypassPaths: v })} placeholder="/api" validate={validateCachePath} />
            </Field>
          </FormSection>
        )}
      </Sections>
    </Card>
  );
}
