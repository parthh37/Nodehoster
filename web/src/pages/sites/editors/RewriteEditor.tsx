import { useEffect, useId, useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { AlertTriangle, ArrowRightLeft, BookOpen, ChevronRight, FileInput, ListOrdered, Plus, Wand2 } from 'lucide-react';
import { rewriteApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import type { OutboundRule, RewriteCondition, RewriteImport, RewriteImportFormat, RewriteMap, RewriteRule, RoutingConfig } from '@/api/types';
import { Callout, Card } from '@/components/Layout';
import { ErrorBox, Field, useFieldError } from '@/components/Field';
import { Input, NumberInput, Select, Textarea } from '@/components/Input';
import { Checkbox, Switch } from '@/components/Switch';
import { RowsEditor } from '@/components/ListEditor';
import { KeyValueEditor } from '@/components/KeyValueEditor';
import { Badge } from '@/components/Badge';
import { Button } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { Segmented } from '@/components/Tabs';
import { useToast } from '@/components/Toast';
import { cn } from '@/lib/cn';
import { pluralize } from '@/lib/format';
import { REDIRECT_CODES } from '@/lib/siteDefaults';
import {
  OUTBOUND_TAGS,
  OUTBOUND_VARIABLES,
  SERVER_VARIABLES,
  defaultStatusFor,
  describeCondition,
  isProxyTarget,
  mergeRewriteImport,
  newCondition,
  newOutboundRule,
  newRewriteMap,
  newRewriteRule,
  summarizeImport,
} from '@/lib/rewrite';
import type { SiteEditorProps } from './types';

function useRouting({ site, update }: SiteEditorProps) {
  const r = site.routing;
  const set = (patch: Partial<RoutingConfig>) =>
    update((d) => {
      d.routing = { ...d.routing, ...patch };
    });
  return { r, set };
}

const QUERY_STRING_OPTIONS = [
  { value: '', label: 'Keep original unless the target has a query' },
  { value: 'append', label: 'Append original query' },
  { value: 'discard', label: 'Discard original query' },
];

function RuleNumber({ n }: { n: number }) {
  return <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded bg-zinc-100 font-mono text-2xs text-zinc-500 dark:bg-zinc-800">{n}</span>;
}

function VariablesList({ id, outbound }: { id: string; outbound?: boolean }) {
  return (
    <datalist id={id}>
      {(outbound ? OUTBOUND_VARIABLES : SERVER_VARIABLES).map((v) => (
        <option key={v} value={v} />
      ))}
    </datalist>
  );
}

// ---------------------------------------------------------------- conditions

function ConditionsEditor({
  conditions,
  matchAny,
  onChange,
  path,
  listId,
}: {
  conditions: RewriteCondition[] | undefined;
  matchAny: boolean | undefined;
  onChange: (patch: { conditions?: RewriteCondition[]; matchAny?: boolean }) => void;
  /** e.g. "routing.rewrites[2]" */
  path: string;
  listId: string;
}) {
  const list = conditions ?? [];
  const [open, setOpen] = useState(list.length > 0 && list.length <= 2);
  const err = useFieldError(`${path}.conditions`, true);
  const shown = open || !!err;
  const cpath = `${path}.conditions`;

  return (
    <div className="rounded-md border border-zinc-200 bg-zinc-50/60 px-3 py-2 dark:border-zinc-800 dark:bg-zinc-900/40">
      <div className="flex min-h-7 flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={() => setOpen(!shown)}
          className="flex items-center gap-1 text-[13px] font-medium text-zinc-700 hover:text-zinc-900 disabled:cursor-default dark:text-zinc-300 dark:hover:text-zinc-100"
          aria-expanded={shown}
        >
          <ChevronRight className={cn('h-3.5 w-3.5 text-zinc-400 transition-transform', shown && 'rotate-90')} />
          Conditions
          {list.length > 0 && <Badge className="ml-1">{list.length}</Badge>}
        </button>
        {list.length > 1 && (
          <Segmented
            value={matchAny ? 'any' : 'all'}
            onChange={(v) => onChange({ matchAny: v === 'any' })}
            options={[
              { value: 'all', label: 'Match all' },
              { value: 'any', label: 'Match any' },
            ]}
          />
        )}
        {!shown && list.length === 0 && (
          <Button
            size="xs"
            variant="ghost"
            icon={<Plus className="h-3 w-3" />}
            onClick={() => {
              onChange({ conditions: [newCondition()] });
              setOpen(true);
            }}
          >
            Add condition
          </Button>
        )}
        {!shown && list.length > 0 && (
          <span className="min-w-0 flex-1 truncate font-mono text-xs text-zinc-500" title={list.map(describeCondition).join('\n')}>
            {list.map(describeCondition).join(matchAny ? '  or  ' : '  and  ')}
          </span>
        )}
      </div>
      {shown && (
        <div className="mt-2 pb-1">
          <RowsEditor<RewriteCondition>
            items={list}
            onChange={(v) => onChange({ conditions: v })}
            path={cpath}
            addLabel="Add condition"
            create={newCondition}
            empty={<p className="text-xs text-zinc-500">No conditions: the rule applies whenever the pattern matches.</p>}
            render={(c, up, j) => {
              const cp = `${cpath}[${j}]`;
              const isPattern = !c.matchType || c.matchType === 'pattern';
              return (
                <div className="space-y-1.5">
                  <div className="grid gap-2 sm:grid-cols-[minmax(8rem,15rem)_9rem_minmax(0,1fr)]">
                    <Field path={`${cp}.input`}>
                      <Input mono list={listId} value={c.input} placeholder="{HTTP_HOST}" onChange={(e) => up({ input: e.target.value })} />
                    </Field>
                    <Field path={`${cp}.matchType`}>
                      <Select
                        value={c.matchType || 'pattern'}
                        onChange={(v) =>
                          up({
                            matchType: v,
                            pattern: v === 'pattern' ? c.pattern : undefined,
                            ignoreCase: v === 'pattern' ? c.ignoreCase : undefined,
                            input: v !== 'pattern' && (!c.input || c.input === '{HTTP_HOST}') ? '{REQUEST_FILENAME}' : c.input,
                          })
                        }
                        options={[
                          { value: 'pattern', label: 'Matches' },
                          { value: 'isFile', label: 'Is a file' },
                          { value: 'isDirectory', label: 'Is a directory' },
                        ]}
                      />
                    </Field>
                    {isPattern ? (
                      <Field path={`${cp}.pattern`}>
                        <Input mono value={c.pattern ?? ''} placeholder="^www\.example\.com$" onChange={(e) => up({ pattern: e.target.value })} />
                      </Field>
                    ) : (
                      <p className="self-center text-xs text-zinc-500">Checked under the site's physical path.</p>
                    )}
                  </div>
                  <div className="flex flex-wrap gap-x-4 gap-y-1">
                    <Checkbox checked={!!c.negate} onChange={(v) => up({ negate: v })} label="Negate" />
                    {isPattern && <Checkbox checked={!!c.ignoreCase} onChange={(v) => up({ ignoreCase: v })} label="Ignore case" />}
                  </div>
                </div>
              );
            }}
          />
        </div>
      )}
    </div>
  );
}

function PatternRow({
  label,
  path,
  match,
  negate,
  ignoreCase,
  placeholder,
  onChange,
}: {
  label: string;
  path: string;
  match: string;
  negate?: boolean;
  ignoreCase?: boolean;
  placeholder: string;
  onChange: (p: { match?: string; negate?: boolean; ignoreCase?: boolean }) => void;
}) {
  return (
    <div className="grid gap-x-4 gap-y-2 sm:grid-cols-[1fr_auto]">
      <Field label={label} path={`${path}.match`}>
        <Input mono value={match} placeholder={placeholder} onChange={(e) => onChange({ match: e.target.value })} />
      </Field>
      <div className="flex items-center gap-4 sm:pt-6">
        <Checkbox checked={!!negate} onChange={(v) => onChange({ negate: v })} label="Negate" />
        <Checkbox checked={!!ignoreCase} onChange={(v) => onChange({ ignoreCase: v })} label="Ignore case" />
      </div>
    </div>
  );
}

function PlaceholderHelp({ outbound }: { outbound?: boolean }) {
  const items: [string, string][] = [
    ['{R:1}  or  $1', "capture groups of the rule's pattern ({R:0} is the whole match)"],
    ['{C:1}', 'capture groups of the last matched condition'],
    ['{HTTP_HOST}  {QUERY_STRING}  …', 'server variables'],
    ['{MapName:{R:1}}', 'look the value up in a rewrite map'],
    ['{ToLower:…}  {ToUpper:…}  {UrlEncode:…}  {UrlDecode:…}', 'functions'],
  ];
  if (outbound) items.splice(3, 0, ['{RESPONSE_CONTENT_TYPE}', 'response headers as {RESPONSE_<HEADER>}']);
  return (
    <details className="group mt-3 text-xs text-zinc-500 dark:text-zinc-400">
      <summary className="flex cursor-pointer select-none list-none items-center [&::-webkit-details-marker]:hidden gap-1 font-medium text-zinc-600 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-200">
        <BookOpen className="h-3.5 w-3.5" /> Placeholders you can use in {outbound ? 'values' : 'targets'}
      </summary>
      <dl className="mt-2 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 pl-5">
        {items.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="whitespace-pre font-mono text-[12px] text-zinc-700 dark:text-zinc-300">{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
    </details>
  );
}

// ---------------------------------------------------------------- inbound

export function RewritesCard(props: SiteEditorProps) {
  const { r, set } = useRouting(props);
  const listId = useId();
  const [importing, setImporting] = useState(false);
  return (
    <Card
      title={<span className="flex items-center gap-2"><ListOrdered className="h-4 w-4 text-zinc-400" />URL rewrite rules</span>}
      description={
        <>
          Inbound rules, evaluated top to bottom. The pattern is a regular expression on the path including the leading slash, e.g.{' '}
          <span className="font-mono">^/blog/(.*)$</span>. A matching rule with <em>Stop processing</em> ends evaluation.
        </>
      }
      actions={
        !props.readOnly && (
          <Button size="sm" icon={<FileInput className="h-3.5 w-3.5" />} onClick={() => setImporting(true)}>
            Import…
          </Button>
        )
      }
    >
      <VariablesList id={listId} />
      <RowsEditor<RewriteRule>
        items={r.rewrites}
        onChange={(v) => set({ rewrites: v })}
        orderable
        path="routing.rewrites"
        addLabel="Add rule"
        create={newRewriteRule}
        empty={<p className="text-xs text-zinc-500">No rewrite rules. Requests pass through unchanged.</p>}
        rowClassName="rounded-lg border border-zinc-200 p-3 dark:border-zinc-800"
        render={(rule, up, i) => <InboundRule rule={rule} up={up} index={i} listId={listId} />}
      />
      <PlaceholderHelp />
      {importing && <ImportRewritesDialog {...props} onClose={() => setImporting(false)} />}
    </Card>
  );
}

function InboundRule({ rule, up, index: i, listId }: { rule: RewriteRule; up: (p: Partial<RewriteRule>) => void; index: number; listId: string }) {
  const p = `routing.rewrites[${i}]`;
  const proxy = rule.action === 'rewrite' && isProxyTarget(rule.target);
  const hasTarget = rule.action === 'rewrite' || rule.action === 'redirect';
  return (
    <div className={cn('space-y-3', !rule.enabled && 'opacity-60')}>
      <div className="flex flex-wrap items-center gap-3">
        <RuleNumber n={i + 1} />
        <Input className="w-56" value={rule.name} placeholder="Rule name" onChange={(e) => up({ name: e.target.value })} />
        <Switch size="sm" checked={rule.enabled} onChange={(v) => up({ enabled: v })} label="Enabled" />
        <Checkbox checked={rule.stop} onChange={(v) => up({ stop: v })} label="Stop processing" />
      </div>
      <PatternRow
        label={rule.negate ? 'Path does not match (regex)' : 'Match path (regex)'}
        path={p}
        match={rule.match}
        negate={rule.negate}
        ignoreCase={rule.ignoreCase}
        placeholder="^/blog/(.*)$"
        onChange={up}
      />
      {!!rule.host && (
        <Field label="Host (regex)" path={`${p}.host`} hint="Shorthand for a {HTTP_HOST} condition. Clear it to remove.">
          <Input mono value={rule.host} placeholder="^www\.example\.com$" onChange={(e) => up({ host: e.target.value })} />
        </Field>
      )}
      <ConditionsEditor conditions={rule.conditions} matchAny={rule.matchAny} onChange={up} path={p} listId={listId} />
      <div className="grid gap-3 sm:grid-cols-[12rem_1fr]">
        <Field label="Action" path={`${p}.action`}>
          <Select
            value={rule.action}
            onChange={(v) =>
              up({
                action: v,
                statusCode: defaultStatusFor(v),
                preserveHost: v === 'rewrite' ? rule.preserveHost : undefined,
                queryString: v === 'rewrite' || v === 'redirect' ? rule.queryString : undefined,
              })
            }
            options={[
              { value: 'rewrite', label: 'Rewrite' },
              { value: 'redirect', label: 'Redirect' },
              { value: 'block', label: 'Block' },
              { value: 'respond', label: 'Custom response' },
              { value: 'none', label: 'None' },
            ]}
          />
        </Field>
        {hasTarget && (
          <Field
            label={
              <span className="flex items-center gap-1.5">
                {rule.action === 'rewrite' ? 'Rewrite to' : 'Redirect to'}
                {proxy && (
                  <Badge tone="violet">
                    <ArrowRightLeft className="h-3 w-3" /> Reverse proxy
                  </Badge>
                )}
              </span>
            }
            path={`${p}.target`}
            hint={
              rule.action === 'rewrite'
                ? proxy
                  ? 'An absolute URL: the request is proxied to it and the response returned to the client.'
                  : 'A path on this site, or an absolute http(s):// URL to proxy the request to another server.'
                : undefined
            }
          >
            <Input
              mono
              value={rule.target ?? ''}
              placeholder={rule.action === 'rewrite' ? '/index.php?p={R:1}' : 'https://www.example.com/{R:1}'}
              onChange={(e) => up({ target: e.target.value, preserveHost: isProxyTarget(e.target.value) ? rule.preserveHost : undefined })}
            />
          </Field>
        )}
        {rule.action === 'block' && (
          <Field label="Status" path={`${p}.statusCode`} hint="403 Forbidden unless set.">
            <NumberInput mono className="w-28" blankZero min={400} max={599} placeholder="403" value={rule.statusCode} onChange={(v) => up({ statusCode: v })} />
          </Field>
        )}
        {rule.action === 'none' && (
          <p className="text-xs text-zinc-500 sm:pt-7">Nothing happens to the request. Useful with <em>Stop processing</em> to skip the rules below.</p>
        )}
      </div>
      {hasTarget && (
        <div className="grid gap-3 sm:grid-cols-[12rem_minmax(0,22rem)]">
          {rule.action === 'redirect' ? (
            <Field label="Status" path={`${p}.statusCode`}>
              <Select value={rule.statusCode || 301} onChange={(v) => up({ statusCode: Number(v) })} options={REDIRECT_CODES} />
            </Field>
          ) : (
            <span className="hidden sm:block" />
          )}
          <Field label="Query string" path={`${p}.queryString`}>
            <Select value={rule.queryString ?? ''} onChange={(v) => up({ queryString: v || undefined })} options={QUERY_STRING_OPTIONS} />
          </Field>
        </div>
      )}
      {proxy && (
        <Checkbox
          checked={!!rule.preserveHost}
          onChange={(v) => up({ preserveHost: v })}
          label="Preserve Host header"
          description="Send the client's Host header to the target instead of the target's own host name."
        />
      )}
      {rule.action === 'respond' && (
        <>
          <div className="grid gap-3 sm:grid-cols-[12rem_minmax(0,18rem)]">
            <Field label="Status" path={`${p}.statusCode`}>
              <NumberInput mono min={200} max={599} value={rule.statusCode} onChange={(v) => up({ statusCode: v })} />
            </Field>
            <Field label="Content type" path={`${p}.contentType`}>
              <Input mono value={rule.contentType ?? ''} placeholder="text/plain" onChange={(e) => up({ contentType: e.target.value })} />
            </Field>
          </div>
          <Field label="Response body" path={`${p}.body`}>
            <Textarea mono rows={3} value={rule.body ?? ''} onChange={(e) => up({ body: e.target.value })} />
          </Field>
        </>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- outbound

export function OutboundRulesCard(props: SiteEditorProps) {
  const { r, set } = useRouting(props);
  const listId = useId();
  return (
    <Card
      title={<span className="flex items-center gap-2"><Wand2 className="h-4 w-4 text-zinc-400" />Outbound rules</span>}
      description="Rewrite responses: a header such as Location, URLs in HTML tag attributes, or any text in the body. Bodies are rewritten only for text responses up to 8 MB."
    >
      <VariablesList id={listId} outbound />
      <RowsEditor<OutboundRule>
        items={r.outboundRules}
        onChange={(v) => set({ outboundRules: v })}
        orderable
        path="routing.outboundRules"
        addLabel="Add outbound rule"
        create={newOutboundRule}
        empty={<p className="text-xs text-zinc-500">No outbound rules. Responses are sent unchanged.</p>}
        rowClassName="rounded-lg border border-zinc-200 p-3 dark:border-zinc-800"
        render={(rule, up, i) => {
          const p = `routing.outboundRules[${i}]`;
          const tags = (rule.tags ?? []).map((t) => t.toLowerCase());
          return (
            <div className={cn('space-y-3', !rule.enabled && 'opacity-60')}>
              <div className="flex flex-wrap items-center gap-3">
                <RuleNumber n={i + 1} />
                <Input className="w-56" value={rule.name} placeholder="Rule name" onChange={(e) => up({ name: e.target.value })} />
                <Switch size="sm" checked={rule.enabled} onChange={(v) => up({ enabled: v })} label="Enabled" />
                <Checkbox checked={rule.stop} onChange={(v) => up({ stop: v })} label="Stop processing" />
              </div>
              <div className="grid gap-3 sm:grid-cols-[12rem_1fr]">
                <Field label="Apply to" path={`${p}.scope`}>
                  <Select
                    value={rule.scope}
                    onChange={(v) =>
                      up({
                        scope: v,
                        header: v === 'header' ? rule.header || 'Location' : undefined,
                        tags: v === 'tags' ? (tags.length ? tags : ['a', 'form', 'img']) : undefined,
                      })
                    }
                    options={[
                      { value: 'header', label: 'A response header' },
                      { value: 'tags', label: 'URLs in HTML tags' },
                      { value: 'body', label: 'The response body' },
                    ]}
                  />
                </Field>
                {rule.scope === 'header' && (
                  <Field label="Header" path={`${p}.header`}>
                    <Input mono className="sm:w-64" value={rule.header ?? ''} placeholder="Location" onChange={(e) => up({ header: e.target.value.trim() })} />
                  </Field>
                )}
                {rule.scope === 'tags' && (
                  <Field label="Tags" path={`${p}.tags`} hint="The URL attributes of these tags (href, src, action…) in HTML responses.">
                    <div className="flex flex-wrap gap-x-4 gap-y-1.5 rounded-md border border-zinc-200 px-3 py-2 dark:border-zinc-800">
                      {OUTBOUND_TAGS.map((t) => (
                        <Checkbox
                          key={t}
                          checked={tags.includes(t)}
                          onChange={(on) => up({ tags: on ? OUTBOUND_TAGS.filter((x) => x === t || tags.includes(x)) : tags.filter((x) => x !== t) })}
                          label={<span className="font-mono text-xs">{t}</span>}
                        />
                      ))}
                    </div>
                  </Field>
                )}
                {rule.scope === 'body' && (
                  <p className="text-xs text-zinc-500 sm:pt-7">Every match of the pattern in a text response (HTML, CSS, JavaScript, JSON…).</p>
                )}
              </div>
              <PatternRow
                label={rule.negate ? 'Value does not match (regex)' : 'Match value (regex)'}
                path={p}
                match={rule.match}
                negate={rule.negate}
                ignoreCase={rule.ignoreCase}
                placeholder="^https?://localhost(:\d+)?/(.*)$"
                onChange={up}
              />
              <ConditionsEditor conditions={rule.conditions} matchAny={rule.matchAny} onChange={up} path={p} listId={listId} />
              <div className="grid gap-3 sm:grid-cols-[12rem_1fr]">
                <Field label="Action" path={`${p}.action`}>
                  <Select
                    value={rule.action}
                    onChange={(v) => up({ action: v })}
                    options={[
                      { value: 'rewrite', label: 'Rewrite' },
                      { value: 'none', label: 'None' },
                    ]}
                  />
                </Field>
                {rule.action === 'rewrite' ? (
                  <Field label="Replace with" path={`${p}.value`}>
                    <Input mono value={rule.value ?? ''} placeholder="/{R:2}" onChange={(e) => up({ value: e.target.value })} />
                  </Field>
                ) : (
                  <p className="text-xs text-zinc-500 sm:pt-7">Leaves the value unchanged. Useful with <em>Stop processing</em>.</p>
                )}
              </div>
            </div>
          );
        }}
      />
      <PlaceholderHelp outbound />
    </Card>
  );
}

// ---------------------------------------------------------------- maps

export function RewriteMapsCard(props: SiteEditorProps) {
  const { r, set } = useRouting(props);
  return (
    <Card
      title={<span className="flex items-center gap-2"><BookOpen className="h-4 w-4 text-zinc-400" />Rewrite maps</span>}
      description={
        <>
          Lookup tables for rule targets, e.g. <span className="font-mono">{'{Redirects:{R:0}}'}</span> for a list of old and new URLs. Keys match
          regardless of case.
        </>
      }
    >
      <RowsEditor<RewriteMap>
        items={r.rewriteMaps}
        onChange={(v) => set({ rewriteMaps: v })}
        path="routing.rewriteMaps"
        addLabel="Add map"
        create={newRewriteMap}
        empty={<p className="text-xs text-zinc-500">No rewrite maps.</p>}
        rowClassName="rounded-lg border border-zinc-200 p-3 dark:border-zinc-800"
        render={(m, up, i) => {
          const p = `routing.rewriteMaps[${i}]`;
          return (
            <div className="space-y-3">
              <div className="grid gap-3 sm:grid-cols-[minmax(0,16rem)_1fr]">
                <Field label="Name" path={`${p}.name`} hint={m.name ? <span className="font-mono">{`{${m.name}:{R:1}}`}</span> : undefined}>
                  <Input mono value={m.name} placeholder="Redirects" onChange={(e) => up({ name: e.target.value.trim() })} />
                </Field>
                <Field label="Default value" path={`${p}.defaultValue`} hint="Used when the key is not in the map. Blank = empty.">
                  <Input mono value={m.defaultValue ?? ''} onChange={(e) => up({ defaultValue: e.target.value })} />
                </Field>
              </div>
              <Field label="Entries" path={`${p}.entries`} prefix>
                <KeyValueEditor
                  value={m.entries}
                  onChange={(v) => up({ entries: v })}
                  keyLabel="Key"
                  valueLabel="Value"
                  keyPlaceholder="/old-page"
                  valuePlaceholder="/new-page"
                  keyWidth="w-64"
                  addLabel="Add entry"
                  empty={<p className="text-xs text-zinc-500">No entries.</p>}
                />
              </Field>
            </div>
          );
        }}
      />
    </Card>
  );
}

// ---------------------------------------------------------------- import

const IMPORT_PLACEHOLDER: Record<RewriteImportFormat, string> = {
  webconfig: `<rewrite>
  <rules>
    <rule name="SPA fallback" stopProcessing="true">
      <match url=".*" />
      <conditions logicalGrouping="MatchAll">
        <add input="{REQUEST_FILENAME}" matchType="IsFile" negate="true" />
      </conditions>
      <action type="Rewrite" url="/index.html" />
    </rule>
  </rules>
</rewrite>`,
  htaccess: `RewriteEngine On
RewriteCond %{HTTP_HOST} ^example\\.com$ [NC]
RewriteRule ^(.*)$ https://www.example.com/$1 [R=301,L]`,
};

function ImportRewritesDialog({ site, update, onClose }: SiteEditorProps & { onClose: () => void }) {
  const toast = useToast();
  const [format, setFormat] = useState<RewriteImportFormat>('webconfig');
  const [text, setText] = useState('');
  const [result, setResult] = useState<RewriteImport | null>(null);
  const convert = useMutation({
    mutationFn: () => rewriteApi.import({ format, text }),
    onSuccess: (r) => setResult(r),
  });

  // Edits invalidate the preview.
  useEffect(() => setResult(null), [format, text]);

  const summary = result ? summarizeImport(result, site.routing) : null;
  const total = summary ? summary.rules + summary.outboundRules + summary.maps : 0;
  const warnings = result?.warnings ?? [];

  const apply = () => {
    if (!result || !total) return;
    update((d) => {
      d.routing = mergeRewriteImport(d.routing, result);
    });
    toast.success(`Imported ${pluralize(total, 'item')}`, 'Review the rules, then save to apply them.');
    onClose();
  };

  return (
    <Dialog
      open
      onClose={onClose}
      size="xl"
      icon={<FileInput className="h-5 w-5 text-zinc-400" />}
      title="Import rewrite rules"
      description="Convert IIS URL Rewrite rules from a web.config or Apache mod_rewrite rules from an .htaccess file. Nothing changes until you save the site."
      onSubmit={() => (result ? apply() : text.trim() && convert.mutate())}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          {result ? (
            <Button type="submit" variant="primary" icon={<Plus className="h-3.5 w-3.5" />} disabled={!total}>
              Add to site
            </Button>
          ) : (
            <Button type="submit" variant="primary" icon={<Wand2 className="h-3.5 w-3.5" />} disabled={!text.trim()} loading={convert.isPending}>
              Convert
            </Button>
          )}
        </>
      }
    >
      <div className="space-y-4">
        <Field label="Format">
          <Select
            className="w-64"
            value={format}
            onChange={(v) => setFormat(v as RewriteImportFormat)}
            options={[
              { value: 'webconfig', label: 'IIS web.config' },
              { value: 'htaccess', label: 'Apache .htaccess' },
            ]}
          />
        </Field>
        <Field
          label={format === 'webconfig' ? 'web.config' : '.htaccess'}
          hint={format === 'webconfig' ? 'The whole file or just its <rewrite> section. Rewrite maps and outbound rules are included.' : 'RewriteCond and RewriteRule lines; other directives are ignored.'}
        >
          <Textarea mono rows={result ? 6 : 14} spellCheck={false} value={text} placeholder={IMPORT_PLACEHOLDER[format]} onChange={(e) => setText(e.target.value)} />
        </Field>
        {convert.isError && <ErrorBox>{errorMessage(convert.error)}</ErrorBox>}
        {result && summary && (
          <div className="space-y-3">
            <div className="flex flex-wrap items-center gap-2">
              <Badge tone="accent">{pluralize(summary.rules, 'inbound rule')}</Badge>
              <Badge tone="accent">{pluralize(summary.outboundRules, 'outbound rule')}</Badge>
              <Badge tone="accent">{pluralize(summary.maps, 'rewrite map')}</Badge>
            </div>
            {total === 0 && <Callout tone="info">Nothing to import: no rules or maps were found.</Callout>}
            {summary.rules > 0 && <ImportList title="Inbound rules (added after the existing ones)" items={(result.rules ?? []).map((r) => ({ name: r.name, tag: r.action }))} />}
            {summary.outboundRules > 0 && (
              <ImportList title="Outbound rules" items={(result.outboundRules ?? []).map((r) => ({ name: r.name, tag: r.scope === 'header' ? r.header || 'header' : r.scope }))} />
            )}
            {summary.maps > 0 && (
              <ImportList
                title="Rewrite maps"
                items={(result.rewriteMaps ?? []).map((m) => ({
                  name: m.name,
                  tag: summary.replacedMaps.some((n) => n.toLowerCase() === m.name.toLowerCase()) ? 'replaces existing' : pluralize(Object.keys(m.entries ?? {}).length, 'entry', 'entries'),
                }))}
              />
            )}
            {warnings.length > 0 && (
              <Callout tone="warning" icon={<AlertTriangle />} title={`${pluralize(warnings.length, 'warning')}: some parts could not be converted`}>
                <ul className="mt-1 list-disc space-y-0.5 pl-4">
                  {warnings.map((w, i) => (
                    <li key={i} className="break-words">
                      {w}
                    </li>
                  ))}
                </ul>
              </Callout>
            )}
          </div>
        )}
      </div>
    </Dialog>
  );
}

function ImportList({ title, items }: { title: string; items: { name: string; tag: string }[] }) {
  return (
    <div>
      <p className="mb-1 text-2xs font-semibold uppercase tracking-wide text-zinc-500">{title}</p>
      <ol className="scrollbar-thin max-h-48 divide-y divide-zinc-100 overflow-y-auto rounded-md border border-zinc-200 text-[13px] dark:divide-zinc-800 dark:border-zinc-800">
        {items.map((it, i) => (
          <li key={i} className="flex items-center gap-2 px-3 py-1.5">
            <RuleNumber n={i + 1} />
            <span className="min-w-0 flex-1 truncate">{it.name || <span className="italic text-zinc-400">Unnamed</span>}</span>
            <Badge mono>{it.tag}</Badge>
          </li>
        ))}
      </ol>
    </div>
  );
}
