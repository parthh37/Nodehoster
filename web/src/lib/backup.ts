// Pure helpers for the Backups settings: defaults, summaries and checks
// that mirror the server's (model.BackupSettings.Validate).

import type { BackupDestination, BackupDestinationType, BackupRunStatus, BackupSettings } from '@/api/types';

export const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

export function defaultBackup(): BackupSettings {
  return {
    enabled: false,
    time: '02:30',
    weekdays: [],
    keepLast: 14,
    keepDays: 0,
    includeCertificates: true,
    includeShared: false,
    sharedSiteIds: [],
    passphrase: '',
    destinations: [],
  };
}

/** Null slices and missing sections become empty values so the form can bind. */
export function normalizeBackup(b: Partial<BackupSettings> | null | undefined): BackupSettings {
  const d = defaultBackup();
  if (!b) return d;
  return {
    ...d,
    ...b,
    time: b.time || d.time,
    weekdays: [...(b.weekdays ?? [])].sort((x, y) => x - y),
    sharedSiteIds: b.sharedSiteIds ?? [],
    passphrase: b.passphrase ?? '',
    destinations: (b.destinations ?? []).map((x) => ({ ...x })),
  };
}

export function newDestination(type: BackupDestinationType): BackupDestination {
  const base = { id: '', name: '', type, enabled: true };
  switch (type) {
    case 'folder':
      return { ...base, folder: { path: '' } };
    case 's3':
      return { ...base, s3: { endpoint: '', region: '', bucket: '', prefix: '', accessKeyId: '', secretAccessKey: '', pathStyle: false } };
    case 'azure':
      return { ...base, azure: { account: '', container: '', prefix: '', sasToken: '', accountKey: '', endpoint: '' } };
    case 'sftp':
      return { ...base, sftp: { host: '', port: 22, username: '', password: '', privateKey: '', passphrase: '', directory: '', hostKey: '' } };
  }
}

export const DESTINATION_TYPES: { value: BackupDestinationType; label: string; description: string }[] = [
  { value: 'folder', label: 'Folder or network share', description: 'A local disk or a UNC path such as \\\\nas\\backups.' },
  { value: 's3', label: 'S3-compatible storage', description: 'AWS S3, Cloudflare R2, Backblaze B2, MinIO, Wasabi…' },
  { value: 'azure', label: 'Azure Blob Storage', description: 'A container, with a SAS token or the account key.' },
  { value: 'sftp', label: 'SFTP', description: 'A directory on an SSH server, verified by its host key.' },
];

/** "Every day at 02:30", "Mon, Wed and Fri at 23:00". */
export function scheduleSummary(time: string, weekdays: number[] | null | undefined): string {
  const days = [...new Set(weekdays ?? [])].filter((d) => d >= 0 && d <= 6).sort((a, b) => a - b);
  if (days.length === 0 || days.length === 7) return `Every day at ${time}`;
  if (days.join() === '1,2,3,4,5') return `Weekdays at ${time}`;
  if (days.join() === '0,6') return `Weekends at ${time}`;
  const names = days.map((d) => WEEKDAYS[d]);
  const list = names.length === 1 ? names[0] : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
  return `${list} at ${time}`;
}

/** "keep the last 14", "keep 30 days", "keep the last 7 and anything from the last 30 days", "keep everything". */
export function retentionSummary(keepLast: number, keepDays: number): string {
  const last = keepLast > 0 ? `the last ${keepLast}` : '';
  const days = keepDays > 0 ? `${keepDays} day${keepDays === 1 ? '' : 's'}` : '';
  if (last && days) return `Keep ${last}, and every archive from the last ${days}`;
  if (last) return `Keep ${last}`;
  if (days) return `Keep ${days}`;
  return 'Keep every archive';
}

/** Where a destination puts archives, for the list. */
export function destinationSummary(d: BackupDestination): string {
  switch (d.type) {
    case 'folder':
      return d.folder?.path ?? '';
    case 's3': {
      const s = d.s3;
      if (!s) return '';
      const where = s.endpoint ? hostOf(s.endpoint) : `AWS ${s.region}`;
      return `s3://${s.bucket}/${s.prefix ?? ''} (${where})`;
    }
    case 'azure': {
      const a = d.azure;
      return a ? `${a.account}/${a.container}/${a.prefix ?? ''}` : '';
    }
    case 'sftp': {
      const s = d.sftp;
      if (!s) return '';
      const port = s.port && s.port !== 22 ? `:${s.port}` : '';
      return `${s.username}@${s.host}${port}:${s.directory || '.'}`;
    }
  }
  return '';
}

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

const HOST_KEY = /^SHA256:[A-Za-z0-9+/]{43}=?$/;

export function isHostKey(v: string): boolean {
  return HOST_KEY.test(v.trim());
}

export function isValidTime(v: string): boolean {
  return /^([01][0-9]|2[0-3]):[0-5][0-9]$/.test(v);
}

/** The first problem with a destination as edited, or null. Secrets may be masked. */
export function destinationProblem(d: BackupDestination): { field: string; message: string } | null {
  if (!d.name.trim()) return { field: 'name', message: 'Give the destination a name' };
  switch (d.type) {
    case 'folder': {
      const p = d.folder?.path.trim() ?? '';
      if (!p) return { field: 'folder.path', message: 'Enter a folder or a UNC path' };
      if (!/^(\\\\|\/|[A-Za-z]:[\\/])/.test(p)) return { field: 'folder.path', message: 'Use an absolute path, e.g. D:\\Backups or \\\\server\\share' };
      return null;
    }
    case 's3': {
      const s = d.s3;
      if (!s?.bucket.trim()) return { field: 's3.bucket', message: 'Enter the bucket' };
      if (!s.endpoint?.trim() && !s.region.trim()) return { field: 's3.region', message: "Enter the bucket's region" };
      if (s.endpoint?.trim() && !/^https?:\/\/./.test(s.endpoint.trim())) return { field: 's3.endpoint', message: 'Use an https:// URL' };
      if (!s.accessKeyId.trim()) return { field: 's3.accessKeyId', message: 'Enter the access key ID' };
      if (!s.secretAccessKey) return { field: 's3.secretAccessKey', message: 'Enter the secret access key' };
      return null;
    }
    case 'azure': {
      const a = d.azure;
      if (!a?.account.trim()) return { field: 'azure.account', message: 'Enter the storage account' };
      if (!a.container.trim()) return { field: 'azure.container', message: 'Enter the container' };
      if (!a.sasToken && !a.accountKey) return { field: 'azure.sasToken', message: 'Enter a SAS token or the account key' };
      return null;
    }
    case 'sftp': {
      const s = d.sftp;
      if (!s?.host.trim()) return { field: 'sftp.host', message: "Enter the server's host name" };
      if (!s.username.trim()) return { field: 'sftp.username', message: 'Enter the user name' };
      if (!s.password && !s.privateKey) return { field: 'sftp.password', message: 'Enter a password or a private key' };
      if (!isHostKey(s.hostKey)) return { field: 'sftp.hostKey', message: "Enter the server's SHA256 host key (Test reads it)" };
      return null;
    }
  }
  return null;
}

export function runTone(status: BackupRunStatus | string | undefined): 'green' | 'amber' | 'red' | 'gray' {
  switch (status) {
    case 'success':
      return 'green';
    case 'partial':
      return 'amber';
    case 'failed':
      return 'red';
  }
  return 'gray';
}

/** Sizes above this make the shared-folders option show a warning. */
export const SHARED_WARN_BYTES = 1024 * 1024 * 1024;
