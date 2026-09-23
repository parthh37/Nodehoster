import { useEffect } from 'react';
import { useBlocker } from 'react-router-dom';

/** Warns before leaving the page (tab close or in-app navigation) with unsaved changes. */
export function useUnsavedChangesPrompt(
  dirty: boolean,
  /** Navigation within this path prefix (e.g. tab switches) is allowed. */
  basePath?: string,
  message = 'You have unsaved changes. Leave without saving?',
) {
  useEffect(() => {
    if (!dirty) return;
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', onBeforeUnload);
    return () => window.removeEventListener('beforeunload', onBeforeUnload);
  }, [dirty]);

  const blocker = useBlocker(({ currentLocation, nextLocation }) => {
    if (!dirty) return false;
    if (basePath && nextLocation.pathname.startsWith(basePath)) return false;
    return currentLocation.pathname !== nextLocation.pathname;
  });

  useEffect(() => {
    if (blocker.state === 'blocked') {
      if (window.confirm(message)) blocker.proceed();
      else blocker.reset();
    }
  }, [blocker, message]);
}
