import { useRef, useState, type ReactNode } from 'react';
import { Loader2, Search } from 'lucide-react';
import { sitesApi } from '@/api/endpoints';
import { ApiError, errorMessage } from '@/api/client';
import type { LogLine, LogSearchParams, LogType, SiteView } from '@/api/types';
import { Button } from '@/components/Button';
import { Input, Select } from '@/components/Input';
import { Segmented } from '@/components/Tabs';
import { Checkbox } from '@/components/Switch';
import { ErrorBox } from '@/components/Field';
import { formatBytes, formatDateTime } from '@/lib/format';
import { highlight, patternProblem, RANGE_PRESETS, timeRange, type RangePreset } from '@/lib/logSearch';
import { cn } from '@/lib/cn';
import { runsNode } from '@/lib/siteDefaults';

type Stream = 'all' | 'stdout' | 'stderr' | 'system';

/**
 * Search through a site's log files, the rotated ones included, newest
 * first. The server stops after a few seconds or a few hundred MB and says
 * so; "Load more" carries on from there.
 */
export function LogSearch({ site, modeSwitch }: { site: SiteView; modeSwitch: ReactNode }) {
  const hasApp = runsNode(site.type);
  // A background worker serves no HTTP, so it has no access log.
  const hasAccess = site.type !== 'worker';
  const [source, setSource] = useState<LogType>(hasApp ? 'app' : 'access');
  const [q, setQ] = useState('');
  const [regex, setRegex] = useState(false);
  const [stream, setStream] = useState<Stream>('all');
  const [range, setRange] = useState<RangePreset>('24h');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [lines, setLines] = useState<LogLine[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [truncated, setTruncated] = useState(false);
  const [scanned, setScanned] = useState(0);
  const [searched, setSearched] = useState<{ q: string; regex: boolean } | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const params = useRef<LogSearchParams>({});
  const abort = useRef<AbortController | null>(null);
  const problem = patternProblem(q, regex);

  const run = async (more: boolean) => {
    abort.current?.abort();
    const ac = new AbortController();
    abort.current = ac;
    if (!more) {
      params.current = { q: q || undefined, regex, source, stream: source === 'app' ? stream : undefined, ...timeRange(range, from, to), limit: 200 };
      setSearched({ q, regex });
    }
    setLoading(true);
    setError(null);
    try {
      const res = await sitesApi.searchLogs(site.id, { ...params.current, cursor: more ? cursor : undefined }, ac.signal);
      setLines((prev) => (more ? prev.concat(res.lines) : res.lines));
      setCursor(res.cursor);
      setTruncated(res.truncated);
      setScanned((prev) => (more ? prev : 0) + res.scannedBytes);
    } catch (e) {
      if (ac.signal.aborted) return;
      setError(e instanceof ApiError && e.field === 'q' ? `Pattern: ${e.message}` : errorMessage(e));
      if (!more) setLines([]);
    } finally {
      if (abort.current === ac) setLoading(false);
    }
  };

  const multiInstance = source === 'app' && (site.node?.instances ?? 1) > 1;

  return (
    <div className="nh-card flex h-[calc(100vh-17rem)] min-h-[420px] flex-col overflow-hidden">
      <form
        className="flex flex-wrap items-center gap-2 border-b border-zinc-200 px-3 py-2 dark:border-zinc-800"
        onSubmit={(e) => {
          e.preventDefault();
          if (!problem) void run(false);
        }}
      >
        {modeSwitch}
        <Segmented<LogType>
          value={source}
          onChange={setSource}
          options={[
            ...(hasApp ? [{ value: 'app' as const, label: 'Application' }] : []),
            ...(hasAccess ? [{ value: 'access' as const, label: 'Access log' }] : []),
          ]}
        />
        <Input
          className="w-64"
          mono={regex}
          prefix={<Search className="h-3.5 w-3.5" />}
          placeholder={regex ? 'Regular expression (RE2)' : 'Text to find'}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          aria-invalid={!!problem || undefined}
          title={problem ?? undefined}
        />
        <Checkbox checked={regex} onChange={setRegex} label="Regex" />
        {source === 'app' && (
          <Select
            className="w-32"
            value={stream}
            onChange={(v) => setStream(v as Stream)}
            options={[
              { value: 'all', label: 'All streams' },
              { value: 'stdout', label: 'stdout' },
              { value: 'stderr', label: 'stderr' },
              { value: 'system', label: 'NodeHoster' },
            ]}
          />
        )}
        <Select className="w-44" value={range} onChange={(v) => setRange(v as RangePreset)} options={RANGE_PRESETS} />
        {range === 'custom' && (
          <>
            <Input type="datetime-local" className="w-48" value={from} onChange={(e) => setFrom(e.target.value)} title="From" />
            <span className="text-xs text-zinc-500">to</span>
            <Input type="datetime-local" className="w-48" value={to} onChange={(e) => setTo(e.target.value)} title="To (empty = now)" />
          </>
        )}
        <Button type="submit" size="sm" variant="primary" icon={<Search className="h-3.5 w-3.5" />} loading={loading && !cursor} disabled={!!problem}>
          Search
        </Button>
      </form>
      {problem && <ErrorBox className="m-3">{problem}</ErrorBox>}
      {error && <ErrorBox className="m-3">{error}</ErrorBox>}
      <div className="scrollbar-thin flex-1 overflow-auto bg-zinc-950 py-2 font-mono text-[12px] leading-[1.35rem] text-zinc-200">
        {!searched ? (
          <p className="px-3 text-zinc-500">Search this site's log files, including rotated ones, newest first.</p>
        ) : lines.length === 0 && !loading ? (
          <p className="px-3 text-zinc-500">No matching lines{truncated ? ' in the part searched so far' : ''}.</p>
        ) : (
          lines.map((l, i) => (
            <div
              key={i}
              className={cn(
                'flex gap-3 whitespace-pre-wrap break-all px-3 hover:bg-white/[0.04]',
                l.s === 'stderr' && 'bg-red-500/[0.06] text-red-300',
                l.s === 'system' && 'text-accent-300',
              )}
            >
              {source === 'app' && <span className="shrink-0 select-none text-zinc-500">{formatDateTime(l.t)}</span>}
              {multiInstance && <span className="w-5 shrink-0 select-none text-right text-zinc-500">{l.i >= 0 ? `#${l.i}` : ''}</span>}
              <span className="min-w-0 flex-1">
                {highlight(l.m, searched.q, searched.regex).map((seg, j) =>
                  seg.match ? (
                    <mark key={j} className="rounded-sm bg-amber-400/30 px-px text-amber-100">
                      {seg.text}
                    </mark>
                  ) : (
                    seg.text
                  ),
                )}
              </span>
            </div>
          ))
        )}
      </div>
      {searched && (
        <div className="flex items-center gap-3 border-t border-zinc-800 bg-zinc-950 px-3 py-1.5 text-2xs text-zinc-500">
          <span>
            {lines.length.toLocaleString()} line{lines.length === 1 ? '' : 's'} · {formatBytes(scanned)} searched
            {truncated && ' · the search stopped at its time limit; older lines may match'}
          </span>
          {loading && <Loader2 className="h-3 w-3 animate-spin" />}
          {cursor && (
            <Button size="xs" variant="ghost" className="ml-auto" loading={loading} onClick={() => void run(true)}>
              {truncated ? 'Continue searching' : 'Load more'}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
