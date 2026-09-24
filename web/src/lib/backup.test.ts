import { describe, expect, it } from 'vitest';
import type { BackupSettings } from '@/api/types';
import {
  defaultBackup,
  destinationProblem,
  destinationSummary,
  isHostKey,
  isValidTime,
  newDestination,
  normalizeBackup,
  retentionSummary,
  runTone,
  scheduleSummary,
} from './backup';

describe('normalizeBackup', () => {
  it('fills missing settings like the server', () => {
    expect(normalizeBackup(undefined)).toEqual(defaultBackup());
    const b = normalizeBackup({ enabled: true, time: '', weekdays: null, destinations: null } as unknown as BackupSettings);
    expect(b.time).toBe('02:30');
    expect(b.weekdays).toEqual([]);
    expect(b.destinations).toEqual([]);
    expect(b.enabled).toBe(true);
  });
  it('sorts weekdays and copies destinations', () => {
    const src = { ...defaultBackup(), weekdays: [5, 1, 3], destinations: [newDestination('folder')] };
    const b = normalizeBackup(src);
    expect(b.weekdays).toEqual([1, 3, 5]);
    expect(b.destinations[0]).not.toBe(src.destinations[0]);
  });
});

describe('scheduleSummary', () => {
  it.each([
    [[], 'Every day at 02:30'],
    [[0, 1, 2, 3, 4, 5, 6], 'Every day at 02:30'],
    [[1, 2, 3, 4, 5], 'Weekdays at 02:30'],
    [[6, 0], 'Weekends at 02:30'],
    [[3], 'Wed at 02:30'],
    [[5, 1, 3], 'Mon, Wed and Fri at 02:30'],
  ])('%j', (days, want) => {
    expect(scheduleSummary('02:30', days)).toBe(want);
  });
});

describe('retentionSummary', () => {
  it('describes both rules', () => {
    expect(retentionSummary(14, 0)).toBe('Keep the last 14');
    expect(retentionSummary(0, 30)).toBe('Keep 30 days');
    expect(retentionSummary(0, 1)).toBe('Keep 1 day');
    expect(retentionSummary(7, 30)).toBe('Keep the last 7, and every archive from the last 30 days');
    expect(retentionSummary(0, 0)).toBe('Keep every archive');
  });
});

describe('destinationSummary', () => {
  it('shows where archives go', () => {
    const f = newDestination('folder');
    f.folder!.path = '\\\\nas\\backups';
    expect(destinationSummary(f)).toBe('\\\\nas\\backups');
    const s = newDestination('s3');
    Object.assign(s.s3!, { bucket: 'nh', prefix: 'web01/', region: 'eu-west-1' });
    expect(destinationSummary(s)).toBe('s3://nh/web01/ (AWS eu-west-1)');
    s.s3!.endpoint = 'https://acc.r2.cloudflarestorage.com';
    expect(destinationSummary(s)).toBe('s3://nh/web01/ (acc.r2.cloudflarestorage.com)');
    const p = newDestination('sftp');
    Object.assign(p.sftp!, { host: 'box', username: 'nh', port: 2222, directory: '/srv/backups' });
    expect(destinationSummary(p)).toBe('nh@box:2222:/srv/backups');
  });
});

describe('checks', () => {
  it('validates host keys and times', () => {
    expect(isHostKey('SHA256:' + 'a'.repeat(43))).toBe(true);
    expect(isHostKey('SHA256:short')).toBe(false);
    expect(isHostKey('MD5:aa:bb')).toBe(false);
    expect(isValidTime('23:59')).toBe(true);
    expect(isValidTime('24:00')).toBe(false);
    expect(isValidTime('2:30')).toBe(false);
  });
  it('finds the first problem of a destination', () => {
    const d = newDestination('sftp');
    expect(destinationProblem(d)?.field).toBe('name');
    d.name = 'Box';
    expect(destinationProblem(d)?.field).toBe('sftp.host');
    Object.assign(d.sftp!, { host: 'box', username: 'nh', password: '__SECRET__' });
    expect(destinationProblem(d)?.field).toBe('sftp.hostKey');
    d.sftp!.hostKey = 'SHA256:' + 'b'.repeat(43);
    expect(destinationProblem(d)).toBeNull();

    const f = newDestination('folder');
    f.name = 'NAS';
    f.folder!.path = 'relative\\path';
    expect(destinationProblem(f)?.field).toBe('folder.path');
    f.folder!.path = 'D:\\Backups';
    expect(destinationProblem(f)).toBeNull();

    const s = newDestination('s3');
    s.name = 'R2';
    Object.assign(s.s3!, { bucket: 'nh', endpoint: 'https://x.r2.cloudflarestorage.com', accessKeyId: 'a', secretAccessKey: 'b' });
    expect(destinationProblem(s)).toBeNull();
    s.s3!.endpoint = '';
    expect(destinationProblem(s)?.field).toBe('s3.region');
  });
  it('colours run statuses', () => {
    expect(runTone('success')).toBe('green');
    expect(runTone('partial')).toBe('amber');
    expect(runTone('failed')).toBe('red');
    expect(runTone(undefined)).toBe('gray');
  });
});
