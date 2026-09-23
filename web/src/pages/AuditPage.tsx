import { useState } from 'react';
import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { ChevronLeft, ChevronRight, ClipboardList, Search } from 'lucide-react';
import { serverApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import { Card, EmptyState, Loading, Mono, PageHeader } from '@/components/Layout';
import { Table, TableMessage, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { Button } from '@/components/Button';
import { Input, Select } from '@/components/Input';
import { ErrorBox } from '@/components/Field';
import { formatDateTime } from '@/lib/format';

export function AuditPage() {
  const [pageSize, setPageSize] = useState(50);
  const [page, setPage] = useState(0);
  const [filter, setFilter] = useState('');
  const offset = page * pageSize;
  const q = useQuery({
    queryKey: qk.audit(pageSize, offset),
    queryFn: () => serverApi.audit(pageSize, offset),
    placeholderData: keepPreviousData,
  });
  const rows = q.data ?? [];
  const term = filter.trim().toLowerCase();
  const visible = term
    ? rows.filter((r) => [r.user, r.ip, r.action, r.target, r.detail ?? ''].some((f) => f.toLowerCase().includes(term)))
    : rows;
  const hasNext = rows.length === pageSize;

  return (
    <div>
      <PageHeader title="Audit log" description="Every configuration change and operator action, with who made it and from where." />
      {q.isError && <ErrorBox className="mb-4">{errorMessage(q.error)}</ErrorBox>}
      <Card flush>
        <div className="flex flex-wrap items-center gap-2 border-b border-zinc-200 px-4 py-2.5 dark:border-zinc-800">
          <Input className="w-64" prefix={<Search className="h-3.5 w-3.5" />} placeholder="Filter this page…" value={filter} onChange={(e) => setFilter(e.target.value)} />
          <div className="ml-auto flex items-center gap-2">
            <Select
              className="w-32"
              value={pageSize}
              onChange={(v) => {
                setPageSize(Number(v));
                setPage(0);
              }}
              options={[25, 50, 100, 200].map((n) => ({ value: n, label: `${n} / page` }))}
            />
            <span className="text-xs tabular text-zinc-500">
              {rows.length ? `${offset + 1}–${offset + rows.length}` : '0'}
            </span>
            <Button size="sm" icon={<ChevronLeft className="h-3.5 w-3.5" />} disabled={page === 0 || q.isFetching} onClick={() => setPage((p) => p - 1)} aria-label="Previous page" />
            <Button size="sm" icon={<ChevronRight className="h-3.5 w-3.5" />} disabled={!hasNext || q.isFetching} onClick={() => setPage((p) => p + 1)} aria-label="Next page" />
          </div>
        </div>
        {q.isPending ? (
          <Loading />
        ) : rows.length === 0 && page === 0 ? (
          <EmptyState icon={<ClipboardList />} title="No audit entries" description="Changes made through the console or API are recorded here." />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th className="w-44">Time</Th>
                <Th>User</Th>
                <Th>IP</Th>
                <Th>Action</Th>
                <Th>Target</Th>
                <Th>Detail</Th>
              </tr>
            </THead>
            <TBody>
              {visible.length === 0 && <TableMessage colSpan={6}>{rows.length ? 'No entries on this page match the filter.' : 'No more entries.'}</TableMessage>}
              {visible.map((r) => (
                <Tr key={r.id}>
                  <Td className="whitespace-nowrap text-xs text-zinc-500">{formatDateTime(r.time)}</Td>
                  <Td className="font-medium">{r.user || <span className="text-zinc-400">system</span>}</Td>
                  <Td>
                    <Mono className="text-zinc-500">{r.ip || '—'}</Mono>
                  </Td>
                  <Td>
                    <Mono>{r.action}</Mono>
                  </Td>
                  <Td className="max-w-[16rem] truncate" title={r.target}>
                    {r.target}
                  </Td>
                  <Td className="max-w-md truncate text-xs text-zinc-500" title={r.detail}>
                    {r.detail || '—'}
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        )}
      </Card>
    </div>
  );
}
