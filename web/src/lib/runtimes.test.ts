import { describe, expect, it } from 'vitest';
import type { NodeConfig, RuntimeReport } from '@/api/types';
import {
  DENO_WEB_PERMISSIONS,
  RUNTIMES,
  agentMetrics,
  defaultInstallCommand,
  defaultWatchIgnore,
  entryError,
  missingRuntime,
  pythonMode,
  runtimeOf,
  startCommand,
  switchRuntime,
  taskCommand,
  versionOptions,
} from './runtimes';
import { defaultTask, newSite } from './siteDefaults';

const node = (n: Partial<NodeConfig>): NodeConfig => ({ ...newSite('node').node!, ...n });

describe('runtimeOf', () => {
  it('treats sites saved before runtimes as Node.js', () => {
    expect(runtimeOf({})).toBe('node');
    expect(runtimeOf(null)).toBe('node');
    expect(runtimeOf({ runtime: 'python' })).toBe('python');
    expect(runtimeOf({ runtime: 'cobol' })).toBe('node');
  });
  it('lists the runtimes in the server order', () => {
    expect(RUNTIMES.map((r) => r.value)).toEqual(['node', 'bun', 'deno', 'python', 'dotnet', 'custom']);
  });
});

describe('defaults mirror the server', () => {
  it('has the model install commands and watch folders', () => {
    expect(defaultInstallCommand('node')).toBe('npm ci --omit=dev');
    expect(defaultInstallCommand(undefined)).toBe('npm ci --omit=dev');
    expect(defaultInstallCommand('bun')).toBe('bun install --production');
    expect(defaultInstallCommand('deno')).toBe('deno install');
    expect(defaultInstallCommand('python')).toBe('python -m pip install -r requirements.txt');
    expect(defaultInstallCommand('dotnet')).toBe('');
    expect(defaultWatchIgnore('python')).toContain('.venv');
    expect(defaultWatchIgnore('node')).toEqual(['node_modules', '.git', 'logs']);
  });
});

describe('switchRuntime', () => {
  it('moves defaults along and keeps what was changed', () => {
    const s = newSite('node');
    switchRuntime(s, 'python');
    expect(s.node!.runtime).toBe('python');
    expect(s.deploy.installCommand).toBe('python -m pip install -r requirements.txt');
    expect(s.node!.watchIgnore).toContain('__pycache__');
    expect(s.node!.python?.venv).toBe('.venv');
    expect(s.node!.script).toBe(''); // server.js was only an example

    s.deploy.installCommand = 'pip install -r requirements/prod.txt';
    s.node!.runtimeVersion = '3.12';
    switchRuntime(s, 'deno');
    expect(s.deploy.installCommand).toBe('pip install -r requirements/prod.txt');
    expect(s.node!.runtimeVersion).toBe('');
    expect(s.node!.python).toBeNull();
    expect(s.node!.script).toBe('main.ts');
    expect(s.node!.nodeArgs).toEqual(DENO_WEB_PERMISSIONS);

    switchRuntime(s, 'bun');
    expect(s.node!.nodeArgs).toEqual([]);
    expect(s.node!.script).toBe('index.ts');

    // Another runtime's arguments would stop the new one from starting.
    s.node!.nodeArgs = ['--smol'];
    switchRuntime(s, 'node');
    expect(s.node!.nodeArgs).toEqual([]);
  });
  it('clears package-script tasks the new runtime cannot run', () => {
    const s = newSite('node');
    s.tasks = [{ ...defaultTask(), id: 'a', name: 'report', script: '', npmScript: 'report' }];
    switchRuntime(s, 'deno');
    expect(s.tasks![0].npmScript).toBe('report');
    switchRuntime(s, 'python');
    expect(s.tasks![0].npmScript).toBe('');
  });
  it('drops a package script the runtime cannot run', () => {
    const s = newSite('worker');
    s.node!.npmScript = 'start';
    s.node!.script = '';
    switchRuntime(s, 'bun');
    expect(s.node!.npmScript).toBe('start');
    switchRuntime(s, 'dotnet');
    expect(s.node!.npmScript).toBe('');
  });
});

