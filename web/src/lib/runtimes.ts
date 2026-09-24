// Runtimes of node and worker sites: mirrors of internal/model/runtimes.go
// (defaults, switching, the start command) and what the console shows for
// each runtime.

import type { NodeConfig, RuntimeName, RuntimeReport, Site } from '@/api/types';

export interface RuntimeInfo {
  value: RuntimeName;
  label: string;
  description: string;
  /** The entry field: label, placeholder, hint. */
  entryLabel: string;
  entryPlaceholder: string;
  entryHint: string;
  /** What npmScript is for this runtime, or null when it has none. */
  packageScript: { label: string; description: string; placeholder: string } | null;
  /** The runtime's own arguments (nodeArgs), or null when there are none. */
  runtimeArgs: { label: string; hint: string; placeholder: string } | null;
}

export const RUNTIMES: RuntimeInfo[] = [
  {
    value: 'node',
    label: 'Node.js',
    description: 'node <file> or npm run <script>',
    entryLabel: 'Entry script',
    entryPlaceholder: 'server.js',
    entryHint: 'Relative to the application path.',
    packageScript: { label: 'npm script', description: 'npm run <script>', placeholder: 'start' },
    runtimeArgs: { label: 'Node arguments', hint: 'e.g. --max-old-space-size=512 --enable-source-maps', placeholder: '--max-old-space-size=512' },
  },
  {
    value: 'bun',
    label: 'Bun',
    description: 'bun <file> or bun run <script>',
    entryLabel: 'Entry script',
    entryPlaceholder: 'index.ts',
    entryHint: 'Relative to the application path. Bun.serve listens on PORT by default.',
    packageScript: { label: 'package.json script', description: 'bun run <script>', placeholder: 'start' },
    runtimeArgs: { label: 'Bun arguments', hint: 'e.g. --smol', placeholder: '--smol' },
  },
  {
    value: 'deno',
    label: 'Deno',
    description: 'deno run <file> or deno task <name>',
    entryLabel: 'Entry script',
    entryPlaceholder: 'main.ts',
    entryHint: 'Relative to the application path. Listen on Deno.env.get("PORT").',
    packageScript: { label: 'Deno task', description: 'deno task <name>', placeholder: 'start' },
    runtimeArgs: {
      label: 'Permissions and flags',
      hint: 'Passed to deno run (a task sets its own in deno.json). Deno allows nothing unless granted.',
      placeholder: '--allow-net',
    },
  },
  {
    value: 'python',
    label: 'Python',
    description: 'A script, a module, or an ASGI/WSGI server',
    entryLabel: 'Script',
    entryPlaceholder: 'app.py',
    entryHint: 'Relative to the application path. Listen on os.environ["PORT"].',
    packageScript: null,
    runtimeArgs: { label: 'Interpreter options', hint: 'Before the script or module, e.g. -X utf8', placeholder: '-X' },
  },
  {
    value: 'dotnet',
    label: '.NET',
    description: 'ASP.NET Core / Kestrel: dotnet app.dll or app.exe',
    entryLabel: 'Application',
    entryPlaceholder: 'publish\\MyApp.dll',
    entryHint: 'The app\'s .dll (run by dotnet) or a self-contained .exe, relative to the application path. Kestrel gets ASPNETCORE_URLS.',
    packageScript: null,
    runtimeArgs: { label: 'Host options', hint: 'Options of dotnet before the .dll, e.g. --roll-forward Major', placeholder: '--roll-forward' },
  },
  {
    value: 'custom',
    label: 'Custom command',
    description: 'Any program that listens on PORT',
    entryLabel: 'Program',
    entryPlaceholder: 'C:\\tools\\server.exe',
    entryHint: 'A full path, a path relative to the application path, or a program on PATH. Its arguments are the script arguments.',
    packageScript: null,
    runtimeArgs: null,
  },
];

export const PYTHON_SERVERS = [
  { value: 'uvicorn', label: 'uvicorn (ASGI)', description: 'FastAPI, Starlette, Django ASGI' },
  { value: 'hypercorn', label: 'Hypercorn (ASGI)', description: 'Quart, FastAPI; HTTP/2' },
  { value: 'waitress', label: 'Waitress (WSGI)', description: 'Flask, Django; runs on Windows (gunicorn does not)' },
];

export const PYTHON_DOWNLOAD = 'https://www.python.org/downloads/windows/';
export const DOTNET_DOWNLOAD = 'https://dotnet.microsoft.com/download/dotnet';

/** A node config's runtime; old documents have none and are Node.js. */
export function runtimeOf(n: Pick<NodeConfig, 'runtime'> | null | undefined): RuntimeName {
  const r = n?.runtime;
  return (RUNTIMES.some((x) => x.value === r) ? r : 'node') as RuntimeName;
}

