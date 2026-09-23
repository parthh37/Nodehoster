import { useEffect, useRef, type ReactNode } from 'react';
import { Save, Undo2 } from 'lucide-react';
import { Button } from './Button';

/** Sticky bottom bar shown while a form has unsaved changes. Ctrl/Cmd+S saves. */
export function SaveBar({ onDiscard, onSave, saving, label = 'You have unsaved changes' }: { onDiscard: () => void; onSave: () => void; saving: boolean; label?: ReactNode }) {
  const saveRef = useRef(onSave);
  saveRef.current = onSave;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
        e.preventDefault();
        saveRef.current();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
  return (
    <div className="fixed inset-x-0 bottom-0 z-30 lg:left-56">
      <div className="mx-auto max-w-[1400px] px-4 pb-4 sm:px-6 lg:px-8">
        <div className="flex animate-pop-in items-center gap-3 rounded-lg border border-zinc-200 bg-white px-4 py-2.5 shadow-pop dark:border-zinc-700 dark:bg-zinc-900">
          <span className="h-2 w-2 rounded-full bg-amber-500" />
          <span className="text-[13px] font-medium">{label}</span>
          <span className="hidden text-xs text-zinc-500 sm:inline">
            <span className="nh-kbd">Ctrl</span> + <span className="nh-kbd">S</span> to save
          </span>
          <div className="ml-auto flex gap-2">
            <Button icon={<Undo2 className="h-3.5 w-3.5" />} onClick={onDiscard} disabled={saving}>
              Discard
            </Button>
            <Button variant="primary" icon={<Save className="h-3.5 w-3.5" />} onClick={onSave} loading={saving}>
              Save changes
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}

