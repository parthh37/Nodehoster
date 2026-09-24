import { useParams } from 'react-router-dom';
import { Mail } from 'lucide-react';
import { usePermissions } from '@/hooks/useAuth';
import { PageHeader } from '@/components/Layout';
import { RouteTabs, type TabItem } from '@/components/Tabs';
import { MailHealthTab } from './MailHealthTab';
import { MailQueueTab } from './MailQueueTab';
import { MailSettingsTab } from './MailSettingsTab';

type TabKey = 'queue' | 'health' | 'settings';

export function MailPage() {
  const { tab: tabParam } = useParams();
  const { canOperate, isAdmin } = usePermissions();
  const tabs: TabItem<TabKey>[] = [
    { key: 'queue', label: 'Queue' },
    { key: 'health', label: 'Deliverability', hidden: !canOperate },
    { key: 'settings', label: 'Settings', hidden: !isAdmin },
  ];
  const tab: TabKey = (tabs.find((t) => t.key === tabParam && !t.hidden)?.key ?? 'queue') as TabKey;

  return (
    <div>
      <PageHeader
        icon={<Mail className="h-4 w-4" />}
        title="Mail"
        description="NodeHoster's built-in SMTP server: your applications hand it mail and it delivers it directly to the recipients' mail servers. No Windows SMTP service is needed."
      />
      <RouteTabs tabs={tabs} base="/mail" value={tab} className="mb-5" />
      {tab === 'queue' && <MailQueueTab />}
      {tab === 'health' && canOperate && <MailHealthTab />}
      {tab === 'settings' && isAdmin && <MailSettingsTab />}
    </div>
  );
}
