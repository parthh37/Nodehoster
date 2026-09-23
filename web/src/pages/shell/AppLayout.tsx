import { useState } from 'react';
import { NavLink, Outlet, useNavigate } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Activity,
  BadgeCheck,
  Boxes,
  ChevronDown,
  ClipboardList,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Mail,
  Menu as MenuIcon,
  Monitor,
  Moon,
  Settings,
  ShieldCheck,
  Sun,
  Users,
  X,
  Hexagon,
  Lock,
} from 'lucide-react';
import { authApi, serverApi } from '@/api/endpoints';
import { qk } from '@/api/queryKeys';
import { useMe, usePermissions } from '@/hooks/useAuth';
import { LiveProvider, useLive } from '@/hooks/useLive';
import { useTheme, type ThemePref } from '@/hooks/useTheme';
import { Menu } from '@/components/Menu';
import { RoleBadge } from '@/components/StatusBadges';
import { cn } from '@/lib/cn';
import { Logo } from './Logo';
import { ChangePasswordDialog } from '../account/ChangePasswordDialog';
import { TwoFactorDialog } from '../account/TwoFactorDialog';

interface NavItem {
  to: string;
  label: string;
  icon: typeof LayoutDashboard;
  admin?: boolean;
  end?: boolean;
}

const nav: NavItem[] = [
  { to: '/', label: 'Dashboard', icon: LayoutDashboard, end: true },
  { to: '/sites', label: 'Sites', icon: Boxes },
  { to: '/certificates', label: 'Certificates', icon: BadgeCheck },
  { to: '/node', label: 'Node.js', icon: Hexagon },
  { to: '/mail', label: 'Mail', icon: Mail },
  { to: '/events', label: 'Events', icon: Activity },
];

// A user allowed on selected sites only sees their sites and their events.
const siteNav: NavItem[] = [
  { to: '/sites', label: 'Sites', icon: Boxes },
  { to: '/events', label: 'Events', icon: Activity },
];

const adminNav: NavItem[] = [
  { to: '/settings', label: 'Settings', icon: Settings, admin: true },
  { to: '/users', label: 'Users', icon: Users, admin: true },
  { to: '/audit', label: 'Audit log', icon: ClipboardList, admin: true },
];

export function AppLayout() {
  return (
    <LiveProvider>
      <Shell />
    </LiveProvider>
  );
}

function Shell() {
  const [mobileOpen, setMobileOpen] = useState(false);
  const me = useMe();
  const qc = useQueryClient();
  const forced = !!me.data?.mustChangePassword;

  return (
    <div className="flex min-h-screen">
      <Sidebar open={mobileOpen} onClose={() => setMobileOpen(false)} />
      <div className="flex min-w-0 flex-1 flex-col lg:pl-56">
        <TopBar onMenu={() => setMobileOpen(true)} />
        <main className="mx-auto w-full max-w-[1400px] flex-1 px-4 py-5 sm:px-6 lg:px-8">
          <Outlet />
        </main>
      </div>
      <ChangePasswordDialog
        open={forced}
        forced
        onClose={() => {
          // Everything else was refused (403) until the password changed.
          void qc.invalidateQueries();
        }}
      />
    </div>
  );
}

function Sidebar({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { isAdmin, siteScoped } = usePermissions();
  const info = useQuery({ queryKey: qk.serverInfo, queryFn: serverApi.info, staleTime: 30_000 });
  const items = siteScoped ? siteNav : isAdmin ? [...nav, ...adminNav] : nav;

  const content = (
    <div className="flex h-full flex-col">
      <div className="flex h-14 items-center gap-2.5 px-4">
        <Logo className="h-7 w-7" />
        <span className="text-[15px] font-semibold tracking-tight text-zinc-900 dark:text-zinc-50">NodeHoster</span>
        <button type="button" onClick={onClose} className="ml-auto rounded p-1 text-zinc-400 lg:hidden" aria-label="Close menu">
          <X className="h-4 w-4" />
        </button>
      </div>
      <nav className="flex-1 space-y-0.5 overflow-y-auto px-2 py-2">
        {items.map((item, i) => (
          <div key={item.to}>
            {item.admin && !items[i - 1]?.admin && (
              <div className="px-2.5 pb-1 pt-4 text-2xs font-semibold uppercase tracking-wider text-zinc-400 dark:text-zinc-500">
                Administration
              </div>
            )}
            <NavLink
              to={item.to}
              end={item.end}
              onClick={onClose}
              className={({ isActive }) =>
                cn(
                  'group flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-[13px] font-medium transition-colors',
                  isActive
                    ? 'bg-zinc-200/70 text-zinc-900 dark:bg-zinc-800 dark:text-zinc-50'
                    : 'text-zinc-600 hover:bg-zinc-100 hover:text-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-800/60 dark:hover:text-zinc-100',
                )
              }
            >
              {({ isActive }) => (
                <>
                  <item.icon className={cn('h-4 w-4', isActive ? 'text-accent-700 dark:text-accent-400' : 'text-zinc-400 group-hover:text-zinc-500')} />
                  {item.label}
                </>
              )}
            </NavLink>
          </div>
        ))}
      </nav>
      <div className="border-t border-zinc-200 px-4 py-3 text-2xs text-zinc-400 dark:border-zinc-800 dark:text-zinc-500">
        {info.data ? (
          <span className="font-mono">
            v{info.data.version}
            {info.data.commit ? ` · ${info.data.commit.slice(0, 7)}` : ''}
          </span>
        ) : (
          'NodeHoster'
        )}
      </div>
    </div>
  );

  return (
    <>
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-56 border-r border-zinc-200 bg-zinc-50 dark:border-zinc-800 dark:bg-zinc-950 lg:block">
        {content}
      </aside>
      {open && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div className="absolute inset-0 bg-zinc-950/40" onClick={onClose} />
          <aside className="absolute inset-y-0 left-0 w-60 animate-slide-in border-r border-zinc-200 bg-zinc-50 dark:border-zinc-800 dark:bg-zinc-950">
            {content}
          </aside>
        </div>
      )}
    </>
  );
}

