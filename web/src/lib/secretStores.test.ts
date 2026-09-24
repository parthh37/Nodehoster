import { describe, expect, it } from 'vitest';
import type { SecretStore } from '@/api/types';
import {
  envSource,
  formatRef,
  newStore,
  normalizeSecretStores,
  parseRefText,
  refProblem,
  reservedEnvName,
  sameStoreServer,
  storeHealth,
  storeProblem,
  storeSummary,
  withSource,
} from './secretStores';
import { normalizeSettings } from './settingsDefaults';
import type { Settings } from '@/api/types';

const TOKEN = '0.ec2c1d46-6a4b-4751-a310-af9601317f2d.C2IgxjjLF7qSshsbwe8JGcbM075YXw:X8vbvA0bduihIDe/qrzIQQ==';

function vault(p: Partial<SecretStore> = {}): SecretStore {
  const s = newStore('vault');
  return { ...s, name: 'vault', url: 'https://vault.example.com:8200', vault: { ...s.vault!, token: 't' }, ...p };
}

describe('references', () => {
  it('formats and parses the text form', () => {
    const r = { store: 'vault', ref: 'app/prod#DB_PASSWORD' };
    expect(formatRef(r)).toBe('secretref:vault/app/prod#DB_PASSWORD');
    expect(parseRefText(` ${formatRef(r)} `)).toEqual(r);
    expect(parseRefText('secretref:bw')).toEqual({ store: 'bw', ref: '' });
    expect(parseRefText('postgres://x')).toBeNull();
  });

  it('checks references like the server', () => {
    expect(refProblem('vault', 'app/prod#DB_PASSWORD')).toBeNull();
    expect(refProblem('vault', 'app/prod')).toMatch(/#/);
    expect(refProblem('vault', '/app#K')).toMatch(/relative/);
    expect(refProblem('vault', 'a/../b#K')).toMatch(/not a valid/);
    expect(refProblem('infisical', 'DB_PASSWORD')).toBeNull();
    expect(refProblem('infisical', '/backend/DB_PASSWORD')).toBeNull();
    expect(refProblem('infisical', 'backend/DB_PASSWORD')).toMatch(/starts with/);
    expect(refProblem('infisical', '/backend/')).not.toBeNull();
    expect(refProblem('bitwarden', '3b3f5c1e-8f8a-4a3e-9c1e-2b7f0a6d4c10')).toBeNull();
    expect(refProblem('bitwarden', 'DB_PASSWORD')).toMatch(/UUID/);
    expect(refProblem('vault', '  ')).toMatch(/Enter/);
    expect(refProblem(undefined, 'x')).toMatch(/Choose/);
  });
});

describe('stores', () => {
  it('fills defaults from older or partial settings', () => {
    expect(normalizeSettings({ proxy: {} } as unknown as Settings).secretStores).toEqual([]);
    const [s] = normalizeSecretStores([{ id: '1', name: 'v', type: 'vault', url: 'https://v', cacheTtlSec: 0, watchIntervalSec: 0, vault: { auth: 'approle', mount: '', kvVersion: 1 } } as SecretStore]);
    expect(s.cacheTtlSec).toBe(300);
    expect(s.vault).toMatchObject({ auth: 'approle', kvVersion: 1, authMount: 'approle' });
    expect(s.infisical).toBeUndefined();
    const [b] = normalizeSecretStores([{ id: '2', name: 'b', type: 'bitwarden', url: '', cacheTtlSec: 60, watchIntervalSec: 0, bitwarden: { accessToken: '__SECRET__' } } as SecretStore]);
    expect(b.bitwarden?.region).toBe('us');
  });

  it('finds the first problem like the server', () => {
    expect(storeProblem(vault())).toBeNull();
    expect(storeProblem(vault({ name: 'has space' }))?.field).toBe('name');
    expect(storeProblem(vault(), ['VAULT'])?.message).toMatch(/Another/);
    expect(storeProblem(vault({ url: '' }))?.field).toBe('url');
    expect(storeProblem(vault({ url: 'ftp://x' }))?.field).toBe('url');
    expect(storeProblem(vault({ cacheTtlSec: 5 }))?.field).toBe('cacheTtlSec');
    expect(storeProblem(vault({ watchIntervalSec: 30 }))?.field).toBe('watchIntervalSec');
    expect(storeProblem(vault({ caCert: 'nope' }))?.field).toBe('caCert');
    const ar = vault();
    ar.vault = { ...ar.vault!, auth: 'approle', roleId: 'r', secretId: '' };
    expect(storeProblem(ar)?.field).toBe('vault.secretId');
    // A stored (masked) credential counts as set.
    ar.vault.secretId = '__SECRET__';
    expect(storeProblem(ar)).toBeNull();

    const inf = { ...newStore('infisical'), name: 'inf' };
    expect(storeProblem(inf)?.field).toBe('infisical.clientId');
    inf.infisical = { clientId: 'c', clientSecret: 's', projectId: 'p', environment: 'prod/x' };
    expect(storeProblem(inf)?.field).toBe('infisical.environment');

    const bw = { ...newStore('bitwarden'), name: 'bw' };
    bw.bitwarden = { ...bw.bitwarden!, accessToken: 'not a token' };
    expect(storeProblem(bw)?.field).toBe('bitwarden.accessToken');
    bw.bitwarden.accessToken = TOKEN;
    expect(storeProblem(bw)).toBeNull();
    bw.bitwarden.region = '';
    expect(storeProblem(bw)?.field).toBe('bitwarden.region');
    expect(storeProblem({ ...bw, url: 'https://bw.example.com' })).toBeNull();
  });

  it('wants https except on this machine', () => {
    expect(storeProblem(vault({ url: 'http://vault.example.com:8200' }))?.message).toMatch(/https/);
    expect(storeProblem(vault({ url: 'http://10.0.0.5:8200' }))?.field).toBe('url');
    expect(storeProblem(vault({ url: 'http://127.0.0.1:8200' }))).toBeNull();
    expect(storeProblem(vault({ url: 'http://localhost:8200' }))).toBeNull();
    expect(storeProblem(vault({ url: 'http://[::1]:8200' }))).toBeNull();
    const bw = { ...newStore('bitwarden'), name: 'bw' };
    bw.bitwarden = { ...bw.bitwarden!, accessToken: TOKEN, apiUrl: 'http://bw.example.com/api', identityUrl: 'https://bw.example.com/identity' };
    expect(storeProblem(bw)?.field).toBe('bitwarden.apiUrl');
  });

  it('asks for saved credentials again when the server changes', () => {
    const saved = vault({ id: 'v1', vault: { ...vault().vault!, token: '__SECRET__' } });
    expect(sameStoreServer(saved, { ...saved, url: 'https://VAULT.example.com:8200' })).toBe(true);
    expect(storeProblem({ ...saved, cacheTtlSec: 60 }, [], saved)).toBeNull();
    expect(storeProblem({ ...saved, url: 'https://evil.example.com' }, [], saved)?.field).toBe('vault.token');
    // Entered again: fine.
    expect(storeProblem({ ...saved, url: 'https://evil.example.com', vault: { ...saved.vault!, token: 'new' } }, [], saved)).toBeNull();
    const bw = { ...newStore('bitwarden'), id: 'b1', name: 'bw' };
    bw.bitwarden = { ...bw.bitwarden!, accessToken: '__SECRET__' };
    expect(storeProblem({ ...bw, bitwarden: { ...bw.bitwarden, region: 'eu' } }, [], bw)?.field).toBe('bitwarden.accessToken');
  });

  it('knows the variables NodeHoster sets', () => {
    for (const n of ['PORT', 'port', 'NODE_OPTIONS', 'ASPNETCORE_URLS', 'NODE_APP_INSTANCE', 'NODEHOSTER_AGENT_TOKEN']) expect(reservedEnvName(n)).toBe(true);
    expect(reservedEnvName('DATABASE_URL')).toBe(false);
  });

  it('summarizes stores', () => {
    expect(storeSummary(vault())).toBe('https://vault.example.com:8200 · secret (KV v2) · token');
    expect(storeSummary({ ...newStore('infisical'), infisical: { clientId: 'c', clientSecret: '', projectId: 'p1', environment: 'prod' } })).toBe(
      'https://app.infisical.com · project p1 · prod',
    );
    expect(storeSummary({ ...newStore('bitwarden'), bitwarden: { accessToken: '', region: 'eu' } })).toBe('Bitwarden cloud (EU)');
    expect(storeSummary({ ...newStore('bitwarden'), url: 'https://bw.example.com' })).toBe('https://bw.example.com (self-hosted)');
  });

  it('tells health from the last success and error', () => {
    expect(storeHealth(undefined).label).toBe('Not used yet');
    const base = { name: 'v', type: 'vault' as const, cached: 0, references: 0 };
    expect(storeHealth({ ...base, lastSuccess: '2026-09-01T10:00:00Z' }).tone).toBe('green');
    expect(storeHealth({ ...base, lastSuccess: '2026-09-01T10:00:00Z', lastError: 'down', lastErrorAt: '2026-09-01T11:00:00Z' }).tone).toBe('red');
    expect(storeHealth({ ...base, lastSuccess: '2026-09-01T12:00:00Z', lastError: 'down', lastErrorAt: '2026-09-01T11:00:00Z' }).tone).toBe('green');
  });
});

describe('variables', () => {
  it('switches a variable between plain, secret and a store', () => {
    const plain = { name: 'A', value: 'x' };
    expect(envSource(plain)).toBe('plain');
    const fromStore = withSource(plain, 'store', 'vault');
    expect(fromStore).toEqual({ name: 'A', value: '', secret: false, from: { store: 'vault', ref: '' } });
    expect(envSource(fromStore)).toBe('store');
    // Back to plain: nothing of the reference remains.
    const back = withSource(fromStore, 'plain');
    expect(back).toEqual({ name: 'A', value: '', secret: false });
    expect('from' in back && back.from !== undefined).toBe(false);
    // A stored secret's value is never carried to plain text.
    expect(withSource({ name: 'S', value: '__SECRET__', secret: true }, 'plain').value).toBe('');
    expect(envSource({ name: 'S', value: '', secret: true })).toBe('secret');
  });
});
