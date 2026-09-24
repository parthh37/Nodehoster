import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Download, ExternalLink, RefreshCw } from 'lucide-react';
import { updatesApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import { Button } from '@/components/Button';
import { Callout, Card, FormSection, Grid, KV, Sections } from '@/components/Layout';
import { ErrorBox, Field } from '@/components/Field';
import { Input } from '@/components/Input';
import { Checkbox, Switch } from '@/components/Switch';
import { Badge } from '@/components/Badge';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { WEEKDAYS, isValidTime, scheduleSummary } from '@/lib/backup';
import { formatBytes, formatDateTime, relativeTime } from '@/lib/format';
import { safeHref } from '@/lib/safeHref';
import { updateHeadline } from '@/lib/updates';
import type { SettingsTabProps } from './SettingsPage';

/** Settings → Updates: automatic installation of new NodeHoster releases. */
export function UpdatesTab({ s, update }: SettingsTabProps) {
  const u = s.updates;
  const set = (p: Partial<typeof u>) =>
    update((d) => {
      d.updates = { ...d.updates, ...p };
    });
  return (
    <div className="space-y-5">
      <StatusCard />
      <Card title="Automatic updates" description="Uses the saved settings; save your changes for them to apply.">
        <Sections>
          <FormSection
            title="When"
            description="Server local time. Installing restarts the service: every site is offline for a few seconds, so pick a quiet hour. A time missed while the server was off is not caught up."
          >
            <Switch
              checked={u.auto}
              onChange={(v) => set({ auto: v })}
              label="Install updates automatically"
              description={u.auto ? scheduleSummary(u.time, u.weekdays) : 'New versions are announced (events, notifications) but only installed with Install now.'}
            />
            <Grid>
              <Field label="Time" path="updates.time" error={isValidTime(u.time) ? null : 'Use HH:MM, 24-hour'}>
                <Input className="w-28" mono value={u.time} onChange={(e) => set({ time: e.target.value.trim() })} placeholder="03:00" />
              </Field>
            </Grid>
            <Field label="Days" path="updates.weekdays" hint="None selected = every day.">
              <div className="flex flex-wrap gap-3">
                {WEEKDAYS.map((name, i) => (
                  <Checkbox
                    key={name}
                    checked={u.weekdays.includes(i)}
                    onChange={(on) => set({ weekdays: on ? [...u.weekdays, i].sort((x, y) => x - y) : u.weekdays.filter((x) => x !== i) })}
                    label={name}
                  />
                ))}
              </div>
            </Field>
          </FormSection>
        </Sections>
      </Card>
    </div>
  );
}

function StatusCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const q = useQuery({
    queryKey: qk.updates,
    queryFn: updatesApi.status,
    // While installing, the service goes away and comes back as the new version.
    refetchInterval: (query) => (query.state.data && query.state.data.state !== 'idle' ? 2000 : 60_000),
    retry: true,
  });
  const check = useMutation({
    mutationFn: updatesApi.check,
    onSuccess: (st) => {
      qc.setQueryData(qk.updates, st);
      toast.success(st.available ? `NodeHoster ${st.available.version} is available` : 'NodeHoster is up to date');
    },
    onError: (e) => toast.error('Could not check for updates', e),
  });
  const install = useMutation({
    mutationFn: updatesApi.install,
    onSuccess: (st) => {
      qc.setQueryData(qk.updates, st);
      toast.success('Installing the update: the console reconnects when the service is back');
    },
    onError: (e) => toast.error('Could not install the update', e),
  });
  const st = q.data;
  const head = st ? updateHeadline(st) : null;
  const busy = !!st && st.state !== 'idle';

  const installNow = async () => {
    if (!st?.available) return;
    const { ok } = await confirm({
      title: `Install NodeHoster ${st.available.version}?`,
      message: `This server runs ${st.current}. The service restarts to install the update: every site is offline for a few seconds.`,
      confirmLabel: 'Install now',
    });
    if (ok) install.mutate();
  };

  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <Download className="h-4 w-4 text-zinc-400" />
          Updates
        </span>
      }
      actions={
        <>
          <Button size="sm" icon={<RefreshCw className="h-3.5 w-3.5" />} loading={check.isPending} disabled={!st?.supported || busy} onClick={() => check.mutate()}>
            Check now
          </Button>
          <Button
            size="sm"
            variant="primary"
            icon={<Download className="h-3.5 w-3.5" />}
            loading={install.isPending}
            disabled={!st?.supported || !st.available || busy}
            onClick={() => void installNow()}
          >
            Install now
          </Button>
        </>
      }
    >
      {q.isError && !st ? (
        <ErrorBox>{errorMessage(q.error)}</ErrorBox>
      ) : (
        <div className="space-y-4">
          {st && !st.supported && (
            <Callout tone="info" title="This server does not update itself">
              {st.reason}. Download new versions from the release page and run setup.
            </Callout>
          )}
          {st?.lastResult && !st.lastResult.ok && (
            <Callout tone="danger" title={`Updating to ${st.lastResult.to} failed`}>
              {st.lastResult.error}
              {st.lastResult.log && (
                <>
                  {' '}
                  Setup's log: <code className="font-mono text-xs">{st.lastResult.log}</code>
                </>
              )}
            </Callout>
          )}
          {st?.available?.manual && !st.available.failed && (
            <Callout tone="warning" title={`${st.available.version} is not installed automatically`}>
              The update policy leaves this release to an administrator. Read its release notes, then use Install now.
            </Callout>
          )}
          <KV
            items={[
              [
                'State',
                head ? (
                  <Badge tone={head.tone} dot pulse={busy}>
                    {head.text}
                  </Badge>
                ) : (
                  '—'
                ),
              ],
              ['Installed', st?.current ?? '—'],
              [
                'Newest',
                st?.available ? (
                  <span className="flex flex-wrap items-center gap-2">
                    <span>{st.available.version}</span>
                    <span className="text-zinc-500">{formatBytes(st.available.size)}</span>
                    <a className="inline-flex items-center gap-1 text-accent-600 hover:underline" href={safeHref(st.available.notes)} target="_blank" rel="noreferrer">
                      Release notes <ExternalLink className="h-3 w-3" />
                    </a>
                  </span>
                ) : st?.lastCheck && !st.lastError ? (
                  st.current
                ) : (
                  '—'
                ),
              ],
              ['Next install', st?.nextInstall ? `${formatDateTime(st.nextInstall)} (${relativeTime(st.nextInstall)})` : '—'],
              [
                'Last check',
                st?.lastCheck ? (
                  <span>
                    {relativeTime(st.lastCheck)}
                    {st.lastError && <span className="ml-2 text-amber-700 dark:text-amber-300">{st.lastError}</span>}
                  </span>
                ) : (
                  'Not yet'
                ),
              ],
              [
                'Last update',
                st?.lastResult ? (
                  <span className="flex flex-wrap items-center gap-2">
                    <Badge tone={st.lastResult.ok ? 'green' : 'red'}>{st.lastResult.ok ? 'succeeded' : 'failed'}</Badge>
                    <span>
                      {st.lastResult.from} → {st.lastResult.to}, {formatDateTime(st.lastResult.startedAt)} ({st.lastResult.trigger})
                    </span>
                  </span>
                ) : (
                  'None'
                ),
              ],
            ]}
          />
        </div>
      )}
    </Card>
  );
}