function TopBar({ onMenu }: { onMenu: () => void }) {
  const info = useQuery({ queryKey: qk.serverInfo, queryFn: serverApi.info, staleTime: 30_000 });
  const { connected } = useLive();
  return (
    <header className="sticky top-0 z-20 flex h-14 items-center gap-3 border-b border-zinc-200 bg-white/85 px-4 backdrop-blur dark:border-zinc-800 dark:bg-zinc-950/85 sm:px-6 lg:px-8">
      <button type="button" onClick={onMenu} className="rounded p-1 text-zinc-500 lg:hidden" aria-label="Open menu">
        <MenuIcon className="h-5 w-5" />
      </button>
      <div className="flex min-w-0 items-center gap-2 text-[13px]">
        <Monitor className="h-4 w-4 shrink-0 text-zinc-400" />
        <span className="truncate font-mono font-medium text-zinc-800 dark:text-zinc-200">{info.data?.hostname ?? '…'}</span>
        {info.data?.os && <span className="hidden truncate text-zinc-400 sm:inline">{info.data.os}</span>}
      </div>
      <div className="ml-auto flex items-center gap-1.5">
        <span
          className={cn(
            'mr-1 hidden items-center gap-1.5 rounded-full px-2 py-0.5 text-2xs font-medium sm:inline-flex',
            connected ? 'text-emerald-700 dark:text-emerald-400' : 'text-amber-600 dark:text-amber-400',
          )}
          title={connected ? 'Live updates connected' : 'Live updates disconnected — reconnecting'}
        >
          <span className={cn('h-1.5 w-1.5 rounded-full', connected ? 'bg-emerald-500' : 'animate-pulse bg-amber-500')} />
          {connected ? 'Live' : 'Reconnecting'}
        </span>
        <ThemeMenu />
        <UserMenu />
      </div>
    </header>
  );
}

function ThemeMenu() {
  const { pref, resolved, setPref } = useTheme();
  const opt = (p: ThemePref, label: string, Icon: typeof Sun) => ({
    label: (
      <span className="flex w-full items-center justify-between gap-4">
        {label}
        {pref === p && <span className="h-1.5 w-1.5 rounded-full bg-accent-600" />}
      </span>
    ),
    icon: <Icon />,
    onSelect: () => setPref(p),
  });
  return (
    <Menu
      trigger={(p) => (
        <button
          type="button"
          {...p}
          title="Theme"
          className="rounded-md p-1.5 text-zinc-500 hover:bg-zinc-100 hover:text-zinc-800 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-100"
        >
          {resolved === 'dark' ? <Moon className="h-4 w-4" /> : <Sun className="h-4 w-4" />}
        </button>
      )}
      items={[opt('light', 'Light', Sun), opt('dark', 'Dark', Moon), opt('system', 'System', Monitor)]}
    />
  );
}

function UserMenu() {
  const me = useMe();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [pwOpen, setPwOpen] = useState(false);
  const [tfaOpen, setTfaOpen] = useState(false);
  const user = me.data?.user;

  const logout = async () => {
    try {
      await authApi.logout();
    } catch {
      /* session may already be gone */
    }
    qc.clear();
    navigate('/login', { replace: true });
  };

  return (
    <>
      <Menu
        header={
          user && (
            <div className="flex items-center justify-between gap-3">
              <div className="min-w-0">
                <p className="truncate text-[13px] font-medium text-zinc-900 dark:text-zinc-100">{user.username}</p>
                <p className="text-2xs text-zinc-500">{user.totpEnabled ? '2FA enabled' : '2FA not enabled'}</p>
              </div>
              <RoleBadge role={user.role} />
            </div>
          )
        }
        trigger={(p) => (
          <button
            type="button"
            {...p}
            className="flex items-center gap-2 rounded-md py-1 pl-1 pr-2 text-[13px] hover:bg-zinc-100 dark:hover:bg-zinc-800"
          >
            <span className="flex h-6 w-6 items-center justify-center rounded-full bg-accent-700 text-2xs font-semibold uppercase text-white dark:bg-accent-600">
              {user?.username.slice(0, 2) ?? '?'}
            </span>
            <span className="hidden max-w-[10rem] truncate font-medium text-zinc-700 dark:text-zinc-200 sm:inline">{user?.username}</span>
            <ChevronDown className="h-3.5 w-3.5 text-zinc-400" />
          </button>
        )}
        items={[
          { label: 'Change password', icon: <Lock />, onSelect: () => setPwOpen(true) },
          { label: 'Two-factor authentication', icon: <ShieldCheck />, onSelect: () => setTfaOpen(true) },
          { label: 'API tokens', icon: <KeyRound />, onSelect: () => navigate('/account/tokens') },
          'separator',
          { label: 'Sign out', icon: <LogOut />, onSelect: () => void logout() },
        ]}
      />
      <ChangePasswordDialog open={pwOpen} onClose={() => setPwOpen(false)} />
      <TwoFactorDialog open={tfaOpen} onClose={() => setTfaOpen(false)} />
    </>
  );
}
