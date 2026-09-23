import { useQuery } from '@tanstack/react-query';
import { Plus, Trash2 } from 'lucide-react';
import { nodeApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { HealthCheck, NodeConfig, Upstream } from '@/api/types';
import { Field } from '@/components/Field';
import { Input, NumberInput, Select } from '@/components/Input';
import { Checkbox, Radio, Switch } from '@/components/Switch';
import { FormSection, Grid, Callout } from '@/components/Layout';
import { ListEditor } from '@/components/ListEditor';
import { SecretInput } from '@/components/SecretInput';
import { Button, IconButton } from '@/components/Button';
import { HHMM_RE, LB_STRATEGIES, REDIRECT_CODES } from '@/lib/siteDefaults';
import { ArgsInput } from './ArgsInput';
import type { SiteEditorProps } from './types';

// ---------------------------------------------------------------- node

export function useNodeVersionOptions(current?: string) {
  const q = useQuery({ queryKey: qk.nodeVersions, queryFn: nodeApi.versions, staleTime: 30_000 });
  const installed = (q.data?.installed ?? []).filter((v) => v.status === 'installed');
  const def = installed.find((v) => v.isDefault);
  const defLabel = def ? `v${def.version.replace(/^v/, '')}` : q.data?.system ? `system ${q.data.system.version}` : 'node on PATH';
  const options = [
    { value: '', label: `Server default (${defLabel})` },
    ...installed.map((v) => ({ value: v.version, label: `v${v.version.replace(/^v/, '')}${v.isDefault ? ' (default)' : ''}` })),
  ];
  if (current && !options.some((o) => o.value === current)) options.push({ value: current, label: `${current} (not installed)` });
  return { options, loading: q.isPending };
}

/** App root, entry point, node version and instances — used by the wizard and the settings tab. */
export function NodeEssentials({ site, update, withInstances = true }: SiteEditorProps & { withInstances?: boolean }) {
  const n = site.node!;
  const { options } = useNodeVersionOptions(n.nodeVersion);
  const mode: 'script' | 'npm' = n.npmScript ? 'npm' : 'script';
  const set = (patch: Partial<NodeConfig>) =>
    update((d) => {
      d.node = { ...d.node!, ...patch };
    });

  return (
    <div className="space-y-4">
      <Field
        label="Application path"
        path="node.appRoot"
        required={!site.activeRelease}
        hint={
          site.activeRelease
            ? 'A deployment is active: a relative path is resolved inside the release; an absolute path is ignored.'
            : 'Physical folder containing package.json, e.g. D:\\apps\\my-api. Once a deployment is active, relative paths resolve inside the release.'
        }
      >
        <Input mono value={n.appRoot} placeholder="C:\inetpub\apps\my-app" onChange={(e) => set({ appRoot: e.target.value })} />
      </Field>
      <Field label="Start with">
        <Radio
          value={mode}
          onChange={(m) => set(m === 'npm' ? { npmScript: n.npmScript || 'start', script: '' } : { npmScript: '', script: n.script || 'server.js' })}
          options={[
            { value: 'script', label: 'Entry script', description: 'node <file>' },
            { value: 'npm', label: 'npm script', description: 'npm run <script>' },
          ]}
        />
      </Field>
      <Grid>
        {mode === 'script' ? (
          <Field label="Entry script" path="node.script" hint="Relative to the application path.">
            <Input mono value={n.script ?? ''} placeholder="server.js" onChange={(e) => set({ script: e.target.value })} />
          </Field>
        ) : (
          <Field label="npm script" path="node.npmScript" hint="A script from package.json.">
            <Input mono value={n.npmScript ?? ''} placeholder="start" onChange={(e) => set({ npmScript: e.target.value })} />
          </Field>
        )}
        <Field label="Node.js version" path="node.nodeVersion">
          <Select value={n.nodeVersion ?? ''} onChange={(v) => set({ nodeVersion: v })} options={options} mono />
        </Field>
      </Grid>
      {withInstances && (
        <Field label="Instances" path="node.instances" hint="Processes load-balanced behind the site (1–64). Each gets its own PORT.">
          <NumberInput className="w-32" min={1} max={64} value={n.instances} onChange={(v) => set({ instances: v })} />
        </Field>
      )}
    </div>
  );
}

export function NodeAdvanced({ site, update }: SiteEditorProps) {
  const n = site.node!;
  const set = (patch: Partial<NodeConfig>) =>
    update((d) => {
      d.node = { ...d.node!, ...patch };
    });
  const setRecycle = (patch: Partial<NodeConfig['recycle']>) => set({ recycle: { ...n.recycle, ...patch } });
  const setLimits = (patch: Partial<NodeConfig['limits']>) => set({ limits: { ...n.limits, ...patch } });
  const setRunAs = (patch: Partial<NodeConfig['runAs']>) => set({ runAs: { ...n.runAs, ...patch } });

  return (
    <>
      <FormSection title="Arguments" description="Extra arguments for your script and for the node executable.">
        <Field label="Script arguments" path="node.args" prefix>
          <ArgsInput value={n.args} onChange={(v) => set({ args: v })} placeholder="--port-from-env" />
        </Field>
        <Field label="Node arguments" path="node.nodeArgs" prefix hint="e.g. --max-old-space-size=512 --enable-source-maps">
          <ArgsInput value={n.nodeArgs} onChange={(v) => set({ nodeArgs: v })} placeholder="--max-old-space-size=512" />
        </Field>
      </FormSection>

      <FormSection title="Processes" description="How many processes run and how they receive their port.">
        <Grid>
          <Field label="Instances" path="node.instances" hint="1–64. A fixed port allows only one instance.">
            <NumberInput min={1} max={64} value={n.instances} onChange={(v) => set({ instances: v })} />
          </Field>
          <Field label="Port" path="node.portMode">
            <Select
              value={n.portMode}
              onChange={(v) => set({ portMode: v, fixedPort: v === 'fixed' ? n.fixedPort || 3000 : 0, instances: v === 'fixed' ? 1 : n.instances })}
              options={[
                { value: 'auto', label: 'Automatic (PORT env var)' },
                { value: 'fixed', label: 'Fixed port' },
              ]}
            />
          </Field>
        </Grid>
        {n.portMode === 'fixed' && (
          <Field label="Fixed port" path="node.fixedPort" hint="The port your app listens on. The proxy forwards to it.">
            <NumberInput className="w-32" mono min={1} max={65535} value={n.fixedPort} onChange={(v) => set({ fixedPort: v })} />
          </Field>
        )}
        <Switch
          checked={n.agentEnabled}
          onChange={(v) => set({ agentEnabled: v })}
          label="NodeHoster agent"
          description="Injects a small preload module for graceful shutdown and heap / event-loop metrics."
        />
      </FormSection>

      <FormSection title="Restarts" description="Crashed instances restart with a growing delay. Rapid-fail protection pauses a site that keeps crashing, like an IIS application pool.">
        <Field label="Restart policy" path="node.restartPolicy">
          <Select
            className="w-64"
            value={n.restartPolicy}
            onChange={(v) => set({ restartPolicy: v })}
            options={[
              { value: 'always', label: 'Always restart' },
              { value: 'on-failure', label: 'Restart on failure (non-zero exit)' },
              { value: 'never', label: 'Never restart' },
            ]}
          />
        </Field>
        <Grid>
          <Field label="Max restarts" path="node.maxRestarts" hint="0 = unlimited">
            <NumberInput min={0} value={n.maxRestarts} onChange={(v) => set({ maxRestarts: v })} />
          </Field>
          <Field label="Within (seconds)" path="node.restartWindowSec">
            <NumberInput min={1} value={n.restartWindowSec} onChange={(v) => set({ restartWindowSec: v })} suffix="sec" />
          </Field>
          <Field label="When rapid-fail protection trips" path="node.rapidFailAction">
            <Select
              value={n.rapidFailAction}
              onChange={(v) => set({ rapidFailAction: v })}
              options={[
                { value: 'recover', label: 'Restart automatically after a pause' },
                { value: 'stop', label: 'Stop until started manually' },
              ]}
            />
          </Field>
          {n.rapidFailAction !== 'stop' && (
            <Field label="Pause before restarting" path="node.recoverAfterSec" hint="Doubles each time it trips again, up to an hour.">
              <NumberInput min={10} max={86400} value={n.recoverAfterSec} onChange={(v) => set({ recoverAfterSec: v })} suffix="sec" />
            </Field>
          )}
          <Field label="Startup timeout" path="node.startupTimeoutSec" hint="Time allowed to start listening.">
            <NumberInput min={1} value={n.startupTimeoutSec} onChange={(v) => set({ startupTimeoutSec: v })} suffix="sec" />
          </Field>
          <Field label="Shutdown timeout" path="node.shutdownTimeoutSec" hint="Graceful stop before the process is killed.">
            <NumberInput min={1} value={n.shutdownTimeoutSec} onChange={(v) => set({ shutdownTimeoutSec: v })} suffix="sec" />
          </Field>
        </Grid>
      </FormSection>

      <FormSection title="File watching" description="Restart automatically when application files change. Useful on staging; avoid in production.">
        <Switch checked={n.watchFiles} onChange={(v) => set({ watchFiles: v })} label="Restart on file changes" />
        {n.watchFiles && (
          <Field label="Ignore" path="node.watchIgnore" prefix>
            <ListEditor values={n.watchIgnore} onChange={(v) => set({ watchIgnore: v })} placeholder="node_modules" />
          </Field>
        )}
      </FormSection>

      <FormSection title="Health check" description="Instances failing the check are taken out of rotation and restarted.">
        <HealthCheckFields value={n.healthCheck} onChange={(h) => set({ healthCheck: h })} pathPrefix="node.healthCheck" />
      </FormSection>

      <FormSection title="Recycling" description="Periodically replace processes with fresh ones using a zero-downtime rolling restart. Leave blank to disable a condition.">
        <Grid>
          <Field label="Memory limit" path="node.recycle.memoryLimitMB" hint="Recycle when private memory exceeds this.">
            <NumberInput blankZero min={0} value={n.recycle.memoryLimitMB} onChange={(v) => setRecycle({ memoryLimitMB: v })} suffix="MB" />
          </Field>
          <Field label="Regular interval" path="node.recycle.periodicMinutes">
            <NumberInput blankZero min={0} value={n.recycle.periodicMinutes} onChange={(v) => setRecycle({ periodicMinutes: v })} suffix="min" />
          </Field>
          <Field label="Request limit" path="node.recycle.maxRequests" hint="Recycle after this many requests per instance.">
            <NumberInput blankZero min={0} value={n.recycle.maxRequests} onChange={(v) => setRecycle({ maxRequests: v })} />
          </Field>
        </Grid>
        <Field label="Specific times" path="node.recycle.scheduleTimes" prefix hint="Local server time, 24h HH:MM.">
          <ListEditor
            values={n.recycle.scheduleTimes}
            onChange={(v) => setRecycle({ scheduleTimes: v })}
            placeholder="03:00"
            path="node.recycle.scheduleTimes"
            validate={(v) => (HHMM_RE.test(v) ? null : 'Use HH:MM (24h)')}
          />
        </Field>
      </FormSection>

      <FormSection title="Resource limits" description="Hard caps enforced by a Windows Job Object on the whole process tree. Exceeding the memory limit kills the process.">
        <Grid>
          <Field label="CPU limit" path="node.limits.cpuPercent" hint="1–100% of total CPU. Blank = unlimited.">
            <NumberInput blankZero min={0} max={100} value={n.limits.cpuPercent} onChange={(v) => setLimits({ cpuPercent: v })} suffix="%" />
          </Field>
          <Field label="Memory hard limit" path="node.limits.memoryLimitMB" hint="Blank = unlimited.">
            <NumberInput blankZero min={0} value={n.limits.memoryLimitMB} onChange={(v) => setLimits({ memoryLimitMB: v })} suffix="MB" />
          </Field>
        </Grid>
      </FormSection>

      <FormSection title="Run as" description="The Windows account the processes run under — the equivalent of an application pool identity. By default they run as the NodeHoster service account.">
        <Switch checked={n.runAs.enabled} onChange={(v) => setRunAs({ enabled: v })} label="Run as a specific user" />
        {n.runAs.enabled && (
          <Grid>
            <Field label="User name" path="node.runAs.username" hint="DOMAIN\user, .\user or user">
              <Input mono value={n.runAs.username ?? ''} onChange={(e) => setRunAs({ username: e.target.value })} placeholder="WEB01\svc-myapp" autoComplete="off" />
            </Field>
            <Field label="Password" path="node.runAs.password">
              <SecretInput value={n.runAs.password} onChange={(v) => setRunAs({ password: v })} />
            </Field>
          </Grid>
        )}
      </FormSection>
    </>
  );
}

export function HealthCheckFields({ value, onChange, pathPrefix }: { value: HealthCheck; onChange: (h: HealthCheck) => void; pathPrefix: string }) {
  const set = (p: Partial<HealthCheck>) => onChange({ ...value, ...p });
  return (
    <>
      <Switch checked={value.enabled} onChange={(v) => set({ enabled: v })} label="Enable health check" description="Periodic HTTP GET; a 2xx or 3xx response is healthy." />
      {value.enabled && (
        <Grid cols={4}>
          <Field label="Path" path={`${pathPrefix}.path`}>
            <Input mono value={value.path} onChange={(e) => set({ path: e.target.value })} placeholder="/healthz" />
          </Field>
          <Field label="Interval" path={`${pathPrefix}.intervalSec`}>
            <NumberInput min={1} value={value.intervalSec} onChange={(v) => set({ intervalSec: v })} suffix="sec" />
          </Field>
          <Field label="Timeout" path={`${pathPrefix}.timeoutSec`}>
            <NumberInput min={1} value={value.timeoutSec} onChange={(v) => set({ timeoutSec: v })} suffix="sec" />
          </Field>
          <Field label="Unhealthy after" path={`${pathPrefix}.unhealthyThreshold`}>
            <NumberInput min={1} value={value.unhealthyThreshold} onChange={(v) => set({ unhealthyThreshold: v })} suffix="fails" />
          </Field>
        </Grid>
      )}
    </>
  );
}

// ---------------------------------------------------------------- proxy

export function UpstreamsEditor({ site, update }: SiteEditorProps) {
  const p = site.proxy!;
  const list = p.upstreams ?? [];
  const setList = (u: Upstream[]) =>
    update((d) => {
      d.proxy = { ...d.proxy!, upstreams: u };
    });
  return (
    <Field label="Upstream servers" path="proxy.upstreams" hint="Requests are forwarded to these URLs. Weight applies to round robin.">
      <div className="space-y-2">
        {list.map((u, i) => (
          <div key={i} className="flex items-start gap-2">
            <Field path={`proxy.upstreams[${i}].url`} className="flex-1">
              <Input
                mono
                value={u.url}
                placeholder="http://127.0.0.1:8080"
                onChange={(e) => setList(list.map((x, j) => (j === i ? { ...x, url: e.target.value.trim() } : x)))}
              />
            </Field>
            <NumberInput
              className="w-24"
              min={0}
              blankZero
              placeholder="weight"
              title="Weight"
              value={u.weight}
              onChange={(v) => setList(list.map((x, j) => (j === i ? { ...x, weight: v } : x)))}
            />
            <IconButton label="Remove" size="md" variant="danger-ghost" icon={<Trash2 className="h-4 w-4" />} onClick={() => setList(list.filter((_, j) => j !== i))} />
          </div>
        ))}
        <Button size="sm" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setList([...list, { url: '', weight: 1 }])}>
          Add upstream
        </Button>
      </div>
    </Field>
  );
}

