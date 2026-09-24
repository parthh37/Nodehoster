import { createBrowserRouter, Navigate, RouterProvider, useRouteError, isRouteErrorResponse, Link } from 'react-router-dom';
import type { ReactNode } from 'react';
import { ShieldOff } from 'lucide-react';
import { AppLayout } from '@/pages/shell/AppLayout';
import { AuthGate } from '@/pages/shell/AuthGate';
import { LoginPage } from '@/pages/LoginPage';
import { DashboardPage } from '@/pages/DashboardPage';
import { SitesPage } from '@/pages/sites/SitesPage';
import { NewSitePage } from '@/pages/sites/NewSitePage';
import { ImportSitesPage } from '@/pages/sites/ImportSitesPage';
import { SiteDetailPage } from '@/pages/sites/SiteDetailPage';
import { CertificatesPage } from '@/pages/certificates/CertificatesPage';
import { NodePage } from '@/pages/node/NodePage';
import { EventsPage } from '@/pages/EventsPage';
import { AlertsPage } from '@/pages/alerts/AlertsPage';
import { MailPage } from '@/pages/mail/MailPage';
import { SettingsPage } from '@/pages/settings/SettingsPage';
import { UsersPage } from '@/pages/UsersPage';
import { AuditPage } from '@/pages/AuditPage';
import { TokensPage } from '@/pages/account/TokensPage';
import { ServersPage } from '@/pages/servers/ServersPage';
import { ThisServerOnly } from '@/pages/shell/ServerSwitcher';
import { EmptyState } from '@/components/Layout';
import { Button } from '@/components/Button';
import { useLocalPermissions, usePermissions } from '@/hooks/useAuth';

function NoAccess({ title, description }: { title: string; description: string }) {
  const { siteScoped } = usePermissions();
  return (
    <EmptyState
      icon={<ShieldOff />}
      title={title}
      description={description}
      action={
        <Link to="/">
          <Button>{siteScoped ? 'Back to sites' : 'Back to dashboard'}</Button>
        </Link>
      }
    />
  );
}

function AdminOnly({ children }: { children: ReactNode }) {
  const { isAdmin, role } = usePermissions();
  if (!role) return null;
  if (!isAdmin) {
    return <NoAccess title="Administrators only" description="Your account does not have permission to view this page. Ask an administrator if you need access." />;
  }
  return <>{children}</>;
}

/** Server-wide pages, which users allowed on selected sites only cannot use. */
function ServerOnly({ children }: { children: ReactNode }) {
  const { siteScoped, role } = usePermissions();
  if (!role) return null;
  if (siteScoped) {
    return <NoAccess title="Not available" description="Your account has access to selected sites only. Server-wide pages need a server role; ask an administrator if you need access." />;
  }
  return <>{children}</>;
}

/** Pages about this server's own server-wide configuration, whichever server the console operates. */
function LocalServerOnly({ children }: { children: ReactNode }) {
  const { siteScoped, role } = useLocalPermissions();
  if (!role) return null;
  if (siteScoped) {
    return <NoAccess title="Not available" description="Your account has access to selected sites only. Server-wide pages need a server role; ask an administrator if you need access." />;
  }
  return <>{children}</>;
}

/** The dashboard is server-wide; site-scoped users start on their sites. */
function Home() {
  const { siteScoped, role } = usePermissions();
  if (!role) return null;
  return siteScoped ? <Navigate to="/sites" replace /> : <DashboardPage />;
}

function RouteError() {
  const err = useRouteError();
  const message = isRouteErrorResponse(err) ? `${err.status} ${err.statusText}` : err instanceof Error ? err.message : 'Unknown error';
  return (
    <div className="flex min-h-screen items-center justify-center p-6">
      <div className="nh-card max-w-lg p-6">
        <h1 className="text-base font-semibold">Something went wrong</h1>
        <p className="mt-2 font-mono text-xs text-zinc-500">{message}</p>
        <div className="mt-4 flex gap-2">
          <Button variant="primary" onClick={() => window.location.reload()}>
            Reload
          </Button>
          <Button onClick={() => window.location.assign('/')}>Go to dashboard</Button>
        </div>
      </div>
    </div>
  );
}

function NotFound() {
  return (
    <EmptyState
      title="Page not found"
      description="The page you are looking for does not exist."
      action={
        <Link to="/">
          <Button>Back to dashboard</Button>
        </Link>
      }
    />
  );
}

const router = createBrowserRouter([
  { path: '/login', element: <LoginPage />, errorElement: <RouteError /> },
  {
    path: '/',
    element: (
      <AuthGate>
        <AppLayout />
      </AuthGate>
    ),
    errorElement: <RouteError />,
    children: [
      { index: true, element: <Home /> },
      { path: 'sites', element: <SitesPage /> },
      { path: 'sites/new', element: <NewSitePage /> },
      { path: 'sites/import', element: <ImportSitesPage /> },
      { path: 'sites/:id/:tab?', element: <SiteDetailPage /> },
      {
        path: 'certificates',
        element: (
          <ServerOnly>
            <CertificatesPage />
          </ServerOnly>
        ),
      },
      {
        path: 'node',
        element: (
          <ServerOnly>
            <NodePage />
          </ServerOnly>
        ),
      },
      {
        path: 'mail/:tab?',
        element: (
          <ServerOnly>
            <MailPage />
          </ServerOnly>
        ),
      },
      { path: 'events', element: <EventsPage /> },
      { path: 'alerts', element: <AlertsPage /> },
      {
        path: 'settings/:tab?',
        element: (
          <AdminOnly>
            <SettingsPage />
          </AdminOnly>
        ),
      },
      {
        path: 'users',
        element: (
          <AdminOnly>
            <UsersPage />
          </AdminOnly>
        ),
      },
      {
        path: 'audit',
        element: (
          <AdminOnly>
            <AuditPage />
          </AdminOnly>
        ),
      },
      {
        path: 'account/tokens',
        element: (
          <ThisServerOnly what="API tokens">
            <TokensPage />
          </ThisServerOnly>
        ),
      },
      {
        path: 'servers',
        element: (
          <LocalServerOnly>
            <ServersPage />
          </LocalServerOnly>
        ),
      },
      { path: 'dashboard', element: <Navigate to="/" replace /> },
      { path: '*', element: <NotFound /> },
    ],
  },
]);

export function App() {
  return <RouterProvider router={router} />;
}
