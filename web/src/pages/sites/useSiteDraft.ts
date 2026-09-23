import { useCallback, useEffect, useMemo, useState } from 'react';
import type { Site, SiteView } from '@/api/types';
import { clone, jsonEqual } from '@/lib/obj';
import { normalizeSite, toSite } from '@/lib/siteDefaults';

function comparable(s: Site): unknown {
  // Server-managed fields are not part of the edit.
  const { createdAt: _c, updatedAt: _u, activeRelease: _a, ...rest } = s;
  void _c;
  void _u;
  void _a;
  return rest;
}

/**
 * Holds an editable copy of a site. Server refreshes replace the draft only
 * while there are no unsaved changes.
 */
export function useSiteDraft(view: SiteView | undefined) {
  const [base, setBase] = useState<Site | null>(null);
  const [draft, setDraft] = useState<Site | null>(null);

  const dirty = useMemo(() => !!base && !!draft && !jsonEqual(comparable(base), comparable(draft)), [base, draft]);

  useEffect(() => {
    if (!view) return;
    const fresh = normalizeSite(toSite(view));
    if (!draft || !dirty || draft.id !== fresh.id) {
      setDraft(fresh);
    } else {
      // Keep the user's edits but pick up server-managed fields.
      setDraft({ ...draft, activeRelease: fresh.activeRelease, updatedAt: fresh.updatedAt });
    }
    setBase(fresh);
    // Only react to new server data.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view]);

  const update = useCallback((fn: (d: Site) => void) => {
    setDraft((d) => {
      if (!d) return d;
      const c = clone(d);
      fn(c);
      return c;
    });
  }, []);

  const reset = useCallback(() => setDraft(base ? clone(base) : null), [base]);

  const commit = useCallback((saved: SiteView) => {
    const fresh = normalizeSite(toSite(saved));
    setBase(fresh);
    setDraft(fresh);
  }, []);

  return { base, draft, dirty, update, reset, commit };
}