describe('startCommand', () => {
  it('matches model.NodeConfig.StartText', () => {
    expect(startCommand(node({ script: 'server.js' }))).toBe('node server.js');
    expect(startCommand(node({ npmScript: 'start' }))).toBe('npm run start');
    expect(startCommand(node({ runtime: 'bun', npmScript: 'dev' }))).toBe('bun run dev');
    expect(startCommand(node({ runtime: 'deno', script: 'main.ts' }))).toBe('deno run main.ts');
    expect(startCommand(node({ runtime: 'deno', npmScript: 'start', script: '' }))).toBe('deno task start');
    expect(startCommand(node({ runtime: 'python', script: '', python: { server: 'uvicorn', app: 'main:app' } }))).toBe('python -m uvicorn main:app');
    expect(startCommand(node({ runtime: 'python', script: '', python: { module: 'worker' } }))).toBe('python -m worker');
    expect(startCommand(node({ runtime: 'dotnet', script: 'publish/Shop.dll' }))).toBe('dotnet publish/Shop.dll');
    expect(startCommand(node({ runtime: 'dotnet', script: 'Shop.exe' }))).toBe('Shop.exe');
    expect(startCommand(node({ runtime: 'custom', script: 'php-cgi.exe' }))).toBe('php-cgi.exe');
  });
  it('runs tasks with the site runtime, never its server', () => {
    const n = node({ runtime: 'python', script: '', python: { server: 'uvicorn', app: 'main:app' } });
    expect(taskCommand(n, { script: 'cleanup.py' })).toBe('python cleanup.py');
    expect(taskCommand(node({ runtime: 'bun' }), { npmScript: 'cron' })).toBe('bun run cron');
    expect(taskCommand(undefined, { script: 'x.js' })).toBe('node x.js');
  });
});

describe('entryError and pythonMode', () => {
  it('checks each runtime entry', () => {
    expect(entryError({ type: 'node', node: node({ script: '', npmScript: '' }) })).toMatch(/npm script/);
    expect(entryError({ type: 'node', node: node({ runtime: 'deno', script: '', npmScript: '' }) })).toMatch(/Deno task/);
    expect(entryError({ type: 'node', node: node({ runtime: 'dotnet', script: '' }) })).toMatch(/application/);
    expect(entryError({ type: 'node', node: node({ runtime: 'dotnet', script: 'Shop.dll' }) })).toBeNull();
    const server = node({ runtime: 'python', script: '', python: { server: 'uvicorn', app: 'main' } });
    expect(pythonMode(server)).toBe('server');
    expect(entryError({ type: 'node', node: server })).toMatch(/module:attribute/);
    server.python!.app = 'main:app';
    expect(entryError({ type: 'node', node: server })).toBeNull();
    expect(entryError({ type: 'worker', node: server })).toMatch(/worker/);
    expect(pythonMode(node({ runtime: 'python', script: '', python: { module: 'jobs' } }))).toBe('module');
  });
});

describe('agentMetrics', () => {
  it('is Node.js and Bun entry scripts with the agent on', () => {
    expect(agentMetrics(node({ agentEnabled: true }))).toBe(true);
    expect(agentMetrics(node({ agentEnabled: false }))).toBe(false);
    expect(agentMetrics(node({ runtime: 'bun', agentEnabled: true }))).toBe(true);
    expect(agentMetrics(node({ runtime: 'bun', agentEnabled: true, npmScript: 'start' }))).toBe(false);
    expect(agentMetrics(node({ runtime: 'python', agentEnabled: true }))).toBe(false);
  });
});

const report: RuntimeReport = {
  bun: {
    system: { version: '1.1.30', path: 'C:\\bun\\bun.exe' },
    installed: [
      { version: '1.2.0', path: 'x', status: 'installed', progress: 100, isDefault: true },
      { version: '1.3.0', path: 'y', status: 'installing', progress: 20, isDefault: false },
    ],
  },
  deno: { system: null, installed: [] },
  python: [
    { version: '3.13.1', path: 'C:\\Python313\\python.exe', source: 'py', isDefault: true },
    { version: '3.12.8', path: 'C:\\Python312\\python.exe', source: 'py', isDefault: false },
  ],
  dotnet: null,
  defaults: { bun: '1.2.0' },
};

describe('versionOptions and missingRuntime', () => {
  it('offers what the server has', () => {
    expect(versionOptions('bun', report, '')).toEqual([
      { value: '', label: 'Server default (1.2.0)' },
      { value: '1.2.0', label: '1.2.0 (default)' },
    ]);
    const py = versionOptions('python', report, '3.11');
    expect(py[0].label).toBe('Server default (Python 3.13.1)');
    expect(py.map((o) => o.value)).toContain('3.12');
    expect(py.map((o) => o.value)).toContain('C:\\Python312\\python.exe');
    expect(py[py.length - 1]).toEqual({ value: '3.11', label: '3.11 (not found)' });
    expect(versionOptions('deno', report, '')[0].label).toBe('Server default (none installed)');
  });
  it('says what is missing', () => {
    expect(missingRuntime('bun', report)).toBeNull();
    expect(missingRuntime('deno', report)).toMatch(/Runtimes page/);
    expect(missingRuntime('python', report)).toBeNull();
    expect(missingRuntime('dotnet', report)).toMatch(/Hosting Bundle/);
    expect(missingRuntime('node', report)).toBeNull();
    expect(missingRuntime('deno', undefined)).toBeNull();
  });
});
