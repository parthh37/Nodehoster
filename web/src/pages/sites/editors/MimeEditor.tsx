import { Link } from 'react-router-dom';
import { FileType } from 'lucide-react';
import type { MimeMap } from '@/api/types';
import { Callout, Card } from '@/components/Layout';
import { Field } from '@/components/Field';
import { Input, Select } from '@/components/Input';
import { RowsEditor } from '@/components/ListEditor';
import type { SiteEditorProps } from './types';

// "." alone maps files without an extension, as in IIS.
const EXT_RE = /^\.([A-Za-z0-9][A-Za-z0-9._+-]{0,31})?$/;
const TYPE_RE = /^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*\/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*(\s*;\s*[A-Za-z0-9_-]+=[^;\s]+)*$/;

export function validateMimeExtension(v: string): string | null {
  if (!v) return null;
  if (!v.startsWith('.')) return 'Start with a dot, e.g. .webmanifest ("." for files without one)';
  return EXT_RE.test(v) ? null : 'Letters, digits and . _ + - only';
}

export function validateMimeType(v: string): string | null {
  if (!v.trim()) return null;
  return TYPE_RE.test(v.trim()) ? null : 'Enter a MIME type, e.g. application/json';
}

/** Extension → Content-Type rows. `path` is the array's error path, e.g. "mime.types". */
export function MimeMapsEditor({
  value,
  onChange,
  path,
  empty,
}: {
  value: MimeMap[] | null | undefined;
  onChange: (v: MimeMap[]) => void;
  path: string;
  empty?: string;
}) {
  return (
    <RowsEditor<MimeMap>
      items={value}
      onChange={onChange}
      path={path}
      addLabel="Add MIME type"
      create={() => ({ extension: '', type: '' })}
      empty={<p className="text-xs text-zinc-500">{empty ?? 'No custom MIME types.'}</p>}
      header={
        <div className="grid grid-cols-[10rem_1fr] gap-2 pr-8 text-2xs font-semibold uppercase tracking-wide text-zinc-500">
          <span>Extension</span>
          <span>Content type</span>
        </div>
      }
      render={(m, up, i) => (
        <div className="grid grid-cols-[10rem_1fr] gap-2">
          <Field path={`${path}[${i}].extension`} error={validateMimeExtension(m.extension)}>
            <Input
              mono
              value={m.extension}
              placeholder=".webmanifest"
              onChange={(e) => up({ extension: e.target.value.trim().toLowerCase() })}
              onBlur={() => m.extension && !m.extension.startsWith('.') && up({ extension: `.${m.extension}` })}
            />
          </Field>
          <Field path={`${path}[${i}].type`} error={validateMimeType(m.type)}>
            <Input mono value={m.type} placeholder="application/manifest+json" onChange={(e) => up({ type: e.target.value })} />
          </Field>
        </div>
      )}
    />
  );
}

export function MimeTypesCard({ site, update, readOnly }: SiteEditorProps) {
  const r = site.routing;
  const servesFiles = site.type === 'static' || (r.locations ?? []).some((l) => l.kind === 'static');
  return (
    <Card
      title={<span className="flex items-center gap-2"><FileType className="h-4 w-4 text-zinc-400" />MIME types</span>}
      description={
        <>
          Content types for files this site serves from disk: the static site and static-folder locations. These add to or override the{' '}
          {readOnly ? 'server MIME types' : <Link to="/settings/mime" className="nh-link">server MIME types</Link>} for this site only.
        </>
      }
    >
      <div className="space-y-4">
        {!servesFiles && (
          <Callout tone="info">
            This site does not serve files from disk, so these settings have no effect until you add a static-folder location. Applications and
            upstreams set their own Content-Type.
          </Callout>
        )}
        <MimeMapsEditor
          value={r.mimeTypes}
          path="routing.mimeTypes"
          onChange={(v) =>
            update((d) => {
              d.routing = { ...d.routing, mimeTypes: v };
            })
          }
        />
        <Field label="Unknown file extensions" path="routing.unknownMimeTypes" hint="Files whose extension has no MIME type in any table.">
          <Select
            className="sm:w-80"
            value={r.unknownMimeTypes ?? ''}
            onChange={(v) =>
              update((d) => {
                d.routing = { ...d.routing, unknownMimeTypes: v };
              })
            }
            options={[
              { value: '', label: 'Server default' },
              { value: 'serve', label: 'Serve as application/octet-stream' },
              { value: 'deny', label: 'Refuse with 404 Not Found' },
            ]}
          />
        </Field>
      </div>
    </Card>
  );
}
