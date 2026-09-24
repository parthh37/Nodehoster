// Shared pieces of the alert pages: severity badges, the list of alerts in
// progress with Silence / Acknowledge, the history table and the silence
// dialog. Used by the Alerts page, a site's Alerts tab and the dashboard.

import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { BellOff, BellRing, CheckCheck, Clock, MoreHorizontal, Server } from 'lucide-react';
import { alertsApi, type Alert } from '@/api/alerts';
import { qk } from '@/api/queryKeys';
import { usePermissions } from '@/hooks/useAuth';
import { canOperate } from '@/lib/access';
import { alertSubject, SILENCE_CHOICES, silenceText } from '@/lib/alerts';
import { formatDateTime, relativeTime } from '@/lib/format';
import { Badge } from '@/components/Badge';
import { Button, IconButton } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { Field } from '@/components/Field';
import { Input, Select } from '@/components/Input';
import { Menu } from '@/components/Menu';
import { Table, TableMessage, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { useToast } from '@/components/Toast';

export function SeverityBadge({ severity, pending }: { severity: string; pending?: boolean }) {
  if (pending) return <Badge tone="gray">pending</Badge>;
  return severity === 'critical' ? (
    <Badge tone="red" dot>
      critical
    </Badge>
  ) : (
    <Badge tone="amber" dot>
      warning
    </Badge>
  );
}

function Subject({ a, showSite }: { a: Alert; showSite: boolean }) {
  if (!showSite) return null;
  return a.siteId ? (
    <Link to={`/sites/${a.siteId}/alerts`} className="nh-link font-medium">
      {alertSubject(a)}
    </Link>
  ) : (
    <span className="inline-flex items-center gap-1 font-medium">
      <Server className="h-3.5 w-3.5 text-zinc-400" /> Server
    </span>
  );
}

/** Whether the signed-in user may silence an alert: operator on its site, or on the server. */
function useCanSilence(): (a: Alert) => boolean {
  const { access, canOperate: serverOperator } = usePermissions();
  return (a) => (a.siteId ? canOperate(access, a.siteId) : serverOperator);
}

/** Alerts firing and pending, with their silence actions. */
export function ActiveAlerts({ alerts, showSite = true, empty = 'No alert is firing.' }: { alerts: Alert[]; showSite?: boolean; empty?: string }) {
  const qc = useQueryClient();
  const toast = useToast();
  const canSilence = useCanSilence();
  const [silencing, setSilencing] = useState<Alert | null>(null);
  const unsilence = useMutation({
    mutationFn: (a: Alert) => alertsApi.unsilence(a.id),
    onSuccess: () => {
      toast.success('Silence lifted');
      void qc.invalidateQueries({ queryKey: qk.alerts });
    },
    onError: (e) => toast.error('Could not lift the silence', e),
  });
  const ack = useMutation({
    mutationFn: (a: Alert) => alertsApi.silence(a.id, { minutes: 0 }),
    onSuccess: () => {
      toast.success('Alert acknowledged', 'It stays quiet until it resolves.');
      void qc.invalidateQueries({ queryKey: qk.alerts });
    },
    onError: (e) => toast.error('Could not acknowledge the alert', e),
  });

  return (
    <>
      <Table>
        <THead>
          <Tr>
            <Th className="w-24">Severity</Th>
            {showSite && <Th className="w-44">Site</Th>}
            <Th>Alert</Th>
            <Th className="w-36">Since</Th>
            <Th className="w-10" />
          </Tr>
        </THead>
        <TBody>
          {alerts.length === 0 && <TableMessage colSpan={showSite ? 5 : 4}>{empty}</TableMessage>}
          {alerts.map((a) => (
            <Tr key={a.id}>
              <Td>
                <SeverityBadge severity={a.severity} pending={a.state === 'pending'} />
              </Td>
              {showSite && (
                <Td>
                  <Subject a={a} showSite />
                </Td>
              )}
              <Td>
                <div className="text-[13px]">{a.message}</div>
                {a.silence && (
                  <div className="mt-0.5 flex items-center gap-1 text-xs text-zinc-500">
                    <BellOff className="h-3 w-3" /> Silenced {silenceText(a, formatDateTime)}
                    {a.silence.note ? ` — ${a.silence.note}` : ''}
                  </div>
                )}
              </Td>
              <Td className="text-xs text-zinc-500" title={formatDateTime(a.since)}>
                {relativeTime(a.since)}
              </Td>
              <Td className="text-right">
                {canSilence(a) && (
                  <Menu
                    trigger={(t) => <IconButton label="Silence" icon={<MoreHorizontal className="h-4 w-4" />} {...t} />}
                    items={[
                      { label: 'Silence…', icon: <BellOff />, onSelect: () => setSilencing(a) },
                      { label: 'Acknowledge', icon: <CheckCheck />, onSelect: () => ack.mutate(a), hidden: !!a.silence && !a.silence.until },
                      { label: 'Unsilence', icon: <BellRing />, onSelect: () => unsilence.mutate(a), hidden: !a.silence },
                    ]}
                  />
                )}
              </Td>
            </Tr>
          ))}
        </TBody>
      </Table>
      <SilenceDialog alert={silencing} onClose={() => setSilencing(null)} />
    </>
  );
}

/** Silence an alert for a while, or until it resolves. */
export function SilenceDialog({ alert, onClose }: { alert: Alert | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [minutes, setMinutes] = useState(60);
  const [note, setNote] = useState('');
  const silence = useMutation({
    mutationFn: () => alertsApi.silence(alert!.id, { minutes, note: note.trim() || undefined }),
    onSuccess: () => {
      toast.success(minutes ? 'Alert silenced' : 'Alert acknowledged');
      void qc.invalidateQueries({ queryKey: qk.alerts });
      setNote('');
      onClose();
    },
    onError: (e) => toast.error('Could not silence the alert', e),
  });
  return (
    <Dialog
      open={!!alert}
      onClose={onClose}
      title="Silence alert"
      icon={<BellOff className="h-5 w-5" />}
      description="No notification or reminder is sent while the alert is silenced. A timed silence also covers the rule's next alerts on the same site until it ends."
      onSubmit={() => silence.mutate()}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" loading={silence.isPending}>
            Silence
          </Button>
        </>
      }
    >
      {alert && (
        <div className="space-y-3">
          <p className="rounded-md bg-zinc-50 px-3 py-2 text-[13px] dark:bg-zinc-800/60">
            <span className="font-medium">{alertSubject(alert)}:</span> {alert.message}
          </p>
          <Field label="Silence for">
            <Select value={minutes} onChange={(v) => setMinutes(Number(v))} options={SILENCE_CHOICES.map((c) => ({ value: c.minutes, label: c.label }))} />
          </Field>
          <Field label="Note" hint="Why, for the others: shown with the alert and in the audit log.">
            <Input value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} placeholder="Deploying a fix" />
          </Field>
        </div>
      )}
    </Dialog>
  );
}