export function runtimeInfo(r: RuntimeName | string | undefined): RuntimeInfo {
  return RUNTIMES.find((x) => x.value === r) ?? RUNTIMES[0];
}

export function runtimeLabel(r: RuntimeName | string | undefined): string {
  return runtimeInfo(r).label;
}

/** Mirror of model.DefaultInstallCommand. */
export function defaultInstallCommand(r: RuntimeName | string | undefined): string {
  switch (r ?? 'node') {
    case 'node':
      return 'npm ci --omit=dev';
    case 'bun':
      return 'bun install --production';
    case 'deno':
      return 'deno install';
    case 'python':
      return 'python -m pip install -r requirements.txt';
  }
  return '';
}

/** Mirror of model.DefaultWatchIgnore. */
export function defaultWatchIgnore(r: RuntimeName | string | undefined): string[] {
  switch (r) {
    case 'python':
      return ['.venv', '__pycache__', '.git', 'logs'];
    case 'dotnet':
    case 'custom':
      return ['.git', 'logs'];
  }
  return ['node_modules', '.git', 'logs'];
}

const sameList = (a: string[] | undefined, b: string[]) => (a ?? []).length === b.length && b.every((v, i) => (a ?? [])[i] === v);

/**
 * Mirror of model.Site.SetRuntime: switch a node or worker site (in a
 * draft) to another runtime. Defaults that followed the old runtime follow
 * the new one; values an administrator changed stay. The version pin, the
 * runtime's own arguments, the Python settings and package scripts (the
 * site's and its tasks') belong to the old runtime; Deno starts with the
 * permissions a web app needs.
 */
export function switchRuntime(d: Site, to: RuntimeName): void {
  const n = d.node;
  if (!n) return;
  const from = runtimeOf(n);
  if (from === to) return;
  if ((d.deploy.installCommand ?? '') === defaultInstallCommand(from)) d.deploy.installCommand = defaultInstallCommand(to);
  if (sameList(n.watchIgnore, defaultWatchIgnore(from))) n.watchIgnore = defaultWatchIgnore(to);
  n.runtime = to;
  n.runtimeVersion = '';
  n.python = to === 'python' ? { module: '', server: '', app: '', venv: '.venv', ...n.python } : null;
  const info = runtimeInfo(to);
  n.nodeArgs = to === 'deno' ? [...DENO_WEB_PERMISSIONS] : [];
  if (!info.packageScript) {
    n.npmScript = '';
    d.tasks = (d.tasks ?? []).map((t) => ({ ...t, npmScript: '' }));
  }
  // The console's only: an example entry of the old runtime becomes the
  // new one's (Python, .NET and custom commands have no usual file name).
  if (!n.npmScript && (!n.script || n.script === runtimeInfo(from).entryPlaceholder)) {
    n.script = info.packageScript ? info.entryPlaceholder : '';
  }
}

/** Mirror of model.DenoWebPermissions: what a Deno web app usually needs (the network, its variables, its files). */
export const DENO_WEB_PERMISSIONS = ['--allow-net', '--allow-env', '--allow-read'];

/** Python's start mode: exactly one of a script, a module or a server. */
export type PythonMode = 'script' | 'module' | 'server';

export function pythonMode(n: NodeConfig): PythonMode {
  if (!n.script && n.python?.server) return 'server';
  if (!n.script && n.python?.module) return 'module';
  return 'script';
}

/** Mirror of model.NodeConfig.StartText: the command, for display. */
export function startCommand(n: NodeConfig, script = n.script ?? '', npmScript = n.npmScript ?? ''): string {
  const rt = runtimeOf(n);
  if (npmScript) {
    if (rt === 'bun') return `bun run ${npmScript}`;
    if (rt === 'deno') return `deno task ${npmScript}`;
    return `npm run ${npmScript}`;
  }
  switch (rt) {
    case 'deno':
      return `deno run ${script}`;
    case 'python': {
      const p = n.python;
      if (!script && p?.module) return `python -m ${p.module}`;
      if (!script && p?.server) return `python -m ${p.server} ${p.app ?? ''}`.trim();
      return `python ${script}`;
    }
    case 'dotnet':
      return script.toLowerCase().endsWith('.dll') ? `dotnet ${script}` : script;
    case 'custom':
      return script;
  }
  return `${rt} ${script}`;
}

/** A task's command in its site's runtime. */
export function taskCommand(n: NodeConfig | null | undefined, t: { script?: string; npmScript?: string }): string {
  return startCommand({ ...(n ?? { appRoot: '' }), python: null } as NodeConfig, t.script ?? '', t.npmScript ?? '');
}

