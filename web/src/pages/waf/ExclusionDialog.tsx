import { useEffect, useState } from 'react';
import { ShieldOff } from 'lucide-react';
import type { WAFExclusion } from '@/api/wafTypes';
import { Dialog } from '@/components/Dialog';
import { Button } from '@/components/Button';
import { Field } from '@/components/Field';
import { Input, Textarea } from '@/components/Input';
import { Checkbox } from '@/components/Switch';
import { Callout, Grid } from '@/components/Layout';
import { WAF_CATEGORIES, describeExclusion, exclusionError, parentPath, parseNames, parseRuleIds } from '@/lib/waf';

interface Draft {
  path: string;
  rules: string;
  categories: string[];
  args: string;
  cookies: string;
  headers: string;
  comment: string;
}

function toDraft(x: WAFExclusion): Draft {
  return {
    path: x.path ?? '',
    rules: (x.ruleIds ?? []).join(', '),
    categories: [...(x.categories ?? [])],
    args: (x.args ?? []).join(', '),
    cookies: (x.cookies ?? []).join(', '),
    headers: (x.headers ?? []).join(', '),
    comment: x.comment ?? '',
  };
}

function fromDraft(d: Draft): WAFExclusion | null {
  const ruleIds = parseRuleIds(d.rules);
  if (!ruleIds) return null;
  const x: WAFExclusion = {};
  if (d.path.trim()) x.path = d.path.trim();
  if (ruleIds.length) x.ruleIds = ruleIds;
  if (d.categories.length) x.categories = d.categories;
  const args = parseNames(d.args);
  const cookies = parseNames(d.cookies);
  const headers = parseNames(d.headers);
  if (args.length) x.args = args;
  if (cookies.length) x.cookies = cookies;
  if (headers.length) x.headers = headers;
  if (d.comment.trim()) x.comment = d.comment.trim();
  return x;
}

/**
 * Edits one firewall exclusion. From an event it starts as the narrowest
 * exclusion that would have let the request through.
 */
export function ExclusionDialog({
  open,
  initial,
  title = 'Firewall exclusion',
  saveLabel = 'Save exclusion',
  saving,
  onClose,
  onSave,
}: {
  open: boolean;
  initial: WAFExclusion;
  title?: string;
  saveLabel?: string;
  saving?: boolean;
  onClose: () => void;
  onSave: (x: WAFExclusion) => void;
}) {
  const [d, setD] = useState<Draft>(() => toDraft(initial));
  useEffect(() => {
    if (open) setD(toDraft(initial));
  }, [open, initial]);
  const set = (p: Partial<Draft>) => setD((cur) => ({ ...cur, ...p }));
  const x = fromDraft(d);
  const err = x ? exclusionError(x) : 'Rule IDs are numbers, separated by commas';

  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="lg"
      icon={<ShieldOff />}
      title={title}
      description="Stops rules from firing where they are wrong — for example a CMS editor that posts HTML, or a webhook whose payload looks like SQL."
      onSubmit={() => x && !err && onSave(x)}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!!err} loading={saving}>
            {saveLabel}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field
          label="Path prefix"
          hint="Only requests under this path: whole segments (/api covers /api/x, not /api-admin), any case. Blank = the whole site."
          labelAction={
            d.path && d.path !== '/' ? (
              <button type="button" className="nh-link text-xs" onClick={() => set({ path: parentPath(d.path) })}>
                Widen to {parentPath(d.path)}
              </button>
            ) : undefined
          }
        >
          <Input mono value={d.path} onChange={(e) => set({ path: e.target.value })} placeholder="/admin/" />
        </Field>
        <Field label="Rule IDs" hint="Comma-separated. Blank with no categories = every rule.">
          <Input mono value={d.rules} onChange={(e) => set({ rules: e.target.value })} placeholder="942100, 941110" />
        </Field>
        <Field label="Categories">
          <div className="grid grid-cols-2 gap-x-4 gap-y-1">
            {WAF_CATEGORIES.map((c) => (
              <Checkbox
                key={c.value}
                checked={d.categories.includes(c.value)}
                onChange={(v) => set({ categories: v ? [...d.categories, c.value] : d.categories.filter((k) => k !== c.value) })}
                label={c.label}
              />
            ))}
          </div>
        </Field>
        <div>
          <p className="mb-1.5 text-xs font-medium text-zinc-700 dark:text-zinc-300">Only for these (optional)</p>
          <Grid cols={3}>
            <Field label="Arguments" hint="Form fields, query string, JSON keys (post.body). A trailing * matches a prefix.">
              <Input mono value={d.args} onChange={(e) => set({ args: e.target.value })} placeholder="content" />
            </Field>
            <Field label="Cookies">
              <Input mono value={d.cookies} onChange={(e) => set({ cookies: e.target.value })} placeholder="prefs" />
            </Field>
            <Field label="Headers">
              <Input mono value={d.headers} onChange={(e) => set({ headers: e.target.value })} placeholder="X-Template" />
            </Field>
          </Grid>
        </div>
        <Field label="Comment">
          <Textarea rows={2} value={d.comment} onChange={(e) => set({ comment: e.target.value })} placeholder="Why this exclusion exists" />
        </Field>
        {x && !err && (
          <Callout tone={x.ruleIds || x.categories || x.args || x.cookies || x.headers ? 'info' : 'warning'}>{describeExclusion(x)}.</Callout>
        )}
        {err && <p className="text-xs text-red-600 dark:text-red-400">{err}</p>}
      </div>
    </Dialog>
  );
}