export function ProxyEssentials({ site, update }: SiteEditorProps) {
  const p = site.proxy!;
  return (
    <div className="space-y-4">
      <UpstreamsEditor site={site} update={update} />
      <Field label="Load balancing" path="proxy.loadBalancing">
        <Select
          className="w-64"
          value={p.loadBalancing}
          onChange={(v) =>
            update((d) => {
              d.proxy!.loadBalancing = v;
            })
          }
          options={LB_STRATEGIES}
        />
      </Field>
    </div>
  );
}

export function ProxyAdvanced({ site, update }: SiteEditorProps) {
  const p = site.proxy!;
  return (
    <>
      <FormSection title="Forwarding">
        <Switch
          checked={p.preserveHost}
          onChange={(v) =>
            update((d) => {
              d.proxy!.preserveHost = v;
            })
          }
          label="Preserve Host header"
          description="Send the client's Host header upstream instead of the upstream's host."
        />
        <Switch
          checked={p.insecureSkipVerify}
          onChange={(v) =>
            update((d) => {
              d.proxy!.insecureSkipVerify = v;
            })
          }
          label="Skip TLS verification"
          description="Accept self-signed or invalid certificates from https:// upstreams."
        />
      </FormSection>
      <FormSection title="Health check" description="Unhealthy upstreams are skipped by the load balancer.">
        <HealthCheckFields
          value={p.healthCheck}
          pathPrefix="proxy.healthCheck"
          onChange={(h) =>
            update((d) => {
              d.proxy!.healthCheck = h;
            })
          }
        />
      </FormSection>
    </>
  );
}

