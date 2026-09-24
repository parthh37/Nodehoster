// Pure helpers for secret stores and references: defaults and checks that
// mirror the server's (model.SecretStore.ApplyDefaults / Validate and
// model.ValidateSecretRef), summaries for lists.

import type { EnvVar, SecretRef, SecretStore, SecretStoreStatus, SecretStoreType } from '@/api/types';

export const SECRET_REF_PREFIX = 'secretref:';
export const DEFAULT_CACHE_TTL_SEC = 300;
export const DEFAULT_INFISICAL_URL = 'https://app.infisical.com';

export const STORE_TYPES: { value: SecretStoreType; label: string; description: string }[] = [
  { value: 'vault', label: 'HashiCorp Vault / OpenBao', description: 'A KV secrets engine (version 1 or 2), with a token or AppRole.' },
  { value: 'infisical', label: 'Infisical', description: 'A project environment, cloud or self-hosted, with a machine identity (Universal Auth).' },
  {
    value: 'bitwarden',
    label: 'Bitwarden Secrets Manager',
    description: "A machine account's access token, US or EU cloud or a self-hosted Bitwarden server (not Vaultwarden).",
  },
];

export function storeTypeLabel(t: string): string {
  return STORE_TYPES.find((x) => x.value === t)?.label ?? t;
}

export function newStore(type: SecretStoreType): SecretStore {
  const base = { id: '', name: '', type, url: '', caCert: '', cacheTtlSec: DEFAULT_CACHE_TTL_SEC, watchIntervalSec: 0 };
  switch (type) {
    case 'vault':
      return { ...base, vault: { auth: 'token', token: '', roleId: '', secretId: '', authMount: 'approle', namespace: '', mount: 'secret', kvVersion: 2 } };
    case 'infisical':
      return { ...base, infisical: { clientId: '', clientSecret: '', projectId: '', environment: 'prod' } };
    case 'bitwarden':
      return { ...base, bitwarden: { accessToken: '', region: 'us', apiUrl: '', identityUrl: '' } };
  }
}

/** Missing sections and zero values filled the way the server does. */
export function normalizeSecretStores(list: SecretStore[] | null | undefined): SecretStore[] {
  return (list ?? []).map((s) => {
    const d = newStore(s.type);
    return {
      ...d,
      ...s,
      caCert: s.caCert ?? '',
      cacheTtlSec: s.cacheTtlSec || DEFAULT_CACHE_TTL_SEC,
      watchIntervalSec: s.watchIntervalSec ?? 0,
      vault: s.type === 'vault' ? { ...d.vault!, ...s.vault } : undefined,
      infisical: s.type === 'infisical' ? { ...d.infisical!, ...s.infisical } : undefined,
      bitwarden: s.type === 'bitwarden' ? { ...d.bitwarden!, ...s.bitwarden, region: s.bitwarden?.region ?? (s.url ? '' : 'us') } : undefined,
    };
  });
}

const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
const UUID_RE = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const SEGMENT_RE = /^[A-Za-z0-9_.-]+$/;

function urlProblem(u: string): string | null {
  try {
    const p = new URL(u);
    if (p.protocol !== 'http:' && p.protocol !== 'https:') return 'must be an http:// or https:// URL';
    return null;
  } catch {
    return 'not a URL';
  }
}

function mountOK(p: string): boolean {
  return !!p && p.split('/').every((s) => s !== '' && s !== '.' && s !== '..' && SEGMENT_RE.test(s));
}

/**
 * The first problem of a store as edited, like the server's validation.
 * Credentials only need to be present (SECRET counts: the saved one is kept).
 * `others` are the names of the other stores.
 */
