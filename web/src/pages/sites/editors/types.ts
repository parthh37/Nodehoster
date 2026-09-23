import type { Site } from '@/api/types';

/** Mutates a cloned draft of the site. */
export type SiteUpdater = (fn: (draft: Site) => void) => void;

export interface SiteEditorProps {
  site: Site;
  update: SiteUpdater;
  /** Read-only (viewer/operator). */
  readOnly?: boolean;
}
