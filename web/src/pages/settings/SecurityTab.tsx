import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Ban as BanIcon, ShieldAlert, Trash2 } from 'lucide-react';
import { bansApi } from '@/api/endpoints';
import { errorMessage } from '@/api/client';
import { qk } from '@/api/queryKeys';
import type { BanRule, IPBanSettings } from '@/api/types';
import { Card, Callout, FormSection, Grid, Loading, Mono, Sections } from '@/components/Layout';
import { ErrorBox, Field } from '@/components/Field';
import { Input, NumberInput } from '@/components/Input';
import { Switch } from '@/components/Switch';
import { ListEditor } from '@/components/ListEditor';
import { Button, IconButton } from '@/components/Button';
import { Badge } from '@/components/Badge';
import { Table, TableMessage, TBody, Td, Th, THead, Tr } from '@/components/Table';
import { useConfirm } from '@/components/Confirm';
import { useToast } from '@/components/Toast';
import { useNow } from '@/hooks/useNow';
import { formatDateTime } from '@/lib/format';
import { banAddressError, banLengthMinutes, banRemaining } from '@/lib/ipban';
import { validateIP } from '../sites/editors/RoutingEditor';
import type { SettingsTabProps } from './SettingsPage';
import { FirewallSettings } from './FirewallSettings';

function RuleFields({ label, path, rule, onChange, unit }: { label: string; path: string; rule: BanRule; onChange: (r: BanRule) => void; unit: string }) {
  return (
    <Grid>
      <Field label={label} path={`${path}.threshold`} hint="Blank = rule off.">
        <NumberInput blankZero min={0} value={rule.threshold} onChange={(v) => onChange({ ...rule, threshold: v })} suffix={unit} />
      </Field>
      <Field label="Within" path={`${path}.windowSec`}>
        <NumberInput min={1} value={rule.windowSec} onChange={(v) => onChange({ ...rule, windowSec: v })} suffix="sec" />
      </Field>
    </Grid>
  );
}

/** Automatic IP banning (like fail2ban or IIS Dynamic IP Restrictions) and the banned addresses. */
export function SecurityTab({ s, update }: SettingsTabProps) {
  const b = s.ipBan;
  const set = (p: Partial<IPBanSettings>) =>
    update((d) => {
      d.ipBan = { ...d.ipBan, ...p };
    });
  const escalation = [1, 2, 3, 4].map((n) => banLengthMinutes(n, b.banMinutes || 15, b.maxBanMinutes || 1440));
  return (
    <div className="space-y-5">
      <Card title={<span className="flex items-center gap-2"><ShieldAlert className="h-4 w-4 text-zinc-400" />Automatic IP banning</span>}>
        <Sections>
          <FormSection
            title="Banning"
            description="Addresses that fail to sign in, scan for pages or flood a site are refused by every site (403) for a while. Sites can opt out on their Routing tab."
          >
            <Switch checked={b.enabled} onChange={(v) => set({ enabled: v })} label="Ban misbehaving addresses" />
            <Callout tone="info">
              Failed web console sign-ins count too, and a banned address cannot reach the console. Loopback is never banned, and NodeHoster Manager on the server
              can always lift a ban.
            </Callout>
          </FormSection>
          <FormSection title="Rules" description="An address is banned when it reaches a threshold within its window.">
            <RuleFields
              label="Failed sign-ins"
              path="ipBan.authFailures"
              unit="×"
              rule={b.authFailures}
              onChange={(r) => set({ authFailures: r })}
            />
            <p className="-mt-2 text-xs text-zinc-500">
              401 answers to requests that tried credentials (basic authentication, a submitted login form), and wrong web console passwords.
            </p>
            <RuleFields label="Missing pages (404)" path="ipBan.notFound" unit="×" rule={b.notFound} onChange={(r) => set({ notFound: r })} />
            <RuleFields label="Rate limited (429)" path="ipBan.rateLimited" unit="×" rule={b.rateLimited} onChange={(r) => set({ rateLimited: r })} />
            <RuleFields label="Blocked by the firewall" path="ipBan.wafBlocks" unit="×" rule={b.wafBlocks} onChange={(r) => set({ wafBlocks: r })} />
            <p className="-mt-2 text-xs text-zinc-500">Requests a site's web application firewall blocked (detect mode never counts).</p>
          </FormSection>
          <FormSection title="Trap paths" description="One request bans. Only scanners ask for these on a site that is not WordPress or PHP; such sites opt out on their Routing tab.">
            <Field path="ipBan.trapPaths" prefix hint="Path prefixes, not case-sensitive.">
              <ListEditor values={b.trapPaths ?? []} onChange={(v) => set({ trapPaths: v })} placeholder="/wp-login.php" />
            </Field>
          </FormSection>
          <FormSection title="Ban length" description="A repeat offender (within a week) is banned twice as long each time, up to the maximum.">
            <Grid>
              <Field label="First ban" path="ipBan.banMinutes">
                <NumberInput min={1} value={b.banMinutes} onChange={(v) => set({ banMinutes: v })} suffix="min" />
              </Field>
              <Field label="Longest ban" path="ipBan.maxBanMinutes">
                <NumberInput min={1} value={b.maxBanMinutes} onChange={(v) => set({ maxBanMinutes: v })} suffix="min" />
              </Field>
            </Grid>
            <p className="text-xs text-zinc-500">Bans in a row: {escalation.map((m) => `${m} min`).join(', ')}…</p>
          </FormSection>
          <FormSection title="Addresses" description="Trusted proxies (TLS & proxy tab) and loopback are never banned; bans apply to the client address they report.">
            <Field label="Never ban" path="ipBan.allowList" prefix hint="Offices, monitoring, partners.">
              <ListEditor values={b.allowList ?? []} onChange={(v) => set({ allowList: v })} placeholder="203.0.113.0/24" validate={validateIP} />
            </Field>
            <Field label="IPv6 prefix banned" path="ipBan.ipv6Prefix" hint="A client can change its address within the prefix it is given, usually a /64.">
              <NumberInput className="w-40" min={32} max={128} value={b.ipv6Prefix} onChange={(v) => set({ ipv6Prefix: v })} prefix="/" />
            </Field>
          </FormSection>
        </Sections>
      </Card>
      <BannedAddresses />
      <FirewallSettings s={s} update={update} />
    </div>
  );
}