// ---------------------------------------------------------------- static

export function StaticEssentials({ site, update, full }: SiteEditorProps & { full?: boolean }) {
  const s = site.static!;
  const set = (patch: Partial<typeof s>) =>
    update((d) => {
      d.static = { ...d.static!, ...patch };
    });
  return (
    <div className="space-y-4">
      <Field label="Root directory" path="static.root" required={!site.activeRelease} hint={site.activeRelease ? 'A deployment is active: a relative path (e.g. dist) is resolved inside the release.' : 'Physical folder served at the site root. With deployments, use a path relative to the release, e.g. dist.'}>
        <Input mono value={s.root} placeholder="C:\inetpub\wwwroot\my-site" onChange={(e) => set({ root: e.target.value })} />
      </Field>
      <Switch
        checked={s.spaFallback}
        onChange={(v) => set({ spaFallback: v })}
        label="Single-page app fallback"
        description="Serve index.html for unknown paths so client-side routing works (React, Vue, Angular…)."
      />
      <Switch checked={s.directoryBrowsing} onChange={(v) => set({ directoryBrowsing: v })} label="Directory browsing" description="List folder contents when no index file exists." />
      {full && (
        <>
          <Field label="Default documents" path="static.indexFiles" prefix hint="Tried in order.">
            <ListEditor values={s.indexFiles} onChange={(v) => set({ indexFiles: v })} placeholder="index.html" />
          </Field>
          <Field label="Cache-Control" path="static.cacheControl" hint="Applied to every file, e.g. public, max-age=3600">
            <Input mono value={s.cacheControl ?? ''} onChange={(e) => set({ cacheControl: e.target.value })} placeholder="public, max-age=3600" />
          </Field>
        </>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- redirect

export function RedirectEssentials({ site, update }: SiteEditorProps) {
  const r = site.redirect!;
  const set = (patch: Partial<typeof r>) =>
    update((d) => {
      d.redirect = { ...d.redirect!, ...patch };
    });
  return (
    <div className="space-y-4">
      <Field label="Target URL" path="redirect.targetUrl" required>
        <Input mono value={r.targetUrl} placeholder="https://www.example.com" onChange={(e) => set({ targetUrl: e.target.value.trim() })} />
      </Field>
      <Field label="Status code" path="redirect.statusCode">
        <Select className="w-72" value={r.statusCode} onChange={(v) => set({ statusCode: Number(v) })} options={REDIRECT_CODES} />
      </Field>
      <Checkbox
        checked={r.preservePath}
        onChange={(v) => set({ preservePath: v })}
        label="Preserve path and query"
        description={
          <>
            <span className="font-mono">/docs?page=2</span> → <span className="font-mono">{(r.targetUrl || 'https://target').replace(/\/$/, '')}/docs?page=2</span>
          </>
        }
      />
      {r.statusCode === 301 || r.statusCode === 308 ? (
        <Callout tone="info">Permanent redirects are cached by browsers. Use 302/307 while testing.</Callout>
      ) : null}
    </div>
  );
}
