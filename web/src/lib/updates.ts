// Automatic updates: client-side mirror of model.UpdateSettings defaults,
// and how the Settings → Updates card describes the updater's state.

import type { UpdateSettings, UpdateStatus } from '@/api/types';

/** Mirror of model.DefaultUpdates: off (setup asks), at 03:00 on any day. */
export function defaultUpdates(): UpdateSettings {
  return { auto: false, time: '03:00', weekdays: [] };
}

/** Fills settings saved before automatic updates existed, like UpdateSettings.ApplyDefaults. */
export function normalizeUpdates(u: Partial<UpdateSettings> | null | undefined): UpdateSettings {
  const d = defaultUpdates();
  return { auto: !!u?.auto, time: u?.time || d.time, weekdays: u?.weekdays ?? [] };
}

export type Tone = 'green' | 'blue' | 'amber' | 'red' | 'gray';

/** The status card's headline: what an administrator should know first. */
export function updateHeadline(st: UpdateStatus): { tone: Tone; text: string } {
  if (!st.supported) return { tone: 'gray', text: 'Not available on this installation' };
  switch (st.state) {
    case 'checking':
      return { tone: 'blue', text: 'Checking for updates…' };
    case 'downloading':
      return { tone: 'blue', text: `Downloading ${st.available?.version ?? 'the update'}…` };
    case 'installing':
      return { tone: 'blue', text: `Installing ${st.available?.version ?? 'the update'}: the service restarts` };
  }
  const a = st.available;
  if (!a) return st.lastError ? { tone: 'amber', text: 'Could not check for updates' } : { tone: 'green', text: 'Up to date' };
  if (a.failed) return { tone: 'red', text: `${a.version} failed to install` };
  if (st.nextInstall) return { tone: 'blue', text: `${a.version} will be installed automatically` };
  return { tone: 'amber', text: `${a.version} is available` };
}
