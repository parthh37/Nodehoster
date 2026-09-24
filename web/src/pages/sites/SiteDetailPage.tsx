import { useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, Boxes, Construction, Lock, Trash2 } from 'lucide-react';
import { sitesApi } from '@/api/endpoints';
import { ApiError, errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { Site, SiteView } from '@/api/types';
import { useSiteStatus } from '@/hooks/useLive';
import { usePermissions } from '@/hooks/useAuth';
import { useUnsavedChangesPrompt } from '@/hooks/useUnsaved';
import { Button } from '@/components/Button';
import { SaveBar } from '@/components/SaveBar';
import { Card, Callout, EmptyState, FormSection, Loading, Sections } from '@/components/Layout';
import { RouteTabs, type TabItem } from '@/components/Tabs';
import { SiteTypeBadge, StateBadge } from '@/components/StatusBadges';
import { ErrorBox, Field, FormErrorBanner, FormErrors } from '@/components/Field';
import { Input, Textarea } from '@/components/Input';
import { Switch } from '@/components/Switch';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { formatDateTime } from '@/lib/format';
import { NAME_RE, runsNode } from '@/lib/siteDefaults';
import { cn } from '@/lib/cn';
import { BindingLink, SiteHeaderActions } from './shared';
import { OverviewTab } from './OverviewTab';
import { LogsTab } from './LogsTab';
import { TasksTab } from './TasksTab';
import { DeployConfigCard, DeploymentsPanel } from './DeploymentsTab';
import { BindingsEditor } from './editors/BindingsEditor';
import { EnvEditor } from './editors/EnvEditor';
import { AccessCard, ErrorPagesCard, HeadersCard, LocationsCard, MaintenanceCard, RoutingGeneral } from './editors/RoutingEditor';
import { OutboundRulesCard, RewriteMapsCard, RewritesCard } from './editors/RewriteEditor';
import { MimeTypesCard } from './editors/MimeEditor';
import { CacheCard } from './editors/CacheEditor';
import { NodeAdvanced, NodeEssentials, NodeLoadBalancer, ProxyAdvanced, ProxyEssentials, RedirectEssentials, StaticEssentials } from './editors/TypeSettings';
import { useSiteDraft } from './useSiteDraft';
import type { SiteEditorProps } from './editors/types';

type TabKey = 'overview' | 'bindings' | 'settings' | 'environment' | 'routing' | 'deployments' | 'tasks' | 'logs';

const EDIT_TABS: TabKey[] = ['bindings', 'settings', 'environment', 'routing', 'deployments', 'tasks'];

/** Which tab shows the field named in a validation error. */
function tabForField(field: string | undefined, type: string): TabKey | null {
  if (!field) return null;
  // A worker has no Bindings or Routing tab; the settings tab shows the error in its banner.
  const http = type !== 'worker';
  if (field.startsWith('tasks')) return 'tasks';
  if (field.startsWith('bindings')) return http ? 'bindings' : 'settings';
  if (field.startsWith('node.env')) return 'environment';
  if (field.startsWith('routing')) return http ? 'routing' : 'settings';
  if (field.startsWith('deploy')) return runsNode(type) || type === 'static' ? 'deployments' : 'settings';
  return 'settings';
}

export function SiteDetailPage() {
  const { id = '', tab: tabParam } = useParams();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const { isAdmin } = usePermissions();
  const q = useQuery({ queryKey: qk.site(id), queryFn: () => sitesApi.get(id) });
  const status = useSiteStatus(id, q.data?.status);
  const { base, draft, dirty, update, reset, commit } = useSiteDraft(q.data);
  const [saveError, setSaveError] = useState<ApiError | Error | null>(null);

  const site = q.data;
  const type = site?.type ?? 'node';
  const worker = type === 'worker';
  const tabs: TabItem<TabKey>[] = [
    { key: 'overview', label: 'Overview' },
    { key: 'bindings', label: 'Bindings', hidden: worker },
    { key: 'settings', label: 'Settings' },
    { key: 'environment', label: 'Environment', hidden: !runsNode(type) },
    { key: 'routing', label: 'Routing', hidden: worker },
    { key: 'deployments', label: 'Deployments', hidden: !runsNode(type) && type !== 'static' },
    { key: 'tasks', label: 'Tasks', hidden: !runsNode(type) },
    { key: 'logs', label: 'Logs' },
  ];
  const tab: TabKey = (tabs.find((t) => t.key === tabParam && !t.hidden)?.key ?? 'overview') as TabKey;

  useUnsavedChangesPrompt(dirty, `/sites/${id}`);

  useEffect(() => setSaveError(null), [id]);

  const save = useMutation({
    mutationFn: (s: Site) => sitesApi.update(id, { ...s, activeRelease: site?.activeRelease ?? s.activeRelease }),
    onSuccess: (saved) => {
      qc.setQueryData(qk.site(id), saved);
      void qc.invalidateQueries({ queryKey: qk.sites, exact: true });
      commit(saved);
      setSaveError(null);
      toast.success('Changes saved', runsNode(type) ? 'Process changes are applied with a rolling recycle.' : 'Applied live.');
    },
    onError: (e) => {
      setSaveError(e instanceof Error ? e : new Error(String(e)));
      const t = e instanceof ApiError ? tabForField(e.field, type) : null;
      if (t && t !== tab) navigate(`/sites/${id}/${t}`, { replace: true });
      toast.error('Could not save changes', e);
    },
  });

  if (q.isPending) return <Loading />;
  if (q.isError || !site) {
    const notFound = q.error instanceof ApiError && q.error.status === 404;
    return (
      <EmptyState
        icon={<Boxes />}
        title={notFound ? 'Site not found' : 'Could not load site'}
        description={notFound ? 'It may have been deleted.' : errorMessage(q.error)}
        action={
          <Link to="/sites">
            <Button icon={<ArrowLeft className="h-4 w-4" />}>Back to sites</Button>
          </Link>
        }
      />
    );
  }

  const editing = EDIT_TABS.includes(tab);
  const editorProps: SiteEditorProps | null = draft ? { site: draft, update, readOnly: !isAdmin } : null;

  return (
    <div className={cn(dirty && editing && 'pb-20')}>
      {/* Header */}
      <div className="mb-4">
        <Link to="/sites" className="mb-2 inline-flex items-center gap-1 text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200">
          <ArrowLeft className="h-3 w-3" /> Sites
        </Link>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="truncate text-xl font-semibold tracking-tight">{site.name}</h1>
              <StateBadge state={status?.state} title={status?.message} />
              <SiteTypeBadge type={site.type} />
              {site.routing?.maintenance?.enabled && (
                <span className="inline-flex items-center gap-1 rounded bg-amber-100 px-1.5 py-px text-xs font-medium text-amber-800 dark:bg-amber-500/15 dark:text-amber-300">
                  <Construction className="h-3 w-3" /> Maintenance
                </span>
              )}
              {dirty && (
                <span className="inline-flex items-center gap-1 text-xs font-medium text-amber-600 dark:text-amber-400">
                  <span className="h-1.5 w-1.5 rounded-full bg-amber-500" /> Unsaved changes
                </span>
              )}
            </div>
            {site.description && <p className="mt-0.5 text-[13px] text-zinc-500">{site.description}</p>}
            <div className="mt-1.5 flex flex-wrap gap-x-4 gap-y-1">
              {(site.bindings ?? []).map((b, i) => (
                <BindingLink key={b.id || i} b={b} />
              ))}
              {worker ? (
                <span className="text-xs text-zinc-500">Background worker — not reachable over HTTP</span>
              ) : (
                (site.bindings ?? []).length === 0 && <span className="text-xs text-amber-600">No bindings — the site is unreachable</span>
              )}
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <SiteHeaderActions site={site} state={status?.state} />
          </div>
        </div>
      </div>

      <RouteTabs tabs={tabs} base={`/sites/${id}`} value={tab} className="mb-5" />

      <FormErrors error={saveError}>
        {editing && <FormErrorBanner className="mb-4" />}
        {editing && !isAdmin && (
          <Callout tone="info" icon={<Lock />} className="mb-4">
            Read-only. Only administrators can change site configuration.
          </Callout>
        )}

        {tab === 'overview' && <OverviewTab site={site} status={status} />}
        {tab === 'logs' && <LogsTab site={site} />}

        {editorProps && (
          <fieldset disabled={!isAdmin} className="min-w-0">
            {tab === 'bindings' && (
              <Card title="Bindings" description="Protocol, IP address, port and host name combinations this site answers, like IIS site bindings.">
                <BindingsEditor {...editorProps} />
              </Card>
            )}
            {tab === 'settings' && <SettingsTab {...editorProps} view={site} />}
            {tab === 'environment' && (
              <Card title="Environment variables" description="Passed to every instance. Changes are applied with a rolling recycle.">
                <EnvEditor {...editorProps} />
              </Card>
            )}
            {tab === 'routing' && (
              <div className="space-y-5">
                <MaintenanceCard {...editorProps} />
                <RoutingGeneral {...editorProps} />
                <RewritesCard {...editorProps} />
                <OutboundRulesCard {...editorProps} />
                <RewriteMapsCard {...editorProps} />
                <LocationsCard {...editorProps} />
                <HeadersCard {...editorProps} />
                <CacheCard {...editorProps} />
                <AccessCard {...editorProps} />
                <ErrorPagesCard {...editorProps} />
                <MimeTypesCard {...editorProps} />
              </div>
            )}
          </fieldset>
        )}
        {tab === 'deployments' && editorProps && base && (
          <div className="space-y-5">
            <DeploymentsPanel site={editorProps.site} savedSite={base} dirty={dirty} />
            <fieldset disabled={!isAdmin} className="min-w-0">
              <DeployConfigCard {...editorProps} />
            </fieldset>
          </div>
        )}
        {/* Not in the fieldset: operators run and cancel tasks; the definitions are read-only for them. */}
        {tab === 'tasks' && editorProps && base && <TasksTab {...editorProps} savedSite={base} />}
      </FormErrors>

      {dirty && isAdmin && (
        <SaveBar
          onDiscard={() => {
            reset();
            setSaveError(null);
          }}
          onSave={() => draft && save.mutate(draft)}
          saving={save.isPending}
        />
      )}
    </div>
  );
}

function SettingsTab(props: SiteEditorProps & { view: SiteView }) {
  const { site, update, view } = props;
  const nameErr = site.name && !NAME_RE.test(site.name) ? "1–64 characters: letters, digits, space, '.', '_' or '-'" : null;
  return (
    <div className="space-y-5">
      <Card title="General">
        <Sections>
          <FormSection title="Identity">
            <Field label="Name" path="name" error={nameErr} required>
              <Input
                value={site.name}
                onChange={(e) =>
                  update((d) => {
                    d.name = e.target.value;
                  })
                }
              />
            </Field>
            <Field label="Description" path="description">
              <Textarea
                rows={2}
                value={site.description ?? ''}
                onChange={(e) =>
                  update((d) => {
                    d.description = e.target.value;
                  })
                }
              />
            </Field>
            <Switch
              checked={site.autoStart}
              onChange={(v) =>
                update((d) => {
                  d.autoStart = v;
                })
              }
              label="Start automatically"
              description="Start this site when the NodeHoster service starts."
            />
            <p className="text-xs text-zinc-500">
              ID <span className="font-mono">{site.id}</span> · created {formatDateTime(view.createdAt)} · updated {formatDateTime(view.updatedAt)}
            </p>
          </FormSection>
        </Sections>
      </Card>

      {runsNode(site.type) && site.node && (
        <Card title={site.type === 'worker' ? 'Background worker' : 'Application'}>
          <Sections>
            <FormSection title="Application" description="Where the app lives and how it starts.">
              <NodeEssentials {...props} withInstances={false} />
            </FormSection>
            <NodeAdvanced {...props} />
          </Sections>
        </Card>
      )}
      {site.type === 'node' && site.node && (
        <Card title="Multiple servers">
          <Sections>
            <NodeLoadBalancer {...props} />
          </Sections>
        </Card>
      )}
      {site.type === 'proxy' && site.proxy && (
        <Card title="Reverse proxy">
          <Sections>
            <FormSection title="Upstreams">
              <ProxyEssentials {...props} />
            </FormSection>
            <ProxyAdvanced {...props} />
          </Sections>
        </Card>
      )}
      {site.type === 'static' && site.static && (
        <Card title="Static files">
          <StaticEssentials {...props} full />
        </Card>
      )}
      {site.type === 'redirect' && site.redirect && (
        <Card title="Redirect">
          <RedirectEssentials {...props} />
        </Card>
      )}

      {!props.readOnly && <DangerZone site={view} />}
    </div>
  );
}

function DangerZone({ site }: { site: SiteView }) {
  const confirm = useConfirm();
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const del = useMutation({
    mutationFn: (deleteFiles: boolean) => sitesApi.remove(site.id, deleteFiles),
    onSuccess: () => {
      toast.success(`${site.name} deleted`);
      qc.removeQueries({ queryKey: qk.site(site.id) });
      void qc.invalidateQueries({ queryKey: qk.sites, exact: true });
      navigate('/sites', { replace: true });
    },
    onError: (e) => toast.error('Could not delete site', e),
  });
  return (
    <Card title="Danger zone" tone="danger">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <p className="text-[13px] font-medium">Delete this site</p>
          <p className="text-xs text-zinc-500">Stops the site and removes its configuration, bindings, logs and metrics.</p>
        </div>
        <Button
          variant="danger"
          icon={<Trash2 className="h-3.5 w-3.5" />}
          loading={del.isPending}
          onClick={async () => {
            const r = await confirm({
              title: `Delete ${site.name}?`,
              message: 'This cannot be undone.',
              danger: true,
              confirmLabel: 'Delete site',
              typeToConfirm: site.name,
              checkbox: {
                label: "Also delete the site's files from disk",
                description: 'Leave unchecked to keep application files and releases.',
              },
            });
            if (r.ok) del.mutate(r.checked);
          }}
        >
          Delete site
        </Button>
      </div>
      {del.isError && <ErrorBox className="mt-3">{errorMessage(del.error)}</ErrorBox>}
    </Card>
  );
}
