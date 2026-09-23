import { Link } from 'react-router-dom';
import { CircleAlert, CircleCheck } from 'lucide-react';
import type { ImportApplyResult, SiteView } from '@/api/types';
import { Card, Callout } from '@/components/Layout';
import { pluralize } from '@/lib/format';

/** What the last apply created and what failed; failures can be fixed on the review step. */
export function ResultStep({ result, sites, started }: { result: ImportApplyResult; sites: SiteView[]; started: boolean }) {
  const created = result.created ?? [];
  const failed = result.failed ?? [];
  const siteName = (id: string) => sites.find((s) => s.id === id)?.name ?? created.find((c) => c.kind === 'site' && c.siteId === id)?.name ?? id;
  const newSites = created.filter((c) => c.kind === 'site').length;

  return (
    <div className="space-y-4">
      {created.length > 0 && (
        <Card title={`Imported ${pluralize(created.length, 'item')}`}>
          <ul className="space-y-2 text-[13px]">
            {created.map((c) => (
              <li key={c.key} className="flex items-start gap-2">
                <CircleCheck className="mt-0.5 h-4 w-4 shrink-0 text-emerald-600 dark:text-emerald-400" />
                <div className="min-w-0">
                  {c.kind === 'task' ? (
                    <>
                      Task <span className="font-medium">{c.name}</span> added to{' '}
                      <Link to={`/sites/${c.siteId}/tasks`} className="font-medium text-accent-700 hover:underline dark:text-accent-400">
                        {siteName(c.siteId)}
                      </Link>
                    </>
                  ) : (
                    <>
                      Site{' '}
                      <Link to={`/sites/${c.siteId}`} className="font-medium text-accent-700 hover:underline dark:text-accent-400">
                        {c.name}
                      </Link>
                    </>
                  )}
                  {c.warning && <p className="text-xs text-amber-700 dark:text-amber-400">{c.warning}</p>}
                </div>
              </li>
            ))}
          </ul>
          {newSites > 0 && !started && (
            <Callout tone="info" className="mt-4">
              The sites were created stopped. Check their settings, stop the old server's sites or apps using the same ports, then start them from the Sites
              page.
            </Callout>
          )}
        </Card>
      )}
      {failed.length > 0 && (
        <Card tone="danger" title={`${pluralize(failed.length, 'item')} could not be imported`} description="Go back to the review to fix them and import them again.">
          <ul className="space-y-2 text-[13px]">
            {failed.map((f) => (
              <li key={f.key} className="flex items-start gap-2">
                <CircleAlert className="mt-0.5 h-4 w-4 shrink-0 text-red-600 dark:text-red-400" />
                <div className="min-w-0">
                  <span className="font-medium">{f.name}</span>
                  <p className="break-words text-xs text-red-700 dark:text-red-400">
                    {f.field && <span className="font-mono">{f.field}: </span>}
                    {f.error}
                  </p>
                </div>
              </li>
            ))}
          </ul>
        </Card>
      )}
      {created.length === 0 && failed.length === 0 && <Callout tone="info">Nothing was imported.</Callout>}
    </div>
  );
}