/** The bans in force, with a manual ban form and unban buttons. */
export function BannedAddresses() {
  const q = useQuery({ queryKey: qk.bans, queryFn: bansApi.list, refetchInterval: 15_000 });
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const now = useNow(30_000);
  const [address, setAddress] = useState('');
  const [minutes, setMinutes] = useState(60);
  const [reason, setReason] = useState('');
  const addrErr = address ? banAddressError(address) : null;

  const ban = useMutation({
    mutationFn: () => bansApi.ban({ address: address.trim(), minutes, reason: reason.trim() }),
    onSuccess: (b) => {
      toast.success(`${b.address} banned`);
      setAddress('');
      setReason('');
      void qc.invalidateQueries({ queryKey: qk.bans });
    },
    onError: (e) => toast.error('Could not ban the address', e),
  });
  const unban = useMutation({
    mutationFn: (addr: string) => bansApi.unban(addr),
    onSuccess: (_, addr) => {
      toast.success(`${addr} unbanned`);
      void qc.invalidateQueries({ queryKey: qk.bans });
    },
    onError: (e) => toast.error('Could not lift the ban', e),
  });

  return (
    <Card title={<span className="flex items-center gap-2"><BanIcon className="h-4 w-4 text-zinc-400" />Banned addresses</span>} flush>
      <form
        className="flex flex-wrap items-start gap-2 border-b border-zinc-200 px-4 py-3 dark:border-zinc-800"
        onSubmit={(e) => {
          e.preventDefault();
          if (address && !addrErr) ban.mutate();
        }}
      >
        <Field error={addrErr} className="w-64">
          <Input mono value={address} placeholder="203.0.113.7 or 2001:db8::/64" onChange={(e) => setAddress(e.target.value)} />
        </Field>
        <NumberInput className="w-36" min={0} blankZero value={minutes} onChange={setMinutes} suffix="min" placeholder="forever" title="Blank = until removed" />
        <Input className="min-w-40 flex-1" value={reason} placeholder="Reason (optional)" onChange={(e) => setReason(e.target.value)} />
        <Button type="submit" variant="danger" loading={ban.isPending} disabled={!address || !!addrErr}>
          Ban
        </Button>
      </form>
      {q.isPending ? (
        <Loading />
      ) : q.isError ? (
        <ErrorBox className="m-4">{errorMessage(q.error)}</ErrorBox>
      ) : (
        <Table>
          <THead>
            <tr>
              <Th>Address</Th>
              <Th>Reason</Th>
              <Th>Banned</Th>
              <Th>Lifted</Th>
              <Th className="w-10" />
            </tr>
          </THead>
          <TBody>
            {q.data.length === 0 && <TableMessage colSpan={5}>No address is banned.</TableMessage>}
            {q.data.map((b) => (
              <Tr key={b.address}>
                <Td>
                  <Mono>{b.address}</Mono>
                </Td>
                <Td>
                  <span className="text-[13px]">{b.reason}</span>
                  {b.manual ? (
                    <Badge className="ml-2" tone="gray">
                      by {b.createdBy || 'an administrator'}
                    </Badge>
                  ) : (
                    b.strikes > 1 && (
                      <Badge className="ml-2" tone="amber">
                        ban {b.strikes}
                      </Badge>
                    )
                  )}
                </Td>
                <Td className="whitespace-nowrap text-xs text-zinc-500">{formatDateTime(b.createdAt)}</Td>
                <Td className="whitespace-nowrap text-xs text-zinc-500">{banRemaining(b, now)}</Td>
                <Td>
                  <IconButton
                    label="Unban"
                    variant="danger-ghost"
                    icon={<Trash2 className="h-3.5 w-3.5" />}
                    onClick={async () => {
                      const r = await confirm({ title: `Unban ${b.address}?`, message: 'Its requests are answered again right away, and its ban history is forgotten.', confirmLabel: 'Unban' });
                      if (r.ok) unban.mutate(b.address);
                    }}
                  />
                </Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      )}
    </Card>
  );
}
