import { describe, expect, it } from 'vitest';
import type { Settings, UpdateStatus } from '@/api/types';
import { defaultUpdates, normalizeUpdates, updateHeadline } from './updates';
import { normalizeSettings } from './settingsDefaults';

const base: UpdateStatus = { ...defaultUpdates(), current: '1.1.0', state: 'idle', supported: true };
const release = { version: '1.2.0', published: '2026-09-01T00:00:00Z', size: 1, notes: '', manual: false, failed: false };

describe('normalizeUpdates', () => {
  it('fills settings saved before automatic updates existed', () => {
    expect(normalizeUpdates(undefined)).toEqual(defaultUpdates());
    expect(normalizeSettings({ proxy: {} } as unknown as Settings).updates).toEqual(defaultUpdates());
    expect(normalizeUpdates({ auto: true, time: '', weekdays: null as unknown as number[] })).toEqual({ auto: true, time: '03:00', weekdays: [] });
  });
});

describe('updateHeadline', () => {
  it('says why a server cannot update itself', () => {
    expect(updateHeadline({ ...base, supported: false, reason: 'portable' }).tone).toBe('gray');
  });

  it('reports progress before anything else', () => {
    expect(updateHeadline({ ...base, state: 'installing', available: release }).text).toContain('Installing 1.2.0');
  });

  it('distinguishes up to date from a failed check', () => {
    expect(updateHeadline(base)).toEqual({ tone: 'green', text: 'Up to date' });
    expect(updateHeadline({ ...base, lastError: 'offline' }).tone).toBe('amber');
  });

  it('says whether an available release installs itself', () => {
    expect(updateHeadline({ ...base, available: release }).text).toBe('1.2.0 is available');
    expect(updateHeadline({ ...base, auto: true, available: release, nextInstall: '2026-09-02T03:00:00Z' }).tone).toBe('blue');
    expect(updateHeadline({ ...base, available: { ...release, failed: true } }).tone).toBe('red');
  });
});
