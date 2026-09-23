import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { settingsApi } from '@/api/endpoints';
import { ApiError, errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { Settings } from '@/api/types';
import { useUnsavedChangesPrompt } from '@/hooks/useUnsaved';
import { Loading, PageHeader } from '@/components/Layout';
import { RouteTabs, type TabItem } from '@/components/Tabs';
import { ErrorBox, FormErrorBanner, FormErrors } from '@/components/Field';
import { SaveBar } from '@/components/SaveBar';
import { useToast } from '@/components/Toast';
import { clone, jsonEqual } from '@/lib/obj';
import { normalizeSettings } from '@/lib/settingsDefaults';
import { AcmeTab, DnsTab, NotificationsTab, ProcessTab, TlsTab } from './SettingsTabs';
import { AdminConsoleTab, BackupTab } from './AdminTabs';
import { MimeTab } from './MimeTab';
import { SsoTab } from './SsoTab';

type TabKey = 'acme' | 'dns' | 'notifications' | 'tls' | 'mime' | 'process' | 'admin' | 'sso' | 'backup';

const tabs: TabItem<TabKey>[] = [
  { key: 'acme', label: 'ACME' },
  { key: 'dns', label: 'DNS providers' },
  { key: 'notifications', label: 'Notifications' },
  { key: 'tls', label: 'TLS & proxy' },
  { key: 'mime', label: 'MIME types' },
  { key: 'process', label: 'Process & logs' },
  { key: 'admin', label: 'Admin console' },
  { key: 'sso', label: 'Single sign-on' },
  { key: 'backup', label: 'Backup & restore' },
];

const SHARED_TABS: TabKey[] = ['acme', 'dns', 'notifications', 'tls', 'mime', 'process', 'sso'];

function tabForField(field: string | undefined): TabKey | null {
  if (!field) return null;
  if (field.startsWith('acme')) return 'acme';
  if (field.startsWith('dnsProviders')) return 'dns';
  if (field.startsWith('webhooks')) return 'notifications';
  if (field.startsWith('tls') || field.startsWith('proxy')) return 'tls';
  if (field.startsWith('mime')) return 'mime';
  if (field.startsWith('sso')) return 'sso';
  return 'process';
}

export type SettingsUpdater = (fn: (d: Settings) => void) => void;

export interface SettingsTabProps {
  s: Settings;
  update: SettingsUpdater;
}

const normalize = normalizeSettings;

export function SettingsPage() {
  const { tab: tabParam } = useParams();
  const navigate = useNavigate();
  const tab: TabKey = (tabs.find((t) => t.key === tabParam)?.key ?? 'acme') as TabKey;
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: qk.settings, queryFn: settingsApi.get });
  const [base, setBase] = useState<Settings | null>(null);
  const [draft, setDraft] = useState<Settings | null>(null);
  const [error, setError] = useState<Error | null>(null);

  const dirty = useMemo(() => !!base && !!draft && !jsonEqual(base, draft), [base, draft]);

  useEffect(() => {
    if (!q.data) return;
    const fresh = normalize(q.data);
    setBase(fresh);
    if (!dirty) setDraft(clone(fresh));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q.data]);

  useUnsavedChangesPrompt(dirty, '/settings');

  const update = useCallback<SettingsUpdater>((fn) => {
    setDraft((d) => {
      if (!d) return d;
      const c = clone(d);
      fn(c);
      return c;
    });
  }, []);

  const save = useMutation({
    mutationFn: (s: Settings) => settingsApi.put(s),
    onSuccess: (saved) => {
      qc.setQueryData(qk.settings, saved);
      const fresh = normalize(saved);
      setBase(fresh);
      setDraft(clone(fresh));
      setError(null);
      void qc.invalidateQueries({ queryKey: qk.nodeVersions });
      toast.success('Settings saved');
    },
    onError: (e) => {
      setError(e instanceof Error ? e : new Error(String(e)));
      const t = e instanceof ApiError ? tabForField(e.field) : null;
      if (t && t !== tab) navigate(`/settings/${t}`, { replace: true });
      toast.error('Could not save settings', e);
    },
  });

  const shared = SHARED_TABS.includes(tab);
  const props = draft ? { s: draft, update } : null;

  return (
    <div className={dirty ? 'pb-20' : undefined}>
      <PageHeader title="Settings" description="Server-wide configuration." />
      <RouteTabs tabs={tabs} base="/settings" value={tab} className="mb-5" />
      {shared && q.isPending && <Loading />}
      {shared && q.isError && <ErrorBox>{errorMessage(q.error)}</ErrorBox>}
      <FormErrors error={error}>
        {shared && <FormErrorBanner className="mb-4" />}
        {props && tab === 'acme' && <AcmeTab {...props} />}
        {props && tab === 'dns' && <DnsTab {...props} />}
        {props && tab === 'notifications' && <NotificationsTab {...props} />}
        {props && tab === 'tls' && <TlsTab {...props} />}
        {props && tab === 'mime' && <MimeTab {...props} />}
        {props && tab === 'process' && <ProcessTab {...props} />}
        {props && tab === 'sso' && <SsoTab {...props} />}
      </FormErrors>
      {tab === 'admin' && <AdminConsoleTab />}
      {tab === 'backup' && <BackupTab />}
      {dirty && (
        <SaveBar
          label="You have unsaved settings"
          saving={save.isPending}
          onSave={() => draft && save.mutate(draft)}
          onDiscard={() => {
            if (base) setDraft(clone(base));
            setError(null);
          }}
        />
      )}
    </div>
  );
}
