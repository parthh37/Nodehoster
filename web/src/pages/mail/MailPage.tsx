import { useParams } from 'react-router-dom';
import { Mail } from 'lucide-react';
import { usePermissions } from '@/hooks/useAuth';
import { PageHeader } from '@/components/Layout';
import { RouteTabs, type TabItem } from '@/components/Tabs';
import { MailQueueTab } from './MailQueueTab';
import { MailSettingsTab } from './MailSettingsTab';

type TabKey = 'queue' | 'settings';

export function MailPage() {
  const { tab: tabParam } = useParams();
  const { isAdmin } = usePermissions();
  const tabs: TabItem<TabKey>[] = [
    { key: 'queue', label: 'Queue' },
    { key: 'settings', label: 'Settings', hidden: !isAdmin },
  ];
  const tab: TabKey = (tabs.find((t) => t.key === tabParam && !t.hidden)?.key ?? 'queue') as TabKey;

  return (
    <div>
      <PageHeader
        icon={<Mail className="h-4 w-4" />}
        title="Mail"
        description="SMTP virtual server: a send-only relay for your applications, like the IIS 6 SMTP service."
      />
      <RouteTabs tabs={tabs} base="/mail" value={tab} className="mb-5" />
      {tab === 'queue' && <MailQueueTab />}
      {tab === 'settings' && isAdmin && <MailSettingsTab />}
    </div>
  );
}
