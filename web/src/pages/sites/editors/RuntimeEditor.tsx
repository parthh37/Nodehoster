import { useQuery } from '@tanstack/react-query';
import { runtimesApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import type { NodeConfig, PythonConfig, RuntimeName } from '@/api/types';
import { Field } from '@/components/Field';
import { Input, Select } from '@/components/Input';
import { Callout, Grid } from '@/components/Layout';
import { Radio } from '@/components/Switch';
import {
  PYTHON_SERVERS,
  RUNTIMES,
  missingRuntime,
  pythonMode,
  runtimeInfo,
  runtimeOf,
  switchRuntime,
  versionOptions,
  type PythonMode,
} from '@/lib/runtimes';
import type { SiteEditorProps } from './types';

/** The server's runtimes (installed Bun/Deno versions, Python and .NET found), for the pickers. */
export function useRuntimeReport() {
  return useQuery({ queryKey: qk.runtimes, queryFn: runtimesApi.report, staleTime: 30_000 });
}

/** The runtime picker of node and worker sites (wizard and settings). */
export function RuntimePicker({ site, update }: SiteEditorProps) {
  const rt = runtimeOf(site.node);
  return (
    <Field label="Runtime" path="node.runtime" hint="What runs the processes. Instances, restarts, recycling, limits and the run-as identity work the same for every runtime.">
      <Radio<RuntimeName>
        value={rt}
        onChange={(v) =>
          update((d) => {
            switchRuntime(d, v);
          })
        }
        options={RUNTIMES.map((r) => ({ value: r.value, label: r.label, description: r.description }))}
      />
    </Field>
  );
}

/**
 * How a site of a runtime other than Node.js starts: its entry (or package
 * script, Python module or server) and the runtime version or interpreter.
 * Node.js keeps its own fields in NodeEssentials.
 */
export function RuntimeStart({ site, update }: SiteEditorProps) {
  const n = site.node!;
  const rt = runtimeOf(n);
  const info = runtimeInfo(rt);
  const report = useRuntimeReport();
  const set = (patch: Partial<NodeConfig>) =>
    update((d) => {
      d.node = { ...d.node!, ...patch };
    });
  const setPython = (patch: Partial<PythonConfig>) => set({ python: { ...n.python, ...patch } });
  const missing = missingRuntime(rt, report.data);
  const pkgMode: 'script' | 'package' = n.npmScript ? 'package' : 'script';

  return (
    <div className="space-y-4">
      {missing && <Callout tone="warning">{missing}</Callout>}

      {info.packageScript && (
        <Field label="Start with">
          <Radio
            value={pkgMode}
            onChange={(m) => set(m === 'package' ? { npmScript: n.npmScript || 'start', script: '' } : { npmScript: '', script: n.script || info.entryPlaceholder })}
            options={[
              { value: 'script', label: info.entryLabel, description: `${rt === 'deno' ? 'deno run' : rt} <file>` },
              { value: 'package', label: info.packageScript.label, description: info.packageScript.description },
            ]}
          />
        </Field>
      )}

      {rt === 'python' && (
        <Field label="Start with">
          <Radio<PythonMode>
            value={pythonMode(n)}
            onChange={(m) =>
              update((d) => {
                const p = { venv: '.venv', ...d.node!.python };
                if (m === 'script') d.node = { ...d.node!, script: d.node!.script || 'app.py', python: { ...p, module: '', server: '', app: '' } };
                // A module name to start from: an empty one would read as script mode.
                if (m === 'module') d.node = { ...d.node!, script: '', python: { ...p, module: p.module || 'main', server: '', app: '' } };
                if (m === 'server') d.node = { ...d.node!, script: '', python: { ...p, module: '', server: p.server || 'uvicorn', app: p.app || 'main:app' } };
              })
            }
            options={[
              { value: 'script', label: 'Script', description: 'python <file>' },
              { value: 'module', label: 'Module', description: 'python -m <module>' },
              { value: 'server', label: 'ASGI / WSGI app', description: 'uvicorn, Hypercorn or Waitress' },
            ]}
          />
        </Field>
      )}

      <Grid>
        {rt === 'python' && pythonMode(n) === 'module' && (
          <Field label="Module" path="node.python.module" hint="Run as python -m, from the application path.">
            <Input mono value={n.python?.module ?? ''} placeholder="myapp.worker" onChange={(e) => setPython({ module: e.target.value })} />
          </Field>
        )}
        {rt === 'python' && pythonMode(n) === 'server' && (
          <>
            <Field label="Server" path="node.python.server" hint="Installed in the app's virtual environment (list it in requirements.txt).">
              <Select value={n.python?.server || 'uvicorn'} onChange={(v) => setPython({ server: v })} options={PYTHON_SERVERS.map((s) => ({ value: s.value, label: s.label }))} />
            </Field>
            <Field label="Application" path="node.python.app" hint="module:attribute. NodeHoster passes 127.0.0.1 and the instance's port.">
              <Input mono value={n.python?.app ?? ''} placeholder="main:app" onChange={(e) => setPython({ app: e.target.value })} />
            </Field>
          </>
        )}
        {(rt !== 'python' || pythonMode(n) === 'script') && pkgMode === 'script' && (
          <Field label={info.entryLabel} path="node.script" hint={info.entryHint}>
            <Input mono value={n.script ?? ''} placeholder={info.entryPlaceholder} onChange={(e) => set({ script: e.target.value })} />
          </Field>
        )}
        {info.packageScript && pkgMode === 'package' && (
          <Field label={info.packageScript.label} path="node.npmScript" hint={rt === 'deno' ? 'A task from deno.json.' : 'A script from package.json.'}>
            <Input mono value={n.npmScript ?? ''} placeholder={info.packageScript.placeholder} onChange={(e) => set({ npmScript: e.target.value })} />
          </Field>
        )}
        <RuntimeVersionField site={site} update={update} />
      </Grid>

      {rt === 'python' && (
        <Field
          label="Virtual environment"
          path="node.python.venv"
          hint="Relative to the application path. Deployments create it in each release and install requirements.txt into it; its python runs the app when it exists."
        >
          <Input mono className="max-w-xs" value={n.python?.venv ?? '.venv'} placeholder=".venv" onChange={(e) => setPython({ venv: e.target.value })} />
        </Field>
      )}
    </div>
  );
}

/** The version (Bun, Deno), interpreter (Python) or host (.NET) of a site; nothing for custom commands. */
function RuntimeVersionField({ site, update }: SiteEditorProps) {
  const n = site.node!;
  const rt = runtimeOf(n);
  const report = useRuntimeReport();
  if (rt === 'node' || rt === 'custom') return null;
  const options = versionOptions(rt, report.data, n.runtimeVersion);
  const label = rt === 'python' ? 'Python interpreter' : rt === 'dotnet' ? '.NET host' : `${runtimeInfo(rt).label} version`;
  const hint =
    rt === 'dotnet'
      ? 'dotnet.exe runs .dll apps; the app\'s runtimeconfig.json picks the runtime version.'
      : rt === 'python'
        ? 'A version picks the newest interpreter of it found on the server.'
        : 'Installed by an administrator on the Runtimes page.';
  return (
    <Field label={label} path="node.runtimeVersion" hint={hint}>
      <Select
        mono
        value={n.runtimeVersion ?? ''}
        onChange={(v) =>
          update((d) => {
            d.node = { ...d.node!, runtimeVersion: v };
          })
        }
        options={options}
      />
    </Field>
  );
}