/** Whether an agent can report heap and event-loop lag for the site's processes. */
export function agentMetrics(n: NodeConfig | null | undefined): boolean {
  const rt = runtimeOf(n);
  return !!n?.agentEnabled && (rt === 'node' || (rt === 'bun' && !n.npmScript));
}

const PY_MODULE_RE = /^[A-Za-z_]\w*(\.[A-Za-z_]\w*)*$/;
const PY_APP_RE = /^[A-Za-z_]\w*(\.[A-Za-z_]\w*)*:[A-Za-z_]\w*(\.[A-Za-z_]\w*)*$/;

/** Client-side check of the entry (the server checks again): an error message or null. */
export function entryError(s: Pick<Site, 'type' | 'node'>): string | null {
  const n = s.node;
  if (!n) return null;
  const rt = runtimeOf(n);
  const info = runtimeInfo(rt);
  switch (rt) {
    case 'python': {
      const mode = pythonMode(n);
      if (mode === 'script' && !n.script?.trim()) return 'Enter the Python script, or run a module or a server.';
      if (mode === 'module' && !PY_MODULE_RE.test(n.python?.module ?? '')) return 'Enter the module to run, e.g. myapp.worker.';
      if (mode === 'server') {
        if (s.type === 'worker') return 'A background worker serves no HTTP: run a script or a module.';
        if (!PY_APP_RE.test(n.python?.app ?? '')) return 'Enter the application as module:attribute, e.g. main:app.';
      }
      return null;
    }
    case 'dotnet':
    case 'custom':
      return n.script?.trim() ? null : `Enter the ${info.entryLabel.toLowerCase()}.`;
  }
  if (!n.script?.trim() && !n.npmScript?.trim()) return `Enter an entry script or a ${info.packageScript?.label ?? 'script'}.`;
  return null;
}

export interface VersionOption {
  value: string;
  label: string;
}

/**
 * The choices of a site's runtime version (runtimeVersion) from the
 * server's report: installed Bun/Deno versions, Python interpreters found,
 * the .NET host. The first choice is always the server default.
 */
export function versionOptions(rt: RuntimeName, report: RuntimeReport | undefined, current: string | undefined): VersionOption[] {
  const opts: VersionOption[] = [];
  const def = report?.defaults ?? {};
  switch (rt) {
    case 'bun':
    case 'deno': {
      const m = report?.[rt];
      const installed = (m?.installed ?? []).filter((i) => i.status === 'installed');
      const defVersion = def[rt];
      const defLabel = defVersion ? defVersion : m?.system ? `on PATH, ${m.system.version}` : 'none installed';
      opts.push({ value: '', label: `Server default (${defLabel})` });
      for (const i of installed) opts.push({ value: i.version, label: `${i.version}${i.isDefault ? ' (default)' : ''}` });
      break;
    }
    case 'python': {
      const list = report?.python ?? [];
      const d = list.find((p) => p.isDefault);
      opts.push({ value: '', label: `Server default (${d ? `Python ${d.version}` : 'none found'})` });
      const minors = new Set<string>();
      for (const p of list) {
        const minor = p.version.split('.').slice(0, 2).join('.');
        if (!minors.has(minor)) {
          minors.add(minor);
          opts.push({ value: minor, label: `Python ${minor} (newest found: ${p.version})` });
        }
      }
      for (const p of list) opts.push({ value: p.path, label: `${p.path} (${p.version})` });
      break;
    }
    case 'dotnet': {
      opts.push({ value: '', label: report?.dotnet ? `Found automatically (${report.dotnet.host})` : 'Found automatically (not installed)' });
      break;
    }
  }
  if (current && !opts.some((o) => o.value === current)) opts.push({ value: current, label: `${current} (not found)` });
  return opts;
}

/** A runtime the server cannot run yet, with what to do about it; null when it can. */
export function missingRuntime(rt: RuntimeName, report: RuntimeReport | undefined): string | null {
  if (!report) return null;
  switch (rt) {
    case 'bun':
    case 'deno': {
      const m = report[rt];
      const ok = m.system || (m.installed ?? []).some((i) => i.status === 'installed');
      return ok ? null : `${runtimeLabel(rt)} is not installed on this server. An administrator installs it on the Runtimes page.`;
    }
    case 'python':
      return (report.python ?? []).length ? null : 'Python was not found on this server. Install it for all users from python.org; NodeHoster does not install Python.';
    case 'dotnet':
      return report.dotnet ? null : '.NET was not found on this server. Install the ASP.NET Core Runtime (Hosting Bundle) from dotnet.microsoft.com, or deploy a self-contained .exe.';
  }
  return null;
}