export function storeProblem(s: SecretStore, others: string[] = []): { field: string; message: string } | null {
  const name = s.name.trim();
  if (!NAME_RE.test(name)) return { field: 'name', message: "1-64 characters: letters, digits, '.', '_' or '-'" };
  if (others.some((o) => o.toLowerCase() === name.toLowerCase())) return { field: 'name', message: 'Another store has this name' };
  if (s.url) {
    const p = urlProblem(s.url);
    if (p) return { field: 'url', message: p };
  }
  if (s.cacheTtlSec < 10 || s.cacheTtlSec > 86400) return { field: 'cacheTtlSec', message: 'Between 10 and 86400 seconds' };
  if (s.watchIntervalSec !== 0 && (s.watchIntervalSec < 60 || s.watchIntervalSec > 86400))
    return { field: 'watchIntervalSec', message: '0 (off) or between 60 and 86400 seconds' };
  if (s.caCert && !s.caCert.includes('-----BEGIN CERTIFICATE-----')) return { field: 'caCert', message: 'Paste PEM certificates (-----BEGIN CERTIFICATE-----)' };
  switch (s.type) {
    case 'vault': {
      const v = s.vault!;
      if (!s.url) return { field: 'url', message: 'Enter the Vault or OpenBao address' };
      if (v.auth === 'token' && !v.token) return { field: 'vault.token', message: 'Enter the token' };
      if (v.auth === 'approle') {
        if (!v.roleId?.trim()) return { field: 'vault.roleId', message: 'Enter the role ID' };
        if (!v.secretId) return { field: 'vault.secretId', message: 'Enter the secret ID' };
        if (!mountOK(v.authMount || 'approle')) return { field: 'vault.authMount', message: 'Not a valid mount path' };
      }
      if (!mountOK((v.mount || 'secret').replace(/^\/+|\/+$/g, ''))) return { field: 'vault.mount', message: 'Not a valid mount path' };
      if (v.namespace && !mountOK(v.namespace.replace(/^\/+|\/+$/g, ''))) return { field: 'vault.namespace', message: 'Not a valid namespace' };
      return null;
    }
    case 'infisical': {
      const i = s.infisical!;
      if (!i.clientId.trim()) return { field: 'infisical.clientId', message: "Enter the machine identity's client ID" };
      if (!i.clientSecret) return { field: 'infisical.clientSecret', message: 'Enter the client secret' };
      if (!i.projectId.trim()) return { field: 'infisical.projectId', message: 'Enter the project ID' };
      if (!i.environment.trim() || /[/?#& ]/.test(i.environment.trim())) return { field: 'infisical.environment', message: "Enter the environment's slug" };
      return null;
    }
    case 'bitwarden': {
      const b = s.bitwarden!;
      if (!b.accessToken) return { field: 'bitwarden.accessToken', message: "Enter the machine account's access token" };
      if (b.accessToken !== '__SECRET__' && !/^0\.[0-9a-fA-F-]{36}\.[^.:]+:[A-Za-z0-9+/]+={0,2}$/.test(b.accessToken.trim()))
        return { field: 'bitwarden.accessToken', message: 'An access token looks like 0.<id>.<secret>:<key>' };
      if (!s.url && b.region !== 'us' && b.region !== 'eu' && !(b.apiUrl && b.identityUrl))
        return { field: 'bitwarden.region', message: 'Choose the US or EU cloud, or enter the URL of a self-hosted server' };
      return null;
    }
  }
  return { field: 'type', message: 'Unknown store type' };
}

/** Mirror of model.ValidateSecretRef: why a reference cannot be right for a store type, or null. */
export function refProblem(type: SecretStoreType | string | undefined, ref: string): string | null {
  const r = ref.trim();
  if (!r) return 'Enter the secret to use';
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(r) || r.length > 512) return 'Not a valid reference';
  switch (type) {
    case 'vault': {
      const i = r.indexOf('#');
      if (i < 0 || i === r.length - 1) return 'Use <path>#<key>, e.g. app/prod#DB_PASSWORD';
      const path = r.slice(0, i);
      if (!path || path.startsWith('/') || path.endsWith('/')) return "The path is relative to the mount, without leading or trailing '/'";
      if (path.split('/').some((s) => !s || s === '.' || s === '..' || /[?#%\\]/.test(s))) return `"${path}" is not a valid secret path`;
      return null;
    }
    case 'infisical': {
      const i = r.lastIndexOf('/');
      const dir = i < 0 ? '/' : r.slice(0, i + 1);
      const name = i < 0 ? r : r.slice(i + 1);
      if (!dir.startsWith('/')) return "A folder starts with '/': /backend/DB_PASSWORD";
      if (!SEGMENT_RE.test(name)) return 'Use the secret name, optionally in a folder: DB_PASSWORD or /backend/DB_PASSWORD';
      if (dir.replace(/^\/+|\/+$/g, '').split('/').some((s) => s === '.' || s === '..' || /[?#%\\]/.test(s))) return `"${dir}" is not a valid folder`;
      return null;
    }
    case 'bitwarden':
      return UUID_RE.test(r) ? null : "Use the secret's ID (a UUID)";
    case undefined:
      return 'Choose a secret store';
  }
  return 'Unknown store type';
}

export function refPlaceholder(type: SecretStoreType | string | undefined): string {
  switch (type) {
    case 'vault':
      return 'app/prod#DB_PASSWORD';
    case 'infisical':
      return '/backend/DB_PASSWORD';
    case 'bitwarden':
      return '3b3f5c1e-8f8a-4a3e-9c1e-2b7f0a6d4c10';
  }
  return '';
}

export function refHint(type: SecretStoreType | string | undefined): string {
  switch (type) {
    case 'vault':
      return "The secret's path under the store's mount, # and the key.";
    case 'infisical':
      return "The secret's name, optionally in a folder (/backend/NAME).";
    case 'bitwarden':
      return "The secret's ID, shown in the Secrets Manager web app.";
  }
  return '';
}

/** secretref:<store>/<ref>, the text form NodeHoster Manager and the CLI show. */
export function formatRef(r: SecretRef): string {
  return `${SECRET_REF_PREFIX}${r.store}/${r.ref}`;
}

export function parseRefText(text: string): SecretRef | null {
  const t = text.trim();
  if (!t.startsWith(SECRET_REF_PREFIX)) return null;
  const rest = t.slice(SECRET_REF_PREFIX.length);
  const i = rest.indexOf('/');
  if (i < 0) return { store: rest.trim(), ref: '' };
  return { store: rest.slice(0, i).trim(), ref: rest.slice(i + 1).trim() };
}

/** Where an environment variable's value comes from. */
export type EnvSource = 'plain' | 'secret' | 'store';

export function envSource(e: EnvVar): EnvSource {
  if (e.from) return 'store';
  return e.secret ? 'secret' : 'plain';
}

/** Switches a variable's source, dropping what does not belong to it (a stored secret's value is not carried over). */
export function withSource(e: EnvVar, src: EnvSource, defaultStore = ''): EnvVar {
  const { from: _from, ...rest } = e;
  void _from;
  switch (src) {
    case 'store':
      return { ...rest, value: '', secret: false, from: e.from ?? { store: defaultStore, ref: '' } };
    case 'secret':
      return { ...rest, value: e.from ? '' : e.value, secret: true, from: undefined };
    case 'plain':
      return { ...rest, value: e.from || e.secret ? '' : e.value, secret: false, from: undefined };
  }
}

/** One line about a store for the list. */
export function storeSummary(s: SecretStore): string {
  switch (s.type) {
    case 'vault': {
      const v = s.vault;
      const parts = [s.url || '(no address)', `${v?.mount || 'secret'} (KV v${v?.kvVersion || 2})`];
      if (v?.namespace) parts.push(`namespace ${v.namespace}`);
      parts.push(v?.auth === 'approle' ? 'AppRole' : 'token');
      return parts.join(' · ');
    }
    case 'infisical':
      return `${s.url || DEFAULT_INFISICAL_URL} · project ${s.infisical?.projectId || '?'} · ${s.infisical?.environment || '?'}`;
    case 'bitwarden':
      if (s.url) return `${s.url} (self-hosted)`;
      if (s.bitwarden?.apiUrl) return s.bitwarden.apiUrl;
      return s.bitwarden?.region === 'eu' ? 'Bitwarden cloud (EU)' : 'Bitwarden cloud (US)';
  }
  return '';
}

/** A store's health for a badge: never used, working, or failing (its last error is newer than its last success). */
export function storeHealth(st: SecretStoreStatus | undefined): { tone: 'green' | 'red' | 'zinc'; label: string } {
  if (!st || (!st.lastSuccess && !st.lastError)) return { tone: 'zinc', label: 'Not used yet' };
  const ok = st.lastSuccess ? Date.parse(st.lastSuccess) : 0;
  const bad = st.lastErrorAt ? Date.parse(st.lastErrorAt) : 0;
  if (st.lastError && bad >= ok) return { tone: 'red', label: 'Failing' };
  return { tone: 'green', label: 'Working' };
}
