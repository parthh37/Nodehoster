// Single shared subscription to /api/stream for live site status and events.
import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { openServerStream } from '@/api/sse';
import { qk } from '@/api/queryKeys';
import type { NHEvent, SiteStatus } from '@/api/types';

interface LiveCtx {
  statuses: Record<string, SiteStatus>;
  /** Events received since the page loaded, newest first. */
  events: NHEvent[];
  connected: boolean;
  /** Timestamp of the last status frame. */
  lastUpdate: number;
}

const Ctx = createContext<LiveCtx>({ statuses: {}, events: [], connected: false, lastUpdate: 0 });

const MAX_EVENTS = 300;

export function LiveProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const [statuses, setStatuses] = useState<Record<string, SiteStatus>>({});
  const [events, setEvents] = useState<NHEvent[]>([]);
  const [connected, setConnected] = useState(false);
  const [lastUpdate, setLastUpdate] = useState(0);
  const qcRef = useRef(qc);
  qcRef.current = qc;

  useEffect(() => {
    const h = openServerStream({
      onOpen: () => setConnected(true),
      onError: () => setConnected(false),
      onStatus: (list) => {
        const map: Record<string, SiteStatus> = {};
        for (const s of list) map[s.siteId] = s;
        setStatuses(map);
        setConnected(true);
        setLastUpdate(Date.now());
      },
      onEvent: (e) => {
        setEvents((prev) => (prev.some((x) => x.id === e.id) ? prev : [e, ...prev].slice(0, MAX_EVENTS)));
        const q = qcRef.current;
        const t = e.type || '';
        if (t.startsWith('cert')) void q.invalidateQueries({ queryKey: qk.certs });
        if (t.startsWith('deploy') && e.siteId) void q.invalidateQueries({ queryKey: qk.deployments(e.siteId) });
        if (t.startsWith('task.') && e.siteId) {
          void q.invalidateQueries({ queryKey: qk.siteTasks(e.siteId) });
          void q.invalidateQueries({ queryKey: qk.siteRuns(e.siteId) });
        }
        if (t.startsWith('slot.') && e.siteId) {
          void q.invalidateQueries({ queryKey: qk.slots(e.siteId) });
          void q.invalidateQueries({ queryKey: qk.site(e.siteId), exact: true });
          void q.invalidateQueries({ queryKey: qk.deployments(e.siteId) });
        }
        if (t.startsWith('node')) void q.invalidateQueries({ queryKey: qk.nodeVersions });
        if (t.startsWith('alert.')) void q.invalidateQueries({ queryKey: qk.alerts });
        if (t.startsWith('site.created') || t.startsWith('site.deleted')) void q.invalidateQueries({ queryKey: qk.sites, exact: true });
        if (t.startsWith('remote.')) void q.invalidateQueries({ queryKey: qk.servers });
      },
    });
    return () => h.close();
  }, []);

  const value = useMemo(() => ({ statuses, events, connected, lastUpdate }), [statuses, events, connected, lastUpdate]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useLive(): LiveCtx {
  return useContext(Ctx);
}

/** Live status for a site, falling back to the status embedded in a SiteView. */
export function useSiteStatus(siteId: string | undefined, fallback?: SiteStatus): SiteStatus | undefined {
  const { statuses } = useLive();
  return (siteId && statuses[siteId]) || fallback;
}
