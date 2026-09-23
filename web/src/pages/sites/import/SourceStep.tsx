import { FileCode, Layers, MonitorCog, Server } from 'lucide-react';
import type { ImportSource } from '@/api/types';
import { Card, Callout } from '@/components/Layout';
import { Field } from '@/components/Field';
import { FileDrop } from '@/components/FileDrop';
import { Input, Textarea } from '@/components/Input';
import { Radio } from '@/components/Switch';
import { cn } from '@/lib/cn';

/** What the source step collects; kept by the page so going back keeps it. */
export interface SourceForm {
  source: ImportSource;
  mode: 'file' | 'text';
  file: File | null;
  text: string;
  /** web.config only: the site name and application folder, which the file does not say. */
  name: string;
  appRoot: string;
}

export const emptySourceForm = (): SourceForm => ({ source: 'iis', mode: 'file', file: null, text: '', name: '', appRoot: '' });

/** Whether the form has something to read. */
export function sourceReady(f: SourceForm): boolean {
  if (f.source === 'local-iis') return true;
  return f.mode === 'file' ? !!f.file : !!f.text.trim();
}

const SOURCES: { value: ImportSource; label: string; description: string; icon: typeof Server }[] = [
  {
    value: 'iis',
    label: 'IIS (applicationHost.config)',
    description: 'Sites, bindings, applications and virtual directories of an IIS server, with iisnode, URL Rewrite and ARR settings.',
    icon: Server,
  },
  {
    value: 'local-iis',
    label: "This server's IIS",
    description: 'Read the IIS configuration of the machine NodeHoster runs on (Windows with IIS installed).',
    icon: MonitorCog,
  },
  {
    value: 'webconfig',
    label: 'iisnode web.config',
    description: "One iisnode (or httpPlatformHandler) application's web.config: entry script, iisnode settings, environment and rewrite rules.",
    icon: FileCode,
  },
  {
    value: 'pm2',
    label: 'PM2',
    description: 'An ecosystem.config.js / .cjs / .json file, or the output of pm2 jlist. Cron apps can become scheduled tasks.',
    icon: Layers,
  },
];

const PLACEHOLDER: Record<Exclude<ImportSource, 'local-iis'>, string> = {
  iis: `<configuration>
  <system.applicationHost>
    <sites>
      <site name="Shop" id="2">
        <application path="/">
          <virtualDirectory path="/" physicalPath="C:\\inetpub\\shop" />
        </application>
        <bindings>
          <binding protocol="http" bindingInformation="*:80:shop.example.com" />
        </bindings>
      </site>
      …`,
  webconfig: `<configuration>
  <system.webServer>
    <handlers>
      <add name="iisnode" path="server.js" verb="*" modules="iisnode" />
    </handlers>
    …`,
  pm2: `module.exports = {
  apps: [
    { name: 'api', script: 'server.js', cwd: 'C:\\\\apps\\\\api', instances: 2,
      env: { NODE_ENV: 'production' } },
  ],
};`,
};

const ACCEPT: Record<Exclude<ImportSource, 'local-iis'>, string> = {
  iis: '.config,.xml',
  webconfig: '.config,.xml',
  pm2: '.js,.cjs,.mjs,.json,.txt',
};

export function SourceStep({ form, onChange }: { form: SourceForm; onChange: (f: SourceForm) => void }) {
  const set = (p: Partial<SourceForm>) => onChange({ ...form, ...p });
  const src = form.source;

  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2">
        {SOURCES.map((s) => {
          const Icon = s.icon;
          const active = src === s.value;
          return (
            <button
              key={s.value}
              type="button"
              onClick={() => set({ source: s.value })}
              className={cn(
                'nh-card flex items-start gap-3 p-4 text-left transition-colors',
                active ? 'border-accent-600 ring-2 ring-accent-500/20 dark:border-accent-500' : 'hover:border-zinc-300 dark:hover:border-zinc-700',
              )}
            >
              <span
                className={cn(
                  'flex h-9 w-9 shrink-0 items-center justify-center rounded-lg',
                  active ? 'bg-accent-600 text-white' : 'bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400',
                )}
              >
                <Icon className="h-5 w-5" />
              </span>
              <span className="min-w-0">
                <span className="block text-sm font-semibold">{s.label}</span>
                <span className="mt-0.5 block text-xs text-zinc-500 dark:text-zinc-400">{s.description}</span>
              </span>
            </button>
          );
        })}
      </div>

      {src === 'local-iis' ? (
        <Card title="This server's IIS">
          <p className="text-[13px] text-zinc-600 dark:text-zinc-400">
            NodeHoster reads <span className="font-mono text-[12.5px]">%windir%\System32\inetsrv\config\applicationHost.config</span> and the web.config
            files in the sites' folders. Nothing is changed in IIS, and nothing is created until you review and import.
          </p>
          <Callout tone="info" className="mt-3">
            IIS keeps listening on its sites' ports. Import the sites stopped, then stop them in IIS Manager (or run{' '}
            <span className="font-mono text-[12.5px]">iisreset /stop</span>) before starting them here.
          </Callout>
        </Card>
      ) : (
        <Card
          title={src === 'iis' ? 'applicationHost.config' : src === 'webconfig' ? 'web.config' : 'PM2 apps'}
          description={
            src === 'iis' ? (
              <>
                On the IIS server it is <span className="font-mono">%windir%\System32\inetsrv\config\applicationHost.config</span>. The sites' own web.config
                files (iisnode, URL Rewrite) are read from their folders when those exist on this server.
              </>
            ) : src === 'webconfig' ? (
              'The web.config in the root of the iisnode application.'
            ) : (
              <>
                An ecosystem file, or run <span className="font-mono">pm2 jlist &gt; apps.json</span> on the old server and upload apps.json (it includes the
                apps started from the command line).
              </>
            )
          }
        >
          <div className="space-y-4">
            <Radio
              value={form.mode}
              onChange={(mode) => set({ mode })}
              options={[
                { value: 'file', label: 'Upload a file' },
                { value: 'text', label: 'Paste the contents' },
              ]}
            />
            {form.mode === 'file' ? (
              <FileDrop
                file={form.file}
                onFile={(file) => set({ file })}
                accept={ACCEPT[src]}
                hint={src === 'pm2' ? '.js, .cjs, .json or pm2 jlist output, up to 8 MB' : '.config file, up to 8 MB'}
              />
            ) : (
              <Textarea mono rows={12} spellCheck={false} value={form.text} placeholder={PLACEHOLDER[src]} onChange={(e) => set({ text: e.target.value })} />
            )}
            {src === 'pm2' && (
              <Callout tone="info">
                The JavaScript is never run: only plain object literals are read (strings, numbers, arrays, objects). A file that computes values, such as{' '}
                <span className="font-mono text-[12.5px]">process.env.PORT</span> or <span className="font-mono text-[12.5px]">require()</span>, is refused;
                upload the output of <span className="font-mono text-[12.5px]">pm2 jlist</span> instead, which has the resolved values.
              </Callout>
            )}
            {src === 'webconfig' && (
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="Site name" hint="Default: iisnode-app. Can be changed on the next step.">
                  <Input value={form.name} maxLength={64} placeholder="my-app" onChange={(e) => set({ name: e.target.value })} />
                </Field>
                <Field label="Application folder" hint="Where the application's files are on this server. Can also be entered on the next step.">
                  <Input mono value={form.appRoot} placeholder="C:\sites\my-app" onChange={(e) => set({ appRoot: e.target.value })} />
                </Field>
              </div>
            )}
          </div>
        </Card>
      )}
    </div>
  );
}