/** Alerts that fired, newest first. */
export function AlertHistory({ alerts, showSite = true }: { alerts: Alert[]; showSite?: boolean }) {
  return (
    <Table dense>
      <THead>
        <Tr>
          <Th className="w-40">Fired</Th>
          {showSite && <Th className="w-44">Site</Th>}
          <Th className="w-24">Severity</Th>
          <Th>Alert</Th>
          <Th className="w-40">Resolved</Th>
        </Tr>
      </THead>
      <TBody>
        {alerts.length === 0 && <TableMessage colSpan={showSite ? 5 : 4}>No alert has fired yet.</TableMessage>}
        {alerts.map((a) => (
          <Tr key={a.id}>
            <Td className="text-xs tabular">{formatDateTime(a.firedAt)}</Td>
            {showSite && (
              <Td>
                <Subject a={a} showSite />
              </Td>
            )}
            <Td>
              <SeverityBadge severity={a.severity} />
            </Td>
            <Td className="text-[13px]">{a.message}</Td>
            <Td className="text-xs">
              {a.state === 'firing' ? (
                <Badge tone="red" dot pulse>
                  firing
                </Badge>
              ) : (
                <span className="inline-flex items-center gap-1 text-zinc-500">
                  <Clock className="h-3 w-3" />
                  {formatDateTime(a.resolvedAt)}
                </span>
              )}
            </Td>
          </Tr>
        ))}
      </TBody>
    </Table>
  );
}
