import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ChevronDown, Search } from 'lucide-react';
import { mimeApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import { Card, FormSection, Loading, Mono, Sections } from '@/components/Layout';
import { ErrorBox, Field } from '@/components/Field';
import { Input, Select } from '@/components/Input';
import { Button } from '@/components/Button';
import { Badge } from '@/components/Badge';
import { Table, TableMessage, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { cn } from '@/lib/cn';
import { formatNumber } from '@/lib/format';
import { MimeMapsEditor } from '../sites/editors/MimeEditor';
import type { SettingsTabProps } from './SettingsPage';

export function MimeTab({ s, update }: SettingsTabProps) {
  const custom = s.mime.types ?? [];
  return (
    <div className="space-y-5">
      <Card title="MIME types" description="Content types for static files, like IIS MIME Types at the server level. Sites can add their own on their Routing tab.">
        <Sections>
          <FormSection title="Custom mappings" description="Added to the built-in table, or replacing its entry for the same extension.">
            <MimeMapsEditor
              value={custom}
              path="mime.types"
              onChange={(v) =>
                update((d) => {
                  d.mime = { ...d.mime, types: v };
                })
              }
            />
          </FormSection>
          <FormSection title="Unknown file extensions" description="Files whose extension has no MIME type. Refusing them, like IIS does, avoids serving files that were never meant to be downloaded.">
            <Field path="mime.unknownTypes">
              <Select
                className="sm:w-80"
                value={s.mime.unknownTypes || 'serve'}
                onChange={(v) =>
                  update((d) => {
                    d.mime = { ...d.mime, unknownTypes: v };
                  })
                }
                options={[
                  { value: 'serve', label: 'Serve as application/octet-stream' },
                  { value: 'deny', label: 'Refuse with 404 Not Found' },
                ]}
              />
            </Field>
          </FormSection>
        </Sections>
      </Card>
      <BuiltInTypes overrides={custom} />
    </div>
  );
}

function BuiltInTypes({ overrides }: { overrides: { extension: string; type: string }[] }) {
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState('');
  const q = useQuery({ queryKey: qk.mimeDefaults, queryFn: mimeApi.defaults, staleTime: Infinity, enabled: open });
  const custom = useMemo(() => new Map(overrides.map((m) => [m.extension.toLowerCase(), m.type])), [overrides]);
  const rows = useMemo(() => {
    const term = filter.trim().toLowerCase();
    return (q.data ?? []).filter((m) => !term || m.extension.toLowerCase().includes(term) || m.type.toLowerCase().includes(term));
  }, [q.data, filter]);

  return (
    <Card
      title="Built-in MIME types"
      description="The table NodeHoster starts from. Read-only; add a custom mapping above to change an entry."
      actions={
        <Button size="sm" variant="ghost" iconRight={<ChevronDown className={cn('h-3.5 w-3.5 transition-transform', open && 'rotate-180')} />} onClick={() => setOpen(!open)}>
          {open ? 'Hide' : 'Show'}
        </Button>
      }
      flush
    >
      {open && (
        <div>
          <div className="flex flex-wrap items-center gap-3 border-b border-zinc-200 px-4 py-2.5 dark:border-zinc-800">
            <Input className="w-72" prefix={<Search className="h-3.5 w-3.5" />} value={filter} placeholder="Filter by extension or type" onChange={(e) => setFilter(e.target.value)} />
            {q.data && (
              <span className="text-xs text-zinc-500">
                {rows.length === q.data.length ? `${formatNumber(q.data.length)} types` : `${formatNumber(rows.length)} of ${formatNumber(q.data.length)}`}
              </span>
            )}
          </div>
          {q.isPending ? (
            <Loading />
          ) : q.isError ? (
            <ErrorBox className="m-4">{errorMessage(q.error)}</ErrorBox>
          ) : (
            <div className="scrollbar-thin max-h-[28rem] overflow-y-auto">
              <Table dense>
                <THead>
                  <tr>
                    <Th className="w-40">Extension</Th>
                    <Th>Content type</Th>
                  </tr>
                </THead>
                <TBody>
                  {rows.length === 0 && <TableMessage colSpan={2}>No built-in type matches “{filter}”.</TableMessage>}
                  {rows.map((m) => {
                    const over = custom.get(m.extension.toLowerCase());
                    return (
                      <Tr key={m.extension}>
                        <Td>
                          <Mono>{m.extension}</Mono>
                        </Td>
                        <Td>
                          <Mono className={cn(over !== undefined && 'text-zinc-400 line-through')}>{m.type}</Mono>
                          {over !== undefined && (
                            <span className="ml-2 inline-flex items-center gap-1.5">
                              <Badge tone="amber">overridden</Badge>
                              <Mono>{over}</Mono>
                            </span>
                          )}
                        </Td>
                      </Tr>
                    );
                  })}
                </TBody>
              </Table>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}
