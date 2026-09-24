import { Fragment, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ChevronDown, ChevronRight, RefreshCw, Search, ShieldCheck, ShieldOff } from 'lucide-react';
import { sitesApi, wafApi } from '@/api/endpoints';
import { ApiError, errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { WAFEvent, WAFEventFilter, WAFExclusion } from '@/api/wafTypes';
import { usePermissions } from '@/hooks/useAuth';
import { useNow } from '@/hooks/useNow';
import { Card, EmptyState, Loading, Mono } from '@/components/Layout';
import { Table, TableMessage, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Input, Select } from '@/components/Input';
import { Button, IconButton } from '@/components/Button';
import { Badge } from '@/components/Badge';
import { ErrorBox } from '@/components/Field';
import { useToast } from '@/components/Toast';
import { formatDateTime, relativeTime } from '@/lib/format';
import { WAF_CATEGORIES, categoryLabel, eventSiteLabel, eventText, matchWhere, severityTone, suggestExclusion } from '@/lib/waf';
import { ExclusionDialog } from './ExclusionDialog';

const PAGE = 100;

/**
 * Firewall events, newest first, with filters and "exclude": for one site
 * (its Firewall tab) or every site the caller may see (the Firewall page).
 */
export function WafEvents({ siteId, initialSite = '' }: { siteId?: string; initialSite?: string }) {
  const { isAdmin } = usePermissions();
  const qc = useQueryClient();
  const toast = useToast();
  const now = useNow(30_000);
  const [site, setSite] = useState(initialSite);
  const [action, setAction] = useState<'' | 'blocked' | 'detected'>('');
  const [category, setCategory] = useState('');
  const [text, setText] = useState('');
  const [open, setOpen] = useState<string | null>(null);
  const [excluding, setExcluding] = useState<WAFEvent | null>(null);

  // The free-text box takes a client IP, a rule ID or a request ID.
  const filter = useMemo<WAFEventFilter>(() => {
    const f: WAFEventFilter = { limit: PAGE };
    if (action) f.action = action;
    if (category) f.category = category;
    if (!siteId && site) f.siteId = site;
    const t = text.trim();
    if (/^\d{6}$/.test(t)) f.rule = Number(t);
    else if (/^[0-9a-f]{16}$/i.test(t)) f.requestId = t.toLowerCase();
    else if (t && (t.includes('.') || t.includes(':'))) f.ip = t;
    return f;
  }, [action, category, site, siteId, text]);
  const badText = text.trim() !== '' && filter.rule === undefined && !filter.requestId && !filter.ip;

  const q = useInfiniteQuery({
    queryKey: [...qk.wafEvents({ ...filter, siteId: siteId ?? filter.siteId })],
    queryFn: ({ pageParam }) => {
      const f = { ...filter, before: pageParam || undefined };
      return siteId ? wafApi.siteEvents(siteId, f) : wafApi.events(f);
    },
    initialPageParam: 0,
    getNextPageParam: (last: WAFEvent[]) => (last.length < PAGE ? undefined : last[last.length - 1].seq),
    enabled: !badText,
  });
  const sites = useQuery({ queryKey: qk.sites, queryFn: sitesApi.list, staleTime: 30_000, enabled: !siteId });
  const siteNames = useMemo(() => Object.fromEntries((sites.data ?? []).map((s) => [s.id, s.name])), [sites.data]);
  const rows = useMemo(() => (q.data?.pages ?? []).flat(), [q.data]);

  const exclude = useMutation({
    mutationFn: ({ ev, x }: { ev: WAFEvent; x: WAFExclusion }) => wafApi.addExclusion(ev.siteId, x),
    onSuccess: (_, { ev }) => {
      toast.success('Exclusion added', 'Applied live. Similar requests now pass.');
      setExcluding(null);
      void qc.invalidateQueries({ queryKey: qk.site(ev.siteId) });
    },
    onError: (e) => toast.error(e instanceof ApiError && e.status === 409 ? 'The site already has this exclusion' : 'Could not add the exclusion', e),
  });

  const initial = useMemo(() => (excluding ? suggestExclusion(excluding) : {}), [excluding]);
  const filtered = !!(action || category || text || (!siteId && site));

  return (
    <Card flush>
      <div className="flex flex-wrap items-center gap-2 border-b border-zinc-200 px-4 py-2.5 dark:border-zinc-800">
        <Input
          className="w-72"
          prefix={<Search className="h-3.5 w-3.5" />}
          placeholder="Client IP, rule ID or request ID…"
          value={text}
          invalid={badText}
          onChange={(e) => setText(e.target.value)}
        />
        <Select
          className="w-36"
          value={action}
          onChange={(v) => setAction(v as typeof action)}
          options={[
            { value: '', label: 'All actions' },
            { value: 'blocked', label: 'Blocked' },
            { value: 'detected', label: 'Detected only' },
          ]}
        />
        <Select className="w-52" value={category} onChange={setCategory} options={[{ value: '', label: 'All categories' }, ...WAF_CATEGORIES]} />
        {!siteId && (
          <Select
            className="w-48"
            value={site}
            onChange={setSite}
            options={[{ value: '', label: 'All sites' }, ...(sites.data ?? []).map((s) => ({ value: s.id, label: s.name }))]}
          />
        )}
        <div className="ml-auto flex items-center gap-2">
          <span className="text-xs text-zinc-500">{rows.length} events</span>
          <IconButton label="Refresh" icon={<RefreshCw className="h-3.5 w-3.5" />} loading={q.isFetching && !q.isFetchingNextPage} onClick={() => void q.refetch()} />
        </div>
      </div>
      {q.isError && <ErrorBox className="m-4">{errorMessage(q.error)}</ErrorBox>}
      {q.isPending && !badText ? (
        <Loading />
      ) : rows.length === 0 && !filtered ? (
        <EmptyState
          icon={<ShieldCheck />}
          title="No firewall events"
          description="Requests the web application firewall blocks — or, in detect mode, would block — appear here with the rules they matched."
        />
      ) : (
        <Table>
          <THead>
            <tr>
              <Th className="w-8" />
              <Th className="w-40">Time</Th>
              <Th className="w-24">Action</Th>
              {!siteId && <Th className="w-36">Site</Th>}
              <Th className="w-36">Client</Th>
              <Th>Request</Th>
              <Th className="w-16 text-right">Score</Th>
              {isAdmin && <Th className="w-24" />}
            </tr>
          </THead>
          <TBody>
            {rows.length === 0 && <TableMessage colSpan={8}>{badText ? 'Enter a client IP, a 6-digit rule ID or a 16-character request ID.' : 'No events match the filters.'}</TableMessage>}
            {rows.map((ev) => {
              const expanded = open === ev.id + ev.seq;
              const first = ev.matches[0];
              return (
                <Fragment key={ev.seq}>
                  <Tr onClick={() => setOpen(expanded ? null : ev.id + ev.seq)}>
                    <Td className="text-zinc-400">{expanded ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}</Td>
                    <Td className="whitespace-nowrap text-xs text-zinc-500" title={formatDateTime(ev.time)}>
                      <div>{formatDateTime(ev.time)}</div>
                      <div className="text-2xs">{relativeTime(ev.time, now)}</div>
                    </Td>
                    <Td>
                      <Badge tone={ev.action === 'blocked' ? 'red' : 'amber'}>{ev.action === 'blocked' ? 'Blocked' : 'Detected'}</Badge>
                    </Td>
                    {!siteId && (
                      <Td>
                        <Link to={`/sites/${encodeURIComponent(ev.siteId)}/firewall`} className="nh-link text-[13px]" onClick={(e) => e.stopPropagation()}>
                          {eventSiteLabel(ev, siteNames)}
                        </Link>
                      </Td>
                    )}
                    <Td className="font-mono text-xs">{ev.clientIp}</Td>
                    <Td className="min-w-0 text-[13px]">
                      {/* The method and path are the client's: shown as escaped text, never as a link. */}
                      <div className="truncate font-mono text-xs" title={eventText(ev.path)}>
                        {eventText(ev.method, 16)} {eventText(ev.path, 300)}
                        {siteId && ev.slot && (
                          <Badge tone="gray" className="ml-1.5">
                            slot {ev.slot}
                          </Badge>
                        )}
                      </div>
                      {first && (
                        <div className="truncate text-xs text-zinc-500">
                          {first.message} in {matchWhere(first)}
                          {ev.matches.length > 1 && ` + ${ev.matches.length - 1} more`}
                        </div>
                      )}
                    </Td>
                    <Td className="text-right font-mono text-xs">
                      {ev.score}/{ev.threshold}
                    </Td>
                    {isAdmin && (
                      <Td className="text-right">
                        <Button
                          size="xs"
                          icon={<ShieldOff className="h-3 w-3" />}
                          onClick={(e) => {
                            e.stopPropagation();
                            setExcluding(ev);
                          }}
                        >
                          Exclude
                        </Button>
                      </Td>
                    )}
                  </Tr>
                  {expanded && (
                    <tr className="bg-zinc-50/60 dark:bg-zinc-900/40">
                      <td colSpan={8} className="px-4 py-3">
                        <EventDetail ev={ev} />
                      </td>
                    </tr>
                  )}
                </Fragment>
              );
            })}
          </TBody>
        </Table>
      )}
      {q.hasNextPage && (
        <div className="border-t border-zinc-200 px-4 py-2.5 text-center dark:border-zinc-800">
          <Button size="sm" loading={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
            Load older events
          </Button>
        </div>
      )}
      <ExclusionDialog
        open={!!excluding}
        initial={initial}
        title="Create an exclusion from this event"
        saveLabel="Add exclusion"
        saving={exclude.isPending}
        onClose={() => setExcluding(null)}
        onSave={(x) => excluding && exclude.mutate({ ev: excluding, x })}
      />
    </Card>
  );
}

function EventDetail({ ev }: { ev: WAFEvent }) {
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-zinc-600 dark:text-zinc-400">
        <span>
          Request ID <Mono>{ev.id}</Mono>
        </span>
        <span>
          Host <Mono>{eventText(ev.host, 255)}</Mono>
        </span>
        {ev.slot && (
          <span>
            Slot <Mono>{ev.slot}</Mono>
          </span>
        )}
        <span className="min-w-0 break-all">
          Path <Mono>{eventText(ev.path)}</Mono>
        </span>
        <span>
          Paranoia level {ev.paranoiaLevel}, threshold {ev.threshold}
        </span>
        {ev.userAgent && (
          <span className="min-w-0 truncate">
            User-Agent <Mono>{eventText(ev.userAgent, 512)}</Mono>
          </span>
        )}
      </div>
      <Table dense>
        <THead>
          <tr>
            <Th className="w-20">Rule</Th>
            <Th className="w-24">Severity</Th>
            <Th className="w-44">Category</Th>
            <Th>Matched</Th>
          </tr>
        </THead>
        <TBody>
          {ev.matches.map((m) => (
            <Tr key={m.ruleId}>
              <Td className="font-mono text-xs">{m.ruleId}</Td>
              <Td>
                <Badge tone={severityTone(m.severity) === 'danger' ? 'red' : severityTone(m.severity) === 'warning' ? 'amber' : 'gray'}>
                  {m.severity} +{m.score}
                </Badge>
              </Td>
              <Td className="text-xs">{categoryLabel(m.category)}</Td>
              <Td className="min-w-0 text-xs">
                <div>
                  {m.message} — in {matchWhere(m)}
                </div>
                {m.snippet && <code className="mt-0.5 block break-all rounded bg-zinc-100 px-1.5 py-0.5 font-mono text-[11px] text-zinc-800 dark:bg-zinc-800 dark:text-zinc-200">{eventText(m.snippet)}</code>}
              </Td>
            </Tr>
          ))}
        </TBody>
      </Table>
    </div>
  );
}
