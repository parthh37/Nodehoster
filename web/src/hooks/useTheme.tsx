import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';

export type ThemePref = 'light' | 'dark' | 'system';

interface ThemeCtx {
  pref: ThemePref;
  resolved: 'light' | 'dark';
  setPref: (p: ThemePref) => void;
  toggle: () => void;
}

const KEY = 'nh-theme';
const Ctx = createContext<ThemeCtx | null>(null);

function readPref(): ThemePref {
  try {
    const v = localStorage.getItem(KEY);
    if (v === 'light' || v === 'dark' || v === 'system') return v;
  } catch {
    /* ignore */
  }
  return 'system';
}

const media = () => window.matchMedia('(prefers-color-scheme: dark)');

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [pref, setPrefState] = useState<ThemePref>(readPref);
  const [systemDark, setSystemDark] = useState(() => media().matches);

  useEffect(() => {
    const m = media();
    const on = (e: MediaQueryListEvent) => setSystemDark(e.matches);
    m.addEventListener('change', on);
    return () => m.removeEventListener('change', on);
  }, []);

  const resolved: 'light' | 'dark' = pref === 'system' ? (systemDark ? 'dark' : 'light') : pref;

  useEffect(() => {
    document.documentElement.classList.toggle('dark', resolved === 'dark');
    document.documentElement.style.colorScheme = resolved;
  }, [resolved]);

  const setPref = useCallback((p: ThemePref) => {
    setPrefState(p);
    try {
      localStorage.setItem(KEY, p);
    } catch {
      /* ignore */
    }
  }, []);

  const toggle = useCallback(() => setPref(resolved === 'dark' ? 'light' : 'dark'), [resolved, setPref]);

  const value = useMemo(() => ({ pref, resolved, setPref, toggle }), [pref, resolved, setPref, toggle]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useTheme(): ThemeCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error('useTheme outside ThemeProvider');
  return c;
}
